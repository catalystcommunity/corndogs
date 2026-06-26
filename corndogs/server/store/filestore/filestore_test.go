package filestore

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/corndogs/server/csilapi"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store"
	"github.com/stretchr/testify/require"
)

// backends enumerates the implementations under test. (buntdb was dropped after
// benchmarking; the slice is kept so conformance tests stay table-driven.)
var backends = []string{"bolt"}

// benchOnly skips disk-heavy benchmarks unless FILESTORE_BENCH=1. CI runs
// `go test ./...` without -short, so gating on an explicit env var keeps these
// (which write gigabytes and take minutes) out of normal test runs.
func benchOnly(t *testing.T) {
	t.Helper()
	if os.Getenv("FILESTORE_BENCH") == "" {
		t.Skip("set FILESTORE_BENCH=1 to run disk benchmarks")
	}
}

// newStore opens a fresh store of the given backend in a temp dir.
func newStore(t testing.TB, backend string, sync SyncMode) (store.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(Config{
		Backend:      backend,
		DataDir:      dir,
		AuditDir:     dir,
		AuditEnabled: true,
		Sync:         sync,
	})
	require.NoError(t, err)
	cleanup, err := s.Initialize()
	require.NoError(t, err)
	return s, cleanup
}

// withFakeClock installs a deterministic monotonic clock so ordering tests are
// not flaky on update_time ties. Restores the real clock on cleanup.
func withFakeClock(t *testing.T) {
	t.Helper()
	var n int64 = 1
	orig := nowNano
	nowNano = func() int64 { return atomic.AddInt64(&n, 1) }
	t.Cleanup(func() { nowNano = orig })
}

func ctx() context.Context { return context.Background() }

func TestBasicFlow(t *testing.T) {
	for _, backend := range backends {
		t.Run(backend, func(t *testing.T) {
			withFakeClock(t)
			s, cleanup := newStore(t, backend, SyncAlways)
			defer cleanup()

			sub, err := s.SubmitTask(ctx(), &api.SubmitTaskRequest{
				Queue:           "q",
				CurrentState:    "submitted",
				AutoTargetState: "submitted-working",
				Timeout:         -1,
				Payload:         []byte("hello"),
			})
			require.NoError(t, err)
			require.NotNil(t, sub.Task)
			require.NotEmpty(t, sub.Task.Uuid)
			require.NotZero(t, sub.Task.SubmitTime)

			// Claim: states swap (current<->auto), matching postgres semantics.
			got, err := s.GetNextTask(ctx(), &api.GetNextTaskRequest{Queue: "q", CurrentState: "submitted"})
			require.NoError(t, err)
			require.NotNil(t, got.Task)
			require.Equal(t, "submitted-working", got.Task.CurrentState)
			require.Equal(t, "submitted", got.Task.AutoTargetState)
			require.Equal(t, sub.Task.Uuid, got.Task.Uuid)
			require.Equal(t, []byte("hello"), got.Task.Payload)

			// Queue is now empty for that state.
			empty, err := s.GetNextTask(ctx(), &api.GetNextTaskRequest{Queue: "q", CurrentState: "submitted"})
			require.NoError(t, err)
			require.Nil(t, empty.Task)

			// Update to a new state.
			upd, err := s.UpdateTask(ctx(), &api.UpdateTaskRequest{
				Uuid:            got.Task.Uuid,
				Queue:           "q",
				CurrentState:    "submitted-working",
				NewState:        "phase2",
				AutoTargetState: "phase2-working",
			})
			require.NoError(t, err)
			require.Equal(t, "phase2", upd.Task.CurrentState)

			// Claim the updated task, then complete it.
			got2, err := s.GetNextTask(ctx(), &api.GetNextTaskRequest{Queue: "q", CurrentState: "phase2"})
			require.NoError(t, err)
			require.NotNil(t, got2.Task)

			done, err := s.CompleteTask(ctx(), &api.CompleteTaskRequest{Uuid: got2.Task.Uuid, Queue: "q", CurrentState: got2.Task.CurrentState})
			require.NoError(t, err)
			require.Equal(t, "completed", done.Task.CurrentState)
			require.Nil(t, done.Task.Payload, "payload dropped on archive")
		})
	}
}

