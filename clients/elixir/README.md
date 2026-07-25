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

task = resp.task
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
