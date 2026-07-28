# corndogs (Ruby client)

The official Ruby client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with a
built-in heartbeat) — you connect and go. No extra setup, no dependencies.

```sh
# TODO: publish corndogs to rubygems.org, then:
gem install corndogs
# or, from a local checkout, in your Gemfile:
#   gem "corndogs", path: "."
```

## Usage

```ruby
require "corndogs"
require "transport"

tr = TcpTransport.new("localhost:5080")   # your corndogs server's TCP address
tr.start_heartbeat                        # keep the connection alive (background thread)
client = CorndogsClient.new(tr)

# Submit a task, then claim the next one from the queue.
client.submit_task(SubmitTaskRequest.new(
  queue: "emails", current_state: "submitted", auto_target_state: "sending",
  timeout: -1, payload: "...".b, priority: 0,
))
delivery = client.get_next_task(GetNextTaskRequest.new(
  queue: "emails", current_state: "submitted",
  override_timeout: 0, override_current_state: "", override_auto_target_state: "",
)).delivery
```

Heartbeat, two ways:

```ruby
stop = tr.start_heartbeat(15)   # background: a Thread, returns a stopper (call it to stop)
stop.call
# ... or run it yourself, blocking, in a Thread you control:
tr.run_heartbeat(15)            # sync: blocks, pinging until you stop it
tr.ping                         # or a single one-shot heartbeat
```

## Notes

- **Transport:** CSIL-RPC over TCP, framed with a 4-byte length prefix. HTTP is not
  used for RPC (the corndogs server serves RPC on its TCP port; HTTP is only for
  health and Prometheus).
- **One connection, one call in flight:** `TcpTransport` serializes calls under a
  `Mutex` and re-dials automatically on a dropped connection.
- **Clustered deployments:** point the transport at any node; a write that lands on
  a follower is transparently redirected to the leader.
- Errors: a service error raises `CorndogsServiceError` (`#code` / `#message`); a
  transport failure (dropped connection, non-zero transport status) raises
  `TransportError`.
