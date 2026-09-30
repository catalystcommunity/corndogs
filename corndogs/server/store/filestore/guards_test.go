package filestore

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func ro(digest string, retention time.Duration) store.ReceiptOptions {
	return store.ReceiptOptions{Digest: []byte(digest), Retention: retention}
}

func keyedReq(queue, key string) *api.SubmitKeyedTaskRequest {
	return &api.SubmitKeyedTaskRequest{
		SubmissionKey: key, Guarded: true, Queue: queue,
		CurrentState: "submitted", AutoTargetState: "submitted-working", Payload: []byte("p-" + key),
	}
}

func claimReq(opID string, queues ...string) *api.ClaimGuardedTaskGroupRequest {
	return &api.ClaimGuardedTaskGroupRequest{OperationId: opID, Queues: queues, CurrentState: "submitted"}
}

func codeOf(t *testing.T, err error) uint64 {
	t.Helper()
	code, ok := api.ServiceErrorCode(err)
	require.True(t, ok, "want a ServiceError, got %v", err)
	return code
}

// The clock is under test control: expiry uses the same nowNano as the store.
func withSettableClock(t *testing.T) *atomic.Int64 {
	t.Helper()
	var now atomic.Int64
	now.Store(1_000_000_000)
	orig := nowNano
	nowNano = func() int64 { return now.Add(1) }
	t.Cleanup(func() { nowNano = orig })
	return &now
}

func TestReceiptExpiryAndPurge(t *testing.T) {
	clock := withSettableClock(t)
	s, cleanup := newStore(t, "bolt", SyncNever)
	defer cleanup()
	bs := s.(*BoltStore)
	retention := time.Second

	first, err := bs.SubmitKeyedTask(ctx(), keyedReq("q", "k"), ro("d1", retention))
	require.NoError(t, err)
	again, err := bs.SubmitKeyedTask(ctx(), keyedReq("q", "k"), ro("d1", retention))
	require.NoError(t, err)
	require.True(t, again.Replayed)

	// After the retention period the key is not deduplicated: the server does
	// not claim deduplication beyond the retained period.
	clock.Add(int64(2 * retention))
	look, err := bs.LookupSubmission(ctx(), &api.LookupSubmissionRequest{Queue: "q", SubmissionKey: "k"})
	require.NoError(t, err)
	require.Nil(t, look.Receipt)
	fresh, err := bs.SubmitKeyedTask(ctx(), keyedReq("q", "k"), ro("d2", retention))
	require.NoError(t, err)
	require.False(t, fresh.Replayed)
	require.NotEqual(t, first.Receipt.TaskUuid, fresh.Receipt.TaskUuid)

	// The purge keeps the replacement record: its expiry is later.
	n, err := bs.PurgeExpiredReceipts(ctx(), nowNano(), 100)
	require.NoError(t, err)
	require.Equal(t, 0, n)
	look, err = bs.LookupSubmission(ctx(), &api.LookupSubmissionRequest{Queue: "q", SubmissionKey: "k"})
	require.NoError(t, err)
	require.Equal(t, fresh.Receipt.TaskUuid, look.Receipt.TaskUuid)

	cl, err := bs.ClaimGuardedTask(ctx(), claimReq("op1", "q"), ro("c1", retention))
	require.NoError(t, err)
	require.NotNil(t, cl.Delivery)
	clock.Add(int64(2 * retention))
	n, err = bs.PurgeExpiredReceipts(ctx(), nowNano(), 100)
	require.NoError(t, err)
	require.Equal(t, 2, n, "the submission and the claim receipt expired")
	require.NoError(t, bs.db.View(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketSubmissions, bucketOperations, bucketReceiptExpiry} {
			k, _ := tx.Bucket(name).Cursor().First()
			require.Nil(t, k, "bucket %s must be empty", name)
		}
		return nil
	}))
}

