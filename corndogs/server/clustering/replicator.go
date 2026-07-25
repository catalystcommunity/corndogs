// Package clustering wires the three Tier-1 halves together: the cluster.Node
// election state machine, the filestore replication primitives (capture / apply /
// snapshot / log), and a pluggable message transport. The Replicator is the
// coordinator that turns "I am the leader / a follower" plus a stream of frames
// into replicated, semi-sync-durable writes. See docs/clustering-tier1.md.
//
// Like cluster.Node, the Replicator is driven (Tick / Recv) rather than
// self-running, and it talks to peers only through the Transport seam — so the
// whole data plane is testable over a deterministic in-memory network with real
// BoltStores, exactly like the election core.
package clustering

import (
	"bytes"

	"github.com/CatalystCommunity/corndogs/corndogs/server/cluster"
	"github.com/CatalystCommunity/corndogs/corndogs/server/store/filestore"
)

// FrameKind tags a data-plane frame.
type FrameKind uint8

const (
	// FrameMsg carries a cluster.Node election/heartbeat/ack/bid message.
	FrameMsg FrameKind = iota
	// FrameBatch ships one replicated MutationBatch leader→follower.
	FrameBatch
	// FrameCatchupReq asks the leader to resend everything after AfterLSN.
	FrameCatchupReq
	// FrameSnapshotReq asks the leader for a full snapshot (follower too far behind
	// or diverged and rolling back).
	FrameSnapshotReq
	// FrameSnapshot delivers a snapshot + its LSN.
	FrameSnapshot

	// --- transport-level control frames (handled by the TCP transport, never by the
	// Replicator) ---

	// FrameHello is the first frame on a peer connection: the sender advertises its
	// id (From) and its client-facing RPC URL (Addr) so nodes can report the current
	// leader's RPC address to watching clients.
	FrameHello
	// FrameSubscribe marks a connection as a topology watcher; the node then pushes
	// FrameTopology on it immediately and on every leadership change (real server
	// push — no polling).
	FrameSubscribe
	// FrameTopology is a pushed cluster view: From = leader id, Addr = leader RPC
	// URL, Epoch = leadership epoch.
	FrameTopology
)

// Frame is one message on the cluster transport. Exactly one payload is set per
// Kind.
type Frame struct {
	Kind        FrameKind
	From, To    string
	Msg         cluster.Message
	Batch       filestore.MutationBatch
	AfterLSN    uint64
	Snapshot    []byte
	SnapshotLSN uint64

	// Control-frame fields (FrameHello / FrameTopology): Addr is the advertised or
	// leader RPC URL; Epoch stamps a topology view.
	Addr  string
	Epoch uint64
}

// Transport delivers frames to peers. Production supplies a CSIL-RPC/StreamCarrier
// implementation; tests supply an in-memory network.
type Transport interface {
	Send(Frame)
}

// Replicator coordinates one node's election + replication.
type Replicator struct {
	id       string
	node     *cluster.Node
	store    *filestore.BoltStore
	log      *filestore.ReplLog
	tr       Transport
	ackCount int

	// leader: highest LSN a durability quorum has reached (the semi-sync commit
	// point). Reads of Committed compare against this.
	committedLSN uint64
	wasLeader    bool
}

// New builds a Replicator. The store's leader-side capture is wired to append to
// the replication log and ship to followers. ackCount is the semi-sync durability
// quorum (default ⌊N/2⌋ for split-brain safety; see docs §6).
func New(id string, node *cluster.Node, store *filestore.BoltStore, log *filestore.ReplLog, tr Transport, ackCount int) *Replicator {
	r := &Replicator{id: id, node: node, store: store, log: log, tr: tr, ackCount: ackCount}
	store.EnableReplication(log.LastLSN(), r.onCaptured)
	return r
}

// onCaptured runs (synchronously, under the store write lock) for each batch the
// leader commits locally: persist it to the log and ship it to every live
// follower.
func (r *Replicator) onCaptured(b filestore.MutationBatch) {
	_ = r.log.Append(b)
	// Advance the election node's head so heartbeats advertise the true LSN; that is
	// how followers learn they are behind and pull (below).
	r.node.SetAppliedLSN(b.LSN)
	for _, f := range r.node.LiveFollowers() {
		r.tr.Send(Frame{Kind: FrameBatch, From: r.id, To: f, Batch: b})
	}
}

// Tick advances the node's clock and flushes any election traffic; on the leader
// it recomputes the semi-sync commit point.
func (r *Replicator) Tick(now int64) {
	r.node.Tick(now)
	r.drainNode()
	r.refreshCommitted()
}

func (r *Replicator) drainNode() {
	for _, m := range r.node.TakeOutbox() {
		r.tr.Send(Frame{Kind: FrameMsg, From: r.id, To: m.To, Msg: m})
	}
}

func (r *Replicator) refreshCommitted() {
	if r.node.Role() == cluster.RoleLeader {
		if !r.wasLeader {
			// Freshly elected: everything already in our log is committed on us.
			r.committedLSN = r.store.ReplLSN()
			r.wasLeader = true
		}
		if d := r.node.DurableLSN(r.ackCount); d > r.committedLSN {
			r.committedLSN = d
		}
	} else {
		r.wasLeader = false
	}
}

// Recv handles one inbound frame at time now.
func (r *Replicator) Recv(now int64, f Frame) {
	switch f.Kind {
	case FrameMsg:
		r.node.Recv(now, f.Msg)
		r.drainNode()
		r.refreshCommitted()
		r.maybeCatchUp()
	case FrameBatch:
		r.applyIncoming(now, f)
	case FrameCatchupReq:
		r.serveCatchup(f)
	case FrameSnapshotReq:
		r.serveSnapshot(f)
	case FrameSnapshot:
		r.restore(f)
	}
}

