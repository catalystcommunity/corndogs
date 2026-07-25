# corndogs (TypeScript client)

The official TypeScript client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with
a built-in heartbeat) — connect and go. No extra setup, no CBOR library, no
HTTP.

```sh
npm install corndogs
```

## Usage

```ts
import { AsyncApiClient } from "corndogs";
import { TcpTransport } from "corndogs/transport";

const tr = new TcpTransport("localhost:5080"); // your corndogs server's TCP address
tr.startHeartbeat();                           // keep the connection alive (background)
const client = new AsyncApiClient(tr);

// Submit a task, then claim the next one from the queue.
await client.corndogs.submitTask({
  queue: "emails", currentState: "submitted", autoTargetState: "sending",
  timeout: -1, payload: new Uint8Array(), priority: 0,
});
const { task } = await client.corndogs.getNextTask({
  queue: "emails", currentState: "submitted",
  overrideTimeout: 0, overrideCurrentState: "", overrideAutoTargetState: "",
});
```

Calls are multiplexed over one connection (correlation ids), so concurrent
`await`s don't block each other:

```ts
// 100 submits, concurrent, one connection:
await Promise.all(
  Array.from({ length: 100 }, (_, i) =>
    client.corndogs.submitTask({
      queue: "emails", currentState: "submitted", autoTargetState: "sending",
      timeout: -1, payload: new TextEncoder().encode(`job-${i}`), priority: 0,
    }),
  ),
);
await tr.close();
```

## Heartbeat (keep the connection alive)

Both a background (async) start and an awaitable (sync-style) loop are
provided:

```ts
const stop = tr.startHeartbeat(15000);        // async: background loop, returns a stop()
// ... or await the loop yourself, until you abort it:
const ac = new AbortController();
tr.runHeartbeat(15000, ac.signal);             // sync-style: awaitable, runs until aborted or a ping fails
await tr.ping();                               // or a single one-shot heartbeat
```

## Connecting

`new TcpTransport(addr)` dials lazily on the first call. To connect eagerly
and await the dial up front, use `TcpTransport.connect` (or the module-level
`connect`) instead:

```ts
import { connect } from "corndogs/transport";

const tr = await connect("localhost:5080");
```

## Notes

- **Transport:** CSIL-RPC over TCP, framed with a 4-byte length prefix. HTTP is
  not used for RPC (the corndogs server serves RPC on its TCP port; HTTP is
  only for health and Prometheus).
- **Clustered deployments:** point the transport at any node; a write that
  lands on a follower is transparently redirected to the leader.
- Errors: a service error rejects with `corndogs/transport`'s `ServiceError`
  (`code`/`message`); a transport failure rejects with `TransportError`.
- Only the async client (`AsyncApiClient` / `CorndogsAsyncClient`) is
  supported — Node's `net.Socket` is non-blocking by nature, so there is no
  synchronous transport.