func TestKeyedSubmitConcurrentGroupCommit(t *testing.T) {
	s, cleanup := newStore(t, "bolt", SyncGroup)
	defer cleanup()
	const n = 32
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := s.SubmitKeyedTask(ctx(), keyedReq("q", "same"), ro("d", time.Hour))
			require.NoError(t, err)
			ids[i] = r.Receipt.TaskUuid
			// A different key in the same batch is a different task.
			_, err = s.SubmitKeyedTask(ctx(), keyedReq("q", fmt.Sprintf("k%d", i)), ro("d", time.Hour))
			require.NoError(t, err)
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		require.Equal(t, ids[0], id)
	}
	counts, err := s.GetQueueTaskCounts(ctx(), &api.GetQueueTaskCountsRequest{})
	require.NoError(t, err)
	require.Equal(t, int64(n+1), counts.QueueCounts["q"])

	// Concurrent claims with one operation id claim one task.
	var claimed sync.Map
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.ClaimGuardedTask(ctx(), claimReq("one-op", "q"), ro("c", time.Hour))
			require.NoError(t, err)
			claimed.Store(r.Delivery.Task.Task.Uuid, true)
		}()
	}
	wg.Wait()
	distinct := 0
	claimed.Range(func(_, _ any) bool { distinct++; return true })
	require.Equal(t, 1, distinct)
}

func TestResilienceStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Backend: "bolt", DataDir: dir, AuditDir: dir, Sync: SyncGroup}
	s := NewBoltStore(cfg)
	cleanup, err := s.Initialize()
	require.NoError(t, err)
	sub, err := s.SubmitKeyedTask(ctx(), keyedReq("q", "k"), ro("d", time.Hour))
	require.NoError(t, err)
	cl, err := s.ClaimGuardedTask(ctx(), claimReq("claim-1", "q"), ro("c", time.Hour))
	require.NoError(t, err)
	cleanup()

	s = NewBoltStore(cfg)
	cleanup, err = s.Initialize()
	require.NoError(t, err)
	defer cleanup()
	again, err := s.SubmitKeyedTask(ctx(), keyedReq("q", "k"), ro("d", time.Hour))
	require.NoError(t, err)
	require.True(t, again.Replayed)
	require.Equal(t, sub.Receipt, again.Receipt)
	replay, err := s.ClaimGuardedTask(ctx(), claimReq("claim-1", "q"), ro("c", time.Hour))
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, cl.Delivery.Task.Revision, replay.Delivery.Task.Revision)
	require.Equal(t, []byte("p-k"), replay.Delivery.Payload)
	op, err := s.LookupOperation(ctx(), &api.LookupOperationRequest{OperationId: "claim-1"})
	require.NoError(t, err)
	require.Equal(t, sub.Receipt.TaskUuid, op.Receipt.TaskUuid)

	// A counts rebuild includes guarded tasks.
	require.NoError(t, s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketMeta).Delete(keyCountsReady) }))
	counts, err := s.GetQueueAndStateCounts(ctx(), &api.GetQueueAndStateCountsRequest{})
	require.NoError(t, err)
	require.Equal(t, int64(1), counts.QueueAndStateCounts["q"].Count)
	require.NoError(t, rebuildCounts(s.db))
	counts, err = s.GetQueueAndStateCounts(ctx(), &api.GetQueueAndStateCountsRequest{})
	require.NoError(t, err)
	require.Equal(t, int64(1), counts.QueueAndStateCounts["q"].Count)
}

