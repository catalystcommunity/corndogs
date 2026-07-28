# Corndogs API

The [Flow](/README.md#flow) section gives a usage example. The `csil/`
directory contains the service contract. The `clients/` directory contains the
generated client types.

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

## General
The regular flow stuff.

### `SubmitTask`

Submit a new task to a `queue`. The response contains the created task
metadata.

### `GetTaskStateByID`

Get task metadata by `uuid`. This operation can return an archived task. It
does not return the payload.

### `GetNextTask`

Claim the next task from `queue` that has the specified `current_state`. The
`override_` fields change task data after Corndogs switches the states. See
[State and Timeout Overrides](/README.md#state-and-timeout-overrides).

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
Will compare tasks to `at_time` to see if they're timed out. Optionally limited to a specific `queue`.\
See the [Timeouts](/README.md#timeouts) section in the readme for more info on how you might use timeouts.\
Returns the number of tasks `timed_out`.

---

## Metrics
For the proto based metrics stuff. 

### `GetQueues`
Returns `GetQueuesResponse` containing a list of `queues`, and the `total_task_count`.

### `GetQueueTaskCounts`
Returns `GetQueueTaskCountsResponse` containing:
- `queue_counts` map of queue name to number of tasks in that queue.
- Also returns `total_task_count`.

### `GetTaskStateCounts`
Accepts `GetTaskStateCountsRequest` with the `queue` you'd like to get the state task count for.\
Returns `GetTaskStateCountsResponse` containing:
- `queue` requested
- `count` of the total tasks in the queue
- `state_counts` map of state name to number of tasks in that state.

### `GetQueueAndStateCounts`
Returns `GetQueueAndStateCountsResponse` containing:
- `queue_and_state_counts` map of queue name to `QueueAndStateCounts` object.

`QueueAndStateCounts` contains:
- `queue` requested
- `count` of the total tasks in the queue
- `state_counts` map of state name to number of tasks in that state.

## Health & metrics
The server exposes `GET /healthz` for Kubernetes liveness/readiness probes
(returns `200` while serving). When `PROMETHEUS_ENABLED=true`, Prometheus metrics
are served on `:8080/metrics`.
