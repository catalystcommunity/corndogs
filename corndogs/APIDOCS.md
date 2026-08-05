# Corndogs API

The [project README](../README.md) gives a usage example. The `csil/` directory
contains the service contract. The `clients/` directory contains the generated
clients.

## Payload rules

Corndogs treats each payload as opaque bytes. Corndogs does not encode, decode,
or inspect these bytes.

Only `GetNextTask` and `GetNextTaskGroup` return payload bytes. These operations
return a `TaskDelivery`. A delivery contains task metadata and the payload.
Other operations return task metadata without the payload.

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

Update a task that has the matching `uuid`, `queue`, and `current_state`. Use
`new_state` to change the current state. The response contains task metadata.

### `CompleteTask`

Complete a task that has the matching `uuid`, `queue`, and `current_state`.
Corndogs sets both states to `completed` and archives the task. The response
contains the archived task metadata.

### `CancelTask`

Cancel a task that has the matching `uuid`, `queue`, and `current_state`.
Corndogs sets both states to `canceled` and archives the task. The response
contains the archived task metadata.

### `CleanUpTimedOut`

Process tasks with a timeout before `at_time`. Set `queue` to process only one
queue. The response gives the number of changed tasks in `timed_out`.

See [Task states and timeouts](../README.md#task-states-and-timeouts).

---

## Metric operations

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

## Health & metrics
The HTTP operations address is `:8080` by default. Set `CORNDOGS_HTTP_LISTEN` to
change it. `GET /healthz` returns `200` while the server runs. If
`PROMETHEUS_ENABLED=true`, `GET /metrics` returns Prometheus metrics.
