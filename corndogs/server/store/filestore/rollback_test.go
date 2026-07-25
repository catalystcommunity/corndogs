package filestore

import (
	"bytes"
	"path/filepath"
	"testing"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

// TestRejoinRollback models the split-brain rejoin: a node that led a losing
// partition applied writes locally that were never cluster-committed. On rejoin it
// must discard its diverged database and re-bootstrap from the winner's snapshot,
// then converge exactly — including tailing the winner's post-snapshot batches.
func TestRejoinRollback(t *testing.T) {
	withFakeClock(t)
	dir := t.TempDir()

	// The winner (surviving majority leader).
	winner := NewBoltStore(Config{Backend: "bolt", DataDir: filepath.Join(dir, "win"), AuditDir: filepath.Join(dir, "win"), Sync: SyncNever})
	wc, err := winner.Initialize()
	require.NoError(t, err)
	defer wc()
	var winStream []MutationBatch
	winner.EnableReplication(0, func(b MutationBatch) { winStream = append(winStream, b) })

	// The loser (was leader in the minority partition).
	loser := NewBoltStore(Config{Backend: "bolt", DataDir: filepath.Join(dir, "lose"), AuditDir: filepath.Join(dir, "lose"), Sync: SyncNever})
	lc, err := loser.Initialize()
	require.NoError(t, err)
	defer lc()
	loser.EnableReplication(0, func(b MutationBatch) {})

	// Both start from the same committed history (apply the same initial ops on
	// each) — simulate a shared past.
	for i := 0; i < 10; i++ {
		_, err := winner.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1, Payload: []byte("shared")})
		require.NoError(t, err)
		_, err = loser.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1, Payload: []byte("shared")})
		require.NoError(t, err)
	}

	// Now the partition: each side diverges independently.
	for i := 0; i < 20; i++ {
		_, err := winner.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1, Payload: []byte("winner-only")})
		require.NoError(t, err)
	}
	// Loser's partition-time writes (never cluster-committed; must be rolled back).
	for i := 0; i < 15; i++ {
		_, err := loser.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1, Payload: []byte("loser-only")})
		require.NoError(t, err)
	}

	// Heal: the loser rejoins. It rolls back by restoring the winner's snapshot.
	var snap bytes.Buffer
	snapLSN, err := winner.SnapshotTo(&snap)
	require.NoError(t, err)
	require.NoError(t, loser.RestoreSnapshot(&snap, snapLSN))

	// The winner keeps working after the snapshot; the loser tails those batches.
	for i := 0; i < 5; i++ {
		_, err := winner.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1, Payload: []byte("post-heal")})
		require.NoError(t, err)
	}
	for _, b := range winStream {
		if b.LSN <= snapLSN {
			continue
		}
		require.NoError(t, ApplyBatch(loser.db, b))
	}

	// The loser is now byte-for-byte the winner — its divergent "loser-only" writes
	// are gone, and it has the winner's full history.
	require.NoError(t, winner.db.View(func(wtx *bolt.Tx) error {
		return loser.db.View(func(ltx *bolt.Tx) error {
			for _, name := range bucketsForReplication {
				assertBucketsEqual(t, name, wtx.Bucket(name), ltx.Bucket(name))
			}
			return nil
		})
	}))

	// And there is no trace of the loser's divergent payloads.
	require.NoError(t, loser.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTasks).ForEach(func(_, v []byte) error {
			require.NotContains(t, string(v), "loser-only", "rolled-back write survived")
			return nil
		})
	}))
}
