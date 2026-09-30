# corndogs (Dart client)

The official Dart client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with a
built-in heartbeat) — connect and go. No extra setup, no dependencies.

```yaml
dependencies:
  corndogs:
    git:
      url: https://github.com/catalystcommunity/corndogs.git
      path: clients/dart
```

## Usage

```dart
import 'dart:typed_data';

import 'package:corndogs/corndogs.dart';
import 'package:corndogs/transport.dart';

Future<void> main() async {
  final tr = await TcpTransport.connect('localhost:5080'); // your corndogs server's TCP address
  tr.startHeartbeat(); // keep the connection alive (background timer)
  final client = CorndogsAsyncClient(tr);

  // Submit a task, then claim the next one from the queue.
  await client.submitTask(SubmitTaskRequest(
    queue: 'emails',
    currentState: 'submitted',
    autoTargetState: 'sending',
    timeout: -1,
    payload: Uint8List(0),
    priority: 0,
  ));
  final got = await client.getNextTask(GetNextTaskRequest(
    queue: 'emails',
    currentState: 'submitted',
    overrideTimeout: 0,
    overrideCurrentState: '',
    overrideAutoTargetState: '',
  ));
  print(got.delivery);

  await tr.close();
}
```

Calls are multiplexed over one connection (correlation ids), so concurrent
`await`s — e.g. many `submitTask`/`getNextTask` calls fired with `Future.wait`
— don't block each other.

## Heartbeat (keep the connection alive)

```dart
final stop = tr.startHeartbeat(const Duration(seconds: 15)); // background Timer, returns a stop function
stop(); // ... later, if you want the heartbeat to end without closing the transport

// ... or run it yourself, as an awaited loop:
unawaited(tr.runHeartbeat(const Duration(seconds: 15))); // blocks until tr.close() or a ping fails
await tr.ping(); // a single one-shot heartbeat
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

```dart
final sub = await client.submitKeyedTask(SubmitKeyedTaskRequest(
  submissionKey: 'order-1234', guarded: true, queue: 'orders', currentState: 'submitted',
  autoTargetState: 'submitted-working', timeout: 60, payload: bytes, priority: 0,
));
// sub.replayed is true when the server already accepted this key.

final d = (await client.claimGuardedTask(ClaimGuardedTaskRequest(
  operationId: 'claim-7f3a', queue: 'orders', currentState: 'submitted',
  overrideTimeout: 0, overrideCurrentState: '', overrideAutoTargetState: '',
))).delivery;
if (d != null) {
  await client.completeGuardedTask(CompleteGuardedTaskRequest(
    operationId: 'done-7f3a', uuid: d.task.task.uuid, queue: 'orders', expectedRevision: d.task.revision,
  ));
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

- **Transport:** CSIL-RPC over TCP (4-byte length-prefix framing). HTTP is not
  used for RPC — the server serves RPC on its TCP port; HTTP is only for
  health/metrics.
- **Async only:** Dart has no synchronous socket API, so `TcpTransport`
  implements the generated `AsyncCsilTransport` and pairs with
  `CorndogsAsyncClient`. The generated synchronous `CorndogsClient` cannot be
  backed by real network I/O in Dart and has no carrier here.
- **Clustered deployments:** point the transport at any node; a write that
  lands on a follower is transparently redirected to the leader.
- **Errors:** a service error throws a `CorndogsServiceException` with
  `code`/`message`; a transport failure throws a `CorndogsTransportException`.
