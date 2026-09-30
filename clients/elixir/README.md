# corndogs (Elixir client)

The official Elixir client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with a
built-in heartbeat) — you connect and go. No extra setup, no dependencies (it uses
only `:gen_tcp`, part of `:kernel`, so it runs as-is under `mix` or a release with no
`extra_applications`).

```elixir
def deps do
  [
    {:corndogs, "~> 0.0.0"}
  ]
end
```

## Usage

```elixir
transport = Corndogs.Transport.connect("localhost:5080")   # your corndogs server's TCP address
Corndogs.Transport.start_heartbeat(transport)               # keep the connection alive (background)
client = Csilgen.Generated.CorndogsClient.new(transport)

# Submit a task, then claim the next one from the queue.
Csilgen.Generated.CorndogsClient.submit_task(client, %Csilgen.Generated.SubmitTaskRequest{
  queue: "emails",
  current_state: "submitted",
  auto_target_state: "sending",
  timeout: -1,
  payload: "...",
  priority: 0
})

resp =
  Csilgen.Generated.CorndogsClient.get_next_task(client, %Csilgen.Generated.GetNextTaskRequest{
    queue: "emails",
    current_state: "submitted",
    override_timeout: 0,
    override_current_state: "",
    override_auto_target_state: ""
  })

delivery = resp.delivery
```

`Corndogs.Transport` is the carrier: it owns the CSIL-RPC envelope, the 4-byte
length-prefix framing, and the TCP connection (a `GenServer`, so calls are
serialized and the socket automatically re-dials after a drop). The typed
`Csilgen.Generated.CorndogsClient` — plus every request/response struct — is the
generated client: it owns (de)serialization only, and calls back into the
transport to move bytes.

## Heartbeat

A background heartbeat keeps an idle connection alive and lets you notice a dead
server quickly. Three ways to run it:

```elixir
stop = Corndogs.Transport.start_heartbeat(transport, 15_000)  # async: background process, returns a stop fun
stop.()                                                        # ... stop it later

# ... or run it yourself, blocking, in a process you own:
Corndogs.Transport.run_heartbeat(transport, 15_000)            # sync: blocks until stopped or a ping fails
# stop a sync loop from another process by sending it the stop message:
# send(heartbeat_pid, :corndogs_stop_heartbeat)

Corndogs.Transport.ping(transport)                              # or a single one-shot heartbeat
```

## Errors

- A structured application error raises `Corndogs.Transport.ServiceError`, with
  `code` and `message` fields.
- A connection/transport failure (dial failed, connection dropped, malformed
  frame) raises `Corndogs.Transport.TransportError`.

```elixir
try do
  Csilgen.Generated.CorndogsClient.submit_task(client, req)
rescue
  e in Corndogs.Transport.ServiceError -> IO.puts("service error #{e.code}: #{e.message}")
  e in Corndogs.Transport.TransportError -> IO.puts("transport error: #{e.message}")
end
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

```elixir
alias Csilgen.Generated.CorndogsClient, as: C

sub =
  C.submit_keyed_task(client, %Csilgen.Generated.SubmitKeyedTaskRequest{
    submission_key: "order-1234", guarded: true, queue: "orders", current_state: "submitted",
    auto_target_state: "submitted-working", timeout: 60, payload: bytes, priority: 0
  })
# sub.replayed is true when the server already accepted this key.

case C.claim_guarded_task(client, %Csilgen.Generated.ClaimGuardedTaskRequest{
       operation_id: "claim-7f3a", queue: "orders", current_state: "submitted",
       override_timeout: 0, override_current_state: "", override_auto_target_state: ""
     }).delivery do
  nil -> :empty
  d ->
    C.complete_guarded_task(client, %Csilgen.Generated.CompleteGuardedTaskRequest{
      operation_id: "done-7f3a", uuid: d.task.task.uuid, queue: "orders",
      expected_revision: d.task.revision
    })
end
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
- **Concurrency:** one `Corndogs.Transport.connect/2` owns one connection and
  serializes calls through its `GenServer`. Share one transport across processes,
  or open more than one if you want concurrent calls in flight.
- `Corndogs.Transport.close/1` closes the connection.
