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
//
// Every Append reaches the operating system before it returns, so a killed
// process loses no appended batch. A power loss can still lose batches that were
// not fsynced; set fsync to prevent that. At open, a partial final frame is cut
// off, so later appends never follow unreadable bytes.
//
// The log also keeps an epoch index in the file "epochs": each line is
// "<firstLSN> <epoch>", and a "base <lsn> <epoch>" line records the position that
// precedes the first batch after Reset. EpochAt uses this index to compare a
// follower's history with the leader's history.
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

	firstSeg uint64 // first LSN of the oldest segment on disk

	epochs  []epochStart // ascending by first LSN
	baseLSN uint64       // the position before the first batch in the log
	baseEp  uint64
}

// epochStart records that batches from first onward have epoch ep, until the
// next entry.
type epochStart struct {
	first, ep uint64
}

const epochsFile = "epochs"

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
	if err := r.loadEpochs(); err != nil {
		return nil, err
	}
	if len(seqs) == 0 {
		// Fresh log: the first segment starts at LSN 1, unless an earlier Reset
		// recorded a base position.
		if err := r.openSegment(r.baseLSN + 1); err != nil {
			return nil, err
		}
		r.lastLSN = r.baseLSN
		r.firstSeg = r.baseLSN + 1
		return r, nil
	}
	// Recover lastLSN from the final segment, cut off a partial final frame, then
	// reopen the segment for appending.
	r.firstSeg = seqs[0]
	last := seqs[len(seqs)-1]
	lastLSN, err := recoverSegmentTail(filepath.Join(dir, replSegmentName(last)))
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

// recoverSegmentTail returns the LSN of the last complete batch in the segment at
// path. If the segment ends with a partial or unreadable frame, it truncates the
// file after the last complete batch. Without this, a later append would follow
// the partial bytes, and a reader would decode the new batch as part of the
// partial one.
func recoverSegmentTail(path string) (uint64, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	cr := &countingReader{r: bufio.NewReader(f)}
	var last uint64
	var good int64
	for {
		b, err := DecodeBatch(cr)
		if err == io.EOF {
			return last, nil
		}
		if err != nil {
			// A partial final frame, or bytes that do not decode, end the log at the
			// last complete batch.
			if terr := f.Truncate(good); terr != nil {
				return 0, terr
			}
			if serr := f.Sync(); serr != nil {
				return 0, serr
			}
			return last, nil
		}
		last = b.LSN
		good = cr.n
	}
}

// countingReader counts bytes consumed through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// loadEpochs reads the epoch index file, if it exists.
func (r *ReplLog) loadEpochs() error {
	data, err := os.ReadFile(filepath.Join(r.dir, epochsFile))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 3 && fields[0] == "base":
			lsn, err1 := strconv.ParseUint(fields[1], 10, 64)
			ep, err2 := strconv.ParseUint(fields[2], 10, 64)
			if err1 != nil || err2 != nil {
				continue // a torn final line
			}
			r.baseLSN, r.baseEp = lsn, ep
			r.epochs = nil
		case len(fields) == 2:
			first, err1 := strconv.ParseUint(fields[0], 10, 64)
			ep, err2 := strconv.ParseUint(fields[1], 10, 64)
			if err1 != nil || err2 != nil {
				continue // a torn final line
			}
			r.epochs = append(r.epochs, epochStart{first: first, ep: ep})
		}
	}
	return nil
}