func TestGetNextTaskOverride(t *testing.T) {
	for _, backend := range backends {
		t.Run(backend, func(t *testing.T) {
			withFakeClock(t)
			s, cleanup := newStore(t, backend, SyncNever)
			defer cleanup()

			_, err := s.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "submitted-working", Timeout: -1})
			require.NoError(t, err)

			got, err := s.GetNextTask(ctx(), &api.GetNextTaskRequest{
				Queue:                   "q",
				CurrentState:            "submitted",
				OverrideCurrentState:    "ocs",
				OverrideAutoTargetState: "oats",
			})
			require.NoError(t, err)
			require.Equal(t, "ocs", got.Task.CurrentState)
			require.Equal(t, "oats", got.Task.AutoTargetState)
		})
	}
}

func TestPriorityAndFIFOOrdering(t *testing.T) {
	for _, backend := range backends {
		t.Run(backend, func(t *testing.T) {
			withFakeClock(t) // monotonic clock => submit order == update_time order
			s, cleanup := newStore(t, backend, SyncNever)
			defer cleanup()

			// Submit in an order that is neither priority nor FIFO, so only correct
			// ordering produces the expected dequeue sequence.
			type spec struct {
				name string
				prio int64
			}
			// Two priority-10 tasks (A then B => FIFO A before B), one priority-5.
			specs := []spec{{"A", 10}, {"C", 5}, {"B", 10}}
			ids := map[string]string{}
			for _, sp := range specs {
				r, err := s.SubmitTask(ctx(), &api.SubmitTaskRequest{
					Queue: "q", CurrentState: "submitted", AutoTargetState: "wip",
					Priority: sp.prio, Timeout: -1, Payload: []byte(sp.name),
				})
				require.NoError(t, err)
				ids[sp.name] = r.Task.Uuid
			}

			// Expected dequeue: A (prio10, oldest), B (prio10, newer), C (prio5).
			var order []string
			for i := 0; i < 3; i++ {
				got, err := s.GetNextTask(ctx(), &api.GetNextTaskRequest{Queue: "q", CurrentState: "submitted"})
				require.NoError(t, err)
				require.NotNil(t, got.Task)
				order = append(order, string(got.Task.Payload))
			}
			require.Equal(t, []string{"A", "B", "C"}, order)
		})
	}
}

func TestGetByIDActiveAndArchived(t *testing.T) {
	for _, backend := range backends {
		t.Run(backend, func(t *testing.T) {
			s, cleanup := newStore(t, backend, SyncNever)
			defer cleanup()

			sub, err := s.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1, Payload: []byte("p")})
			require.NoError(t, err)

			// Active lookup.
			active, err := s.MustGetTaskStateByID(ctx(), &api.GetTaskStateByIDRequest{Uuid: sub.Task.Uuid})
			require.NoError(t, err)
			require.NotNil(t, active.Task)
			require.Equal(t, []byte("p"), active.Task.Payload)

			// Complete then archived lookup.
			_, err = s.CompleteTask(ctx(), &api.CompleteTaskRequest{Uuid: sub.Task.Uuid, Queue: "q", CurrentState: "submitted"})
			require.NoError(t, err)
			arch, err := s.MustGetTaskStateByID(ctx(), &api.GetTaskStateByIDRequest{Uuid: sub.Task.Uuid})
			require.NoError(t, err)
			require.NotNil(t, arch.Task)
			require.Equal(t, "completed", arch.Task.CurrentState)

			// Unknown id => nil, no error.
			none, err := s.MustGetTaskStateByID(ctx(), &api.GetTaskStateByIDRequest{Uuid: "does-not-exist"})
			require.NoError(t, err)
			require.Nil(t, none.Task)
		})
	}
}

