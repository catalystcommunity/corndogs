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
	zlog "github.com/rs/zerolog/log"
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

	// AfterEpoch is the epoch of the requester's batch at AfterLSN, so the leader
	// can check that the requester's history matches its own. SnapshotEpoch is
	// the epoch of the snapshot position.
	AfterEpoch    uint64
	SnapshotEpoch uint64

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
//
// Every node records a replication position (epoch, LSN) with its data: a
// follower in the bbolt transaction that applies a batch, a leader in its
// replication log. At start, New takes the newer of the two. The leader checks
// each follower's position against its own history before it counts the
// follower's acknowledgements or sends it log batches. A follower with a
// different history is rolled back with a snapshot. A follower with newer
// history makes the leader step down, so the next election (which prefers the
// newest position) keeps that history.
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

	// needSnapshot: the local data has no known position (or a history the
	// leader does not share), so this node accepts only a leader snapshot.
	needSnapshot bool
	now          int64
	snapAskedAt  int64            // follower: when it last asked for a snapshot
	snapSentAt   map[string]int64 // leader: when it last sent each follower a snapshot
}

// snapshotGapTicks limits how often one follower asks for, or receives, a
// snapshot. A snapshot copies the whole database, so repeated requests for the
// same rollback must not repeat that work every heartbeat.
const snapshotGapTicks = 20

// New builds a Replicator. The store's leader-side capture is wired to append to
// the replication log and ship to followers. ackCount is the semi-sync durability
// quorum (default ⌊N/2⌋ for split-brain safety; see docs §6).
func New(id string, node *cluster.Node, store *filestore.BoltStore, log *filestore.ReplLog, tr Transport, ackCount int) *Replicator {
	r := &Replicator{id: id, node: node, store: store, log: log, tr: tr, ackCount: ackCount, snapSentAt: map[string]int64{}}
	pos, known := r.recoverPosition()
	store.SetReplicationTag(func() uint64 { return cluster.HistoryTag(node.Epoch(), node.MemberIndex()) })
	store.EnableReplication(pos.LSN, r.onCaptured)
	node.SetPosition(pos)
	if !known {
		r.needSnapshot = true
		node.HoldJoining()
	}
	return r
}

// recoverPosition finds the replication position of the local data. The stored
// position (written by follower applies and before snapshots) and the log head
// (written by leader appends) can each be newer, depending on the node's last
// role; the newer one describes the data. When the log does not end at that
// position, it is reset to continue from it. known is false when the data has
// tasks but no recorded position, for example a follower from a release that did
// not record positions: LSN 0 would be wrong, so the node must take a snapshot.
func (r *Replicator) recoverPosition() (pos cluster.Position, known bool) {
	logPos := cluster.Position{LastEpoch: r.log.LastEpoch(), LSN: r.log.LastLSN()}
	pos = logPos
	lsn, ep, ok, err := filestore.ReadReplPosition(r.store.DB())
	if err != nil {
		zlog.Error().Err(err).Str("node", r.id).Msg("clustering: cannot read the stored replication position")
		return cluster.Position{}, false
	}
	if ok {
		if stored := (cluster.Position{LastEpoch: ep, LSN: lsn}); logPos.Less(stored) {
			pos = stored
		}
	}
	if pos.LSN == 0 {
		if has, herr := filestore.HasTaskData(r.store.DB()); herr != nil || has {
			return cluster.Position{}, false
		}
	}
	if pos != logPos {
		if err := r.log.Reset(pos.LSN+1, pos.LastEpoch); err != nil {
			zlog.Error().Err(err).Str("node", r.id).Msg("clustering: cannot reset the replication log")
			return cluster.Position{}, false
		}
	}
	return pos, true
}

// onCaptured runs (synchronously, under the store write lock) for each batch the
// leader commits locally: persist it to the log and ship it to every live
// follower.
func (r *Replicator) onCaptured(b filestore.MutationBatch) {
	r.appendLog(b)
	// Advance the election node's head so heartbeats advertise the true LSN; that is
	// how followers learn they are behind and pull (below).
	r.node.SetPosition(cluster.Position{LastEpoch: b.Epoch, LSN: b.LSN})
	for _, f := range r.node.LiveFollowers() {
		r.tr.Send(Frame{Kind: FrameBatch, From: r.id, To: f, Batch: b})
	}
}

