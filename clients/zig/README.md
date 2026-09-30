# corndogs (Zig client)

The official Zig client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with a
built-in heartbeat) — vendor this directory and go.

## Install

Vendor this directory (`clients/zig/`) into your project — `types.gen.zig`,
`codec.gen.zig`, `client.gen.zig`, and `transport.zig` build together as a small,
dependency-free unit (`std` only):

```zig
const corndogs_client = @import("client.gen.zig");
const corndogs_types = @import("types.gen.zig");
const corndogs_transport = @import("transport.zig");
```

## Usage

```zig
const std = @import("std");
const client = @import("client.gen.zig");
const types = @import("types.gen.zig");
const transport = @import("transport.zig");

pub fn main() !void {
    var gpa = std.heap.GeneralPurposeAllocator(.{}){};
    defer _ = gpa.deinit();
    const alloc = gpa.allocator();

    var tr = try transport.Transport.connect(alloc, "localhost:5080"); // your corndogs server's TCP address
    defer tr.close();
    const svc = client.CorndogsClient.init(tr.asCsilgenTransport());

    // Everything decoded into `resp` is allocated from the arena; free it all at
    // once (this is the generated client's convention, not transport-specific).
    var arena = std.heap.ArenaAllocator.init(alloc);
    defer arena.deinit();
    const a = arena.allocator();

    // Submit a task, then claim the next one from the queue.
    var submitted: types.SubmitTaskResponse = undefined;
    try svc.submit_task(a, &types.SubmitTaskRequest{
        .queue = "emails", .current_state = "submitted", .auto_target_state = "sending",
        .timeout = -1, .payload = "...", .priority = 0,
    }, &submitted);

    var next: types.GetNextTaskResponse = undefined;
    try svc.get_next_task(a, &types.GetNextTaskRequest{
        .queue = "emails", .current_state = "submitted",
        .override_timeout = 0, .override_current_state = "", .override_auto_target_state = "",
    }, &next);
}
```

## Heartbeat (keep the connection alive)

Both a sync (blocking) and an async (background thread) start are provided:

```zig
const hb = try tr.startHeartbeat(alloc, 15 * std.time.ns_per_s); // async: background std.Thread
defer hb.stop();
// ... or run it yourself, blocking, on a thread you control:
//   try tr.runHeartbeat(alloc, 15 * std.time.ns_per_s, &my_stop_flag); // sync-style: blocks until stop/err
//   try tr.ping(alloc);                                                // single one-shot heartbeat
```

`startHeartbeat`'s `alloc` is used from the background thread — pass a thread-safe
allocator (e.g. a `GeneralPurposeAllocator`, which defaults to thread-safe, or
`std.heap.page_allocator`/`c_allocator`).

## Clustered deployments

Point `Transport.connect` at any node; a write that lands on a follower is
transparently redirected to the leader.

## Errors

`Transport.call` (and every typed method built on it) returns a Zig error union.
Two members carry extra context, since a plain Zig `error` can't hold a payload:

- `error.TransportStatus` — a transport-level failure; the server's message is in
  `tr.last_transport_error` (owned, valid until the transport's next call).
- `error.ServiceErrorOccurred` — a typed service error; the code/message are in
  `tr.last_service_error` (valid until the transport's next call — read it
  immediately after the failing call returns).

## Resilience operations

Use these operations when a retry must not create a second task or apply a
stale change. They need a Corndogs 0.8.0 or later server. An older server
returns transport status 2 (unknown operation). After that error, do not send
the legacy operation instead.

- Make one `submission_key` for each logical submission. Send the same key on each retry.
- Make one `operation_id` for each claim, update, completion, or cancellation. Send the same id on each retry.
- Send the `revision` from the claim or from the last result as `expected_revision`.
- `error.OutcomeUncertain` means that the request possibly ran. Retry a resilience operation with the same request. Do not retry a legacy mutation automatically.

```zig
var sub: types.SubmitKeyedTaskResponse = undefined;
try svc.submit_keyed_task(a, &types.SubmitKeyedTaskRequest{
    .submission_key = "order-1234", .guarded = true, .queue = "orders", .current_state = "submitted",
    .auto_target_state = "submitted-working", .timeout = 60, .payload = bytes, .priority = 0,
}, &sub);
// sub.replayed is true when the server already accepted this key.

var claimed: types.ClaimGuardedTaskResponse = undefined;
try svc.claim_guarded_task(a, &types.ClaimGuardedTaskRequest{
    .operation_id = "claim-7f3a", .queue = "orders", .current_state = "submitted",
    .override_timeout = 0, .override_current_state = "", .override_auto_target_state = "",
}, &claimed);
if (claimed.delivery) |d| {
    var done: types.CompleteGuardedTaskResponse = undefined;
    try svc.complete_guarded_task(a, &types.CompleteGuardedTaskRequest{
        .operation_id = "done-7f3a", .uuid = d.task.task.uuid, .queue = "orders",
        .expected_revision = d.task.revision,
    }, &done);
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

- **Transport:** CSIL-RPC over TCP (4-byte big-endian length-prefix framing). HTTP
  is not used for RPC — the server serves RPC on its TCP port; HTTP is only for
  health/metrics.
- **One call in flight:** `transport.zig`'s `Transport` serializes calls under a
  mutex (unlike the multiplexed Go/Python transports) and re-dials on failure.
  Callers from multiple threads simply queue behind the mutex.
- **Envelope framing:** the generated CBOR codec's generic value tree
  (encoder/decoder) is private to `codec.gen.zig`, so `transport.zig` hand-frames
  the small, fixed-shape CSIL-RPC envelope itself — a handful of `append*`/`read*`
  helpers, not a general CBOR library. Every per-type request/response payload is
  still (de)serialized exclusively by the generated `codec.gen.zig` functions;
  the envelope is the only thing this carrier touches by hand.
- Decoded responses (typed and `ServiceError` alike) follow the generated client's
  arena convention: pass an arena allocator to `call`/typed methods and free it
  once when done, rather than freeing individual fields.
