package cluster

import (
	"math/rand"
	"sort"
)

// Role is a node's current role in the cluster.
type Role uint8

const (
	// RoleJoining is a node that is not yet caught up (a fresh member or a
	// restarted one still replaying the mutation stream). It follows whatever leader
	// it hears but never stands for election. It becomes RoleFollower once caught up.
	RoleJoining Role = iota
	// RoleFollower is an in-sync member: it applies the stream, serves reads, and
	// may stand for election if the leader disappears.
	RoleFollower
	// RoleCandidate is collecting bids in an election round.
	RoleCandidate
	// RoleLeader is the single writer for its epoch.
	RoleLeader
)

func (r Role) String() string {
	switch r {
	case RoleJoining:
		return "joining"
	case RoleFollower:
		return "follower"
	case RoleCandidate:
		return "candidate"
	case RoleLeader:
		return "leader"
	default:
		return "unknown"
	}
}

// Node is the per-member cluster state machine. Leadership is decided by a
// highest-random-bid election rather than quorum voting: when the leader is gone,
// each in-sync node draws a random bid in [0, BidMax) and broadcasts it; the
// highest bid in the round (ties broken by node id) becomes leader. A running
// leader is left alone — a node that hears a current leader simply follows it.
//
// This favors availability: a 2- or 3-node cluster fails over on a crash with a
// fresh election, and there is no quorum requirement. The trade is that a true
// network partition (leader alive but unreachable) lets each side elect its own
// leader; on heal the higher epoch wins and the loser's partition-time writes are
// discarded. See docs/clustering-tier1.md §3.
//
// The Node is pure and tick-driven: no goroutines, clocks, or sockets. Time (now)
// and randomness (seed) are injected, so the protocol is deterministically
// testable over an in-memory network with fault injection.
type Node struct {
	id    string
	peers []string // sorted member ids INCLUDING self
	cfg   Config

	role     Role
	epoch    uint64
	leaderID string // current known leader; "" == none

	// Replication position. AppliedLSN is how far this node has applied the
	// mutation stream; leaderHeadLSN is the latest leader head we heard.
	appliedLSN    uint64
	leaderHeadLSN uint64

	// Timers, in Config's tick units.
	now         int64
	lastHeard   int64 // last time we heard the current leader (or concluded an election)
	standAt     int64 // when a leaderless node will stand for election
	nextHBAt    int64 // leader: next heartbeat
	heardLeader bool  // have we ever heard a leader heartbeat?

	// Election round state (RoleCandidate).
	myBid    float64
	bids     map[string]float64
	decideAt int64 // when the bid-collection window closes

	// Leader bookkeeping: follower liveness + replication position, learned from
	// heartbeat acks.
	winBid   float64 // the bid we won our epoch with (for same-epoch conflict resolution)
	ackedAt  map[string]int64
	ackedLSN map[string]uint64

	rnd    *rand.Rand
	outbox []Message
}

// NewNode constructs a member. peers must include this node's own id. The node
// starts in RoleJoining at the given AppliedLSN; MarkCaughtUp / SetAppliedLSN
// promote it once it is in sync. seed makes bids and jitter deterministic.
func NewNode(id string, peers []string, cfg Config, seed int64, appliedLSN uint64) *Node {
	ps := append([]string(nil), peers...)
	sort.Strings(ps)
	return &Node{
		id:         id,
		peers:      ps,
		cfg:        cfg,
		role:       RoleJoining,
		appliedLSN: appliedLSN,
		ackedAt:    map[string]int64{},
		ackedLSN:   map[string]uint64{},
		rnd:        rand.New(rand.NewSource(seed)),
	}
}

// --- accessors -------------------------------------------------------------

func (n *Node) ID() string         { return n.id }
func (n *Node) Role() Role         { return n.role }
func (n *Node) Epoch() uint64      { return n.epoch }
func (n *Node) AppliedLSN() uint64 { return n.appliedLSN }
func (n *Node) LeaderID() string   { return n.leaderID }

// Leader reports the currently known leader id and epoch ("" if none known).
func (n *Node) Leader() (string, uint64) { return n.leaderID, n.epoch }

// LeaderHeadLSN reports the most recent leader head AppliedLSN this node has heard
// (via heartbeat). A follower whose own AppliedLSN is below this is behind and
// should catch up.
func (n *Node) LeaderHeadLSN() uint64 { return n.leaderHeadLSN }

// Serving reports whether this node may accept writes right now (it is the
// leader). Note this design permits, under a partition, one serving leader per
// connected component — that is the accepted availability trade, not a bug.
func (n *Node) Serving() bool { return n.role == RoleLeader }

// Eligible reports whether this node may stand for election: in-sync (not
// RoleJoining and within MaxLagToVote of the last leader head).
func (n *Node) Eligible() bool {
	if n.role == RoleJoining {
		return false
	}
	return n.caughtUp()
}

func (n *Node) caughtUp() bool {
	if n.leaderHeadLSN <= n.appliedLSN {
		return true
	}
	return n.leaderHeadLSN-n.appliedLSN <= n.cfg.MaxLagToVote
}

