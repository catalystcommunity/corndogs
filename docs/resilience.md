# Resilience contract

Corndogs 0.8.0 adds two optional features. They make retries safe and reject
stale workers. They do not change the legacy operations.

- **Submission keys** stop duplicate tasks when a caller retries a submission.
  Operations: `SubmitKeyedTask`, `LookupSubmission`.
- **Task guards** reject stale workers and stale transitions. Operations:
  `ClaimGuardedTask`, `ClaimGuardedTaskGroup`, `UpdateGuardedTask`,
  `CompleteGuardedTask`, `CancelGuardedTask`, `GetGuardedTask`,
  `LookupOperation`.

`GetServerInfo` returns the server version, the features, and the policies.

Submission keys and task guards are separate. A required submission key does
not protect a task from a stale worker. Only task guards do that.

## Changes in 0.8.0

- New operations: see above. The request and response shapes of the legacy
  operations do not change.
- New server settings: `CORNDOGS_SUBMISSION_KEY_POLICY`,
  `CORNDOGS_TASK_GUARD_POLICY`, `CORNDOGS_RECEIPT_RETENTION`. Helm values:
  `resilience.*`. The default policy is `compatibility`.
- Every task has a revision. Each mutation increases it, including a legacy
  claim and a timeout release.
- A legacy claim does not select a guarded task. A legacy update, completion,
  or cancellation of a guarded task returns ServiceError code 3.
- The server returns ServiceError replies (status 0, variant `ServiceError`)
  for the new rules. Other errors keep their transport status.
- A leader without a write quorum returns transport status 7 (`unavailable`)
  before it applies a write. A commit timeout stays status 6: its outcome is
  uncertain.
- The Go client classifies failures: `ErrNotApplied`, `ErrOutcomeUncertain`,
  `ErrUnsupported`. It never sends a legacy mutation again after an uncertain
  outcome. It replays keyed and guarded requests with the same bytes.
- The CLI `submit-task` has `--submission-key` and `--guarded`.
- Storage migrations: PostgreSQL migration `00005_resilience.sql`; new bbolt
  buckets. Both run at startup.
