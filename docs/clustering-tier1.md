# Tier-1 Clustering: Replicated File Backend (redundancy + failover)

**Status:** design (not yet implemented)
**Goal:** make the embedded file backend survive a node death without losing
committed work, with automatic failover, while preserving the property that
matters most — **per-queue arrival order** — and without adding an external
system to operate.

**Explicit non-goals:** this is *not* for write-throughput scale-out. A single
leader remains the only writer (that is what keeps ordering free). If you need
horizontal write scale, that is the postgres backend, or a later tier / a
dedicated system (NATS JetStream, etc.). See `docs/storage-backends.md`.

---

## 1. Shape

One **leader** (the only writer) and N **followers**. The leader replicates its
committed writes to followers as a physical mutation stream. Followers apply the
stream to their own local bbolt, serve reads, and stand by to be promoted.

```
          ┌─────────┐   mutation stream (LSN-ordered)   ┌──────────┐
client ──▶│ LEADER  │──────────────────────────────────▶│ FOLLOWER │
 (writes) │ (bbolt) │──────────────────────────────────▶│ FOLLOWER │
          └─────────┘                                    └──────────┘
              ▲  writes only accepted while leader holds a majority lease
```

Everything the current single-node backend does stays the same on the leader.
Clustering wraps it.

## 2. Replicate physical mutations, not operations

The audit log is **not** a sufficient replication source: `AuditEvent` records
only `Op, UUID, Queue, FromState, ToState, Priority` — no payload, no timeout, no
auto_target_state. It rebuilds transitions, not tasks.

Instead we ship the actual bbolt mutations. Every write already funnels through
`putTask` / `deleteTask` and (in group-commit mode) the single committer
goroutine. We tap that choke point: each committed transaction emits its
`(bucket, key, value | delete)` tuples, tagged with a monotonic **LSN**, into a
**replication log** (same append-only, segmented, resumable infra pattern as the
audit log — reusable). Followers apply raw KV mutations in LSN order.

Why physical, not logical (re-running ops):

- **It sidesteps the determinism problem entirely.** `nowNano()` is stamped at 6
  write-path sites today; if followers re-ran operations they would each stamp
  their own clock and diverge. With physical replication the leader has already
  resolved every timestamp, override, and state-swap into concrete bytes.
  Followers copy bytes — no logic, no divergence, no clock coordination.
- It is strictly simpler to reason about and to test (apply is a pure function of
  the log).

The LSN also becomes the natural FIFO tiebreak, replacing `update_time` (whose
ties are a real hazard today — the tests install a fake monotonic clock to avoid
them).

## 3. Election model: highest random bid (availability-favoring)

Leadership is decided by a **highest-random-bid election**, not quorum voting.
When a node finds itself leaderless, it draws a random `float64` in `[0, BidMax)`
(BidMax = 10) and broadcasts it; within a short collection window the highest bid
in the round (ties broken by node id) wins and becomes leader. A node that is
already hearing a leader's heartbeats does nothing — **"if a leader is present,
stick with it."** This is simple, needs no quorum, and establishes a leader in
about one election window (well under a second at a ~100ms tick).

**This is a deliberate CAP choice: availability over consistency-under-partition.**
The consequence, stated plainly because it reverses the earlier quorum design:

- **Crash / restart failover is clean.** A dead leader is not writing, so a fresh
  election simply picks a successor. No split-brain. This is the common failure
  mode and 2-node clusters fail over fine.
- **A network partition's outcome is set by the durability `ack_count` (§6).**
  The election alone always lets each side *have* a leader — but whether a side can
  *commit* is gated by semi-sync. With a write-majority `ack_count` (`≥ ⌊N/2⌋`, the
  default), the minority side cannot obtain the acks it needs, so it **blocks** and
  never commits; only the majority side makes progress → **no divergence, ordering
  preserved even across a partition** (CP). With `ack_count = 0` (async) there is
  no guard and the minority leader diverges (AP) — that is the case to avoid.

So `ack_count` is the real dial: the random-bid election gives fast, quorum-free
failover, and the durability requirement — not the election — is what decides the
split-brain question. Default to write-majority semi-sync for safety; drop to
async only if you deliberately want availability over consistency. A rejoining
ex-minority-leader must roll its **locally-applied-but-never-acked** writes back to
the last cluster-committed LSN and re-sync from the winner (§10 remaining work).

## 4. Node state machine (election)

States: `Joining → Follower → Candidate → Leader`.