// SetAppliedLSN advances this node's applied position (called by the replication
// layer). Catching up promotes a Joining node to Follower.
func (n *Node) SetAppliedLSN(lsn uint64) {
	if lsn > n.appliedLSN {
		n.appliedLSN = lsn
	}
	if n.role == RoleJoining && n.caughtUp() {
		n.role = RoleFollower
		n.armStandTimer()
	}
}

// MarkCaughtUp declares this node has finished catching up and may participate in
// elections. Called when snapshot+stream catch-up completes, or at startup for a
// fresh cluster.
func (n *Node) MarkCaughtUp() {
	if n.role == RoleJoining {
		n.role = RoleFollower
		n.armStandTimer()
	}
}

// TakeOutbox returns and clears pending outbound messages.
func (n *Node) TakeOutbox() []Message {
	if len(n.outbox) == 0 {
		return nil
	}
	out := n.outbox
	n.outbox = nil
	return out
}

func (n *Node) send(m Message) {
	m.From = n.id
	n.outbox = append(n.outbox, m)
}

func (n *Node) broadcast(m Message) {
	for _, p := range n.peers {
		if p == n.id {
			continue
		}
		mm := m
		mm.To = p
		n.send(mm)
	}
}

// armStandTimer schedules when a leaderless node will stand for election. Before
// any leader has ever been seen (fresh cluster) it stands almost immediately, so
// initial establishment takes ~ElectionWindow (well under a second); once a leader
// has existed, it waits a full FailureTimeout after last contact before deciding
// the leader is gone. Jitter staggers simultaneous starts.
func (n *Node) armStandTimer() {
	jitter := int64(0)
	if n.cfg.ElectionJitter > 0 {
		jitter = n.rnd.Int63n(n.cfg.ElectionJitter)
	}
	if !n.heardLeader {
		n.standAt = n.now + jitter // fast initial election
		return
	}
	n.standAt = n.lastHeard + n.cfg.FailureTimeout + jitter
}

// --- driving the machine ---------------------------------------------------

// Tick advances logical time to now and performs time-triggered work.
func (n *Node) Tick(now int64) {
	n.now = now
	switch n.role {
	case RoleLeader:
		if n.now >= n.nextHBAt {
			n.broadcastHeartbeat()
			n.nextHBAt = n.now + n.cfg.HeartbeatInterval
		}
	case RoleCandidate:
		if n.now >= n.decideAt {
			n.decideElection()
		}
	case RoleFollower:
		if n.now >= n.standAt {
			if n.Eligible() {
				n.startElection()
			} else {
				n.lastHeard = n.now
				n.armStandTimer()
			}
		}
	case RoleJoining:
		// Follows heartbeats passively; never stands for election.
	}
}

func (n *Node) broadcastHeartbeat() {
	n.broadcast(Message{Type: MsgHeartbeat, Epoch: n.epoch, LSN: n.appliedLSN, Bid: n.winBid})
}

// startElection opens a new bid-collection round.
func (n *Node) startElection() {
	n.epoch++
	n.role = RoleCandidate
	n.leaderID = ""
	n.myBid = n.rnd.Float64() * n.cfg.BidMax
	n.bids = map[string]float64{n.id: n.myBid}
	n.decideAt = n.now + n.cfg.ElectionWindow
	n.broadcast(Message{Type: MsgBid, Epoch: n.epoch, Bid: n.myBid})
	// Single-node cluster: nobody to hear from, decide immediately at the window.
}

// decideElection closes the collection window and applies the highest-bid rule.
func (n *Node) decideElection() {
	winner, _ := n.highestBidder()
	if winner == n.id {
		n.becomeLeader()
		return
	}
	// We lost (or expect someone else to win). Fall back to follower and give the
	// winner a full failure-timeout to assert itself; if it never heartbeats, we
	// stand again.
	n.role = RoleFollower
	n.leaderID = ""
	n.bids = nil
	n.lastHeard = n.now
	jitter := int64(0)
	if n.cfg.ElectionJitter > 0 {
		jitter = n.rnd.Int63n(n.cfg.ElectionJitter)
	}
	n.standAt = n.now + n.cfg.FailureTimeout + jitter
}

// highestBidder returns the id and bid of the winner among collected bids, ties
// broken by larger node id so every node computes the same winner.
func (n *Node) highestBidder() (string, float64) {
	bestID, bestBid := "", -1.0
	// Iterate deterministically.
	ids := make([]string, 0, len(n.bids))
	for id := range n.bids {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		b := n.bids[id]
		if b > bestBid || (b == bestBid && id > bestID) {
			bestID, bestBid = id, b
		}
	}
	return bestID, bestBid
}

func (n *Node) becomeLeader() {
	n.role = RoleLeader
	n.leaderID = n.id
	n.winBid = n.myBid
	n.heardLeader = true
	n.bids = nil
	n.ackedAt = map[string]int64{}
	n.ackedLSN = map[string]uint64{}
	n.nextHBAt = n.now + n.cfg.HeartbeatInterval
	n.broadcastHeartbeat()
}