func TestLegacyTaskFromOldReleaseUnderGuards(t *testing.T) {
	s, cleanup := newStore(t, "bolt", SyncNever)
	defer cleanup()
	bs := s.(*BoltStore)
	// A task written by 0.7.x has no revision field.
	old := &Task{UUID: "00000000-0000-4000-8000-000000000001", Queue: "q", CurrentState: "waiting", AutoTargetState: "waiting", SubmitTime: 1, UpdateTime: 1}
	require.NoError(t, bs.db.Update(func(tx *bolt.Tx) error {
		if err := bs.putPayload(tx, old.UUID, []byte("legacy")); err != nil {
			return err
		}
		return bs.putTask(tx, old)
	}))
	view, err := bs.GetGuardedTask(ctx(), &api.GetGuardedTaskRequest{Uuid: old.UUID, Queue: "q"})
	require.NoError(t, err)
	require.Equal(t, int64(0), view.Task.Revision)
	require.False(t, view.Task.Guarded)

	// A human-wait task needs no claim: its revision guards the transition.
	_, err = bs.FinishGuardedTask(ctx(), &api.CompleteGuardedTaskRequest{OperationId: "f1", Uuid: old.UUID, Queue: "q", ExpectedRevision: 1}, store.OpComplete, ro("f", time.Hour))
	require.Equal(t, api.CodeRevisionConflict, codeOf(t, err))
	done, err := bs.FinishGuardedTask(ctx(), &api.CompleteGuardedTaskRequest{OperationId: "f2", Uuid: old.UUID, Queue: "q", ExpectedRevision: 0}, store.OpComplete, ro("f", time.Hour))
	require.NoError(t, err)
	require.True(t, done.Task.Terminal)
	require.Equal(t, int64(1), done.Task.Revision)
}

func TestGuardedReplicationConvergence(t *testing.T) {
	withFakeClock(t)
	dir := t.TempDir()
	leader := NewBoltStore(Config{Backend: "bolt", DataDir: filepath.Join(dir, "leader"), AuditDir: filepath.Join(dir, "leader"), Sync: SyncNever})
	cleanup, err := leader.Initialize()
	require.NoError(t, err)
	defer cleanup()
	var stream bytes.Buffer
	leader.EnableReplication(0, func(b MutationBatch) { require.NoError(t, EncodeBatch(&stream, b)) })

	for i := 0; i < 20; i++ {
		_, err := leader.SubmitKeyedTask(ctx(), keyedReq("q", fmt.Sprintf("k%d", i)), ro("d", time.Hour))
		require.NoError(t, err)
		_, err = leader.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "w", Payload: []byte("l")})
		require.NoError(t, err)
	}
	for i := 0; i < 10; i++ {
		cl, err := leader.ClaimGuardedTask(ctx(), claimReq(fmt.Sprintf("c%d", i), "q"), ro("c", time.Hour))
		require.NoError(t, err)
		g := cl.Delivery.Task
		up, err := leader.UpdateGuardedTask(ctx(), &api.UpdateGuardedTaskRequest{OperationId: fmt.Sprintf("u%d", i), Uuid: g.Task.Uuid, Queue: "q", ExpectedRevision: g.Revision, NewState: "s2", AutoTargetState: "s2w", Timeout: 5}, ro("u", time.Hour))
		require.NoError(t, err)
		op := store.OpComplete
		if i%2 == 1 {
			op = store.OpCancel
		}
		_, err = leader.FinishGuardedTask(ctx(), &api.CompleteGuardedTaskRequest{OperationId: fmt.Sprintf("f%d", i), Uuid: g.Task.Uuid, Queue: "q", ExpectedRevision: up.Task.Revision}, op, ro("f", time.Hour))
		require.NoError(t, err)
	}
	_, err = leader.CleanUpTimedOut(ctx(), &api.CleanUpTimedOutRequest{AtTime: nowNano() + 1})
	require.NoError(t, err)
	_, err = leader.PurgeExpiredReceipts(ctx(), nowNano()+int64(2*time.Hour), 7)
	require.NoError(t, err)

	fdb, err := bolt.Open(filepath.Join(dir, "follower.bolt"), 0o600, nil)
	require.NoError(t, err)
	defer fdb.Close()
	for {
		b, err := DecodeBatch(&stream)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.NoError(t, ApplyBatch(fdb, b))
	}
	require.NoError(t, leader.db.View(func(ltx *bolt.Tx) error {
		return fdb.View(func(ftx *bolt.Tx) error {
			for _, name := range bucketsForReplication {
				assertBucketsEqual(t, name, ltx.Bucket(name), ftx.Bucket(name))
			}
			return nil
		})
	}))
}
