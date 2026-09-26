package filestore

import (
	"fmt"
	"math/rand"
	"testing"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

// scanCounts counts the task keys directly. It is the reference that the counts
// bucket must match.
func scanCounts(t *testing.T, db *bolt.DB) map[string]map[string]int64 {
	t.Helper()
	out := map[string]map[string]int64{}
	require.NoError(t, db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketTasks).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			q, st := parseKeyQueueState(k)
			if out[q] == nil {
				out[q] = map[string]int64{}
			}
			out[q][st]++
		}
		return nil
	}))
	return out
}

// requireCountsMatch checks every metric operation against a full scan.
func requireCountsMatch(t *testing.T, s *BoltStore) {
	t.Helper()
	want := scanCounts(t, s.db)

	qs, err := s.GetQueueAndStateCounts(ctx(), &api.GetQueueAndStateCountsRequest{})
	require.NoError(t, err)
	require.Len(t, qs.QueueAndStateCounts, len(want))
	var total int64
	for q, states := range want {
		got, ok := qs.QueueAndStateCounts[q]
		require.True(t, ok, "queue %s missing", q)
		var n int64
		for st, c := range states {
			require.Equal(t, c, got.StateCounts[st], "queue %s state %s", q, st)
			n += c
		}
		require.Len(t, got.StateCounts, len(states))
		require.Equal(t, n, got.Count)
		total += n

		ts, err := s.GetTaskStateCounts(ctx(), &api.GetTaskStateCountsRequest{Queue: q})
		require.NoError(t, err)
		require.Equal(t, n, ts.Count)
		require.Equal(t, api.StringInt64Map(states), ts.StateCounts)
	}

	qc, err := s.GetQueueTaskCounts(ctx(), &api.GetQueueTaskCountsRequest{})
	require.NoError(t, err)
	require.Equal(t, total, qc.TotalTaskCount)
	require.Len(t, qc.QueueCounts, len(want))

	gq, err := s.GetQueues(ctx(), &api.GetQueuesRequest{})
	require.NoError(t, err)
	require.Equal(t, total, gq.TotalTaskCount)
	require.Len(t, gq.Queues, len(want))
}

// TestCountsFollowEveryWrite drives each write operation in a random mix and
// checks that the maintained counts equal a full scan after each step.
func TestCountsFollowEveryWrite(t *testing.T) {
	withFakeClock(t)
	st, cleanup := newStore(t, "bolt", SyncNever)
	defer cleanup()
	s := st.(*BoltStore)
	rng := rand.New(rand.NewSource(7))
	queues := []string{"a", "b", "c"}
	states := []string{"submitted", "parked", "retry"}
	var live []string

	for i := 0; i < 600; i++ {
		q := queues[rng.Intn(len(queues))]
		switch op := rng.Intn(7); {
		case op <= 1 || len(live) == 0:
			timeout := int64(-1)
			if rng.Intn(2) == 0 {
				timeout = 1
			}
			sub, err := s.SubmitTask(ctx(), &api.SubmitTaskRequest{
				Queue: q, CurrentState: states[rng.Intn(len(states))], AutoTargetState: "working",
				Timeout: timeout, Payload: []byte("p"), Priority: int64(rng.Intn(3)),
			})
			require.NoError(t, err)
			live = append(live, sub.Task.Uuid)
		case op == 2:
			_, err := s.GetNextTask(ctx(), &api.GetNextTaskRequest{Queue: q, CurrentState: states[rng.Intn(len(states))]})
			require.NoError(t, err)
		case op == 3:
			id := live[rng.Intn(len(live))]
			_, err := s.UpdateTask(ctx(), &api.UpdateTaskRequest{
				Uuid: id, NewState: states[rng.Intn(len(states))], AutoTargetState: "working",
				Timeout: int64(rng.Intn(2)),
			})
			require.NoError(t, err)
		case op == 4:
			i := rng.Intn(len(live))
			_, err := s.CompleteTask(ctx(), &api.CompleteTaskRequest{Uuid: live[i]})
			require.NoError(t, err)
			live = append(live[:i], live[i+1:]...)
		case op == 5:
			i := rng.Intn(len(live))
			_, err := s.CancelTask(ctx(), &api.CancelTaskRequest{Uuid: live[i]})
			require.NoError(t, err)
			live = append(live[:i], live[i+1:]...)
		default:
			_, err := s.CleanUpTimedOut(ctx(), &api.CleanUpTimedOutRequest{AtTime: nowNano() + 1_000_000})
			require.NoError(t, err)
		}
		if i%25 == 0 {
			requireCountsMatch(t, s)
		}
	}
	requireCountsMatch(t, s)

	// Drain everything: no count key may stay behind with a zero value.
	for _, id := range live {
		_, err := s.CompleteTask(ctx(), &api.CompleteTaskRequest{Uuid: id})
		require.NoError(t, err)
	}
	requireCountsMatch(t, s)
	require.NoError(t, s.db.View(func(tx *bolt.Tx) error {
		require.Equal(t, 0, tx.Bucket(bucketCounts).Stats().KeyN)
		return nil
	}))
}

