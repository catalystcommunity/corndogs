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
