package filestore

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/corndogs/server/csilapi"
	"github.com/stretchr/testify/require"
)

// auditLines counts records (newlines) across all segments in dir.
func auditLines(t *testing.T, dir string) int {
	t.Helper()
	segs, err := filepath.Glob(filepath.Join(dir, "audit-*.log"))
	require.NoError(t, err)
	var n int
	for _, p := range segs {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		n += bytes.Count(b, []byte{'\n'})
	}
	return n
}

// TestAuditRotation verifies size-based segment rotation: the log rolls to new
// files at the chunk threshold, every record is preserved across segments, and
// reopening resumes the latest segment.
func TestAuditRotation(t *testing.T) {
	dir := t.TempDir()
	ev := AuditEvent{Op: "claim", UUID: "8f14e45fceea467a95759a1d0f0b1c2d", Queue: "default", FromState: "submitted", ToState: "submitted-working"}

	a, err := NewAuditLog(dir, true, SyncNever, 1) // 1 MB segments
	require.NoError(t, err)
	const records = 30_000 // ~100 B/record => a few MB => multiple segments
	for i := 0; i < records; i++ {
		a.Record(ev)
	}
	require.NoError(t, a.Close())

	segs, _ := filepath.Glob(filepath.Join(dir, "audit-*.log"))
	require.Greater(t, len(segs), 1, "expected rotation into multiple segments")
	require.Equal(t, records, auditLines(t, dir), "no records lost across segments")

	// Reopen: resumes the latest segment and keeps accumulating.
	a2, err := NewAuditLog(dir, true, SyncNever, 1)
	require.NoError(t, err)
	for i := 0; i < 5_000; i++ {
		a2.Record(ev)
	}
	require.NoError(t, a2.Close())
	require.Equal(t, records+5_000, auditLines(t, dir))
	segs2, _ := filepath.Glob(filepath.Join(dir, "audit-*.log"))
	require.GreaterOrEqual(t, len(segs2), len(segs), "reopen must not lose segments")
}

// TestAuditScaling shows append latency does not grow with file size: the audit
// log is an append-only file, so each write is O(1) no matter how large the log
// already is. Run on real disk (ext4); skipped under -short.
func TestAuditScaling(t *testing.T) {
	benchOnly(t)
	dir, err := os.MkdirTemp(".", "audit-")
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	// Buffered (group/never) append, the realistic hot-path behavior. Chunking
	// disabled (0) so this measures pure append into one growing segment.
	a, err := NewAuditLog(dir, true, SyncNever, 0)
	require.NoError(t, err)
	defer a.Close()

	ev := AuditEvent{Op: "claim", UUID: "8f14e45f- ceea-467a-9575-9a1d0f0b1c2d", Queue: "default", FromState: "submitted", ToState: "submitted-working"}
	const window = 1_000_000
	const windows = 10
	fmt.Printf("\n=== audit append scaling (append-only file, buffered) ===\n")
	fmt.Printf("%-12s %-12s %-14s %-10s\n", "records", "ns/append", "appends/sec", "file_MB")
	for w := 0; w < windows; w++ {
		start := time.Now()
		for i := 0; i < window; i++ {
			a.Record(ev)
		}
		el := time.Since(start)
		a.Close() // flush so the on-disk size is accurate; reopen to continue appending
		fi, _ := os.Stat(filepath.Join(dir, segmentName(1)))
		fmt.Printf("%-12d %-12.1f %-14.0f %-10d\n",
			(w+1)*window, float64(el.Nanoseconds())/float64(window), float64(window)/el.Seconds(), fi.Size()/(1<<20))
		a, err = NewAuditLog(dir, true, SyncNever, 0)
		require.NoError(t, err)
	}
	fmt.Println()
}

// TestAuditOverhead quantifies the write-path cost of the audit log: submit
// throughput (group mode, concurrent) with the audit log on vs off.
func TestAuditOverhead(t *testing.T) {
	benchOnly(t)
	const n, workers = 50_000, 64
	fmt.Printf("\n=== audit write-path overhead (bolt, sync=group, on-disk) ===\n")
	fmt.Printf("%-14s %-14s\n", "audit", "submits/sec")
	for _, on := range []bool{false, true} {
		dir, err := os.MkdirTemp(".", "aud-")
		require.NoError(t, err)
		bs := NewBoltStore(Config{Backend: "bolt", DataDir: dir, AuditDir: dir, AuditEnabled: on, Sync: SyncGroup})
		cleanup, err := bs.Initialize()
		require.NoError(t, err)

		var idx int64 = -1
		var wg sync.WaitGroup
		start := time.Now()
		for wkr := 0; wkr < workers; wkr++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for atomic.AddInt64(&idx, 1) < int64(n) {
					_, err := bs.SubmitTask(ctx(), &api.SubmitTaskRequest{Queue: "q", CurrentState: "submitted", AutoTargetState: "wip", Timeout: -1})
					require.NoError(t, err)
				}
			}()
		}
		wg.Wait()
		el := time.Since(start)
		label := "disabled"
		if on {
			label = "enabled"
		}
		fmt.Printf("%-14s %-14.0f\n", label, float64(n)/el.Seconds())
		cleanup()
		os.RemoveAll(dir)
	}
	fmt.Println()
}
