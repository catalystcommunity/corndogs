package cluster

import (
	"fmt"
	"sort"
	"testing"
)

// simNet is a deterministic in-memory network of Nodes with fault injection. It
// drives the election protocol with no goroutines, no sockets, and a controllable
// logical clock, so every scenario is exactly reproducible. Invariants are checked
// after every step, so a violation fails at the tick it happens.
type simNet struct {
	t       *testing.T
	nodes   map[string]*Node
	ids     []string
	crashed map[string]bool
	part    map[string]int // node -> partition group; equal group == can communicate
	latency int64          // message delivery delay, in ticks
	drop    func(from, to string) bool

	inflight []simMsg
	now      int64

	// dualLeaderStreak counts consecutive ticks in which some connected component
	// has more than one leader. A brief transient is legal (a contested election
	// resolves when heartbeats cross); a persistent one is a bug.
	dualLeaderStreak int
	dualLeaderBound  int
}

type simMsg struct {
	deliverAt int64
	m         Message
}

func newSimNet(t *testing.T, ids []string, cfg Config) *simNet {
	t.Helper()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("bad config: %v", err)
	}
	s := &simNet{
		t:       t,
		nodes:   map[string]*Node{},
		ids:     append([]string(nil), ids...),
		crashed: map[string]bool{},
		part:    map[string]int{},
		latency: 1,
		// A contested election or a partition heal resolves within a few heartbeat
		// intervals; anything past this bound is a non-resolving (buggy) design.
		dualLeaderBound: int(4*cfg.HeartbeatInterval + cfg.FailureTimeout + cfg.ElectionWindow),
	}
	sort.Strings(s.ids)
	for i, id := range s.ids {
		// Distinct seeds so jitter differs per node (breaks split votes), but fixed
		// so runs are reproducible.
		s.nodes[id] = NewNode(id, s.ids, cfg, int64(1000+i), 0)
		s.part[id] = 0
	}
	return s
}

// bootstrap marks every node caught up so the initial election can proceed.
func (s *simNet) bootstrap() {
	for _, id := range s.ids {
		s.nodes[id].MarkCaughtUp()
	}
}

func (s *simNet) connected(a, b string) bool { return s.part[a] == s.part[b] }

func (s *simNet) collect(id string) {
	for _, m := range s.nodes[id].TakeOutbox() {
		s.inflight = append(s.inflight, simMsg{deliverAt: s.now + s.latency, m: m})
	}
}

// step advances one tick: deliver due messages, then tick every live node, then
// check invariants.
func (s *simNet) step() {
	s.now++

	var due, rest []simMsg
	for _, im := range s.inflight {
		if im.deliverAt <= s.now {
			due = append(due, im)
		} else {
			rest = append(rest, im)
		}
	}
	s.inflight = rest
	// Deterministic delivery order.
	sort.Slice(due, func(i, j int) bool {
		a, b := due[i].m, due[j].m
		if a.To != b.To {
			return a.To < b.To
		}
		if a.From != b.From {
			return a.From < b.From
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Epoch < b.Epoch
	})
	for _, im := range due {
		to := im.m.To
		if s.crashed[to] || !s.connected(im.m.From, to) {
			continue
		}
		if s.drop != nil && s.drop(im.m.From, to) {
			continue
		}
		s.nodes[to].Recv(s.now, im.m)
		s.collect(to)
	}

	for _, id := range s.ids {
		if s.crashed[id] {
			continue
		}
		s.nodes[id].Tick(s.now)
		s.collect(id)
	}

	s.checkInvariants()
}

func (s *simNet) run(ticks int) {
	for i := 0; i < ticks; i++ {
		s.step()
	}
}

// checkInvariants asserts the safety property this (availability-favoring) design
// actually guarantees: WITHIN A SINGLE CONNECTED COMPONENT there is at most one
// leader. Across a partition, one leader per side is expected and legal (that is
// the accepted trade), so the invariant is scoped to each component, not global.
func (s *simNet) checkInvariants() {
	leadersByGroup := map[int][]string{}
	for _, id := range s.ids {
		if s.crashed[id] {
			continue
		}
		if s.nodes[id].Role() == RoleLeader {
			g := s.part[id]
			leadersByGroup[g] = append(leadersByGroup[g], id)
		}
	}
	dual := false
	for _, ls := range leadersByGroup {
		if len(ls) > 1 {
			dual = true
			break
		}
	}
	if dual {
		s.dualLeaderStreak++
		if s.dualLeaderStreak > s.dualLeaderBound {
			s.t.Fatalf("tick %d: SAFETY VIOLATION: two leaders persisted in one connected component for %d ticks (bound %d): %v",
				s.now, s.dualLeaderStreak, s.dualLeaderBound, leadersByGroup)
		}
	} else {
		s.dualLeaderStreak = 0
	}
}

// --- helpers for assertions ------------------------------------------------

// leaders returns the ids of live nodes currently in RoleLeader.
func (s *simNet) leaders() []string {
	var ls []string
	for _, id := range s.ids {
		if !s.crashed[id] && s.nodes[id].Role() == RoleLeader {
			ls = append(ls, id)
		}
	}
	return ls
}

// requireOneLeader runs until exactly one live leader exists (and a follower
// agrees on it), or fails after maxTicks.
func (s *simNet) requireStableLeader(maxTicks int) string {
	s.t.Helper()
	for i := 0; i < maxTicks; i++ {
		s.step()
		ls := s.leaders()
		if len(ls) == 1 && s.nodes[ls[0]].Serving() {
			return ls[0]
		}
	}
	s.t.Fatalf("no stable leader after %d ticks; leaders=%v", maxTicks, s.leaders())
	return ""
}

func (s *simNet) crash(id string)   { s.crashed[id] = true }
func (s *simNet) restart(id string) { s.crashed[id] = false } // node keeps its state (warm restart)

// partition assigns nodes to communication groups by a function of id.
func (s *simNet) partitionInto(groups map[string]int) {
	for id, g := range groups {
		s.part[id] = g
	}
}

func (s *simNet) heal() {
	for _, id := range s.ids {
		s.part[id] = 0
	}
}

func ids(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf("n%d", i+1)
	}
	return out
}
