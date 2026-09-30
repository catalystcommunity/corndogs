# corndogs (Python client)

The official Python client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with a
built-in heartbeat) — you connect and go, in **sync** or **async** style. No extra
setup, no dependencies.

```sh
pip install corndogs
```

## Sync

```python
from corndogs import CorndogsClient, SubmitTaskRequest, GetNextTaskRequest
from corndogs.transport import TcpTransport

tr = TcpTransport("localhost:5080")      # your corndogs server's TCP address
tr.start_heartbeat()                     # keep the connection alive (background thread)
client = CorndogsClient(tr)

# Submit a task, then claim the next one from the queue.
client.submit_task(SubmitTaskRequest(
    queue="emails", current_state="submitted", auto_target_state="sending",
    timeout=-1, payload=b"...", priority=0,
))
delivery = client.get_next_task(GetNextTaskRequest(
    queue="emails", current_state="submitted",
    override_timeout=0, override_current_state="", override_auto_target_state="",
)).delivery
```

Heartbeat, two ways:

```python
stop = tr.start_heartbeat(interval=15.0)   # async: background thread, returns a stop()
# ... or run it yourself, blocking, in a thread you control:
tr.run_heartbeat(interval=15.0)            # sync: blocks, pinging until you stop it
tr.ping()                                  # or a single one-shot heartbeat
```

## Async (asyncio)

Concurrent calls are multiplexed over one connection (correlation ids), so they
don't block each other.

```python
import asyncio
from corndogs import CorndogsAsyncClient, SubmitTaskRequest
from corndogs.transport_async import AsyncTcpTransport

async def main():
    tr = await AsyncTcpTransport.connect("localhost:5080")
    tr.start_heartbeat()                    # async: background task
    client = CorndogsAsyncClient(tr)

    # 100 submits, concurrent, one connection:
    await asyncio.gather(*(
        client.submit_task(SubmitTaskRequest(
            queue="emails", current_state="submitted", auto_target_state="sending",
            timeout=-1, payload=f"job-{i}".encode(), priority=0,
        )) for i in range(100)
    ))
    await tr.close()

asyncio.run(main())
```

Async heartbeat, two ways:

```python
stop = tr.start_heartbeat(interval=15.0)   # background asyncio task, returns stop()
await tr.run_heartbeat(interval=15.0)      # or await the loop yourself
await tr.ping()                            # single one-shot heartbeat
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

```python
from corndogs import (SubmitKeyedTaskRequest, ClaimGuardedTaskRequest,
                      CompleteGuardedTaskRequest)

sub = client.submit_keyed_task(SubmitKeyedTaskRequest(
    submission_key="order-1234", guarded=True, queue="orders", current_state="submitted",
    auto_target_state="submitted-working", timeout=60, payload=b"...", priority=0))
# sub.replayed is True when the server already accepted this key.

claim = client.claim_guarded_task(ClaimGuardedTaskRequest(
    operation_id="claim-7f3a", queue="orders", current_state="submitted",
    override_timeout=0, override_current_state="", override_auto_target_state="")).delivery
if claim is not None:
    client.complete_guarded_task(CompleteGuardedTaskRequest(
        operation_id="done-7f3a", uuid=claim.task.task.uuid, queue="orders",
        expected_revision=claim.task.revision))
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
- Errors: a service error raises `corndogs.client.ServiceError`; a transport failure
  raises `corndogs.transport.TransportError`.