// applyIncoming applies a replicated batch on a follower, requesting catch-up on a
// gap and immediately acking its new applied position so the leader's semi-sync
// commit point advances promptly (rather than only at heartbeat cadence).
func (r *Replicator) applyIncoming(now int64, f Frame) {
	have := r.store.ReplLSN()
	switch {
	case f.Batch.LSN <= have:
		// Duplicate/old; already applied.
	case f.Batch.LSN == have+1:
		if err := r.store.ApplyReplicated(f.Batch); err != nil {
			return
		}
		r.node.SetAppliedLSN(f.Batch.LSN)
		r.ackLeader()
	default:
		// Gap: ask the leader to resend from where we are. If we are so far behind
		// the leader has truncated its log, it will answer with a snapshot instead.
		r.tr.Send(Frame{Kind: FrameCatchupReq, From: r.id, To: f.From, AfterLSN: have})
	}
}

// maybeCatchUp asks the leader to resend missing batches when a heartbeat reveals
// the leader's head is ahead of our applied position (a batch was never shipped to
// us, or was lost). Catch-up replays are idempotent, so an occasional redundant
// request is harmless.
func (r *Replicator) maybeCatchUp() {
	if r.node.Role() == cluster.RoleLeader {
		return
	}
	leader, _ := r.node.Leader()
	if leader == "" || leader == r.id {
		return
	}
	if r.node.LeaderHeadLSN() > r.store.ReplLSN() {
		r.tr.Send(Frame{Kind: FrameCatchupReq, From: r.id, To: leader, AfterLSN: r.store.ReplLSN()})
	}
}

// ackLeader sends the current leader an immediate applied-LSN ack.
func (r *Replicator) ackLeader() {
	leader, epoch := r.node.Leader()
	if leader == "" || leader == r.id {
		return
	}
	r.tr.Send(Frame{Kind: FrameMsg, From: r.id, To: leader, Msg: cluster.Message{
		Type: cluster.MsgHeartbeatAck, From: r.id, To: leader, Epoch: epoch, AckLSN: r.store.ReplLSN(),
	}})
}

// serveCatchup (leader) resends log batches after the requested LSN. If the
// requested point predates the log (truncated), it falls back to a snapshot.
func (r *Replicator) serveCatchup(f Frame) {
	if r.node.Role() != cluster.RoleLeader {
		return
	}
	sent := false
	_ = r.log.ReadFrom(f.AfterLSN, func(b filestore.MutationBatch) error {
		r.tr.Send(Frame{Kind: FrameBatch, From: r.id, To: f.From, Batch: b})
		sent = true
		return nil
	})
	if !sent && f.AfterLSN < r.store.ReplLSN() {
		r.serveSnapshot(f)
	}
}

// serveSnapshot (leader) ships a consistent snapshot to the requester.
func (r *Replicator) serveSnapshot(f Frame) {
	if r.node.Role() != cluster.RoleLeader {
		return
	}
	var buf bytes.Buffer
	lsn, err := r.store.SnapshotTo(&buf)
	if err != nil {
		return
	}
	r.tr.Send(Frame{Kind: FrameSnapshot, From: r.id, To: f.From, Snapshot: buf.Bytes(), SnapshotLSN: lsn})
}

// restore (follower) installs a snapshot — the rejoin-rollback path: it discards
// any diverged local state and re-bases on the leader's snapshot.
func (r *Replicator) restore(f Frame) {
	if err := r.store.RestoreSnapshot(bytes.NewReader(f.Snapshot), f.SnapshotLSN); err != nil {
		return
	}
	r.node.SetAppliedLSN(f.SnapshotLSN)
	r.ackLeader()
}

// --- client-facing API -----------------------------------------------------

// Node exposes the underlying election node (role/leader queries).
func (r *Replicator) Node() *cluster.Node { return r.node }

// IsLeader reports whether this node may accept writes.
func (r *Replicator) IsLeader() bool { return r.node.Role() == cluster.RoleLeader }

// CanCommit reports whether the leader currently sees enough live followers to
// reach the semi-sync durability quorum. When it does not, a proposed write would
// apply locally (and to at most a minority) but never commit — leaving this node's
// state diverged from what the client is told. The engine checks this *before*
// applying a write so it can reject cleanly (no local mutation) rather than
// claim-a-task-then-time-out. ackCount<=0 (async / single-node) always commits.
// This closes the dominant partitioned-minority case; a quorum lost in the narrow
// window after this check still relies on the commit timeout + rejoin rollback.
func (r *Replicator) CanCommit() bool {
	if r.ackCount <= 0 {
		return true
	}
	return len(r.node.LiveFollowers()) >= r.ackCount
}

// CommittedLSN is the semi-sync commit point: writes at or below it are durable on
// a quorum and safe to acknowledge to the client.
func (r *Replicator) CommittedLSN() uint64 { return r.committedLSN }

// Committed reports whether the write that produced lsn has reached the semi-sync
// quorum and may be acked to the client.
func (r *Replicator) Committed(lsn uint64) bool { return r.committedLSN >= lsn }

// LastLSN is this node's local replication position.
func (r *Replicator) LastLSN() uint64 { return r.store.ReplLSN() }

// RequestRollback makes a follower ask the current leader for a fresh snapshot and
// re-base on it — used when a demoted ex-leader detects its local history diverged
// from the winner (a higher epoch it now follows).
func (r *Replicator) RequestRollback() {
	leader, _ := r.node.Leader()
	if leader == "" || leader == r.id {
		return
	}
	r.tr.Send(Frame{Kind: FrameSnapshotReq, From: r.id, To: leader})
}
