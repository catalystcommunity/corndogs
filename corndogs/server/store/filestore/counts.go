package filestore

import (
	"bytes"
	"encoding/binary"

	bolt "go.etcd.io/bbolt"
)

// The counts bucket holds the number of live tasks for each (queue, state). The
// key is queue<sep>state and the value is an 8-byte big-endian count. A key with
// a zero count is deleted. putTask and deleteTask keep it current in the same
// transaction as the task write, so the metric operations read one small bucket
// instead of every task key. The mutations are captured like any other write,
// so followers replicate the counts with the tasks.
var bucketCounts = []byte("counts")

// keyCountsReady marks a counts bucket that matches the tasks bucket. It is
// local to each node: a node sets it after it builds the counts from a full
// scan, and a follower removes it when it applies a batch from a leader that
// does not maintain counts. Without the marker, the metric operations scan the
// tasks bucket as before.
var keyCountsReady = []byte("counts-ready")

func countKey(queue, state string) []byte {
	k := make([]byte, 0, len(queue)+1+len(state))
	k = append(k, queue...)
	k = append(k, sep)
	return append(k, state...)
}

func parseCountKey(k []byte) (queue, state string) {
	i := bytes.IndexByte(k, sep)
	if i < 0 {
		return string(k), ""
	}
	return string(k[:i]), string(k[i+1:])
}

// adjustCount adds delta to the (queue, state) count and captures the change
// for replication.
func (s *BoltStore) adjustCount(tx *bolt.Tx, queue, state string, delta int64) error {
	b := tx.Bucket(bucketCounts)
	key := countKey(queue, state)
	var n int64
	if v := b.Get(key); len(v) == 8 {
		n = int64(binary.BigEndian.Uint64(v))
	}
	n += delta
	if n <= 0 {
		if err := b.Delete(key); err != nil {
			return err
		}
		if s.cap != nil {
			s.cap.del(bucketCounts, key)
		}
		return nil
	}
	val := make([]byte, 8)
	binary.BigEndian.PutUint64(val, uint64(n))
	if err := b.Put(key, val); err != nil {
		return err
	}
	if s.cap != nil {
		s.cap.put(bucketCounts, key, val)
	}
	return nil
}

func countsReady(tx *bolt.Tx) bool {
	return tx.Bucket(bucketMeta).Get(keyCountsReady) != nil
}

// rebuildCounts replaces the counts bucket with counts from a full scan of the
// tasks bucket and sets the ready marker. It runs in one transaction, so the
// counts match the tasks exactly. It is not captured: each node builds its own
// counts from the same replicated tasks.
func rebuildCounts(db *bolt.DB) error {
	return db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketCounts) != nil {
			if err := tx.DeleteBucket(bucketCounts); err != nil {
				return err
			}
		}
		b, err := tx.CreateBucket(bucketCounts)
		if err != nil {
			return err
		}
		counts := map[string]uint64{}
		c := tx.Bucket(bucketTasks).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			q, st := parseKeyQueueState(k)
			counts[string(countKey(q, st))]++
		}
		for k, n := range counts {
			val := make([]byte, 8)
			binary.BigEndian.PutUint64(val, n)
			if err := b.Put([]byte(k), val); err != nil {
				return err
			}
		}
		return tx.Bucket(bucketMeta).Put(keyCountsReady, []byte{1})
	})
}

// ensureCounts builds the counts when the ready marker is absent: on the first
// start after an upgrade, after a snapshot restore, or after a follower applied
// batches from a leader that does not maintain counts.
func ensureCounts(db *bolt.DB) error {
	ready := false
	if err := db.View(func(tx *bolt.Tx) error {
		ready = tx.Bucket(bucketMeta) != nil && countsReady(tx) && tx.Bucket(bucketCounts) != nil
		return nil
	}); err != nil {
		return err
	}
	if ready {
		return nil
	}
	return rebuildCounts(db)
}

// batchKeepsCounts reports whether a replicated batch keeps the counts current:
// a batch that changes the tasks bucket must also change the counts bucket. A
// leader that does not maintain counts sends task changes only.
func batchKeepsCounts(b MutationBatch) bool {
	tasks, counts := false, false
	for _, m := range b.Mutations {
		switch {
		case bytes.Equal(m.Bucket, bucketTasks):
			tasks = true
		case bytes.Equal(m.Bucket, bucketCounts):
			counts = true
		}
	}
	return !tasks || counts
}

// forEachCount calls fn for each (queue, state) count, in key order. It uses the
// counts bucket when it is ready, and otherwise counts the task keys.
func forEachCount(tx *bolt.Tx, prefix []byte, fn func(queue, state string, n int64)) {
	if countsReady(tx) {
		c := tx.Bucket(bucketCounts).Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			if len(v) != 8 {
				continue
			}
			q, st := parseCountKey(k)
			fn(q, st, int64(binary.BigEndian.Uint64(v)))
		}
		return
	}
	c := tx.Bucket(bucketTasks).Cursor()
	for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		q, st := parseKeyQueueState(k)
		fn(q, st, 1)
	}
}
