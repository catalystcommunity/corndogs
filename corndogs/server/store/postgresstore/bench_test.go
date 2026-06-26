package postgresstore

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/corndogs/server/csilapi"
	"github.com/stretchr/testify/require"
)

// TestPostgresThroughput benchmarks the postgres backend's GetNextTask dequeue
// with the same drain harness used for the file backends, so the numbers are
// directly comparable. It is gated behind BENCH_PG=1 and needs a reachable
// postgres (DATABASE_* env vars). Run:
//
//	BENCH_PG=1 DATABASE_HOST=localhost DATABASE_PORT=5432 \
//	  DATABASE_MAX_OPEN_CONNS=64 DATABASE_MAX_IDLE_CONNS=64 \
//	  go test -run TestPostgresThroughput ./server/store/postgresstore/ -v
func TestPostgresThroughput(t *testing.T) {
	if os.Getenv("BENCH_PG") == "" {
		t.Skip("set BENCH_PG=1 with a reachable postgres to run")
	}
	s := PostgresStore{}
	closeFn, err := s.Initialize()
	require.NoError(t, err)
	if closeFn != nil {
		defer closeFn()
	}
	ctx := context.Background()

	seed := func(n, workers int) {
		require.NoError(t, DB.Exec("TRUNCATE tasks, archived_tasks").Error)
		var idx int64 = -1
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for atomic.AddInt64(&idx, 1) < int64(n) {
					_, err := s.SubmitTask(ctx, &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1})
					require.NoError(t, err)
				}
			}()
		}
		wg.Wait()
	}

	drain := func(workers int) (int64, time.Duration) {
		var claimed int64
		var wg sync.WaitGroup
		start := time.Now()
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					got, err := s.GetNextTask(ctx, &api.GetNextTaskRequest{Queue: "q", CurrentState: "submitted"})
					if err != nil || got.Task == nil {
						return
					}
					atomic.AddInt64(&claimed, 1)
				}
			}()
		}
		wg.Wait()
		return claimed, time.Since(start)
	}

	fmt.Printf("\n=== postgres GetNextTask throughput (max_open_conns=%d) ===\n", MaxOpenConns)
	fmt.Printf("%-10s %12s\n", "workers", "claims/sec")
	for _, w := range []int{1, 8, 32, 128, 256} {
		n := 20_000
		if w == 1 {
			n = 4_000 // single-worker is slow; keep it short
		}
		seed(n, 64)
		claimed, elapsed := drain(w)
		require.Equal(t, int64(n), claimed)
		fmt.Printf("%-10d %12.0f\n", w, float64(n)/elapsed.Seconds())
	}
	fmt.Println()
}
