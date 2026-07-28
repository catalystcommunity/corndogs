# corndogs (Java client)

The official Java client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with a
built-in heartbeat) — you connect and go. No extra setup, no third-party
dependencies (the codec and transport are both self-contained, using only the JDK).

```xml
<dependency>
    <groupId>csilgen.generated</groupId>
    <artifactId>corndogs</artifactId>
    <version>0.0.0</version>
</dependency>
```

## Usage

```java
import csilgen.generated.*;

TcpTransport tr = TcpTransport.connect("localhost:5080"); // your corndogs server's TCP address
tr.startHeartbeat(15_000);                                // keep the connection alive (background)
CorndogsClient client = new CorndogsClient(tr);

// Submit a task, then claim the next one from the queue.
client.submitTask(new SubmitTaskRequest(
    "emails", "submitted", "sending", -1L, "...".getBytes(), 0L));
GetNextTaskResponse got = client.getNextTask(new GetNextTaskRequest(
    "emails", "submitted", 0L, "", ""));
TaskDelivery delivery = got.delivery();
```

Java calls are synchronous, one in flight at a time: each `call()` is serialized
under a lock. The connection is dialed lazily on first use and re-dialed on failure.

## Heartbeat (keep the connection alive)

Three ways to run it:

```java
// Async: background daemon thread, returns a stop handle.
AutoCloseable stop = tr.startHeartbeat(15_000);
// ... later ...
stop.close();

// Sync: blocks the calling thread, pinging until it's interrupted or a ping fails.
new Thread(() -> tr.runHeartbeat(15_000)).start();

// One-shot: a single ping.
tr.ping();
```

## Notes

- **Transport:** CSIL-RPC over TCP, framed with a 4-byte big-endian length prefix
  (the CSIL StreamCarrier). HTTP is not used for RPC — the corndogs server serves
  RPC on its TCP port; HTTP is only for health and Prometheus.
- **Clustered deployments:** point the transport at any node; a write that lands on
  a follower is transparently redirected to the leader.
- Errors: both a service error and a transport failure raise
  `csilgen.generated.ClientException` (a `RuntimeException`) — a service error's
  message is prefixed `corndogs: service error <code>: <message>`; a transport
  failure's is prefixed `corndogs: transport status ...` or wraps the underlying
  `IOException`.
- `TcpTransport` implements `AutoCloseable`; call `close()` (or use
  try-with-resources) to stop any heartbeat and close the connection when you're
  done with it.
