# corndogs (Kotlin client)

The official Kotlin client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with a
built-in heartbeat) — connect and go.

## Install

This package builds to a standard Gradle (Kotlin/JVM) artifact. Publish it to your
local Maven repository with `./gradlew publishToMavenLocal` — TODO: publish it to a
shared repository — then depend on it:

```kotlin
dependencies {
    implementation("community.catalyst.csilgen.generated:corndogs:0.0.0")
}
```

## Usage

```kotlin
import community.catalyst.csilgen.generated.CorndogsClient
import community.catalyst.csilgen.generated.GetNextTaskRequest
import community.catalyst.csilgen.generated.SubmitTaskRequest
import community.catalyst.csilgen.generated.TcpTransport

fun main() {
    val tr = TcpTransport("localhost:5080")  // your corndogs server's TCP address
    tr.startHeartbeat()                      // keep the connection alive (background thread)
    val client = CorndogsClient(tr)

    // Submit a task, then claim the next one from the queue.
    client.submitTask(SubmitTaskRequest(
        queue = "emails", currentState = "submitted", autoTargetState = "sending",
        timeout = -1, payload = "...".toByteArray(), priority = 0,
    ))
    val delivery = client.getNextTask(GetNextTaskRequest(
        queue = "emails", currentState = "submitted",
        overrideTimeout = 0, overrideCurrentState = "", overrideAutoTargetState = "",
    )).delivery
}
```

## Heartbeat (keep the connection alive)

Both a sync (blocking) and an async (background thread) start are provided:

```kotlin
val tr = TcpTransport("localhost:5080")
val client = CorndogsClient(tr)

val stop = tr.startHeartbeat(15_000)   // async: daemon thread, returns a stop handle
// stop()

// ... or run it yourself, blocking, on a thread you control:
//   thread { tr.runHeartbeat(15_000) }   // sync-style: blocks until stopped
//   tr.ping()                             // single one-shot heartbeat
```

## Notes

- **Transport:** CSIL-RPC over TCP (4-byte big-endian length-prefix framing). HTTP is
  not used for RPC — the server serves RPC on its TCP port; HTTP is only for
  health/metrics.
- **Clustered deployments:** point the transport at any node; a write that lands on a
  follower is transparently redirected to the leader.
- Calls are serialized (one in flight) over the connection, guarded by a lock; the
  connection is dialed lazily and re-dialed on failure.
- Errors: a service error throws `community.catalyst.csilgen.generated.ClientError`
  (`code`/`message`); a transport failure throws
  `community.catalyst.csilgen.generated.TransportError`.
- Dependency-free: the transport reuses the package's own CBOR codec.
