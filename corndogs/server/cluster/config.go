package cluster

import "fmt"

// Config holds the cluster timing parameters. All durations are in abstract
// "ticks" so the core stays unit-agnostic and deterministically testable; the
// production wiring drives Tick from a real ticker. A production tick of ~100ms
// makes the defaults heartbeat ≈1s, failure-detect ≈2s, election ≈0.5s — the
// cadence the design calls for ("heartbeats every second ... a full other second
// of response time").
type Config struct {
	// HeartbeatInterval is how often a leader broadcasts heartbeats.
	HeartbeatInterval int64

	// FailureTimeout is how long a follower goes without a leader heartbeat before
	// concluding the leader is gone and starting an election. Must exceed a couple
	// of heartbeat intervals so ordinary jitter/scheduling never triggers a
	// needless election.
	FailureTimeout int64

	// ElectionWindow is how long a candidate collects bids before deciding the
	// winner. It should comfortably exceed one message round-trip so that, in a
	// connected cluster, every node sees every bid and they all pick the same
	// winner — yielding a single leader.
	ElectionWindow int64

	// ElectionJitter staggers when nodes first stand for election so they don't all
	// start the same tick. The actual wait is FailureTimeout + rand[0, jitter).
	ElectionJitter int64

	// BidMax is the exclusive upper bound of the random election draw. The design
	// specifies [0, 10).
	BidMax float64

	// MaxLagToVote is the in-sync gate: a node whose AppliedLSN is more than this
	// far behind the leader head does not stand for election (a stale node must
	// never win and truncate committed work). 0 means "must be fully caught up".
	MaxLagToVote uint64
}

// DefaultConfig returns the standard cadence (ticks; ~100ms/tick in production):
// heartbeat 10 (~1s), failure-detect 20 (~2s), election window 5 (~0.5s).
func DefaultConfig() Config {
	return Config{
		HeartbeatInterval: 10,
		FailureTimeout:    20,
		ElectionWindow:    5,
		ElectionJitter:    3,
		BidMax:            10.0,
		MaxLagToVote:      0,
	}
}

// Validate rejects internally inconsistent timings.
func (c Config) Validate() error {
	if c.HeartbeatInterval <= 0 {
		return fmt.Errorf("cluster: HeartbeatInterval must be > 0")
	}
	if c.FailureTimeout < 2*c.HeartbeatInterval {
		return fmt.Errorf("cluster: FailureTimeout (%d) must be >= 2*HeartbeatInterval (%d) so jitter never triggers a needless election", c.FailureTimeout, c.HeartbeatInterval)
	}
	if c.ElectionWindow <= 0 {
		return fmt.Errorf("cluster: ElectionWindow must be > 0")
	}
	if c.ElectionWindow >= c.FailureTimeout {
		return fmt.Errorf("cluster: ElectionWindow (%d) must be < FailureTimeout (%d)", c.ElectionWindow, c.FailureTimeout)
	}
	if c.ElectionJitter < 0 {
		return fmt.Errorf("cluster: ElectionJitter must be >= 0")
	}
	if c.BidMax <= 0 {
		return fmt.Errorf("cluster: BidMax must be > 0")
	}
	return nil
}
