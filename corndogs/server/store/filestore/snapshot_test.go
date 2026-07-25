package filestore

import (
	"os"
	"path/filepath"
	"testing"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

// TestSnapshotBootstrapThenTail proves a far-behind/new follower converges by
// restoring a consistent snapshot and then applying only the batches after the
// snapshot's LSN — the catch-up path for a joining node.
func TestSnapshotBootstrapThenTail(t *testing.T) {
	withFakeClock(t)
	dir := t.TempDir()
	leader := NewBoltStore(Config{Backend: "bolt", DataDir: filepath.Join(dir, "leader"), AuditDir: filepath.Join(dir, "leader"), Sync: SyncNever})
	cleanup, err := leader.Initialize()
	require.NoError(t, err)
	defer cleanup()

	var batches []MutationBatch
	leader.EnableReplication(0, func(b MutationBatch) { batches = append(batches, b) })

	// Phase A: some work, captured pre-snapshot.
	for i := 0; i < 50; i++ {
		_, err := leader.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1, Payload: []byte("a")})
		require.NoError(t, err)
	}

	// Snapshot at this point.
	snapPath := filepath.Join(dir, "snap.bolt")
	sf, err := os.Create(snapPath)
	require.NoError(t, err)
	snapLSN, err := leader.SnapshotTo(sf)
	require.NoError(t, err)
	require.NoError(t, sf.Close())
	require.Greater(t, snapLSN, uint64(0))

	// Phase B: more work AFTER the snapshot (claims + submits + completes).
	for i := 0; i < 50; i++ {
		_, err := leader.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1, Payload: []byte("b")})
		require.NoError(t, err)
		_, err = leader.GetNextTask(ctx(), &api.GetNextTaskRequest{Queue: "q", CurrentState: "submitted"})
		require.NoError(t, err)
	}

	// Follower bootstraps from the snapshot file itself, then tails LSN > snapLSN.
	fdb, err := bolt.Open(snapPath, 0o600, nil)
	require.NoError(t, err)
	defer fdb.Close()
	applied := snapLSN
	for _, b := range batches {
		if b.LSN <= snapLSN {
			continue // already contained in the snapshot
		}
		require.Equal(t, applied+1, b.LSN, "tail must be gap-free from the snapshot")
		applied = b.LSN
		require.NoError(t, ApplyBatch(fdb, b))
	}
	require.Greater(t, applied, snapLSN, "expected post-snapshot batches to tail")

	// Converged byte-for-byte.
	require.NoError(t, leader.db.View(func(ltx *bolt.Tx) error {
		return fdb.View(func(ftx *bolt.Tx) error {
			for _, name := range bucketsForReplication {
				assertBucketsEqual(t, name, ltx.Bucket(name), ftx.Bucket(name))
			}
			return nil
		})
	}))
}