func TestCleanUpTimedOut(t *testing.T) {
	for _, backend := range backends {
		t.Run(backend, func(t *testing.T) {
			s, cleanup := newStore(t, backend, SyncNever)
			defer cleanup()

			// timeout=1s; submit, claim (sets a live timeout), then sweep far in future.
			_, err := s.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: 1})
			require.NoError(t, err)
			got, err := s.GetNextTask(ctx(), &api.GetNextTaskRequest{Queue: "q", CurrentState: "submitted"})
			require.NoError(t, err)
			require.NotNil(t, got.Task)
			require.Equal(t, "wip", got.Task.CurrentState)

			future := time.Now().Add(time.Hour).UnixNano()
			res, err := s.CleanUpTimedOut(ctx(), &api.CleanUpTimedOutRequest{AtTime: future})
			require.NoError(t, err)
			require.Equal(t, int64(1), res.TimedOut)

			// State reverted (current<->auto swap), and it is claimable again as "submitted".
			reverted, err := s.GetNextTask(ctx(), &api.GetNextTaskRequest{Queue: "q", CurrentState: "submitted"})
			require.NoError(t, err)
			require.NotNil(t, reverted.Task, "timed-out task should be back in 'submitted'")
		})
	}
}

func TestMetrics(t *testing.T) {
	for _, backend := range backends {
		t.Run(backend, func(t *testing.T) {
			s, cleanup := newStore(t, backend, SyncNever)
			defer cleanup()

			submit := func(q, state string) {
				_, err := s.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: q, CurrentState: state, AutoTargetState: "wip", Timeout: -1})
				require.NoError(t, err)
			}
			submit("q1", "submitted")
			submit("q1", "submitted")
			submit("q1", "other")
			submit("q2", "submitted")

			queues, err := s.GetQueues(ctx(), &api.GetQueuesRequest{})
			require.NoError(t, err)
			require.Equal(t, int64(4), queues.TotalTaskCount)
			require.ElementsMatch(t, []string{"q1", "q2"}, queues.Queues)

			qc, err := s.GetQueueTaskCounts(ctx(), &api.GetQueueTaskCountsRequest{})
			require.NoError(t, err)
			require.Equal(t, int64(3), qc.QueueCounts["q1"])
			require.Equal(t, int64(1), qc.QueueCounts["q2"])

			sc, err := s.GetTaskStateCounts(ctx(), &api.GetTaskStateCountsRequest{Queue: "q1"})
			require.NoError(t, err)
			require.Equal(t, int64(3), sc.Count)
			require.Equal(t, int64(2), sc.StateCounts["submitted"])
			require.Equal(t, int64(1), sc.StateCounts["other"])

			qsc, err := s.GetQueueAndStateCounts(ctx(), &api.GetQueueAndStateCountsRequest{})
			require.NoError(t, err)
			require.Equal(t, int64(2), qsc.QueueAndStateCounts["q1"].StateCounts["submitted"])
			require.Equal(t, int64(1), qsc.QueueAndStateCounts["q2"].StateCounts["submitted"])
		})
	}
}

// TestConcurrentNoDoubleClaim is the core concurrency guarantee: many workers
// draining the same (queue, state) must each get a distinct task, with every
// task claimed exactly once.
func TestConcurrentNoDoubleClaim(t *testing.T) {
	for _, backend := range backends {
		t.Run(backend, func(t *testing.T) {
			s, cleanup := newStore(t, backend, SyncNever)
			defer cleanup()

			const N = 5000
			for i := 0; i < N; i++ {
				_, err := s.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1})
				require.NoError(t, err)
			}

			const workers = 16
			var wg sync.WaitGroup
			results := make([][]string, workers)
			var claimed int64
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					for {
						got, err := s.GetNextTask(ctx(), &api.GetNextTaskRequest{Queue: "q", CurrentState: "submitted"})
						if err != nil {
							t.Errorf("worker %d: %v", w, err)
							return
						}
						if got.Task == nil {
							return // empty => all claimed (no skip-locked invisibility)
						}
						results[w] = append(results[w], got.Task.Uuid)
						atomic.AddInt64(&claimed, 1)
					}
				}(w)
			}
			wg.Wait()

			require.Equal(t, int64(N), claimed, "every task claimed exactly once")
			seen := make(map[string]struct{}, N)
			for _, r := range results {
				for _, id := range r {
					_, dup := seen[id]
					require.False(t, dup, "task %s claimed twice", id)
					seen[id] = struct{}{}
				}
			}
			require.Len(t, seen, N)
		})
	}
}