- Replicated-file recovery after a full cluster restart is fixed. See
  [clustering](./clustering-tier1.md#restart-and-recovery).

## Admission policy

| Setting | Helm value | Default | Values |
| --- | --- | --- | --- |
| `CORNDOGS_SUBMISSION_KEY_POLICY` | `resilience.submissionKeyPolicy` | `compatibility` | `compatibility`, `required` |
| `CORNDOGS_TASK_GUARD_POLICY` | `resilience.taskGuardPolicy` | `compatibility` | `compatibility`, `required` |
| `CORNDOGS_RECEIPT_RETENTION` | `resilience.receiptRetention` | `1h` | Go duration, `1m` or more |

The binary and the chart have the same defaults. The server does not start
with an invalid value. The chart rejects an invalid policy.

What each policy rejects. A rejection happens before the store is called, so a
rejected request changes nothing.

| Request | Compatibility | Submission keys required | Task guards required |
| --- | --- | --- | --- |
| `SubmitTask` | Accepted | Code 2 | Code 3 |
| `SubmitKeyedTask`, `guarded=false` | Accepted | Accepted | Code 3 |
| `SubmitKeyedTask`, `guarded=true` | Accepted | Accepted | Accepted |
| `GetNextTask`, `GetNextTaskGroup` | Accepted; skips guarded tasks | Accepted | Code 3 |
| `UpdateTask`, `CompleteTask`, `CancelTask` | Accepted; code 3 for a guarded task | Accepted | Code 3 |
| Guarded operations | Accepted | Accepted | Accepted |
| `CleanUpTimedOut`, reads, metric operations | Accepted | Accepted | Accepted |

The policy checks protocol features, not a client version. A new client that
sends a legacy request gets the same rejection as an old client.

At startup the server prints the policy, whatever `LOGLEVEL` is. With
Prometheus enabled, these metrics show the migration state:

| Metric | Labels | Meaning |
| --- | --- | --- |
| `corndogs_legacy_requests_total` | `op` | Accepted legacy mutations |
| `corndogs_policy_rejections_total` | `op`, `reason` | Requests that a policy rejected |
| `corndogs_replayed_requests_total` | `op` | Requests answered from a receipt |
| `corndogs_resilience_policy` | `feature`, `policy` | 1 for the active policy |
| `corndogs_receipts_purged_total` | none | Expired receipts deleted |

## Submission keys

`SubmitKeyedTask` creates a task at most once for each `(queue,
submission_key)` during the retention period.

- **Namespace.** The key belongs to the receiving server's storage and to the
  queue. The queue must not be empty. There is no caller namespace. A key is
  not authentication.
- **Key format.** 1 to 128 bytes of text. An empty key returns code 1. The
  server never makes a key for a request that has none.
- **Same key, same request.** The server returns the first receipt with
  `replayed = true`. It does not create a task. It does not change the
  priority, the timeout, or the state.
- **Same key, different request.** The server returns code 4 and changes
  nothing.
- **Request equality.** The server compares a SHA-256 digest of the request
  as sent, before it applies defaults: `guarded`, `queue`, `current_state`,
  `auto_target_state`, `timeout`, the exact `payload` bytes, and `priority`.
  An empty string and a default value are different values. The digest format
  has a version (`corndogs-request-digest-v1`). A change of a server default
  cannot reinterpret an accepted key, because the server reads the receipt
  before it applies defaults.
- **Receipt and current state.** The response has `receipt` (the first
  acceptance) and `task` (the current state, which can be terminal). A retry
  after completion does not make the task ready again.
- **Lookup.** `LookupSubmission` reads a receipt and the current task. It does
  not change data. An absent receipt means that the server has no unexpired
  record of the key.
- **Retention.** A receipt is valid for `CORNDOGS_RECEIPT_RETENTION` after its
  acceptance. The server checks the expiry on every read. A background job
  deletes expired receipts every minute, in batches. After expiry, the same
  key creates a new task. Corndogs does not deduplicate beyond the retention
  period.
- **Durability.** The receipt and the task are written in one transaction.
  Receipts survive completion, cancellation, restart, and leader change. They
  replicate with the task data and are in every backup of the database.

## Task guards

- **Revision.** Every task has a revision. Each mutation increases it: claim,
  update, timeout release, completion, cancellation. A timeout release
  invalidates the claim, also when the next claim uses the same state names.
  Tasks from releases before 0.8.0 start at revision 0.
- **Guarded tasks.** `SubmitKeyedTask` with `guarded = true` makes a guarded
  task. The choice is permanent. A legacy claim does not select a guarded
  task. A legacy update, completion, or cancellation of a guarded task returns
  code 3. Legacy reads (`GetTaskStateByID`, metrics) still work.
- **Guarded claims.** `ClaimGuardedTask` and `ClaimGuardedTaskGroup` claim
  guarded and unguarded tasks. They return the revision of the claim. An
  unguarded task stays unguarded: its revision still detects a change, but a
  legacy client can still change it.
- **Guarded mutations.** `UpdateGuardedTask`, `CompleteGuardedTask`, and
  `CancelGuardedTask` change the task only if `uuid` and `queue` identify it,
  its revision equals `expected_revision`, and its state equals
  `expected_state` when that field is present. Otherwise they return code 5
  and change nothing. The check and the write are in one transaction.
- **Tasks without a worker.** A task that waits for a person needs no claim.
  Read its revision with `GetGuardedTask`, then complete it with that
  revision.
- **Operation receipts.** Each guarded claim and mutation has an
  `operation_id` (1 to 128 bytes). The server stores a receipt with the
  result in the same transaction. The same `operation_id` and request return
  the first result with `replayed = true`, even after later changes. The same
  `operation_id` with a different request returns code 6.
- **Replayed claims.** A replayed claim returns the same task and payload
  while the claim still holds the task. After the task changed (for example a
  timeout release), it returns code 8.
- **Terminal tasks.** A guarded mutation of a completed or canceled task
  returns code 5 with the terminal state. `GetGuardedTask` returns archived
  tasks with `terminal = true`. The archive keeps metadata and the revision,
  not the payload or a business result.

## ServiceError codes

| Code | Name | Meaning |
| --- | --- | --- |
| 1 | invalid argument | Empty or too long key or operation id, empty queue, bad UUID |
| 2 | submission key required | Legacy `SubmitTask` in required mode |
| 3 | task guard required | Legacy mutation in required mode, or of a guarded task |
| 4 | submission key conflict | Same key, different request |
| 5 | revision conflict | Revision, state, or queue differs; or the task is terminal |
| 6 | operation conflict | Same operation id, different request |
| 7 | task not found | No live or archived task has the UUID |
| 8 | claim superseded | A replayed claim no longer holds the task |

A ServiceError is final. Do not retry it as a transport failure.

## Client retry rules

| Outcome | Client behavior |
| --- | --- |
| Leader redirect (`not-leader leader=...`) | Follow it within the request deadline |
| Connection failed before the request was sent | Retry within the attempt budget |
| Transport status 7: no write quorum, not applied | Retry within the attempt budget |
| Legacy mutation sent, reply lost or status 6 | Return an uncertain outcome; do not send it again |
| Keyed or guarded request sent, reply lost | Send the same bytes again (same key or operation id) |
| ServiceError | Return it; do not retry |
| Transport status 2 (unknown operation) | Return "unsupported"; do not retry as a legacy request |
| Commit timeout in a cluster (status 6) | Uncertain: the leader applied the write before the timeout |

The Go client (`clients/corndogs`) follows these rules in `StreamTransport`
and `ClusterTransport`:

- `errors.Is(err, corndogs.ErrOutcomeUncertain)`: the operation may have run.
- `errors.Is(err, corndogs.ErrNotApplied)`: the server did not apply it.
- `errors.Is(err, corndogs.ErrUnsupported)`: the server does not know it.
- `corndogs.ServiceErrorCode(err)`: the ServiceError code.
- `StreamTransport.MaxAttempts` bounds the sends of one call (default 3).

The attempt budget is seconds. The minimum retention is one minute, so a
transport retry always stays inside the retention period. An application that
retries later must check the elapsed time against `receipt_retention_seconds`
from `GetServerInfo`. After the retention period, or after an uncertain
lookup, do not send a keyed request again without a decision.

A lost claim reply never claims another task silently. A keyed or guarded
claim replays its receipt. A legacy claim returns an uncertain outcome. The
claimed task then returns to ready at its timeout, when `CleanUpTimedOut`
runs.

A client that needs these guarantees must not continue with an older server.
Call `RequireFeatures` (Go) or `GetServerInfo` first, or rely on the new
operations: an older server returns "unknown operation" and runs nothing.
Never remove the key and retry as a legacy request.

### Released clients

This release cannot repair a released client. In compatibility mode, a
released client can still retry an uncertain legacy submission and create a
duplicate. Before 0.8.0, the Go `ClusterTransport` did this automatically.
Required mode stops keyless submissions. It does not make an old client's
retry algorithm safe.

Released clients of every language decode the rejections of required mode as
errors with the message. The released Zig client reports
`error.ServiceErrorOccurred` without the message text.

## Read consistency in a cluster

In a replicated file cluster, only the leader answers `LookupSubmission`,
`GetGuardedTask`, and `LookupOperation`. A follower redirects them. The
leader answers only after the data that it reports is committed to the
write quorum. A follower can lag the leader, so a missing record on a
follower does not prove that a request did not run. Legacy reads
(`GetTaskStateByID`, metrics) still read the local copy and can be stale.

Do not retry a write because one follower has not yet shown it.

## Upgrade

1. **Set the policy before the upgrade.** Keep `compatibility` for both
   policies while released clients run. The chart always sets the values, so
   the policy stays as you set it across chart upgrades. The chart and the
   binary never enable required mode on their own.
2. **Upgrade every server.** All write-serving servers must run 0.8.0 before
   a client sends a keyed or guarded request.
   - PostgreSQL: servers of different versions can share the database during
     a rolling upgrade, but a 0.7.x server ignores guards and does not
     increase revisions. Finish the rollout before clients use the new
     operations. Migration `00005` adds columns with constant defaults, so it
     does not rewrite the tables.
   - File backend: stop the server, replace it, start it. The new buckets are
     created at startup.
   - Replicated file cluster: stop all nodes, upgrade them, start them. Mixed
     versions in one cluster are not supported. See
     [clustering](./clustering-tier1.md#upgrade).
3. **Upgrade clients at their own pace.** Producers change `SubmitTask` to
   `SubmitKeyedTask` with `guarded = true`. Workers change to
   `ClaimGuardedTask` and the guarded mutations. Guarded workers also claim
   legacy tasks, so workers can upgrade before or after producers.
4. **Drain or adopt legacy tasks.** A legacy task has no guard, but it has a
   revision. A guarded worker can claim it. A person or tool can complete a
   waiting legacy task with `CompleteGuardedTask` and its revision. No claim
   token from a legacy claim is reusable: each claim increases the revision.
   A task that a legacy worker holds when you enable required mode can then be
   finished only with guarded operations, or it returns to ready at its
   timeout.
5. **Enable required mode.** When `corndogs_legacy_requests_total` stops
   increasing, set `taskGuardPolicy` and `submissionKeyPolicy` to
   `required`. You can set the two at different times.

## Backup and rollback

Receipts and revisions are in the same database file or PostgreSQL database
as the tasks. Back up the storage before the upgrade.

A downgrade to 0.7.x is not supported after clients use the new operations:

- A 0.7.x file server does not read the guarded-task bucket, so guarded tasks
  are not visible to it.
- A 0.7.x PostgreSQL server ignores the `guarded` column, so legacy clients
  can change guarded tasks again.
- A 0.7.x server does not deduplicate keys or check revisions.

To roll back, restore the backup from before the upgrade, or drain every
guarded task first. A 0.7.x binary does not detect a newer schema, so it
cannot refuse to start.

## Storage

- **File backend (bbolt):** guarded tasks are in bucket `gtasks`; receipts
  are in `submissions`, `operations`, and `receipt-expiry`. Task records have
  optional `revision` and `guarded` fields. All buckets replicate.
- **PostgreSQL:** migration `00005_resilience.sql` adds `revision` and
  `guarded` to `tasks` and `archived_tasks`, and the tables
  `submission_receipts` and `operation_receipts`. The keyed submission claims
  its key with `INSERT ... ON CONFLICT` before it creates the task, so
  concurrent requests with one key create one task.

## Test evidence

The acceptance tests are in `corndogs/test/resilience_test.go` and
`corndogs/test/phase_test.go`. They use the Go client over real CSIL-RPC TCP
connections and a fault proxy that drops a complete reply after the server
sends it.

```sh
# One server (any backend), compatibility or required mode:
CORNDOGS_TEST_ADDR=127.0.0.1:5080 go test -count=1 -run Resilience ./test/
# Two servers on one PostgreSQL database:
CORNDOGS_TEST_ADDR=host1:5080 CORNDOGS_TEST_ALT_ADDR=host2:5080 go test -run Resilience ./test/
# Replicated file cluster:
CORNDOGS_TEST_SEEDS=n0:5080,n1:5080,n2:5080 go test -run Resilience ./test/
# SIGKILL recovery: run phase "write", kill and restart, run phase "verify":
RESILIENCE_PHASE=write RESILIENCE_STATE=/tmp/s.json go test -run 'TestResiliencePhase$' ./test/
```

## Limits

- Deduplication lasts only for the retention period.
- An unguarded task stays open to legacy clients in compatibility mode.
- Released clients can still create duplicates in compatibility mode.
- A legacy claim with a lost reply is uncertain. Recovery waits for the task
  timeout and `CleanUpTimedOut`.
- These tests do not establish power-loss, disk-loss, or arbitrary network
  partition guarantees.
