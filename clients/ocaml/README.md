# corndogs (OCaml client)

The official OCaml client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with a
built-in heartbeat) — you connect and go. No extra setup, no dependencies beyond the
OCaml stdlib (`unix`, `threads.posix`).

```sh
opam install corndogs
```

## Usage

```ocaml
open Corndogs

let () =
  let tr = Transport.connect "localhost:5080" in  (* your corndogs server's TCP address *)
  let stop = Transport.start_heartbeat tr in       (* keep the connection alive (background) *)
  let client = Client.make_client ~call:(Transport.call tr) in

  (* Submit a task, then claim the next one from the queue. *)
  let submitted =
    Client.Corndogs_service.submit_task client
      { queue = "emails"; current_state = "submitted"; auto_target_state = "sending";
        timeout = -1L; payload = Bytes.of_string "..."; priority = 0L }
  in
  let next =
    Client.Corndogs_service.get_next_task client
      { queue = "emails"; current_state = "submitted";
        override_timeout = 0L; override_current_state = ""; override_auto_target_state = "" }
  in
  (match submitted, next with
   | Ok _, Ok { delivery = Some d } -> Printf.printf "claimed %s\n" d.task.uuid
   | _, _ -> ());

  stop ();
  Transport.close tr
```

Every generated call returns `('resp, string) result` — `Ok resp` on success, `Error msg`
on either a transport failure or a decoded `ServiceError` from the server.

## Heartbeat

Both a sync (blocking) and an async (background thread) start are provided, plus a
one-shot ping:

```ocaml
let stop = Transport.start_heartbeat ~interval:15.0 tr in  (* async: background Thread, returns stop() *)
(* ... or run it yourself, blocking, in a thread you control: *)
(* ignore (Thread.create (fun () -> ignore (Transport.run_heartbeat ~interval:15.0 tr)) ()) *)
(* Transport.run_heartbeat ~interval:15.0 tr                  (* sync: blocks, pinging until it fails *) *)
(* ignore (Transport.ping tr)                                 (* single one-shot heartbeat *) *)
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

```ocaml
let sub_req : Types.submit_keyed_task_request =
  { submission_key = "order-1234"; guarded = true; queue = "orders";
    current_state = "submitted"; auto_target_state = "submitted-working";
    timeout = 60L; payload = bytes; priority = 0L }
in
(* On Ok r, r.replayed is true when the server already accepted this key. *)
let _ = Client.Corndogs_service.submit_keyed_task client sub_req in
let claim_req : Types.claim_guarded_task_request =
  { operation_id = "claim-7f3a"; queue = "orders"; current_state = "submitted";
    override_timeout = 0L; override_current_state = ""; override_auto_target_state = "" }
in
match Client.Corndogs_service.claim_guarded_task client claim_req with
| Ok { delivery = Some (d : Types.guarded_delivery); _ } ->
  let done_req : Types.complete_guarded_task_request =
    { operation_id = "done-7f3a"; uuid = d.task.task.uuid; queue = "orders";
      expected_revision = d.task.revision; expected_state = None }
  in
  ignore (Client.Corndogs_service.complete_guarded_task client done_req)
| Ok _ -> ()
| Error e -> prerr_endline e
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

- **Transport:** CSIL-RPC over TCP, framed with a 4-byte big-endian length prefix. HTTP
  is not used for RPC (the corndogs server serves RPC on its TCP port; HTTP is only for
  health and Prometheus).
- **One call in flight:** `Transport.t` serializes calls behind a `Mutex` (OCaml's stdlib
  `Unix` sockets are blocking, so this — not multiplexing — is the natural fit); the
  connection is dialed lazily and re-dialed automatically after a failure.
- **Clustered deployments:** point the transport at any node; a write that lands on a
  follower is transparently redirected to the leader.
- Errors: both a decoded `ServiceError` from the server and a transport-level failure
  (connection dropped, non-zero transport status) surface as `Error <message>` — there is
  no separate exception type to catch.
