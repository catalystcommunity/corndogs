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
task = client.get_next_task(GetNextTaskRequest(
    queue="emails", current_state="submitted",
    override_timeout=0, override_current_state="", override_auto_target_state="",
)).task
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

## Notes

- **Transport:** CSIL-RPC over TCP, framed with a 4-byte length prefix. HTTP is not
  used for RPC (the corndogs server serves RPC on its TCP port; HTTP is only for
  health and Prometheus).
- **Clustered deployments:** point the transport at any node; a write that lands on
  a follower is transparently redirected to the leader.
- Errors: a service error raises `corndogs.client.ServiceError`; a transport failure
  raises `corndogs.transport.TransportError`.
