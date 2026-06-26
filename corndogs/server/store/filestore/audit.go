package filestore

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AuditEvent is one append-only record of a task mutation. The audit log is the
// genuine time-ordered / append-only artifact of the system (in contrast to the
// mutable live-task store), which is why it can be pointed at its own volume.
type AuditEvent struct {
	Time      int64  `json:"time"` // unix nanos
	Op        string `json:"op"`   // submit|claim|update|complete|cancel|timeout
	UUID      string `json:"uuid"`
	Queue     string `json:"queue,omitempty"`
	FromState string `json:"from_state,omitempty"`
	ToState   string `json:"to_state,omitempty"`
	Priority  int64  `json:"priority,omitempty"`
}

// AuditLog is an append-only writer for AuditEvents. It is safe for concurrent
// use; writes are serialized through a mutex (cheap relative to the data-store
// write that always accompanies them).
//
// The log is written as size-bounded segments named audit-NNNNNN.log. When the
// active segment reaches the configured chunk size a fresh one is started; the
// size is a threshold to roll at, not a hard cap, so a segment may slightly
// exceed it (the final record is never split). A chunk size of 0 disables
// rotation (a single ever-growing segment).
type AuditLog struct {
	mu         sync.Mutex
	dir        string
	f          *os.File
	w          *bufio.Writer
	sync       SyncMode
	chunkBytes int64 // 0 => no rotation
	curBytes   int64 // bytes in the active segment
	seq        int   // active segment number
	stop       chan struct{}
	stopped    bool
}

// nowNano is indirected so tests can make timestamps deterministic.
var nowNano = func() int64 { return time.Now().UnixNano() }

const auditPrefix = "audit-"
const auditSuffix = ".log"

func segmentName(seq int) string { return fmt.Sprintf("%s%06d%s", auditPrefix, seq, auditSuffix) }

// latestSegment returns the highest existing segment number in dir and its size,
// or (0, 0) if there are none.
func latestSegment(dir string) (seq int, size int64) {
	matches, _ := filepath.Glob(filepath.Join(dir, auditPrefix+"*"+auditSuffix))
	for _, p := range matches {
		base := filepath.Base(p)
		num := strings.TrimSuffix(strings.TrimPrefix(base, auditPrefix), auditSuffix)
		n, err := strconv.Atoi(num)
		if err != nil || n <= seq {
			continue
		}
		seq = n
		if fi, err := os.Stat(p); err == nil {
			size = fi.Size()
		}
	}
	return
}

// NewAuditLog opens (creating if needed) the audit log in dir, resuming the
// latest segment. chunkMB is the rotation threshold in megabytes (0 disables
// rotation). If enabled is false it returns a no-op log (all methods are safe).
func NewAuditLog(dir string, enabled bool, sync SyncMode, chunkMB int) (*AuditLog, error) {
	if !enabled {
		return &AuditLog{}, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	a := &AuditLog{
		dir:  dir,
		sync: sync,
		stop: make(chan struct{}),
	}
	if chunkMB > 0 {
		a.chunkBytes = int64(chunkMB) << 20
	}
	seq, size := latestSegment(dir)
	if seq == 0 {
		seq = 1
	} else if a.chunkBytes > 0 && size >= a.chunkBytes {
		// the latest segment is already full; start the next one.
		seq++
	}
	if err := a.openSegment(seq); err != nil {
		return nil, err
	}
	if sync == SyncInterval {
		go a.flushLoop()
	}
	return a, nil
}

// openSegment opens (append mode) the given segment and makes it active.
func (a *AuditLog) openSegment(seq int) error {
	f, err := os.OpenFile(filepath.Join(a.dir, segmentName(seq)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	a.f = f
	a.w = bufio.NewWriter(f)
	a.seq = seq
	a.curBytes = 0
	if fi, err := f.Stat(); err == nil {
		a.curBytes = fi.Size()
	}
	return nil
}

func (a *AuditLog) flushLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
			a.mu.Lock()
			_ = a.flushLocked()
			a.mu.Unlock()
		}
	}
}

// Record appends one event. It is a no-op on a disabled log.
func (a *AuditLog) Record(e AuditEvent) {
	if a == nil || a.f == nil {
		return
	}
	if e.Time == 0 {
		e.Time = nowNano()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	n, _ := a.w.Write(b)
	_ = a.w.WriteByte('\n')
	a.curBytes += int64(n + 1)
	if a.sync == SyncAlways {
		_ = a.flushLocked()
	}
	if a.chunkBytes > 0 && a.curBytes >= a.chunkBytes {
		_ = a.rotateLocked()
	}
}

// rotateLocked flushes and closes the active segment and opens the next one.
func (a *AuditLog) rotateLocked() error {
	_ = a.flushLocked()
	_ = a.f.Close()
	return a.openSegment(a.seq + 1)
}

func (a *AuditLog) flushLocked() error {
	if err := a.w.Flush(); err != nil {
		return err
	}
	if a.sync != SyncNever {
		return a.f.Sync()
	}
	return nil
}

// Close flushes and closes the audit log.
func (a *AuditLog) Close() error {
	if a == nil || a.f == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return nil
	}
	a.stopped = true
	if a.stop != nil {
		close(a.stop)
	}
	_ = a.w.Flush()
	_ = a.f.Sync()
	return a.f.Close()
}
