# Corndogs API

The [project README](../README.md) gives a usage example. The `csil/` directory
contains the service contract. The `clients/` directory contains the generated
clients.

## Payload rules

Corndogs treats each payload as opaque bytes. Corndogs does not encode, decode,
or inspect these bytes.

Only the claim operations return payload bytes: `GetNextTask` and
`GetNextTaskGroup` return a `TaskDelivery`, and `ClaimGuardedTask` and
`ClaimGuardedTaskGroup` return a `GuardedDelivery`. A delivery contains task
metadata and the payload. Other operations return task metadata without the
payload.

`UpdateTaskRequest.payload` is optional. If the field is absent, Corndogs keeps
the stored payload. If the field is present and empty, Corndogs replaces the
stored payload with an empty byte string. If the field is present and not
empty, Corndogs replaces the stored payload with the new bytes.

The default maximum payload size is 16 MiB. Set
`CORNDOGS_MAX_PAYLOAD_BYTES` to change the limit. The value must be from `1`
through `1073741823` bytes. The server rejects a larger payload.

## Task operations

### `SubmitTask`

Submit a new task to a `queue`. The response contains the created task
metadata.

### `GetTaskStateByID`

Get task metadata by `uuid`. This operation can return an archived task. It
does not return the payload.

### `GetNextTask`

