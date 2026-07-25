package filestore

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ReplLog is the leader's on-disk, LSN-ordered replication log: the durable record
// of the physical MutationBatches (replication.go) that followers replay. It is
// the data-plane analogue of the audit log and reuses the same segmented-file
// shape — size-bounded segments, resumable on restart — but its frames are the
// self-framing batch codec, not JSON audit lines.
//
// Segments are named repl-<firstLSN>.log, so the file name encodes the first LSN
// a segment holds; ReadFrom therefore seeks to the right segment by name without a
// separate index, and Truncate drops whole segments below a floor.
type ReplLog struct {
	mu         sync.Mutex
	dir        string
	f          *os.File
	w          *bufio.Writer
	firstSeq   uint64 // first LSN of the active segment
	curBytes   int64
	chunkBytes int64 // roll threshold; 0 => never roll
	lastLSN    uint64
	fsync      bool
}

const replPrefix = "repl-"
const replSuffix = ".log"

func replSegmentName(firstLSN uint64) string {
	return fmt.Sprintf("%s%020d%s", replPrefix, firstLSN, replSuffix)
}

func replSegmentFirstLSN(name string) (uint64, bool) {
	base := filepath.Base(name)
	s := strings.TrimSuffix(strings.TrimPrefix(base, replPrefix), replSuffix)
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// listReplSegments returns the segment first-LSNs present in dir, ascending.
func listReplSegments(dir string) ([]uint64, error) {
	matches, err := filepath.Glob(filepath.Join(dir, replPrefix+"*"+replSuffix))
	if err != nil {
		return nil, err
	}
	var seqs []uint64
	for _, p := range matches {
		if n, ok := replSegmentFirstLSN(p); ok {
			seqs = append(seqs, n)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	return seqs, nil
}

// OpenReplLog opens (creating if needed) the replication log in dir, resuming the
// latest segment and recovering lastLSN by scanning it. chunkMB is the segment
// roll threshold (0 disables rolling). fsync forces a flush+sync on every append.
func OpenReplLog(dir string, chunkMB int, fsync bool) (*ReplLog, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r := &ReplLog{dir: dir, fsync: fsync}
	if chunkMB > 0 {
		r.chunkBytes = int64(chunkMB) << 20
	}
	seqs, err := listReplSegments(dir)
	if err != nil {
		return nil, err
	}
	if len(seqs) == 0 {
		// Fresh log: the first segment starts at LSN 1.
		if err := r.openSegment(1); err != nil {
			return nil, err
		}
		return r, nil
	}
	// Recover lastLSN from the final segment, then reopen it for appending.
	last := seqs[len(seqs)-1]
	lastLSN, err := scanLastLSN(filepath.Join(dir, replSegmentName(last)))
	if err != nil {
		return nil, err
	}
	if err := r.openSegment(last); err != nil {
		return nil, err
	}
	if lastLSN != 0 {
		r.lastLSN = lastLSN
	} else {
		// Empty final segment; lastLSN is the previous segment's end (or 0).
		r.lastLSN = last - 1
	}
	return r, nil
}

func scanLastLSN(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	var last uint64
	for {
		b, err := DecodeBatch(br)
		if err == io.EOF {
			return last, nil
		}
		if err != nil {
			// A truncated tail (partial final frame) is tolerated: stop at the last
			// intact batch.
			if err == io.ErrUnexpectedEOF {
				return last, nil
			}
			return 0, err
		}
		last = b.LSN
	}
}

func (r *ReplLog) openSegment(firstLSN uint64) error {
	path := filepath.Join(r.dir, replSegmentName(firstLSN))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	r.f = f
	r.w = bufio.NewWriter(f)
	r.firstSeq = firstLSN
	r.curBytes = 0
	if fi, err := f.Stat(); err == nil {
		r.curBytes = fi.Size()
	}
	return nil
}

// Append writes one batch. LSNs must be gap-free and monotonic (b.LSN ==
// lastLSN+1), which is how the leader assigns them.
func (r *ReplLog) Append(b MutationBatch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b.LSN != r.lastLSN+1 {
		return fmt.Errorf("repllog: non-contiguous LSN %d (last %d)", b.LSN, r.lastLSN)
	}
	// Roll to a new segment (named by this batch's LSN) if the active one is full.
	if r.chunkBytes > 0 && r.curBytes >= r.chunkBytes {
		if err := r.w.Flush(); err != nil {
			return err
		}
		_ = r.f.Close()
		if err := r.openSegment(b.LSN); err != nil {
			return err
		}
	}
	before := countingWriter{w: r.w}
	if err := EncodeBatch(&before, b); err != nil {
		return err
	}
	r.curBytes += before.n
	r.lastLSN = b.LSN
	if r.fsync {
		if err := r.w.Flush(); err != nil {
			return err
		}
		return r.f.Sync()
	}
	return nil
}

// countingWriter counts bytes written through it (EncodeBatch flushes its own
// bufio, so we wrap the destination to measure frame size).
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// LastLSN returns the highest LSN appended.
func (r *ReplLog) LastLSN() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastLSN
}

// ReadFrom replays every batch with LSN > afterLSN, in order, to fn. This is how
// a follower catches up from a point (or streams live). It flushes pending writes
// first so the freshest batches are visible.
func (r *ReplLog) ReadFrom(afterLSN uint64, fn func(MutationBatch) error) error {
	r.mu.Lock()
	if r.w != nil {
		if err := r.w.Flush(); err != nil {
			r.mu.Unlock()
			return err
		}
	}
	dir := r.dir
	r.mu.Unlock()

	seqs, err := listReplSegments(dir)
	if err != nil {
		return err
	}
	// Start at the last segment whose firstLSN <= afterLSN+1 (it may contain the
	// first batch we want); earlier segments are entirely consumed.
	start := 0
	for i, s := range seqs {
		if s <= afterLSN+1 {
			start = i
		}
	}
	for _, s := range seqs[start:] {
		if err := replaySegment(filepath.Join(dir, replSegmentName(s)), afterLSN, fn); err != nil {
			return err
		}
	}
	return nil
}

func replaySegment(path string, afterLSN uint64, fn func(MutationBatch) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	for {
		b, err := DecodeBatch(br)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
		if b.LSN <= afterLSN {
			continue
		}
		if err := fn(b); err != nil {
			return err
		}
	}
}

// Truncate deletes whole segments that lie entirely below floorLSN (every LSN in
// them is < floorLSN), reclaiming space once all live followers are past them. The
// active segment is never removed.
func (r *ReplLog) Truncate(floorLSN uint64) error {
	r.mu.Lock()
	active := r.firstSeq
	dir := r.dir
	r.mu.Unlock()

	seqs, err := listReplSegments(dir)
	if err != nil {
		return err
	}
	for i, s := range seqs {
		if s == active {
			break
		}
		// Segment s spans [s, next-1]; safe to drop iff its whole range < floorLSN,
		// i.e. the next segment's firstLSN <= floorLSN.
		next := active
		if i+1 < len(seqs) {
			next = seqs[i+1]
		}
		if next <= floorLSN {
			if err := os.Remove(filepath.Join(dir, replSegmentName(s))); err != nil {
				return err
			}
		}
	}
	return nil
}

// Close flushes and closes the active segment.
func (r *ReplLog) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w != nil {
		_ = r.w.Flush()
	}
	if r.f != nil {
		_ = r.f.Sync()
		return r.f.Close()
	}
	return nil
}
