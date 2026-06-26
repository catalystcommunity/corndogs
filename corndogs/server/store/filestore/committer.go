package filestore

import (
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"
)

// writeReq is one pending write routed through the group committer.
type writeReq struct {
	fn  func(tx *bolt.Tx) error
	res chan error
}

// committer implements group commit (a.k.a. commit batching) for bbolt.
//
// The problem with sync=always is that every claim pays its own fsync (~7ms on
// real disk), so N concurrent writers cost N fsyncs even though they could share
// one. The problem with sync=interval is that it acks a write before it is on
// disk. Group commit gets both properties: a single committer goroutine gathers
// every write that is in flight, applies them all inside ONE bbolt transaction
// (which fsyncs exactly once on commit), and only then releases each caller. So
// every caller is acked durably, but the fsync cost is amortized across the
// whole batch — one disk flush covers many messages.
//
// The batch is self-tuning: while a commit/fsync is running, new writes queue
// up, and the next iteration commits all of them together. Under load the batch
// naturally grows to "however many arrived during the last fsync"; with no load
// it degrades to one-write-per-fsync (i.e. sync=always). An optional maxDelay
// adds a small linger to widen batches under bursty/low concurrency.
type committer struct {
	db       *bolt.DB
	ops      chan writeReq
	maxBatch int
	maxDelay time.Duration
	stop     chan struct{}

	// stats (atomic) for observability / benchmarking.
	batches   int64
	totalOps  int64
	maxObserv int64
}

func newCommitter(db *bolt.DB, maxBatch int, maxDelay time.Duration) *committer {
	if maxBatch <= 0 {
		maxBatch = 1 << 30 // effectively unbounded
	}
	c := &committer{
		db:       db,
		ops:      make(chan writeReq, 1024),
		maxBatch: maxBatch,
		maxDelay: maxDelay,
		stop:     make(chan struct{}),
	}
	go c.run()
	return c
}

// submit enqueues a write and blocks until it is committed (durably) or fails.
func (c *committer) submit(fn func(tx *bolt.Tx) error) error {
	res := make(chan error, 1)
	c.ops <- writeReq{fn: fn, res: res}
	return <-res
}

func (c *committer) run() {
	for {
		var first writeReq
		select {
		case <-c.stop:
			return
		case first = <-c.ops:
		}
		batch := c.collect(first)

		// Apply the whole batch in one transaction => one fsync on commit.
		// Per-op errors are reported individually; the transaction itself still
		// commits the successful ops. Our op funcs never mutate before a possible
		// error (they read/marshal first), so a failing op leaves no partial state.
		errs := make([]error, len(batch))
		commitErr := c.db.Update(func(tx *bolt.Tx) error {
			for i := range batch {
				errs[i] = batch[i].fn(tx)
			}
			return nil
		})
		for i := range batch {
			if commitErr != nil {
				batch[i].res <- commitErr
			} else {
				batch[i].res <- errs[i]
			}
		}

		atomic.AddInt64(&c.batches, 1)
		atomic.AddInt64(&c.totalOps, int64(len(batch)))
		if n := int64(len(batch)); n > atomic.LoadInt64(&c.maxObserv) {
			atomic.StoreInt64(&c.maxObserv, n)
		}
	}
}

// collect gathers first plus as many already-queued (or, with maxDelay, soon-to-
// arrive) writes as possible, up to maxBatch.
func (c *committer) collect(first writeReq) []writeReq {
	batch := make([]writeReq, 1, 64)
	batch[0] = first
	if c.maxDelay > 0 {
		timer := time.NewTimer(c.maxDelay)
		defer timer.Stop()
		for len(batch) < c.maxBatch {
			select {
			case op := <-c.ops:
				batch = append(batch, op)
			case <-timer.C:
				return batch
			}
		}
		return batch
	}
	for len(batch) < c.maxBatch {
		select {
		case op := <-c.ops:
			batch = append(batch, op)
		default:
			return batch
		}
	}
	return batch
}

// avgBatch returns the mean batch size observed so far (1.0 == no batching).
func (c *committer) avgBatch() float64 {
	b := atomic.LoadInt64(&c.batches)
	if b == 0 {
		return 0
	}
	return float64(atomic.LoadInt64(&c.totalOps)) / float64(b)
}

func (c *committer) close() { close(c.stop) }