// appendLog adds b to the replication log. If the log cannot continue at b (for
// example after an I/O error), the log restarts at b; followers behind that
// point then receive a snapshot.
func (r *Replicator) appendLog(b filestore.MutationBatch) {
	if err := r.log.Append(b); err != nil {
		zlog.Warn().Err(err).Str("node", r.id).Uint64("lsn", b.LSN).Msg("clustering: replication log append failed; restarting the log")
		if rerr := r.log.Reset(b.LSN+1, b.Epoch); rerr != nil {
			zlog.Error().Err(rerr).Str("node", r.id).Msg("clustering: cannot reset the replication log")
		}
	}
}

// Tick advances the node's clock and flushes any election traffic; on the leader
// it recomputes the semi-sync commit point.
func (r *Replicator) Tick(now int64) {
	r.now = now
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
			r.snapSentAt = map[string]int64{}
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
	r.now = now
	switch f.Kind {
	case FrameMsg:

		if f.Msg.Type == cluster.MsgHeartbeatAck && r.node.Role() == cluster.RoleLeader && f.Msg.Epoch == r.node.Epoch() {
			// Count an acknowledgement only from a follower whose history matches.
			if !r.admitFollower(f.From, cluster.Position{LastEpoch: f.Msg.LastEpoch, LSN: f.Msg.AckLSN}) {
				return
			}
		}
		r.node.Recv(now, f.Msg)
		r.drainNode()
		r.refreshCommitted()
		r.maybeCatchUp()
	case FrameBatch:
		r.applyIncoming(f)
	case FrameCatchupReq:
		r.serveCatchup(f)
	case FrameSnapshotReq:
		r.serveSnapshot(f.From)
	case FrameSnapshot:
		r.restore(f)
	}
}

// admitFollower (leader) reports whether a follower at pos holds a prefix of
// this leader's history. If the follower is ahead, the leader steps down. If the
// histories differ, or the log no longer holds the follower's position, the
// follower receives a snapshot.
func (r *Replicator) admitFollower(from string, pos cluster.Position) bool {
	if r.node.Position().Less(pos) {
		zlog.Warn().Str("node", r.id).Str("follower", from).Uint64("follower_lsn", pos.LSN).
			Uint64("follower_tag", pos.LastEpoch).Msg("clustering: follower holds newer history; stepping down")
		r.node.StepDown()
		r.drainNode()
		return false
	}
	if ep, ok := r.log.EpochAt(pos.LSN); ok && ep == pos.LastEpoch {
		return true
	}
	r.serveSnapshot(from)
	return false
}