// appendEpochLine durably adds one line to the epoch index. Epoch changes are
// rare, so the fsync cost is small.
func (r *ReplLog) appendEpochLine(line string) error {
	f, err := os.OpenFile(filepath.Join(r.dir, epochsFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// epochAtLocked returns the epoch of the batch at lsn, if the log knows it.
func (r *ReplLog) epochAtLocked(lsn uint64) (uint64, bool) {
	if lsn == 0 {
		return 0, true
	}
	if lsn == r.baseLSN {
		return r.baseEp, true
	}
	if lsn < r.baseLSN || lsn > r.lastLSN || lsn < r.firstLSNLocked() {
		return 0, false
	}
	ep, found := uint64(0), false
	for _, e := range r.epochs {
		if e.first > lsn {
			break
		}
		ep, found = e.ep, true
	}
	if !found {
		// Batches written before the log recorded epochs have epoch 0.
		return 0, true
	}
	return ep, true
}

// EpochAt returns the epoch of the batch at lsn. ok is false when the log does
// not hold that position.
func (r *ReplLog) EpochAt(lsn uint64) (uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.epochAtLocked(lsn)
}

// LastEpoch returns the epoch of the last batch (or of the base position when
// the log is empty).
func (r *ReplLog) LastEpoch() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep, _ := r.epochAtLocked(r.lastLSN)
	return ep
}

func (r *ReplLog) firstLSNLocked() uint64 {
	if r.firstSeg == 0 || r.firstSeg > r.lastLSN {
		return r.lastLSN + 1
	}
	return r.firstSeg
}

// FirstLSN returns the lowest LSN that ReadFrom can still return. It is
// LastLSN()+1 when the log holds no batch.
func (r *ReplLog) FirstLSN() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.firstLSNLocked()
}

// Reset discards every segment and the epoch index, and restarts the log so that
// the next Append has LSN nextLSN. baseEpoch is the epoch of the position
// nextLSN-1. Use it when the local data moved to a position that the log does
// not continue, for example after a snapshot restore.
func (r *ReplLog) Reset(nextLSN, baseEpoch uint64) error {
	if nextLSN == 0 {
		nextLSN = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f != nil {
		_ = r.w.Flush()
		_ = r.f.Close()
		r.f, r.w = nil, nil
	}
	seqs, err := listReplSegments(r.dir)
	if err != nil {
		return err
	}
	for _, s := range seqs {
		if err := os.Remove(filepath.Join(r.dir, replSegmentName(s))); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	// Write the new index to a temporary file, then rename it, so a crash leaves
	// either the old or the new index.
	tmp := filepath.Join(r.dir, epochsFile+".tmp")
	line := fmt.Sprintf("base %d %d\n", nextLSN-1, baseEpoch)
	if err := os.WriteFile(tmp, []byte(line), 0o644); err != nil {
		return err
	}
	if f, err := os.Open(tmp); err == nil {
		_ = f.Sync()
		f.Close()
	}
	if err := os.Rename(tmp, filepath.Join(r.dir, epochsFile)); err != nil {
		return err
	}
	r.epochs = nil
	r.baseLSN, r.baseEp = nextLSN-1, baseEpoch
	r.lastLSN = nextLSN - 1
	r.firstSeg = nextLSN
	return r.openSegment(nextLSN)
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
	if cur, _ := r.epochAtLocked(r.lastLSN); b.Epoch != cur {
		if err := r.appendEpochLine(fmt.Sprintf("%d %d", b.LSN, b.Epoch)); err != nil {
			return err
		}
		r.epochs = append(r.epochs, epochStart{first: b.LSN, ep: b.Epoch})
	}
	before := countingWriter{w: r.w}
	if err := EncodeBatch(&before, b); err != nil {
		return err
	}
	r.curBytes += before.n
	r.lastLSN = b.LSN
	// Hand the frame to the operating system now, so a killed process cannot lose
	// it or leave a partial frame from a half-flushed buffer.
	if err := r.w.Flush(); err != nil {
		return err
	}
	if r.fsync {
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
	epochs := append([]epochStart(nil), r.epochs...)
	r.mu.Unlock()

	seqs, err := listReplSegments(dir)
	if err != nil {
		return err
	}
	withEpoch := func(b MutationBatch) error {
		for _, e := range epochs {
			if e.first > b.LSN {
				break
			}
			b.Epoch = e.ep
		}
		return fn(b)
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
		if err := replaySegment(filepath.Join(dir, replSegmentName(s)), afterLSN, withEpoch); err != nil {
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
			r.mu.Lock()
			r.firstSeg = next
			r.mu.Unlock()
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