// TestCountsRebuiltOnUpgrade opens a database written by a version without the
// counts bucket and checks that Initialize builds the counts.
func TestCountsRebuiltOnUpgrade(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Backend: "bolt", DataDir: dir, AuditDir: dir, Sync: SyncNever}
	s := NewBoltStore(cfg)
	cleanup, err := s.Initialize()
	require.NoError(t, err)
	for i := 0; i < 50; i++ {
		_, err := s.SubmitTask(ctx(), &api.SubmitTaskRequest{
			Queue: fmt.Sprintf("q%d", i%3), CurrentState: "submitted", AutoTargetState: "w",
			Timeout: -1, Payload: []byte("p"),
		})
		require.NoError(t, err)
	}
	// Remove the counts and the marker, as an older version leaves the file.
	require.NoError(t, s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bucketCounts); err != nil {
			return err
		}
		return tx.Bucket(bucketMeta).Delete(keyCountsReady)
	}))
	cleanup()

	s = NewBoltStore(cfg)
	cleanup, err = s.Initialize()
	require.NoError(t, err)
	defer cleanup()
	require.NoError(t, s.db.View(func(tx *bolt.Tx) error {
		require.True(t, countsReady(tx))
		return nil
	}))
	requireCountsMatch(t, s)
}

// TestCountsFallbackForOldLeader applies a batch without count changes, as a
// leader that does not maintain counts sends. The follower must drop the ready
// marker and give correct metrics from a scan.
func TestCountsFallbackForOldLeader(t *testing.T) {
	withFakeClock(t)
	st, cleanup := newStore(t, "bolt", SyncNever)
	defer cleanup()
	f := st.(*BoltStore)

	task := &Task{UUID: "u1", Queue: "q", CurrentState: "submitted", AutoTargetState: "w", Priority: 1, UpdateTime: 5}
	key := encodeTaskKey(task)
	require.NoError(t, f.ApplyReplicated(MutationBatch{LSN: 1, Mutations: []Mutation{
		{Bucket: bucketTasks, Key: key, Value: []byte(`{"uuid":"u1","queue":"q","current_state":"submitted"}`)},
		{Bucket: bucketByUUID, Key: []byte("u1"), Value: key},
	}}))
	require.NoError(t, f.db.View(func(tx *bolt.Tx) error {
		require.False(t, countsReady(tx))
		return nil
	}))
	requireCountsMatch(t, f)

	// A restart rebuilds the counts from the replicated tasks.
	require.NoError(t, ensureCounts(f.db))
	require.NoError(t, f.db.View(func(tx *bolt.Tx) error {
		require.True(t, countsReady(tx))
		return nil
	}))
	requireCountsMatch(t, f)
}
