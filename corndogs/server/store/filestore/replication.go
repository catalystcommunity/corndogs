package filestore

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	bolt "go.etcd.io/bbolt"
)

// This file implements the physical-mutation replication primitives for Tier-1
// clustering (docs/clustering-tier1.md). The leader captures the raw bbolt
// key/value changes of each committed write and ships them, LSN-ordered, to
// followers, which apply the bytes verbatim. Followers never re-run store logic,
// so replication is deterministic and sidesteps the wall-clock nondeterminism in
// the write path (claim/update/timeout all stamp nowNano on the leader; followers
// just copy the resulting bytes).

// Mutation is one physical change to a bbolt bucket: a Put (Delete=false) or a
// Delete (Delete=true, Value nil). Bucket, Key, and Value are owned copies —
// bbolt reuses its page buffers, so capture must copy.
type Mutation struct {
	Bucket []byte
	Key    []byte
	Value  []byte
	Delete bool
}

// MutationBatch is the complete set of mutations produced by one committed bbolt
// transaction, stamped with a monotonic LSN. Followers apply a batch atomically
// and in LSN order; the LSN is also each node's replication position (AppliedLSN
// in the cluster election).
//
// Epoch is the leadership epoch of the leader that produced the batch. It is not
// part of EncodeBatch: the peer frame carries it next to the batch, and the
// replication log keeps it in its epoch index. A zero Epoch means a batch from a
// release that did not record epochs.
type MutationBatch struct {
	LSN       uint64
	Epoch     uint64
	Mutations []Mutation
}

// captureBuf accumulates the mutations of an in-progress write transaction. It is
// only ever touched by the single goroutine executing a bbolt write transaction
// (bbolt serializes writers), so it needs no locking.
type captureBuf struct {
	muts []Mutation
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

func (c *captureBuf) put(bucket, key, value []byte) {
	c.muts = append(c.muts, Mutation{Bucket: cloneBytes(bucket), Key: cloneBytes(key), Value: cloneBytes(value)})
}

func (c *captureBuf) del(bucket, key []byte) {
	c.muts = append(c.muts, Mutation{Bucket: cloneBytes(bucket), Key: cloneBytes(key), Delete: true})
}

func (c *captureBuf) reset() { c.muts = c.muts[:0] }

// applyBatch applies a batch to a bbolt transaction verbatim. Buckets are created
// if missing so a follower bootstrapping from an empty database converges. It
// also records the batch position in the meta bucket, in the same transaction,
// so a restarted follower knows exactly which history its data reflects.
func applyBatch(tx *bolt.Tx, b MutationBatch) error {
	for _, name := range bucketsForReplication {
		if _, err := tx.CreateBucketIfNotExists(name); err != nil {
			return err
		}
	}
	for _, m := range b.Mutations {
		bkt, err := tx.CreateBucketIfNotExists(m.Bucket)
		if err != nil {
			return err
		}
		if m.Delete {
			if err := bkt.Delete(m.Key); err != nil {
				return err
			}
			continue
		}
		if err := bkt.Put(m.Key, m.Value); err != nil {
			return err
		}
	}
	meta, err := tx.CreateBucketIfNotExists(bucketMeta)
	if err != nil {
		return err
	}
	return putReplPosition(meta, b.LSN, b.Epoch)
}

// keyReplPosition holds the replication position (LSN, epoch) that the local
// data reflects. A follower writes it in each apply transaction. A leader writes
// it before it takes a snapshot, so a restored snapshot carries its exact
// position.
var keyReplPosition = []byte("repl-position")

func putReplPosition(meta *bolt.Bucket, lsn, epoch uint64) error {
	var v [16]byte
	binary.BigEndian.PutUint64(v[:8], lsn)
	binary.BigEndian.PutUint64(v[8:], epoch)
	return meta.Put(keyReplPosition, v[:])
}

// ReadReplPosition returns the replication position stored in db. ok is false
// when no position was stored.
func ReadReplPosition(db *bolt.DB) (lsn, epoch uint64, ok bool, err error) {
	err = db.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		if meta == nil {
			return nil
		}
		v := meta.Get(keyReplPosition)
		if v == nil {
			return nil
		}
		if len(v) != 16 {
			return fmt.Errorf("filestore: replication position has %d bytes, want 16", len(v))
		}
		lsn, epoch, ok = binary.BigEndian.Uint64(v[:8]), binary.BigEndian.Uint64(v[8:]), true
		return nil
	})
	return lsn, epoch, ok, err
}

// StampReplPosition records the replication position in its own transaction.
// Call it only when no store write can run at the same time, for example on the
// cluster engine goroutine, which serializes every clustered write.
func StampReplPosition(db *bolt.DB, lsn, epoch uint64) error {
	return db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists(bucketMeta)
		if err != nil {
			return err
		}
		return putReplPosition(meta, lsn, epoch)
	})
}

// HasTaskData reports whether db holds any live or archived task. A node with
// task data but no known replication position cannot trust LSN 0 as its start.
func HasTaskData(db *bolt.DB) (bool, error) {
	found := false
	err := db.View(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketTasks, bucketGuarded, bucketArchived} {
			if b := tx.Bucket(name); b != nil {
				if k, _ := b.Cursor().First(); k != nil {
					found = true
					return nil
				}
			}
		}
		return nil
	})
	return found, err
}