// stepToFollower demotes to follower at the given epoch and arms the stand timer.
func (n *Node) stepToFollower(epoch uint64, leader string) {
	n.epoch = epoch
	n.role = RoleFollower
	n.leaderID = leader
	n.bids = nil
	n.lastHeard = n.now
	n.heardLeader = true
	n.armStandTimer()
}

// --- message handling ------------------------------------------------------

// Recv processes one inbound message at time now.
func (n *Node) Recv(now int64, m Message) {
	n.now = now
	switch m.Type {
	case MsgBid:
		n.handleBid(m)
	case MsgHeartbeat:
		n.handleHeartbeat(m)
	case MsgHeartbeatAck:
		n.handleHeartbeatAck(m)
	}
}

func (n *Node) handleBid(m Message) {
	switch {
	case m.Epoch < n.epoch:
		// Stale round; ignore. Our newer heartbeat/bid will bring them forward.
	case m.Epoch > n.epoch:
		// A newer election round. Join it: adopt the epoch and, if we are eligible,
		// draw our own bid and compete; otherwise just track the round and follow
		// whoever wins.
		n.epoch = m.Epoch
		n.leaderID = ""
		if n.Eligible() {
			n.role = RoleCandidate
			n.myBid = n.rnd.Float64() * n.cfg.BidMax
			n.bids = map[string]float64{n.id: n.myBid, m.From: m.Bid}
			n.decideAt = n.now + n.cfg.ElectionWindow
			n.broadcast(Message{Type: MsgBid, Epoch: n.epoch, Bid: n.myBid})
		} else {
			// Not eligible to win (still joining/behind): track the round and follow
			// whoever emerges. A joining node stays joining.
			if n.role != RoleJoining {
				n.role = RoleFollower
			}
			n.bids = nil
			n.lastHeard = n.now
			n.armStandTimer()
		}
	default: // same epoch
		if n.role == RoleCandidate {
			n.bids[m.From] = m.Bid
		}
	}
}

func (n *Node) handleHeartbeat(m Message) {
	if m.Epoch < n.epoch {
		// Stale leader (an older round). Ignore; our newer epoch supersedes it and
		// it will step down when it hears us.
		return
	}
	if m.Epoch == n.epoch && n.role == RoleLeader && m.From != n.id {
		// Two leaders in one epoch (possible if a bid was lost during the window).
		// Resolve deterministically by (bid, id); the loser steps down.
		if m.Bid > n.winBid || (m.Bid == n.winBid && m.From > n.id) {
			n.stepToFollower(m.Epoch, m.From)
			n.leaderHeadLSN = m.LSN
			n.lastHeard = n.now
			n.send(Message{Type: MsgHeartbeatAck, To: m.From, Epoch: n.epoch, AckLSN: n.appliedLSN})
		}
		// else: we keep leading; the other side will step down when it hears us.
		return
	}

	// Accept this leader (equal-or-newer epoch). A newcomer or a node that had no
	// leader simply follows — "if a leader is already present, stick with it."
	n.epoch = m.Epoch
	n.leaderID = m.From
	n.leaderHeadLSN = m.LSN
	n.lastHeard = n.now
	n.heardLeader = true
	if n.role == RoleJoining {
		if n.caughtUp() {
			n.role = RoleFollower
		}
	} else {
		n.role = RoleFollower
	}
	n.bids = nil
	n.armStandTimer()
	n.send(Message{Type: MsgHeartbeatAck, To: m.From, Epoch: n.epoch, AckLSN: n.appliedLSN})
}

func (n *Node) handleHeartbeatAck(m Message) {
	if n.role != RoleLeader || m.Epoch != n.epoch {
		return
	}
	n.ackedAt[m.From] = n.now
	if m.AckLSN > n.ackedLSN[m.From] {
		n.ackedLSN[m.From] = m.AckLSN
	}
}

// LiveFollowers returns the ids of followers that acked within FailureTimeout —
// the leader's view of who is currently in the cluster (used for replication
// fan-out and semi-sync).
func (n *Node) LiveFollowers() []string {
	if n.role != RoleLeader {
		return nil
	}
	cutoff := n.now - n.cfg.FailureTimeout
	var live []string
	for _, p := range n.peers {
		if p == n.id {
			continue
		}
		if at, ok := n.ackedAt[p]; ok && at >= cutoff {
			live = append(live, p)
		}
	}
	return live
}

// DurableLSN reports the highest AppliedLSN that a durability quorum of ackCount
// followers (plus the leader itself) has reached — the semi-sync commit point
// used to release client acks. Only meaningful while leader.
func (n *Node) DurableLSN(ackCount int) uint64 {
	if n.role != RoleLeader {
		return 0
	}
	if ackCount <= 0 {
		return n.appliedLSN
	}
	lsns := make([]uint64, 0, len(n.ackedLSN))
	for _, l := range n.ackedLSN {
		lsns = append(lsns, l)
	}
	sort.Slice(lsns, func(i, j int) bool { return lsns[i] > lsns[j] })
	if len(lsns) < ackCount {
		return 0
	}
	return lsns[ackCount-1]
}
