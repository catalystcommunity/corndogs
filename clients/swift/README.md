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
if let task = next.task { print(task) }
```

Heartbeat, three ways:

```swift
let stop = tr.startHeartbeat(interval: 15.0)   // async: background thread, returns a stop closure
// ... or run it yourself, blocking, on a thread you control:
try tr.runHeartbeat(interval: 15.0)            // sync: blocks, pinging until you stop it
try tr.ping()                                  // or a single one-shot heartbeat

stop()                                         // stop the background heartbeat
```

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
