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

## Resilience operations

Use these operations when a retry must not create a second task or apply a
stale change. They need a Corndogs 0.8.0 or later server. An older server
returns transport status 2 (unknown operation). After that error, do not send
the legacy operation instead.

- Make one `submission_key` for each logical submission. Send the same key on each retry.
- Make one `operation_id` for each claim, update, completion, or cancellation. Send the same id on each retry.
- Send the `revision` from the claim or from the last result as `expected_revision`.
- An error with "outcome uncertain" in its message means that the request possibly ran. Retry a resilience operation with the same request. Do not retry a legacy mutation automatically.

```ruby
sub = client.submit_keyed_task(SubmitKeyedTaskRequest.new(
  submission_key: "order-1234", guarded: true, queue: "orders", current_state: "submitted",
  auto_target_state: "submitted-working", timeout: 60, payload: bytes, priority: 0
))
# sub.replayed is true when the server already accepted this key.

d = client.claim_guarded_task(ClaimGuardedTaskRequest.new(
  operation_id: "claim-7f3a", queue: "orders", current_state: "submitted",
  override_timeout: 0, override_current_state: "", override_auto_target_state: ""
)).delivery
unless d.nil?
  client.complete_guarded_task(CompleteGuardedTaskRequest.new(
    operation_id: "done-7f3a", uuid: d.task.task.uuid, queue: "orders", expected_revision: d.task.revision
  ))
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
- **One connection, one call in flight:** `TcpTransport` serializes calls under a
  `Mutex` and re-dials automatically on a dropped connection.
- **Clustered deployments:** point the transport at any node; a write that lands on
  a follower is transparently redirected to the leader.
- Errors: a service error raises `CorndogsServiceError` (`#code` / `#message`); a
  transport failure (dropped connection, non-zero transport status) raises
  `TransportError`.