// applyIncoming applies a replicated batch on a follower, requesting catch-up on a
// gap and immediately acking its new applied position so the leader's semi-sync
// commit point advances promptly (rather than only at heartbeat cadence).
func (r *Replicator) applyIncoming(f Frame) {
	if leader, _ := r.node.Leader(); leader == "" || f.From != leader {
		return // only the current leader's stream is applied
	}
	if r.needSnapshot {
		r.requestSnapshot()
		return
	}
	have := r.store.ReplLSN()
	switch {
	case f.Batch.LSN <= have:
		// Duplicate/old; already applied.
	case f.Batch.LSN == have+1:
		// ApplyReplicated records the position in the same transaction as the data.
		if err := r.store.ApplyReplicated(f.Batch); err != nil {
			zlog.Error().Err(err).Str("node", r.id).Uint64("lsn", f.Batch.LSN).Msg("clustering: cannot apply a replicated batch")
			return
		}
		r.appendLog(f.Batch)
		r.node.SetPosition(cluster.Position{LastEpoch: f.Batch.Epoch, LSN: f.Batch.LSN})
		r.ackLeader()
	default:
		// Gap: ask the leader to resend from where we are. If the leader cannot
		// continue our history from its log, it answers with a snapshot instead.
		r.tr.Send(Frame{Kind: FrameCatchupReq, From: r.id, To: f.From, AfterLSN: have, AfterEpoch: r.node.Position().LastEpoch})
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
	if r.needSnapshot {
		r.requestSnapshot()
		return
	}
	if r.node.LeaderHeadLSN() > r.store.ReplLSN() {
		r.tr.Send(Frame{Kind: FrameCatchupReq, From: r.id, To: leader, AfterLSN: r.store.ReplLSN(), AfterEpoch: r.node.Position().LastEpoch})
	}
}

// requestSnapshot (follower) asks the current leader for a snapshot, at most
// once per snapshotGapTicks.
func (r *Replicator) requestSnapshot() {
	leader, _ := r.node.Leader()
	if leader == "" || leader == r.id {
		return
	}
	if r.snapAskedAt != 0 && r.now-r.snapAskedAt < snapshotGapTicks {
		return
	}
	r.snapAskedAt = r.now
	r.tr.Send(Frame{Kind: FrameSnapshotReq, From: r.id, To: leader})
}

// ackLeader sends the current leader an immediate applied-position ack.
func (r *Replicator) ackLeader() {
	leader, epoch := r.node.Leader()
	if leader == "" || leader == r.id {
		return
	}
	pos := r.node.Position()
	r.tr.Send(Frame{Kind: FrameMsg, From: r.id, To: leader, Msg: cluster.Message{
		Type: cluster.MsgHeartbeatAck, From: r.id, To: leader, Epoch: epoch, AckLSN: pos.LSN, LastEpoch: pos.LastEpoch,
	}})
}

// serveCatchup (leader) resends log batches after the requested position. It
// sends a snapshot instead when the requester's history differs from the log,
// or when the log no longer holds the batches that follow the requester.
func (r *Replicator) serveCatchup(f Frame) {
	if r.node.Role() != cluster.RoleLeader {
		return
	}
	pos := cluster.Position{LastEpoch: f.AfterEpoch, LSN: f.AfterLSN}
	if !r.admitFollower(f.From, pos) {
		return
	}
	if f.AfterLSN >= r.store.ReplLSN() {
		return // nothing to send
	}
	if f.AfterLSN+1 < r.log.FirstLSN() {
		r.serveSnapshot(f.From)
		return
	}
	err := r.log.ReadFrom(f.AfterLSN, func(b filestore.MutationBatch) error {
		r.tr.Send(Frame{Kind: FrameBatch, From: r.id, To: f.From, Batch: b})
		return nil
	})
	if err != nil {
		zlog.Warn().Err(err).Str("node", r.id).Msg("clustering: cannot read the replication log; sending a snapshot")
		r.serveSnapshot(f.From)
	}
}

// serveSnapshot (leader) ships a consistent snapshot to the requester, at most
// once per snapshotGapTicks per requester. The leader first records its
// position in the database, so the snapshot carries that position.
func (r *Replicator) serveSnapshot(to string) {
	if r.node.Role() != cluster.RoleLeader {
		return
	}
	if last, ok := r.snapSentAt[to]; ok && r.now-last < snapshotGapTicks {
		return
	}
	r.snapSentAt[to] = r.now
	pos := r.node.Position()
	// All clustered writes run on the engine goroutine, as this call does, so no
	// write can change the data between this stamp and the snapshot.
	if err := filestore.StampReplPosition(r.store.DB(), pos.LSN, pos.LastEpoch); err != nil {
		zlog.Error().Err(err).Str("node", r.id).Msg("clustering: cannot record the snapshot position")
		return
	}
	var buf bytes.Buffer
	lsn, err := r.store.SnapshotTo(&buf)
	if err != nil {
		zlog.Error().Err(err).Str("node", r.id).Msg("clustering: cannot take a snapshot")
		return
	}
	r.tr.Send(Frame{Kind: FrameSnapshot, From: r.id, To: to, Snapshot: buf.Bytes(), SnapshotLSN: lsn, SnapshotEpoch: pos.LastEpoch})
}

// restore (follower) installs a snapshot from the current leader — the rollback
// path: it discards any diverged or unknown local state, re-bases on the leader's
// snapshot, and restarts its log at the snapshot position.
func (r *Replicator) restore(f Frame) {
	if leader, _ := r.node.Leader(); leader == "" || f.From != leader {
		return
	}
	if err := r.store.RestoreSnapshot(bytes.NewReader(f.Snapshot), f.SnapshotLSN); err != nil {
		zlog.Error().Err(err).Str("node", r.id).Msg("clustering: cannot restore a snapshot")
		return
	}
	// A snapshot from a leader that did not record its position has none.
	if err := filestore.StampReplPosition(r.store.DB(), f.SnapshotLSN, f.SnapshotEpoch); err != nil {
		zlog.Error().Err(err).Str("node", r.id).Msg("clustering: cannot record the restored position")
		return
	}
	if err := r.log.Reset(f.SnapshotLSN+1, f.SnapshotEpoch); err != nil {
		zlog.Error().Err(err).Str("node", r.id).Msg("clustering: cannot reset the replication log")
	}
	r.needSnapshot = false
	r.node.SetPosition(cluster.Position{LastEpoch: f.SnapshotEpoch, LSN: f.SnapshotLSN})
	r.node.MarkCaughtUp()
	zlog.Info().Str("node", r.id).Uint64("lsn", f.SnapshotLSN).Uint64("tag", f.SnapshotEpoch).Msg("clustering: restored a leader snapshot")
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
	r.needSnapshot = true
	r.snapAskedAt = 0
	r.requestSnapshot()
}
