package filestore

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"path/filepath"
	"testing"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

// TestBatchEncodeRoundTrip proves the wire/log framing is lossless, including
// deletes, empty values, and multi-mutation batches, and that a concatenated
// stream decodes back to the same batches ending in a clean io.EOF.
func TestBatchEncodeRoundTrip(t *testing.T) {
	batches := []MutationBatch{
		{LSN: 1, Mutations: []Mutation{
			{Bucket: bucketTasks, Key: []byte("k1"), Value: []byte("v1")},
			{Bucket: bucketByUUID, Key: []byte("uuid-1"), Value: []byte("k1")},
		}},
		{LSN: 2, Mutations: []Mutation{
			{Bucket: bucketTasks, Key: []byte("k1"), Delete: true},
			{Bucket: bucketArchived, Key: []byte("uuid-1"), Value: []byte{}}, // empty value
		}},
		{LSN: 3, Mutations: nil}, // empty batch (shouldn't normally be shipped, must still round-trip)
	}
	var buf bytes.Buffer
	for _, b := range batches {
		require.NoError(t, EncodeBatch(&buf, b))
	}
	for i, want := range batches {
		got, err := DecodeBatch(&buf)
		require.NoError(t, err, "batch %d", i)
		require.Equal(t, want.LSN, got.LSN, "batch %d LSN", i)
		require.Equal(t, len(want.Mutations), len(got.Mutations), "batch %d count", i)
		for j := range want.Mutations {
			// bytes.Equal treats a nil and an empty slice as equal, which is exactly
			// bbolt's own semantics for an empty value — the distinction is not
			// meaningful on the wire.
			require.Equal(t, want.Mutations[j].Delete, got.Mutations[j].Delete)
			require.True(t, bytes.Equal(want.Mutations[j].Bucket, got.Mutations[j].Bucket))
			require.True(t, bytes.Equal(want.Mutations[j].Key, got.Mutations[j].Key))
			require.True(t, bytes.Equal(want.Mutations[j].Value, got.Mutations[j].Value))
		}
	}
	// Stream is exhausted at a clean boundary.
	_, err := DecodeBatch(&buf)
	require.ErrorIs(t, err, io.EOF)
}

// TestReplicationConvergence is the core replication correctness proof: drive a
// leader store through a randomized mix of EVERY write operation while capturing
// its physical mutations, replay the captured batches (also through their wire
// encode/decode) into a fresh follower bbolt, and assert the follower is
// byte-for-byte identical to the leader across every replicated bucket.
func TestReplicationConvergence(t *testing.T) {
	withFakeClock(t) // deterministic timestamps so leader bytes are reproducible

	dir := t.TempDir()
	leader := NewBoltStore(Config{Backend: "bolt", DataDir: filepath.Join(dir, "leader"), AuditDir: filepath.Join(dir, "leader"), Sync: SyncNever})
	cleanup, err := leader.Initialize()
	require.NoError(t, err)
	defer cleanup()

	// Capture batches, round-tripping each through the wire codec so the test also
	// exercises encode/decode on real store output.
	var stream bytes.Buffer
	leader.EnableReplication(0, func(b MutationBatch) {
		require.NoError(t, EncodeBatch(&stream, b))
	})

	// A pool of live task ids we can target with follow-up ops.
	type live struct {
		uuid, queue, state string
	}
	var pool []live
	rng := rand.New(rand.NewSource(42))
	queues := []string{"q1", "q2", "q3"}
	ctxb := ctx()

	op := func() {
		switch rng.Intn(6) {
		case 0, 1: // submit
			q := queues[rng.Intn(len(queues))]
			resp, err := leader.SubmitTask(ctxb, &api.SubmitTaskRequest{
				Queue: q, CurrentState: "submitted", AutoTargetState: "wip",
				Priority: int64(rng.Intn(5)), Timeout: -1, Payload: []byte(fmt.Sprintf("p%d", rng.Intn(1000))),
			})
			require.NoError(t, err)
			pool = append(pool, live{resp.Task.Uuid, q, "submitted"})
		case 2: // claim next from a random queue
			q := queues[rng.Intn(len(queues))]
			resp, err := leader.GetNextTask(ctxb, &api.GetNextTaskRequest{Queue: q, CurrentState: "submitted"})
			require.NoError(t, err)
			_ = resp
		case 3: // claim across a group of queues
			_, err := leader.GetNextTaskGroup(ctxb, &api.GetNextTaskGroupRequest{Queues: queues, CurrentState: "submitted"})
			require.NoError(t, err)
		case 4: // update a random live task
			if len(pool) == 0 {
				return
			}
			p := pool[rng.Intn(len(pool))]
			_, err := leader.UpdateTask(ctxb, &api.UpdateTaskRequest{
				Uuid: p.uuid, Queue: p.queue, CurrentState: p.state,
				NewState: "phase2", AutoTargetState: "phase2-wip",
			})
			require.NoError(t, err)
		case 5: // complete/cancel a random live task (archive path)
			if len(pool) == 0 {
				return
			}
			i := rng.Intn(len(pool))
			p := pool[i]
			if rng.Intn(2) == 0 {
				_, _ = leader.CompleteTask(ctxb, &api.CompleteTaskRequest{Uuid: p.uuid, Queue: p.queue, CurrentState: p.state})
			} else {
				_, _ = leader.CancelTask(ctxb, &api.CancelTaskRequest{Uuid: p.uuid, Queue: p.queue, CurrentState: p.state})
			}
			pool = append(pool[:i], pool[i+1:]...)
		}
	}
	for i := 0; i < 500; i++ {
		op()
	}
	// Also exercise the timeout sweep (produces "timeout" mutations).
	_, err = leader.CleanUpTimedOut(ctxb, &api.CleanUpTimedOutRequest{AtTime: nowNano() + 1})
	require.NoError(t, err)

	// Build the follower purely by applying the replicated stream.
	followerPath := filepath.Join(dir, "follower.bolt")
	fdb, err := bolt.Open(followerPath, 0o600, nil)
	require.NoError(t, err)
	defer fdb.Close()
	var lastLSN uint64
	for {
		b, err := DecodeBatch(&stream)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.Equal(t, lastLSN+1, b.LSN, "LSNs must be gap-free and monotonic")
		lastLSN = b.LSN
		require.NoError(t, ApplyBatch(fdb, b))
	}
	require.Greater(t, lastLSN, uint64(0), "expected some replicated batches")

	// Assert byte-for-byte identity of every replicated bucket.
	require.NoError(t, leader.db.View(func(ltx *bolt.Tx) error {
		return fdb.View(func(ftx *bolt.Tx) error {
			for _, name := range bucketsForReplication {
				assertBucketsEqual(t, name, ltx.Bucket(name), ftx.Bucket(name))
			}
			return nil
		})
	}))
}

func assertBucketsEqual(t *testing.T, name []byte, lb, fb *bolt.Bucket) {
	t.Helper()
	if lb == nil && fb == nil {
		return
	}
	require.NotNil(t, lb, "leader bucket %s missing", name)
	require.NotNil(t, fb, "follower bucket %s missing", name)
	lc, fc := lb.Cursor(), fb.Cursor()
	lk, lv := lc.First()
	fk, fv := fc.First()
	for lk != nil || fk != nil {
		require.True(t, bytes.Equal(lk, fk), "bucket %s key mismatch: leader=%x follower=%x", name, lk, fk)
		require.True(t, bytes.Equal(lv, fv), "bucket %s value mismatch at key %x", name, lk)
		lk, lv = lc.Next()
		fk, fv = fc.Next()
	}
}
