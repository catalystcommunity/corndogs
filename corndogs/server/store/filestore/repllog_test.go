package filestore

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func batchN(lsn uint64) MutationBatch {
	return MutationBatch{LSN: lsn, Mutations: []Mutation{
		{Bucket: bucketTasks, Key: []byte{byte(lsn)}, Value: []byte{byte(lsn), byte(lsn)}},
	}}
}

func collect(t *testing.T, r *ReplLog, after uint64) []uint64 {
	t.Helper()
	var got []uint64
	require.NoError(t, r.ReadFrom(after, func(b MutationBatch) error {
		got = append(got, b.LSN)
		return nil
	}))
	return got
}

// TestReplLogAppendReadRoll: append across a segment roll, read from various
// points, and confirm gap-free ordering and rejection of non-contiguous LSNs.
func TestReplLogAppendReadRoll(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenReplLog(dir, 0, false)
	require.NoError(t, err)
	// Tiny chunk so we roll often.
	r.chunkBytes = 40

	for lsn := uint64(1); lsn <= 20; lsn++ {
		require.NoError(t, r.Append(batchN(lsn)))
	}
	require.Equal(t, uint64(20), r.LastLSN())

	// Non-contiguous append is rejected.
	require.Error(t, r.Append(batchN(22)))

	// Read everything, and from the middle.
	require.Equal(t, []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}, collect(t, r, 0))
	require.Equal(t, []uint64{16, 17, 18, 19, 20}, collect(t, r, 15))
	require.Nil(t, collect(t, r, 20))
	require.NoError(t, r.Close())

	// Reopen and confirm resume recovers lastLSN and the full history.
	r2, err := OpenReplLog(dir, 0, false)
	require.NoError(t, err)
	require.Equal(t, uint64(20), r2.LastLSN())
	require.NoError(t, r2.Append(batchN(21)))
	require.Equal(t, []uint64{19, 20, 21}, collect(t, r2, 18))
	require.NoError(t, r2.Close())
}

// TestReplLogTruncate: truncation drops whole segments below a floor but never
// loses batches at or above it.
func TestReplLogTruncate(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenReplLog(dir, 0, false)
	require.NoError(t, err)
	r.chunkBytes = 40
	for lsn := uint64(1); lsn <= 30; lsn++ {
		require.NoError(t, r.Append(batchN(lsn)))
	}
	require.NoError(t, r.Truncate(20))
	// Everything >= 20 must still be readable; some below may be gone, but nothing
	// at/above the floor is lost.
	got := collect(t, r, 19)
	require.Equal(t, []uint64{20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30}, got)
	require.NoError(t, r.Close())
}

// TestReplLogTornTailIsCutOff reproduces the replicated-file restart failure: a
// killed leader left a partial final frame, and appends after restart followed
// those bytes. A reader then decoded a false 1.9 GB field length. Open must cut
// the partial frame off so the log stays readable.
func TestReplLogTornTailIsCutOff(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenReplLog(dir, 0, false)
	require.NoError(t, err)
	for lsn := uint64(1); lsn <= 5; lsn++ {
		b := batchN(lsn)
		b.Epoch = 7
		require.NoError(t, r.Append(b))
	}
	// Simulate a kill during a write: the file ends with part of batch 6.
	var frame bytes.Buffer
	require.NoError(t, EncodeBatch(&frame, MutationBatch{LSN: 6, Mutations: []Mutation{
		{Bucket: bucketPayloads, Key: []byte("k"), Value: bytes.Repeat([]byte("payload:"), 64)},
	}}))
	seg := filepath.Join(dir, replSegmentName(1))
	f, err := os.OpenFile(seg, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	_, err = f.Write(frame.Bytes()[:frame.Len()/2])
	require.NoError(t, err)
	require.NoError(t, f.Close())

	r2, err := OpenReplLog(dir, 0, false)
	require.NoError(t, err)
	require.Equal(t, uint64(5), r2.LastLSN())
	ep, ok := r2.EpochAt(5)
	require.True(t, ok)
	require.Equal(t, uint64(7), ep)
	for lsn := uint64(6); lsn <= 8; lsn++ {
		b := batchN(lsn)
		b.Epoch = 9
		require.NoError(t, r2.Append(b))
	}
	require.Equal(t, []uint64{1, 2, 3, 4, 5, 6, 7, 8}, collect(t, r2, 0))
	var epochs []uint64
	require.NoError(t, r2.ReadFrom(4, func(b MutationBatch) error {
		epochs = append(epochs, b.Epoch)
		return nil
	}))
	require.Equal(t, []uint64{7, 9, 9, 9}, epochs)
}

// TestReplLogAppendReachesOS checks that an appended batch is readable by a new
// reader without Close, as after a killed process.
func TestReplLogAppendReachesOS(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenReplLog(dir, 0, false)
	require.NoError(t, err)
	require.NoError(t, r.Append(batchN(1)))
	r2, err := OpenReplLog(dir, 0, false)
	require.NoError(t, err)
	require.Equal(t, uint64(1), r2.LastLSN())
}

// TestReplLogReset restarts the log at a later position and keeps the base
// position's epoch across a reopen.
func TestReplLogReset(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenReplLog(dir, 0, false)
	require.NoError(t, err)
	for lsn := uint64(1); lsn <= 3; lsn++ {
		require.NoError(t, r.Append(batchN(lsn)))
	}
	require.NoError(t, r.Reset(41, 12))
	require.Equal(t, uint64(40), r.LastLSN())
	require.Equal(t, uint64(41), r.FirstLSN())
	require.Equal(t, uint64(12), r.LastEpoch())
	_, ok := r.EpochAt(3)
	require.False(t, ok, "the reset log no longer holds LSN 3")
	b := batchN(41)
	b.Epoch = 13
	require.NoError(t, r.Append(b))

	r2, err := OpenReplLog(dir, 0, false)
	require.NoError(t, err)
	require.Equal(t, uint64(41), r2.LastLSN())
	ep, ok := r2.EpochAt(40)
	require.True(t, ok)
	require.Equal(t, uint64(12), ep)
	ep, ok = r2.EpochAt(41)
	require.True(t, ok)
	require.Equal(t, uint64(13), ep)
	require.Equal(t, []uint64{41}, collect(t, r2, 0))
}

// TestDecodeBatchRejectsFalseLengths checks that a corrupt header fails without
// an allocation of the size that the header claims.
func TestDecodeBatchRejectsFalseLengths(t *testing.T) {
	var hdr bytes.Buffer
	_ = binary.Write(&hdr, binary.BigEndian, uint64(1))
	_ = binary.Write(&hdr, binary.BigEndian, uint32(1))
	hdr.WriteByte(0)
	_ = binary.Write(&hdr, binary.BigEndian, uint32(1952543348)) // bytes seen in the failed log
	hdr.WriteString("short")
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := DecodeBatch(bytes.NewReader(hdr.Bytes()))
	runtime.ReadMemStats(&after)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(16<<20), "decode allocated from the false length")

	var many bytes.Buffer
	_ = binary.Write(&many, binary.BigEndian, uint64(1))
	_ = binary.Write(&many, binary.BigEndian, uint32(0xffffffff))
	_, err = DecodeBatch(bytes.NewReader(many.Bytes()))
	require.Error(t, err)
}