// ApplyBatch applies a batch to a bbolt database in its own transaction. Follower
// side.
func ApplyBatch(db *bolt.DB, b MutationBatch) error {
	return db.Update(func(tx *bolt.Tx) error { return applyBatch(tx, b) })
}

// --- wire / log encoding ---------------------------------------------------
//
// A batch encodes as: LSN(u64) count(u32) then per mutation:
//   flags(u8: bit0=delete) blen(u32) bucket klen(u32) key vlen(u32) value
// All big-endian. This is the frame written to the replication log segments and
// shipped over the StreamCarrier; it is length-delimited so a stream of batches
// is self-framing.

func putUvarintFields(w *bufio.Writer, b MutationBatch) error {
	var scratch [8]byte
	binary.BigEndian.PutUint64(scratch[:], b.LSN)
	if _, err := w.Write(scratch[:8]); err != nil {
		return err
	}
	binary.BigEndian.PutUint32(scratch[:4], uint32(len(b.Mutations)))
	if _, err := w.Write(scratch[:4]); err != nil {
		return err
	}
	for _, m := range b.Mutations {
		var flags byte
		if m.Delete {
			flags = 1
		}
		if err := w.WriteByte(flags); err != nil {
			return err
		}
		for _, field := range [][]byte{m.Bucket, m.Key, m.Value} {
			binary.BigEndian.PutUint32(scratch[:4], uint32(len(field)))
			if _, err := w.Write(scratch[:4]); err != nil {
				return err
			}
			if len(field) > 0 {
				if _, err := w.Write(field); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// EncodeBatch writes one length-framed batch to w.
func EncodeBatch(w io.Writer, b MutationBatch) error {
	bw := bufio.NewWriter(w)
	if err := putUvarintFields(bw, b); err != nil {
		return err
	}
	return bw.Flush()
}

// Decode limits. A torn or corrupt log tail can contain any bytes, so the decoder
// never allocates from a length field before the bytes arrive: it preallocates at
// most decodePrealloc entries and copies field bytes as they are read.
const (
	maxBatchMutations = 1 << 24
	maxMutationField  = 1 << 31
	decodePrealloc    = 1024
	readChunk         = 64 << 10
)

// readFull reads exactly n bytes. It grows its buffer only as data arrives, so a
// false length on a truncated stream fails with io.ErrUnexpectedEOF instead of
// one large allocation.
func readFull(r io.Reader, n uint32) ([]byte, error) {
	if n == 0 {
		return nil, nil
	}
	if n <= readChunk {
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return buf, nil
	}
	var buf bytes.Buffer
	buf.Grow(readChunk)
	got, err := io.CopyN(&buf, r, int64(n))
	if err != nil {
		if err == io.EOF && got < int64(n) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeBatch reads one batch previously written by EncodeBatch. It returns
// io.EOF cleanly when r is exhausted at a batch boundary.
func DecodeBatch(r io.Reader) (MutationBatch, error) {
	var head [8]byte
	if _, err := io.ReadFull(r, head[:8]); err != nil {
		return MutationBatch{}, err // io.EOF at a clean boundary
	}
	b := MutationBatch{LSN: binary.BigEndian.Uint64(head[:8])}
	var cnt [4]byte
	if _, err := io.ReadFull(r, cnt[:4]); err != nil {
		return MutationBatch{}, unexpected(err)
	}
	n := binary.BigEndian.Uint32(cnt[:4])
	if n > maxBatchMutations {
		return MutationBatch{}, fmt.Errorf("filestore: batch LSN %d has %d mutations; limit is %d", b.LSN, n, maxBatchMutations)
	}
	b.Mutations = make([]Mutation, 0, min(n, decodePrealloc))
	for i := uint32(0); i < n; i++ {
		var fl [1]byte
		if _, err := io.ReadFull(r, fl[:1]); err != nil {
			return MutationBatch{}, unexpected(err)
		}
		// Fields are length-prefixed and interleaved (len,data per field), matching
		// the encoder: bucket, key, value in order.
		fields := make([][]byte, 3)
		for j := 0; j < 3; j++ {
			var l [4]byte
			if _, err := io.ReadFull(r, l[:4]); err != nil {
				return MutationBatch{}, unexpected(err)
			}
			fieldLen := binary.BigEndian.Uint32(l[:4])
			if fieldLen > maxMutationField {
				return MutationBatch{}, fmt.Errorf("filestore: batch LSN %d has a %d-byte field; limit is %d", b.LSN, fieldLen, maxMutationField)
			}
			data, err := readFull(r, fieldLen)
			if err != nil {
				return MutationBatch{}, unexpected(err)
			}
			fields[j] = data
		}
		b.Mutations = append(b.Mutations, Mutation{Bucket: fields[0], Key: fields[1], Value: fields[2], Delete: fl[0]&1 == 1})
	}
	return b, nil
}

// unexpected converts an EOF encountered mid-frame into ErrUnexpectedEOF so a
// truncated tail is distinguishable from a clean end-of-stream.
func unexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// bucketsForReplication is the durable task state. The schema metadata is local
// initialization state and is not changed by normal replicated writes.
var bucketsForReplication = [][]byte{
	bucketTasks,
	bucketByUUID,
	bucketPayloads,
	bucketDeadlines,
	bucketArchived,
	bucketCounts,
	bucketGuarded,
	bucketSubmissions,
	bucketOperations,
	bucketReceiptExpiry,
}
