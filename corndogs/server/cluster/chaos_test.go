package cluster

import (
	"math/rand"
	"testing"
)

// TestChaos hammers the protocol with seeded random faults — partitions, crashes,
// restarts, heals — across many seeds and thousands of ticks. The harness checks
// after every tick that no connected component holds two leaders for longer than
// a bounded settling window (a contested election or partition heal resolves
// quickly; a design that let dual-leadership persist would fail here). After the
// storm we heal fully and require reconvergence to exactly one leader.
//
// Note the guarantee is deliberately weaker than the quorum design's: DURING a
// partition each side legitimately has its own leader (availability). What must
// hold is that within any single connected component leadership settles to one,
// and the whole cluster reconverges once healed.
func TestChaos(t *testing.T) {
	const (
		seeds        = 60
		ticksPerSeed = 3000
		clusterSize  = 5
	)
	for seed := int64(0); seed < seeds; seed++ {
		seed := seed
		t.Run("", func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			s := newSimNet(t, ids(clusterSize), DefaultConfig())
			s.latency = int64(1 + rng.Intn(3))
			s.bootstrap()

			for i := 0; i < ticksPerSeed; i++ {
				switch {
				case rng.Intn(150) == 0:
					for _, id := range s.ids {
						s.part[id] = rng.Intn(2)
					}
				case rng.Intn(150) == 0:
					s.heal()
				case rng.Intn(170) == 0:
					s.crash(s.ids[rng.Intn(len(s.ids))])
				case rng.Intn(110) == 0:
					s.restart(s.ids[rng.Intn(len(s.ids))])
				}
				s.step()
			}

			s.heal()
			for _, id := range s.ids {
				s.restart(id)
			}
			s.requireStableLeader(3000)
			s.run(400)
			if ls := s.leaders(); len(ls) != 1 {
				t.Fatalf("seed %d: did not reconverge to a single leader; got %v", seed, ls)
			}
		})
	}
}

// TestChaosTwoAndThreeNode stresses the small clusters the quorum design could not
// safely fail over — here they must, and must not livelock or hold persistent
// dual-leadership within a connected component.
func TestChaosTwoAndThreeNode(t *testing.T) {
	for _, size := range []int{2, 3} {
		size := size
		t.Run("", func(t *testing.T) {
			for seed := int64(0); seed < 40; seed++ {
				rng := rand.New(rand.NewSource(seed + int64(size*7919)))
				s := newSimNet(t, ids(size), DefaultConfig())
				s.latency = int64(1 + rng.Intn(2))
				s.bootstrap()
				for i := 0; i < 2000; i++ {
					switch {
					case rng.Intn(120) == 0:
						for _, id := range s.ids {
							s.part[id] = rng.Intn(2)
						}
					case rng.Intn(120) == 0:
						s.heal()
					case rng.Intn(160) == 0:
						s.crash(s.ids[rng.Intn(len(s.ids))])
					case rng.Intn(90) == 0:
						s.restart(s.ids[rng.Intn(len(s.ids))])
					}
					s.step()
				}
				s.heal()
				for _, id := range s.ids {
					s.restart(id)
				}
				s.requireStableLeader(2000)
			}
		})
	}
}