// seedTasks submits n tasks using `workers` concurrent goroutines so that, in
// group-commit mode, the submits batch instead of paying one fsync each.
func seedTasks(t *testing.T, s store.Store, n, workers int) {
	t.Helper()
	var idx int64 = -1
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for atomic.AddInt64(&idx, 1) < int64(n) {
				_, err := s.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1})
				require.NoError(t, err)
			}
		}()
	}
	wg.Wait()
}

// drainBolt seeds n tasks then drains them with the given worker count,
// returning claims/sec and (for group mode) the mean batch size.
func drainBolt(t *testing.T, cfg Config, n, workers int) (rate, avgBatch float64) {
	t.Helper()
	dir, err := os.MkdirTemp(".", "sweep-")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	cfg.Backend, cfg.DataDir, cfg.AuditDir = "bolt", dir, dir
	bs := NewBoltStore(cfg)
	cleanup, err := bs.Initialize()
	require.NoError(t, err)
	defer cleanup()

	// Seed concurrently: in group mode a sequential seed would pay one fsync per
	// submit (defeating the batching we are here to measure), so fan it out.
	seedTasks(t, bs, n, 64)
	// Reset committer stats so seeding doesn't skew batch averages.
	if bs.committer != nil {
		atomic.StoreInt64(&bs.committer.batches, 0)
		atomic.StoreInt64(&bs.committer.totalOps, 0)
	}
	var claimed int64
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				got, err := bs.GetNextTask(ctx(), &api.GetNextTaskRequest{Queue: "q", CurrentState: "submitted"})
				if err != nil || got.Task == nil {
					return
				}
				atomic.AddInt64(&claimed, 1)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	require.Equal(t, int64(n), claimed)
	if bs.committer != nil {
		avgBatch = bs.committer.avgBatch()
	}
	return float64(n) / elapsed.Seconds(), avgBatch
}

// TestGroupCommitSweep finds the throughput/durability balance: it compares the
// durable-but-serial "always" path against group commit at increasing
// concurrency (wider batches), plus the non-durable never/interval references.
func TestGroupCommitSweep(t *testing.T) {
	benchOnly(t)
	fmt.Printf("\n=== bolt durability/throughput sweep (on-disk ext4) ===\n")
	fmt.Printf("%-26s %-10s %12s %10s\n", "mode", "workers", "claims/sec", "avgBatch")
	row := func(label string, workers int, rate, avg float64) {
		fmt.Printf("%-26s %-10d %12.0f %10.1f\n", label, workers, rate, avg)
	}

	// Durable, serial baseline (small N — it is fsync-bound and slow).
	r, _ := drainBolt(t, Config{Sync: SyncAlways}, 2_000, 8)
	row("always (durable)", 8, r, 1)

	// Group commit: durable, but the batch widens with concurrency. N scales
	// with worker count because low concurrency degrades toward the slow
	// (fsync-bound) "always" rate.
	groupCases := []struct{ workers, n int }{
		{1, 2_000}, {8, 10_000}, {32, 20_000}, {128, 20_000},
	}
	for _, gc := range groupCases {
		r, avg := drainBolt(t, Config{Sync: SyncGroup}, gc.n, gc.workers)
		row("group (durable)", gc.workers, r, avg)
	}

	// Knee finding: at fixed high concurrency (256 workers), vary the batch cap.
	// Unbounded coalescing builds giant transactions that hurt; a cap is better.
	for _, cap := range []int{16, 64, 256, 1024} {
		r, avg := drainBolt(t, Config{Sync: SyncGroup, GroupMaxBatch: cap}, 20_000, 256)
		row(fmt.Sprintf("group maxBatch=%d", cap), 256, r, avg)
	}

	// Does a small linger widen batches / help under moderate concurrency?
	r, avg := drainBolt(t, Config{Sync: SyncGroup, GroupMaxBatch: 256, GroupMaxDelay: 200 * time.Microsecond}, 20_000, 32)
	row("group +200us linger", 32, r, avg)

	// Non-durable references (ack before on disk).
	r, _ = drainBolt(t, Config{Sync: SyncInterval}, 20_000, 8)
	row("interval (~1s window)", 8, r, 1)
	r, _ = drainBolt(t, Config{Sync: SyncNever}, 20_000, 8)
	row("never (no fsync)", 8, r, 1)
	fmt.Println()
}

