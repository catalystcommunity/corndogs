package cluster

import (
	"testing"
)

// TestSingleNodeBecomesLeader: a lone node establishes itself quickly.
func TestSingleNodeBecomesLeader(t *testing.T) {
	s := newSimNet(t, ids(1), DefaultConfig())
	s.bootstrap()
	leader := s.requireStableLeader(50)
	if leader != "n1" {
		t.Fatalf("expected n1 to lead, got %s", leader)
	}
}

// TestEstablishesQuickly: a fresh 3-node cluster elects a leader well within the
// "under a second" budget — with a ~100ms tick that is ElectionJitter+ElectionWindow
// (~8 ticks). We assert a leader exists within a generous 15 ticks.
func TestEstablishesQuickly(t *testing.T) {
	cfg := DefaultConfig()
	s := newSimNet(t, ids(3), cfg)
	s.bootstrap()
	for i := 0; i < 15; i++ {
		s.step()
		if ls := s.leaders(); len(ls) == 1 {
			return
		}
	}
	t.Fatalf("no leader established within 15 ticks; leaders=%v", s.leaders())
}

// TestHappyPathOneLeader: a healthy cluster converges to exactly one leader and
// holds it steady (no churn while everyone keeps hearing heartbeats).
func TestHappyPathOneLeader(t *testing.T) {
	s := newSimNet(t, ids(3), DefaultConfig())
	s.bootstrap()
	leader := s.requireStableLeader(60)
	for i := 0; i < 500; i++ {
		s.step()
		if ls := s.leaders(); len(ls) != 1 || ls[0] != leader {
			t.Fatalf("tick %d: leadership churned; want stable %s, got %v", s.now, leader, ls)
		}
	}
	for _, id := range s.ids {
		if id == leader {
			continue
		}
		if l, _ := s.nodes[id].Leader(); l != leader {
			t.Fatalf("follower %s thinks leader is %q, want %s", id, l, leader)
		}
	}
}

// TestTwoNodeFailover: the whole point of dropping quorum — a 2-node cluster fails
// over when the leader crashes. The survivor must take over.
func TestTwoNodeFailover(t *testing.T) {
	s := newSimNet(t, ids(2), DefaultConfig())
	s.bootstrap()
	leader := s.requireStableLeader(60)

	s.crash(leader)
	survivor := "n1"
	if leader == "n1" {
		survivor = "n2"
	}
	for i := 0; i < 200; i++ {
		s.step()
		if s.nodes[survivor].Role() == RoleLeader {
			return
		}
	}
	t.Fatalf("2-node cluster did not fail over to %s; role=%s", survivor, s.nodes[survivor].Role())
}

// TestLeaderCrashElectsNew: 3-node leader crash → a survivor takes over.
func TestLeaderCrashElectsNew(t *testing.T) {
	s := newSimNet(t, ids(3), DefaultConfig())
	s.bootstrap()
	old := s.requireStableLeader(60)
	s.crash(old)

	for i := 0; i < 200; i++ {
		s.step()
		ls := s.leaders()
		if len(ls) == 1 && ls[0] != old {
			return
		}
	}
	t.Fatalf("no new leader after crashing %s; leaders=%v", old, s.leaders())
}

// TestNewNodeSticksWithExistingLeader: a node that joins (or catches up) while a
// healthy leader is present must adopt it, NOT trigger a new election. We start a
// 3-node cluster with only two nodes caught up, let them elect, then bring the
// third in and assert the leader never changes.
func TestNewNodeSticksWithExistingLeader(t *testing.T) {
	s := newSimNet(t, ids(3), DefaultConfig())
	// Only n1, n2 caught up initially; n3 stays joining.
	s.nodes["n1"].MarkCaughtUp()
	s.nodes["n2"].MarkCaughtUp()
	leader := s.requireStableLeader(60)

	// Now the third node catches up while a leader is already established.
	s.nodes["n3"].MarkCaughtUp()
	for i := 0; i < 300; i++ {
		s.step()
		if ls := s.leaders(); len(ls) != 1 || ls[0] != leader {
			t.Fatalf("tick %d: newcomer disrupted leadership; want %s, got %v", s.now, leader, ls)
		}
	}
	// n3 ended up following the established leader.
	if l, _ := s.nodes["n3"].Leader(); l != leader {
		t.Fatalf("newcomer n3 follows %q, want %s", l, leader)
	}
}

// TestPartitionEachSideLeads: this design favors availability — under a partition
// EACH side elects (or keeps) a leader, and on heal it reconverges to one. This is
// the accepted split-brain trade, asserted here as intended behavior.
func TestPartitionEachSideLeads(t *testing.T) {
	s := newSimNet(t, ids(3), DefaultConfig())
	s.bootstrap()
	old := s.requireStableLeader(60)

	// Split the leader off alone from the other two.
	groups := map[string]int{}
	for _, id := range s.ids {
		if id == old {
			groups[id] = 1
		} else {
			groups[id] = 0
		}
	}
	s.partitionInto(groups)

	// The majority side elects its own leader; the isolated old leader keeps
	// leading (availability). So both sides have a leader.
	var neu string
	for i := 0; i < 300 && neu == ""; i++ {
		s.step()
		for _, id := range s.ids {
			if id != old && s.nodes[id].Role() == RoleLeader {
				neu = id
			}
		}
	}
	if neu == "" {
		t.Fatal("majority side never elected its own leader (availability expected)")
	}
	if !s.nodes[old].Serving() {
		t.Fatal("isolated old leader should keep serving (availability trade)")
	}

	// Heal → reconverge to a single leader.
	s.heal()
	s.requireStableLeader(300)
	s.run(200)
	if ls := s.leaders(); len(ls) != 1 {
		t.Fatalf("did not reconverge to one leader after heal; got %v", ls)
	}
}

// TestLaggingNodeDoesNotLead: a node kept far behind the leader head must not win
// an election even when it is a survivor with a live peer to hand it leadership.
func TestLaggingNodeDoesNotLead(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxLagToVote = 5
	s := newSimNet(t, ids(3), cfg)
	s.bootstrap()
	leader := s.requireStableLeader(60)

	var laggard, insync string
	for _, id := range s.ids {
		if id == leader {
			continue
		}
		if insync == "" {
			insync = id
		} else {
			laggard = id
		}
	}
	s.nodes[leader].SetAppliedLSN(100)
	s.nodes[insync].SetAppliedLSN(100)
	s.run(40) // propagate head=100 to the laggard via heartbeats

	s.crash(leader)
	for i := 0; i < 300; i++ {
		s.step()
		if s.nodes[laggard].Role() == RoleLeader {
			t.Fatalf("tick %d: lagging node %s became leader (applied=%d)", s.now, laggard, s.nodes[laggard].AppliedLSN())
		}
	}
	if s.nodes[insync].Role() != RoleLeader {
		t.Fatalf("in-sync node %s should have taken over; role=%s", insync, s.nodes[insync].Role())
	}
}
