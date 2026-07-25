package filestore

import (
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