// TestGroupCommitCorrectness ensures group commit (many claims coalesced into
// one transaction) still hands every worker a distinct task. Because batched
// claim closures run sequentially inside the shared transaction, each Seek must
// see the prior claim's deletion.
func TestGroupCommitCorrectness(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Config{Backend: "bolt", DataDir: dir, AuditDir: dir, AuditEnabled: true, Sync: SyncGroup})
	require.NoError(t, err)
	cleanup, err := s.Initialize()
	require.NoError(t, err)
	defer cleanup()

	const N = 5000
	for i := 0; i < N; i++ {
		_, err := s.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1})
		require.NoError(t, err)
	}
	const workers = 32
	var wg sync.WaitGroup
	results := make([][]string, workers)
	var claimed int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for {
				got, err := s.GetNextTask(ctx(), &api.GetNextTaskRequest{Queue: "q", CurrentState: "submitted"})
				require.NoError(t, err)
				if got.Task == nil {
					return
				}
				results[w] = append(results[w], got.Task.Uuid)
				atomic.AddInt64(&claimed, 1)
			}
		}(w)
	}
	wg.Wait()
	require.Equal(t, int64(N), claimed)
	seen := make(map[string]struct{}, N)
	for _, r := range results {
		for _, id := range r {
			_, dup := seen[id]
			require.False(t, dup, "task %s claimed twice", id)
			seen[id] = struct{}{}
		}
	}
	require.Len(t, seen, N)
}

// TestThroughput reports claims/sec for each backend in each sync mode. It is
// skipped under -short. Run with: go test -run TestThroughput -v
func TestThroughput(t *testing.T) {
	benchOnly(t)
	type mode struct {
		sync SyncMode
		n    int
	}
	// Data dirs live on real disk (the package directory, ext4) rather than
	// t.TempDir() which is tmpfs (fsync ~= free). sync=always fsyncs on every
	// single claim, so it uses a much smaller N to finish in reasonable time;
	// rates are per-second so they remain comparable.
	modes := []mode{
		{SyncNever, 20_000},
		{SyncInterval, 20_000},
		{SyncAlways, 2_000},
	}
	const workers = 8
	fmt.Printf("\n=== GetNextTask throughput (workers=%d, on-disk) ===\n", workers)
	for _, backend := range backends {
		for _, m := range modes {
			dir, err := os.MkdirTemp(".", "bench-")
			require.NoError(t, err)
			s, err := New(Config{Backend: backend, DataDir: dir, AuditDir: dir, AuditEnabled: true, Sync: m.sync})
			require.NoError(t, err)
			cleanup, err := s.Initialize()
			require.NoError(t, err)
			rm := func() { cleanup(); _ = os.RemoveAll(dir) }
			for i := 0; i < m.n; i++ {
				_, err := s.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1})
				require.NoError(t, err)
			}
			var claimed int64
			var wg sync.WaitGroup
			start := time.Now()
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						got, err := s.GetNextTask(ctx(), &api.GetNextTaskRequest{Queue: "q", CurrentState: "submitted"})
						if err != nil || got.Task == nil {
							return
						}
						atomic.AddInt64(&claimed, 1)
					}
				}()
			}
			wg.Wait()
			elapsed := time.Since(start)
			require.Equal(t, int64(m.n), claimed)
			fmt.Printf("%-5s sync=%-9s N=%-7d %8.0f claims/sec  (%v)\n",
				backend, m.sync, m.n, float64(m.n)/elapsed.Seconds(), elapsed.Round(time.Millisecond))
			rm()
		}
	}
	fmt.Println()
}
