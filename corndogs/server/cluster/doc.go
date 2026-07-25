// Package cluster implements Tier-1 clustering for the corndogs file backend:
// one leader (the only writer) plus followers that replicate the leader's
// committed state, for redundancy and automatic failover — NOT write throughput.
//
// The design is documented in docs/clustering-tier1.md. The two halves are:
//
//   - Leader election (this file's Node state machine): epochs, majority votes,
//     a leader lease, and an in-sync eligibility gate. This is election ONLY —
//     deliberately not Raft's log-replication consensus machinery.
//   - State replication (Phase 2): the leader ships physical bbolt mutations,
//     LSN-ordered, to followers; followers copy bytes (no re-execution), which
//     sidesteps the wall-clock nondeterminism in the store's write path.
//
// The one correctness law is "at most one leader serving writes at a time"
// (two sequencers on one queue would destroy per-queue ordering). It is upheld
// by two rules working together:
//
//  1. A candidate needs a MAJORITY of the configured members to win. A minority
//     partition therefore cannot elect a leader.
//  2. A leader that cannot renew a majority of heartbeat acks within LeaseTimeout
//     STEPS DOWN (stops serving). Because LeaseTimeout < ElectionTimeout, a
//     partitioned-away old leader has already stopped serving before the majority
//     side can elect a replacement — the two serving windows never overlap.
//
// The Node is a pure, tick-driven state machine: no goroutines, no real clocks,
// no sockets. Time and randomness are injected. That makes the whole protocol
// testable over a deterministic in-memory network with fault injection
// (partitions, drops, delays, crashes, restarts) — see the simulation tests.
// Production wires Node.Tick/Recv to a real ticker and the CSIL-RPC transport.
package cluster
