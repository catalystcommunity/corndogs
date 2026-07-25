package clustering

import (
	"errors"
	"sync"
	"time"
)

// ErrNotLeader is returned for a write attempted on a non-leader node. The caller
// (RPC layer) turns it into a redirect to Leader().
var ErrNotLeader = errors.New("clustering: not the leader")

// ErrCommitTimeout is returned when a write applied locally on the leader but did
// not reach the semi-sync durability quorum in time (e.g. the leader lost contact
// with enough followers — the split-brain guard, docs §6).
var ErrCommitTimeout = errors.New("clustering: write not committed to quorum in time")

// Engine owns a Replicator and drives it on a single goroutine (the Node and
// Replicator are not thread-safe). Everything that touches them — inbound frames,
// ticks, and client writes — is funneled through that goroutine via channels, so
// there is no shared-memory concurrency on the protocol state. This is the actor
// model: the network and the RPC handlers are the outside world; the run loop is
// the actor.
type Engine struct {
	rep      *Replicator
	settings Settings
	tickDur  time.Duration

	frames    chan Frame
	proposals chan proposal
	stop      chan struct{}
	stopped   chan struct{}

	// commitWaiters: leader-side client writes blocked until their LSN reaches the
	// semi-sync commit point.
	waiters []commitWaiter
	now     int64

	// leadership edge tracking: leaderSince is the tick at which this node last
	// became leader (used to grant a freshly elected leader a grace window before the
	// pre-apply quorum check can reject writes — followers need a moment to send their
	// first ack). wasLeader mirrors the last observed leader role on this goroutine.
	leaderSince int64
	wasLeader   bool

	// topology + stats snapshots for the RPC/watch/metrics layer (guarded; read
	// off-goroutine, written only on the engine goroutine).
	mu    sync.RWMutex
	topo  Topology
	stats Stats

	// watch subscribers get signaled on any topology change.
	watchers map[chan struct{}]struct{}
}

type proposal struct {
	fn   func() error // performs the store write on the engine goroutine
	lsn0 uint64       // filled in-loop
	done chan error
}

type commitWaiter struct {
	lsn      uint64
	deadline int64
	done     chan error
}

// Topology is the cluster view: the current leader id and the epoch that stamps
// it. The leader's client RPC address is filled in by the transport (which learns
// peer RPC URLs) when it pushes this to a client.
type Topology struct {
	Epoch    uint64 `json:"epoch"`
	LeaderID string `json:"leader_id"`
}

// NewEngine builds an Engine around an already-constructed Replicator. tickDur is
// the wall-clock duration of one logical tick (the cluster Config timings are in
// ticks; ~100ms is the design cadence).
func NewEngine(rep *Replicator, settings Settings, tickDur time.Duration) *Engine {
	if tickDur <= 0 {
		tickDur = 100 * time.Millisecond
	}
	return &Engine{
		rep:       rep,
		settings:  settings,
		tickDur:   tickDur,
		frames:    make(chan Frame, 1024),
		proposals: make(chan proposal, 256),
		stop:      make(chan struct{}),
		stopped:   make(chan struct{}),
		watchers:  map[chan struct{}]struct{}{},
	}
}

// Deliver hands an inbound frame to the engine (called by the network receiver).
// Non-blocking: if the queue is full the frame is dropped, which the lossy
// protocol tolerates (heartbeats + catch-up recover).
func (e *Engine) Deliver(f Frame) {
	select {
	case e.frames <- f:
	default:
	}
}

// Run drives the engine until Stop. It is the only goroutine that touches the
// Replicator.
func (e *Engine) Run() {
	defer close(e.stopped)
	ticker := time.NewTicker(e.tickDur)
	defer ticker.Stop()
	e.publishTopology()
	for {
		select {
		case <-e.stop:
			e.failAllWaiters(ErrCommitTimeout)
			return
		case f := <-e.frames:
			e.rep.Recv(e.now, f)
			e.afterStep()
		case p := <-e.proposals:
			e.handleProposal(p)
			e.afterStep()
		case <-ticker.C:
			e.now++
			e.rep.Tick(e.now)
			e.afterStep()
		}
	}
}

// Stop shuts the engine down and waits for the loop to exit.
func (e *Engine) Stop() {
	select {
	case <-e.stop:
	default:
		close(e.stop)
	}
	<-e.stopped
}

func (e *Engine) handleProposal(p proposal) {
	if !e.rep.IsLeader() {
		p.done <- ErrNotLeader
		return
	}
	if e.quorumUnreachable() {
		// We have led long enough to have a current view of followers yet still cannot
		// reach the semi-sync durability quorum — a real partition, not the brief
		// post-election window. Applying now would persist locally (and to a minority)
		// but never commit, leaving state diverged from what the client is told (a
		// claimed-but-undelivered task, or a submit the client retries into a
		// duplicate). Reject *before* applying so the error truthfully means "nothing
		// happened" and a retry is safe (docs §6).
		p.done <- ErrCommitTimeout
		return
	}
	before := e.rep.LastLSN()
	if err := p.fn(); err != nil {
		p.done <- err
		return
	}
	lsn := e.rep.LastLSN()
	if lsn == before {
		// The op made no durable change (e.g. GetNextTask on an empty queue). Nothing
		// to replicate; release immediately.
		p.done <- nil
		return
	}
	e.waiters = append(e.waiters, commitWaiter{
		lsn:      lsn,
		deadline: e.now + e.commitTimeoutTicks(),
		done:     p.done,
	})
}