- **Epoch:** monotonic; each election bumps it. A higher epoch always supersedes,
  which is how the cluster reconverges after a partition heals (the side that
  re-elected more recently has the higher epoch and wins).
- **Joining:** a fresh/restarted node follows any leader it hears but never stands
  for election until caught up — the **in-sync gate** (a stale node can't win and
  truncate committed work). It pulls a snapshot (§5) then tails the stream.
- **Follower:** in-sync. Serves reads. If it stops hearing the leader for
  `FailureTimeout`, it stands for election. A newly-caught-up node with a live
  leader just follows — it does not disrupt.
- **Candidate:** drew a bid and is collecting others' bids for `ElectionWindow`.
  At the deadline the highest bid wins: self ⇒ Leader, else ⇒ Follower awaiting
  the winner's heartbeat.
- **Leader:** broadcasts heartbeats every `HeartbeatInterval` (carrying head LSN +
  its winning bid), tracks follower liveness/LSN from acks. It keeps leading until
  it hears a **higher epoch** (then steps down) or a **same-epoch leader with a
  higher (bid,id)** (the rare contested-election tiebreak). There is no lease /
  majority-contact requirement — an isolated leader keeps serving (availability).

**Guarantees the tests hold (see §8):**
1. In any single connected component, leadership settles to **one** leader; a
   contested-election or partition-heal transient of two resolves within a bounded
   window (it never persists).
2. A fully-connected cluster establishes a leader quickly (~one election window)
   and reconverges to one leader after any heal.
3. Crash/restart failover produces a single new leader (no split-brain).
4. A lagging (out-of-sync) node never wins.
5. Replication convergence (§5): a follower applying the stream is byte-for-byte
   identical to the leader. (Per-queue order is preserved within a leader's reign;
   see §3 for the partition caveat.)

## 5. Replication log & catch-up

- Append-only, LSN-indexed, segmented (reuse the audit-log segment machinery:
  rotation, resume, `latest`-scan). Records complete `(bucket, key, value|tomb)`
  mutation sets per committed txn + the LSN.
