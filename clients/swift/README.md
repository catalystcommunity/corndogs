# corndogs (Swift client)

The official Swift client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with a
built-in heartbeat) — you connect and go. No extra setup, no third-party dependencies
(raw POSIX sockets, the package's own CBOR codec).

## Install

Add the package to your `Package.swift`:

```swift
.package(url: "https://github.com/catalystcommunity/corndogs.git", from: "0.1.0"),
```

and list `"Corndogs"` in your target's `dependencies`.

## Use

```swift
import Corndogs

let tr = TcpTransport.connect("localhost:5080")   // your corndogs server's TCP address
tr.startHeartbeat()                               // keep the connection alive (background thread)
let client = CorndogsClient(transport: tr)

// Submit a task, then claim the next one from the queue.
_ = try client.submitTask(SubmitTaskRequest(
    queue: "emails", currentState: "submitted", autoTargetState: "sending",
    timeout: -1, payload: [], priority: 0))

let next = try client.getNextTask(GetNextTaskRequest(
    queue: "emails", currentState: "submitted",
    overrideTimeout: 0, overrideCurrentState: "", overrideAutoTargetState: ""))
if let delivery = next.delivery { print(delivery) }
```

Heartbeat, three ways:

```swift
let stop = tr.startHeartbeat(interval: 15.0)   // async: background thread, returns a stop closure
// ... or run it yourself, blocking, on a thread you control:
try tr.runHeartbeat(interval: 15.0)            // sync: blocks, pinging until you stop it
try tr.ping()                                  // or a single one-shot heartbeat

stop()                                         // stop the background heartbeat
```

## Resilience operations

Use these operations when a retry must not create a second task or apply a
stale change. They need a Corndogs 0.8.0 or later server. An older server
returns transport status 2 (unknown operation). After that error, do not send
the legacy operation instead.

- Make one `submission_key` for each logical submission. Send the same key on each retry.
- Make one `operation_id` for each claim, update, completion, or cancellation. Send the same id on each retry.
- Send the `revision` from the claim or from the last result as `expected_revision`.
- An error with "outcome uncertain" in its message means that the request possibly ran. Retry a resilience operation with the same request. Do not retry a legacy mutation automatically.

```swift
let sub = try client.submitKeyedTask(SubmitKeyedTaskRequest(
    submissionKey: "order-1234", guarded: true, queue: "orders", currentState: "submitted",
    autoTargetState: "submitted-working", timeout: 60, payload: bytes, priority: 0))
// sub.replayed is true when the server already accepted this key.

if let d = try client.claimGuardedTask(ClaimGuardedTaskRequest(
    operationId: "claim-7f3a", queue: "orders", currentState: "submitted",
    overrideTimeout: 0, overrideCurrentState: "", overrideAutoTargetState: "")).delivery {
    _ = try client.completeGuardedTask(CompleteGuardedTaskRequest(
        operationId: "done-7f3a", uuid: d.task.task.uuid, queue: "orders", expectedRevision: d.task.revision))
}
```

The server returns a `ServiceError` with one of these codes:

| Code | Meaning |
| --- | --- |
| 1 | Invalid argument |
| 2 | Submission key required |
| 3 | Task guard required |
| 4 | Submission key conflict |
| 5 | Revision conflict |
| 6 | Operation conflict |
| 7 | Task not found |
| 8 | Claim superseded |

Do not retry a `ServiceError`. See [docs/resilience.md](../../docs/resilience.md).

## Notes

- **Transport:** CSIL-RPC over TCP, framed with a 4-byte length prefix. HTTP is not
  used for RPC (the corndogs server serves RPC on its TCP port; HTTP is only for
  health and Prometheus).
- **Clustered deployments:** point the transport at any node; a write that lands on
  a follower is transparently redirected to the leader.
- **Concurrency:** `TcpTransport` serializes calls (one in flight at a time) behind a
  lock, and re-dials automatically after a dropped connection. Safe to share and call
  from multiple threads.
- Errors: a service error throws the generated `CsilClientError` (has `code` and
  `message`); a transport-level failure (dropped connection, connect timeout, dial
  failure) throws `TransportError`.
- Call `tr.close()` when you're done — it stops any background heartbeat and closes
  the socket.