// commitTimeoutTicks bounds how long a write waits for quorum before failing — a
// few failure-timeouts, long enough to ride out a normal election but short enough
// that a partitioned minority write returns an error rather than hanging.
func (e *Engine) commitTimeoutTicks() int64 {
	return 3 * e.settings.Election.FailureTimeout
}

// quorumUnreachable reports that a proposed write should be rejected before it is
// applied because the semi-sync durability quorum is genuinely out of reach. It is
// deliberately conservative: a freshly elected leader has not yet received its
// followers' first heartbeat-ack, so CanCommit() is momentarily false even though
// the write would commit as soon as those acks arrive. During that grace window
// (one failure-timeout after becoming leader) we let the write through and rely on
// the commit waiter; only once we have led long enough for a true view of followers
// and still lack a quorum do we treat it as a partition and reject up front.
func (e *Engine) quorumUnreachable() bool {
	if e.rep.CanCommit() {
		return false
	}
	return e.now-e.leaderSince >= e.settings.Election.FailureTimeout
}

// afterStep releases any commit waiters the latest step satisfied (or timed out)
// and republishes topology if it changed.
func (e *Engine) afterStep() {
	// Track the leader-role edge so quorumUnreachable can grant a grace window from
	// the moment this node becomes leader.
	if isLeader := e.rep.IsLeader(); isLeader && !e.wasLeader {
		e.leaderSince = e.now
		e.wasLeader = true
	} else if !isLeader {
		e.wasLeader = false
	}

	committed := e.rep.CommittedLSN()
	kept := e.waiters[:0]
	for _, w := range e.waiters {
		switch {
		case e.rep.IsLeader() && committed >= w.lsn:
			w.done <- nil
		case e.now >= w.deadline || !e.rep.IsLeader():
			// Timed out, or we lost leadership before committing → the client must
			// retry (against the new leader).
			w.done <- ErrCommitTimeout
		default:
			kept = append(kept, w)
		}
	}
	e.waiters = kept
	e.publishTopology()
	e.refreshStats()
}

func (e *Engine) failAllWaiters(err error) {
	for _, w := range e.waiters {
		w.done <- err
	}
	e.waiters = nil
}

// Propose runs a store write through the engine goroutine and blocks until it is
// committed to the semi-sync quorum, returns ErrNotLeader, or times out. fn is the
// actual store operation (it executes on the engine goroutine, so its replication
// capture is race-free).
func (e *Engine) Propose(fn func() error) error {
	done := make(chan error, 1)
	select {
	case e.proposals <- proposal{fn: fn, done: done}:
	case <-e.stop:
		return ErrCommitTimeout
	}
	return <-done
}

// --- topology (read off-goroutine by the RPC/watch layer) ------------------

func (e *Engine) publishTopology() {
	leaderID, epoch := e.rep.Node().Leader()
	topo := Topology{Epoch: epoch, LeaderID: leaderID}
	e.mu.Lock()
	changed := topo.Epoch != e.topo.Epoch || topo.LeaderID != e.topo.LeaderID
	e.topo = topo
	if changed {
		for ch := range e.watchers {
			close(ch)
		}
		e.watchers = map[chan struct{}]struct{}{}
	}
	e.mu.Unlock()
}

// Topology returns the current cluster view.
func (e *Engine) Topology() Topology {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.topo
}

// IsLeader reports whether this node currently leads (safe to call off-goroutine;
// derived from the published topology).
func (e *Engine) IsLeader() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.topo.LeaderID == e.settings.NodeID
}

// WatchTopology returns a channel that closes when the topology (leader/epoch)
// next changes — the long-poll primitive behind the client's "leader changed"
// notification. The caller selects on it plus its own timeout.
func (e *Engine) WatchTopology() <-chan struct{} {
	ch := make(chan struct{})
	e.mu.Lock()
	e.watchers[ch] = struct{}{}
	e.mu.Unlock()
	return ch
}

// Stats is a point-in-time snapshot of this node's cluster state for operators /
// metrics. It is captured on the engine goroutine (via a proposal-style hop) so it
// reads the protocol state without a data race.
type Stats struct {
	NodeID       string `json:"node_id"`
	Role         string `json:"role"`
	IsLeader     bool   `json:"is_leader"`
	Epoch        uint64 `json:"epoch"`
	LeaderID     string `json:"leader_id"`
	AppliedLSN   uint64 `json:"applied_lsn"`
	CommittedLSN uint64 `json:"committed_lsn"`
	AckCount     int    `json:"ack_count"`
}

// Stats returns the latest snapshot, refreshed on the engine goroutine after every
// step (so it never races the protocol state).
func (e *Engine) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.stats
}

// refreshStats runs on the engine goroutine (from afterStep) and stores a race-free
// snapshot for off-goroutine readers.
func (e *Engine) refreshStats() {
	s := Stats{
		NodeID:       e.settings.NodeID,
		Role:         e.rep.Node().Role().String(),
		IsLeader:     e.rep.IsLeader(),
		Epoch:        e.rep.Node().Epoch(),
		LeaderID:     e.rep.Node().LeaderID(),
		AppliedLSN:   e.rep.LastLSN(),
		CommittedLSN: e.rep.CommittedLSN(),
		AckCount:     e.settings.DurabilityAckCount(),
	}
	e.mu.Lock()
	e.stats = s
	e.mu.Unlock()
}