- Followers request "everything after LSN X"; the leader streams. A new/far-behind
  follower first pulls a consistent **bbolt snapshot** (bolt's `Tx.WriteTo`),
  notes the snapshot's LSN, then tails from there.
- Log retention: keep enough to catch up the slowest live follower; snapshot +
  truncate beyond that.

## 6. Durability `ack_count` — and it doubles as the split-brain guard

A config knob alongside the existing `SyncAlways/Group/Interval/Never`:

- **semi-sync (default):** the leader waits for `ack_count` followers to ack a
  mutation batch before acking the client. Cost: ~1 network round-trip per
  group-commit batch (amortized by the committer — the batch, not each write,
  waits).
- **async (`ack_count = 0`):** leader acks immediately, ships in background →
  fastest, but see below.

**The important part (see §3): `ack_count` is not just durability — it is the
consistency/CAP control.** Because an isolated leader cannot obtain acks it can't
reach, a required `ack_count` prevents the minority side of a partition from
committing anything:

- **`ack_count = 0` (async):** no guard. A partitioned leader keeps committing
  locally → split-brain divergence. Choose this only when you truly prefer
  availability over consistency.
- **`ack_count ≥ ⌊N/2⌋` (write-majority — the safe default):** the leader plus its
  required acks form a majority, so only a side that holds a majority can commit.
  At most one side ever does → **no divergence.** 3-node ⇒ `ack_count = 1`;
  5-node ⇒ `ack_count = 2`. The minority side blocks (returns errors / times out)
  until the partition heals — CP behavior. `ack_count = 1` is only sufficient up
  to 3 nodes; it must scale with N.

Worked 3-node example (`ack_count = 1`): a **2–1 split** → the 2-node side keeps
writing, the lone node stops accepting updates; a **1–1–1 split** → every node
stops. Two sides never both commit.

Default: `ack_count = ⌊N/2⌋` (write-majority, split-brain-safe). Operators who
want raw availability can drop to `0`, accepting divergence.

## 7. Transports & client protocol

**Peer transport = TCP StreamCarrier.** Nodes exchange all cluster traffic —
election bids/heartbeats, replication batches, snapshots — over persistent TCP
connections framed with the canonical CSIL 4-byte length prefix
(`csilrpc.StreamCarrier`). No HTTP, no polling. Each node dials every peer
(auto-reconnecting, send-only) and accepts inbound connections it reads frames
from (`server/clustering/tcptransport.go`).

Two liveness mechanisms keep connections healthy: (1) the **election heartbeat**
(`MsgHeartbeat`, leader→followers every `HeartbeatInterval`, with follower acks)
keeps every leader↔follower link busy and drives failure detection; (2) **TCP
keepalive** (explicit 15s period) is set on every connection so that
follower↔follower links — idle in steady state, since only the leader heartbeats —
are not silently reaped by a NAT/firewall and a truly dead peer is detected before
the next election needs the link.

**Topology = real server push over TCP.** A client opens a TCP connection to any
node's cluster port and sends a `FrameSubscribe`; the node pushes a `FrameTopology`
(leader id + leader RPC URL + epoch) immediately and again on every leadership
change. This is genuine push on a kept-open connection — the working realization
of the `<- ClusterTopology` idea (a later swap to the formal `CsilRpcPush`
envelope, §1.3, is drop-in on the same carrier).

**Client (Go):** `corndogs.NewCluster(seedRPCURLs…)`. It needs no separate
discovery channel — a write that lands on a follower returns a
`not-leader leader=<addr>` redirect carrying the leader's RPC URL, so the client
caches the leader and re-points; on a dead node it rotates to the next seed. A
not-leader write is rejected *before* it executes, so retrying is safe. Reads go to
any node. (The TCP topology push is available for consumers that want proactive
change notification; the Go client uses the simpler, sufficient redirect-follow.)

- **Client RPC calls** still ride the existing CSIL-RPC **HTTP** envelope-in-body
  profile (`POST /csil/v1/rpc`) — that is corndogs' current client-facing transport,
  independent of clustering. Moving the client RPC itself onto the TCP StreamCarrier
  is a separate, repo-wide change.

## 8. Testability — prove the algorithm over all reachable states

The election + replication logic is written against an **abstract message
transport** interface (`send(to, msg)`, delivery callbacks), *not* sockets:

- **Production** wires it to the TCP StreamCarrier peer transport; client RPCs use
  the existing HTTP CSIL-RPC profile.
- **Tests** wire it to a **deterministic in-memory network** with a controllable
  clock and fault injection: drop/delay/reorder messages, partition arbitrary
  node subsets, crash/restart/rejoin nodes, stall a follower to force the in-sync
  gate, simultaneous candidacies, leader dies mid-batch, etc. No real network ⇒
  no real network flakes; every scenario is reproducible.

The suite asserts the §4 invariants as properties across scripted and
randomized/seeded interleavings ("acting as multiple nodes" in one process). This
is how etcd/raft and the MIT 6.824 labs test the same class of algorithm.

## 9. Config additions (env, matching existing `CORNDOGS_FILESTORE_*`)

- `CORNDOGS_CLUSTER_ENABLED`
- `CORNDOGS_CLUSTER_NODE_ID`, `CORNDOGS_CLUSTER_PEERS` (`id=clusterTcpAddr` list)
- `CORNDOGS_CLUSTER_LISTEN` (this node's cluster TCP StreamCarrier addr)
- `CORNDOGS_CLUSTER_RPC_ADVERTISE` (this node's client CSIL-RPC **TCP** address, `host:port` — pushed to clients so redirects resolve; not a URL)
- `CORNDOGS_CLUSTER_ACK_COUNT` (default ⌊N/2⌋) / `CORNDOGS_CLUSTER_ASYNC`
- `CORNDOGS_CLUSTER_HEARTBEAT_TICKS`, `..._FAILURE_TICKS`
- `CORNDOGS_LISTEN` (the CSIL-RPC **TCP** addr; configurable so nodes coexist on a host) / `CORNDOGS_HTTP_LISTEN` (health + Prometheus)

Single-node (cluster disabled) stays byte-for-byte the current behavior.

## 10. Phased implementation & status

1. **Election core + in-memory test harness.** ✅ **DONE.**
   `server/cluster/`: the `Node` state machine (**highest-random-bid election**,
   epochs, heartbeats + failure detection, in-sync gate, contested-election and
   partition-heal reconvergence), the abstract `Message`/transport seam, and a
   deterministic in-memory network with fault injection. Proven by named scenarios
   (`election_test.go` — single node, quick establishment, happy path, **2-node
   failover**, 3-node crash failover, **newcomer sticks with existing leader**,
   partition→each-side-leads→reconverge, lagging-node-can't-lead) + a seeded fault
   storm (`chaos_test.go`, incl. 2- and 3-node) that checks after every tick that
   no connected component holds two leaders past a bounded settling window, and
   requires reconvergence on heal. Passes under `-race`.
   *(An earlier quorum/PreVote implementation of this phase lives in git history;
   it was replaced by the random-bid design — see §3 for the trade.)*
2. **Replication log + apply + snapshot/catch-up + semi-sync + rejoin rollback.**
   ✅ **DONE.** `server/store/filestore/replication.go` (capture/apply/codec/
   `SnapshotTo`), `repllog.go` (segmented, LSN-indexed, resumable persistent log +
   `ReadFrom`/`Truncate`), `RestoreSnapshot` (the rejoin rollback — atomic db
   swap), and the coordinator in `server/clustering/replicator.go` that ties the
   election node + store + a `Transport` seam together: leader ships batches +
   advances the semi-sync commit point (`DurableLSN(ackCount)`), followers apply +
   ack + pull on a heartbeat gap. Proven by: byte-for-byte convergence over every
   write op; wire + log round-trips; snapshot-bootstrap-then-tail; and — over a
   deterministic in-memory network of **real BoltStores** — `TestReplicationAndSemiSync`
   (replicate + commit + converge), **`TestMinorityBlocks`** (isolated leader can't
   commit; majority side can — the §6 guard), and **`TestRejoinRollbackConverges`**
   (ex-minority-leader rolls back and the cluster reconverges). All under `-race`.
   **Remaining:** group-commit-mode capture (today capture selects the direct write
   path); wiring `DurableLSN`-gated client acks into the actual RPC response path
   (Phase 3).
3. **Network transport + engine.** ✅ **DONE.** `server/clustering/`: the `Frame`
   wire codec (all kinds, round-trip tested); the **`Engine`** actor — one
   goroutine owns the Replicator, fed by an inbound-frame channel, a real ticker,
   and client `Propose` calls, with semi-sync commit waiters (`engine.go`); the
   **`TCPTransport`** — persistent TCP peer connections via `csilrpc.StreamCarrier`
   (4-byte length-prefix framing, auto-reconnect), `FrameHello` RPC-addr
   advertisement, and topology **server push** (`tcptransport.go`); the
   `not-leader leader=<addr>` redirect; and the `ClusteredStore` that maps
   writes→`Propose` / reads→local so the existing RPC server uses it transparently.
   Proven by **`TestLiveClusterOverTCP`** — a real 3-node cluster on localhost TCP
   with real tickers: elect, replicated semi-sync write, follower write-refusal,
   leader-kill failover (`-race` clean) — and by a live server smoke test (single-
   and 2-node) driving SubmitTask/GetNextTaskGroup over real CSIL-RPC + the
   `NewCluster` client, including failover + rejoin. Topology watch
   is **real server push over the TCP StreamCarrier** (`FrameSubscribe` →
   `FrameTopology` on a kept-open connection) — no HTTP, no long-poll. Proven by
   `TestLiveClusterOverTCP`.
4. **Client integration.** ✅ **DONE.** `clients/corndogs/cluster_transport.go`:
   `corndogs.NewCluster(seedRPCURLs…)` — pure redirect-follow (a follower returns
   `not-leader leader=<addr>`), seed rotation on connection failure, no-retry on
   genuine application errors (safe for non-idempotent writes); no separate
   discovery channel needed. Verified against a live 2-node cluster: write+claim,
   then automatic follow to the new leader after a failover + rejoin.
4. **Client integration.** ⛳ **TODO.** Discovery, redirect-follow, watch+re-init
   in the Go carrier; then the generated clients.
5. **Ops.** ✅ **DONE.** Config env parsing (`config.go`, `CORNDOGS_CLUSTER_*` →
   `Settings`, write-majority `ack_count` default, `Validate`, tested);
   **Prometheus** cluster gauges (`server/metrics/cluster_metrics.go` —
   `corndogs_cluster_{is_leader,epoch,applied_lsn,committed_lsn,ack_count}` +
   `leader_changes_total`, low-cardinality, labeled only by `node_id`, verified via
   `:8080/metrics`); server wiring (`selectStore` builds the cluster when enabled
   and leaves the single-node path byte-for-byte unchanged; `CORNDOGS_LISTEN` made
   configurable); and the operator runbook (§11).

**All five phases are implemented and tested** — election, byte-for-byte
replication, semi-sync durability, the split-brain guard, rejoin rollback, the live
**TCP StreamCarrier** transport + engine with real topology push, the
leader-following client, and Prometheus ops. The only remaining item is
group-commit-mode capture (today capture forces the direct write path); everything
else is functional and tested over real sockets.

## 11. Operator runbook

**Enable clustering** (wraps the file backend; takes precedence over
`STORAGE_BACKEND`). Set on every node:

| Env | Meaning |
| --- | --- |
| `CORNDOGS_CLUSTER_ENABLED=true` | turn clustering on |
| `CORNDOGS_CLUSTER_NODE_ID=n1` | this node's id (must appear in PEERS) |
| `CORNDOGS_CLUSTER_PEERS=n1=host1:5090,n2=host2:5090,n3=host3:5090` | membership as `id=clusterTcpAddr` |
| `CORNDOGS_CLUSTER_LISTEN=0.0.0.0:5090` | this node's cluster TCP StreamCarrier (peer) address |
| `CORNDOGS_CLUSTER_RPC_ADVERTISE=host1:5080` | this node's client CSIL-RPC **TCP** address (pushed to clients so redirects resolve) |
| `CORNDOGS_LISTEN=0.0.0.0:5080` | the CSIL-RPC **TCP** address clients connect to |
| `CORNDOGS_HTTP_LISTEN=:8080` | HTTP for `/healthz` + `/metrics` **only** (never RPC) |
| `CORNDOGS_FILESTORE_DIR=/data` | local bbolt + replication log dir |
| `CORNDOGS_CLUSTER_ACK_COUNT` | semi-sync quorum; default ⌊N/2⌋ (write-majority, split-brain-safe). `0` = async (available, divergent) |

Three TCP/HTTP surfaces per node: **CSIL-RPC over TCP** on `CORNDOGS_LISTEN` (clients);
the **cluster StreamCarrier** on `CORNDOGS_CLUSTER_LISTEN` (peer frames + topology
push); and **HTTP** on `CORNDOGS_HTTP_LISTEN` for liveness/readiness (`/healthz`) and
Prometheus (`/metrics`) — HTTP never carries RPC.

**Clients:** the Go transport is a persistent, multiplexed TCP connection
(`corndogs.New(addr)`); it exposes a heartbeat with both a **sync** start
(`RunHeartbeat(ctx, interval)`, blocking) and an **async** start
(`StartHeartbeat(interval)` → stop func). Clustered clients use
`corndogs.NewCluster(addr1, addr2, …)` seeded with nodes' CSIL-RPC TCP addresses; it
sends writes to the leader, follows the `not-leader leader=<addr>` redirect on a
follower, and rotates seeds if a node is down. Reads may go to any node (followers
serve them, eventually consistent).

> k8s probes: liveness/readiness moved to `CORNDOGS_HTTP_LISTEN` `/healthz`; a TCP
> socket probe on `CORNDOGS_LISTEN` also works. Update the helm chart's probes
> accordingly (they previously pointed at `:5080/healthz` over HTTP).

**Sizing.** Run **3+ (odd)**. Writes need `ack_count` follower acks, so with the
default write-majority a 3-node cluster tolerates **1** node down and keeps
committing; a 2-node cluster elects a leader on failure but **cannot commit** until
its peer returns (the survivor isn't a write-majority) — 2-node buys read
availability + fast leader handoff, not write fault-tolerance, unless you set
`ack_count=0` (and accept divergence).

**Observe.** Prometheus at `:8080/metrics`:
`corndogs_cluster_{is_leader,epoch,applied_lsn,committed_lsn,ack_count}` and
`corndogs_cluster_leader_changes_total`, each labeled by `node_id`. A healthy
cluster shows exactly one node with `is_leader=1`, a stable `epoch`, and followers'
`applied_lsn` tracking the leader's `committed_lsn`.

**Failure playbook.**
- *A node crashes:* survivors re-elect in ~`FailureTimeout`+one election window
  (~2–3s at defaults). If the survivors still form a write-majority, writes
  continue; else writes return `ErrCommitTimeout` until the node returns.
- *A partition:* each side elects a leader, but only a side holding a write-majority
  can commit (the minority blocks) — no divergence with the default `ack_count`.
- *A node rejoins:* it catches up from the leader's log, or (if it had diverged as a
  minority leader) rolls back via a snapshot and re-syncs. No operator action.
- *Metrics show `epoch` climbing with no stable `is_leader`:* a persistent partition
  or `ack_count` set too high for the reachable set — check connectivity and the
  per-node cluster metrics.

## 12. Open boundaries

- 2-node auto-failover (§3) — pick run-3 / witness / manual.
- Snapshot mechanism: bolt `Tx.WriteTo` (whole-file) is simplest; incremental
  snapshots are a later optimization.
- `GetNextTaskGroup` is unaffected here (single leader owns all queues); it only
  gets interesting under the *sharded* Tier-3 design, which this is not.
