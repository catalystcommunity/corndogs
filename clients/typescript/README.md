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
const { delivery } = await client.corndogs.getNextTask({
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

## Resilience operations

Use these operations when a retry must not create a second task or apply a
stale change. They need a Corndogs 0.8.0 or later server. An older server
returns transport status 2 (unknown operation). After that error, do not send
the legacy operation instead.

- Make one `submission_key` for each logical submission. Send the same key on each retry.
- Make one `operation_id` for each claim, update, completion, or cancellation. Send the same id on each retry.
- Send the `revision` from the claim or from the last result as `expected_revision`.
- An error with "outcome uncertain" in its message means that the request possibly ran. Retry a resilience operation with the same request. Do not retry a legacy mutation automatically.

```ts
const sub = await client.corndogs.submitKeyedTask({
  submissionKey: "order-1234", guarded: true, queue: "orders", currentState: "submitted",
  autoTargetState: "submitted-working", timeout: 60, payload: bytes, priority: 0,
});
// sub.replayed is true when the server already accepted this key.

const { delivery } = await client.corndogs.claimGuardedTask({
  operationId: "claim-7f3a", queue: "orders", currentState: "submitted",
  overrideTimeout: 0, overrideCurrentState: "", overrideAutoTargetState: "",
});
if (delivery) {
  await client.corndogs.completeGuardedTask({
    operationId: "done-7f3a", uuid: delivery.task.task.uuid, queue: "orders",
    expectedRevision: delivery.task.revision,
  });
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