Claim the next task from `queue` that has the specified `current_state`. The
`override_` fields change task data after Corndogs switches the states. See
[Task states and timeouts](../README.md#task-states-and-timeouts).

The response contains an optional `TaskDelivery`. The delivery contains the
task metadata and payload.

### `GetNextTaskGroup`

Claim the next task from a group of queues. The response has the same
`TaskDelivery` form as `GetNextTask`.

### `UpdateTask`

Update the live task that has the matching `uuid`. The response contains task
metadata. If no live task has the `uuid`, the response contains no task.

Corndogs applies these rules to each field. Both storage backends obey the
same rules.

| Field | Absent | Present |
| --- | --- | --- |
| `new_state` | Not possible (required). An empty value becomes `updated`. | Replaces the current state. |
| `auto_target_state` | Not possible (required). An empty value becomes `new_state` plus the working suffix. | Replaces the auto target state. |
| `timeout` | Not possible (required). | Replaces the timeout. |
| `payload` | Keeps the stored payload. Corndogs does not write the payload again. | Replaces the stored payload. An empty value stores an empty byte string. |
| `priority` | Keeps the stored priority. | Replaces the stored priority. The value `0` is a priority, not "absent". |

Each update also sets `update_time` to the current time. Claim order is
priority descending, then `update_time` ascending. Thus an update moves the
task to the end of its priority band.

To park a task and try it again later, send only `new_state`,
`auto_target_state`, and `timeout`. Do not send `payload` or `priority`. The
task keeps its payload and its place above lower-priority work.

### `CompleteTask`

Complete the live task that has the matching `uuid`. Corndogs does not compare
`queue` or `current_state`. Corndogs sets both states to `completed` and archives
the task. The response
contains the archived task metadata.

### `CancelTask`

Cancel the live task that has the matching `uuid`. Corndogs does not compare
`queue` or `current_state`. Corndogs sets both states to `canceled` and archives
the task. The response
contains the archived task metadata.

### `CleanUpTimedOut`

Process tasks with a timeout before `at_time`. Set `queue` to process only one
queue. The response gives the number of changed tasks in `timed_out`.

See [Task states and timeouts](../README.md#task-states-and-timeouts).

---

## Resilience operations

Server 0.8.0 adds operations for safe retries and stale-worker protection.
[docs/resilience.md](../docs/resilience.md) gives the full contract, the
server policies, the client retry rules, and the upgrade steps. A server
before 0.8.0 returns transport status 2 (unknown operation) for them.

The server can reject legacy mutations by policy
(`CORNDOGS_SUBMISSION_KEY_POLICY`, `CORNDOGS_TASK_GUARD_POLICY`). The default
is `compatibility`, which accepts them. Rejections and contract errors are
`ServiceError` replies. The codes are in the resilience document.

### `GetServerInfo`

Get the server version, the features that the server enforces
(`submission-keys-v1`, `task-guards-v1`), the two policies, and the receipt
retention in seconds.

### `SubmitKeyedTask`

Submit a task at most once for each `(queue, submission_key)`. The same key
and request return the first receipt with `replayed = true`. A different
request with the same key returns code 4. Set `guarded = true` to put the task
under task guards. The `queue` must not be empty.

### `LookupSubmission`

Get the receipt of a `(queue, submission_key)` and the current task. It does
not change data.

### `ClaimGuardedTask`

Claim the next task like `GetNextTask`. The response contains the payload and
the task `revision`. A retry with the same `operation_id` returns the same
claim while it still holds the task.

### `ClaimGuardedTaskGroup`

Claim the best task across a set of queues like `GetNextTaskGroup`, with the
`ClaimGuardedTask` rules.

### `UpdateGuardedTask`

`UpdateTask` with an atomic guard: `uuid`, `queue`, `expected_revision`, and
the optional `expected_state` must match. Otherwise it returns code 5. The
payload and priority rules are the same as for `UpdateTask`.

### `CompleteGuardedTask` and `CancelGuardedTask`

Archive the task with the same guard. A task that waits without a worker needs
no claim: send its current revision.

### `GetGuardedTask`

Get a live or archived task with its revision, its guard flag, and a terminal
flag.

### `LookupOperation`

Get the receipt of a guarded claim or mutation by `operation_id`.

## Metric operations

With the `file` backend, Corndogs keeps a count for each queue and state. Each
task write updates the count in the same transaction. A metric operation reads
these counts. It does not read each task. Its cost increases with the number of
queue and state pairs, not with the number of tasks. At 300,000 live tasks, one
`GetQueueAndStateCounts` call took approximately 2 µs with the counts. The full
scan that it replaces took approximately 38 ms.

At the first start after an upgrade, Corndogs builds the counts from one full
scan. A clustered follower that receives writes from a leader of an older
version counts the tasks again for each metric operation, until it starts
again.

With the `postgres` backend, a metric operation is one `GROUP BY` query on the
tasks table.

### `GetQueues`

Return the queue names and `total_task_count`.

### `GetQueueTaskCounts`

Return `queue_counts`, which maps each queue name to its task count. Also return
`total_task_count`.

### `GetTaskStateCounts`

Return the task counts for the requested queue. `state_counts` maps each state
name to its task count. `count` gives the total for the queue.

### `GetQueueAndStateCounts`

Return `queue_and_state_counts`, which maps each queue name to its
`QueueAndStateCounts` value. Each value contains the queue name, its total task
count, and a count for each state.

## TLS

Corndogs can serve CSIL-RPC over TLS on `CORNDOGS_LISTEN`. Set both variables:

| Variable | Default | Description |
| --- | --- | --- |
| `CORNDOGS_TLS_CERT_FILE` | empty | PEM certificate chain for the server. |
| `CORNDOGS_TLS_KEY_FILE` | empty | PEM private key for the certificate. |
| `CORNDOGS_TLS_RELOAD_INTERVAL` | `10s` | How often Corndogs examines the two files for a change. |

When both files are set, the port accepts TLS 1.2 or later only. A plaintext
client cannot connect. The server does not ask for a client certificate.

Corndogs reads the files again after they change, without a restart. It
examines them during a TLS handshake, at most once for each interval. A
Kubernetes Secret update (an atomic symlink swap) is a change. If the new
files do not load, Corndogs keeps the last good certificate and logs an error.

The HTTP operations port (`CORNDOGS_HTTP_LISTEN`) stays plain HTTP. The
cluster peer port (`CORNDOGS_CLUSTER_LISTEN`) does not use TLS.

Clients with TLS support:

- Go: `corndogs.NewTLS(addr, cfg)` and `corndogs.NewClusterTLS(cfg, seeds...)`.
- Rust: the `tls` cargo feature. See `clients/rust/README.md`.
- The `corndogs timeout` and `corndogs submit-task` commands: `--tls`,
  `--tls-ca-file`, and `--tls-server-name`.

The other language clients do not have TLS support at this time.

## Health & metrics
The HTTP operations address is `:8080` by default. Set `CORNDOGS_HTTP_LISTEN` to
change it. `GET /healthz` returns `200` while the server runs. If
`PROMETHEUS_ENABLED=true`, `GET /metrics` returns Prometheus metrics.
