(* Corndogs client transport: CSIL-RPC over a persistent TCP connection.
   ------------------------------------------------------------------------
   This is Corndogs' own, ready-to-use carrier — you do not need to write one
   or read csilgen docs. Point it at a corndogs server's TCP address and hand
   it to the generated client:

     let tr = Corndogs.Transport.connect "localhost:5080" in
     let _stop = Corndogs.Transport.start_heartbeat tr in  (* keep-alive, background *)
     let client = Corndogs.Client.make_client ~call:(Corndogs.Transport.call tr) in
     match Corndogs.Client.Corndogs_service.submit_task client req with
     | Ok resp -> ...
     | Error msg -> ...

   The wire is CSIL-RPC framed with the canonical 4-byte big-endian length
   prefix over TCP. This carrier owns only the envelope + framing + connection;
   the generated [Codec] owns (de)serialization. Dependency-free (stdlib
   [Unix] plus this package's own [Csil_cbor]).

   One connection, ONE call in flight at a time: [call] is guarded by a
   [Mutex], and the connection is dialed lazily and re-dialed on failure. This
   mirrors the official Python carrier's sync [TcpTransport] (OCaml's stdlib
   [Unix] sockets are blocking, so a multiplexed reader-thread design — as
   used by the Go carrier — is not the natural fit here). *)

module Cbor = Csil_cbor

let tag_encoded_cbor = 24 (* RFC 8949 §3.4.5.1 — embedded encoded CBOR data item *)
let max_frame = 1025 lsl 20 (* payload hard maximum plus RPC envelope allowance *)
let default_connect_timeout = 5.0 (* seconds *)
let default_heartbeat_interval = 15.0 (* seconds *)
let control_service = "CorndogsService"
let op_ping = "$ping" (* control-plane heartbeat op (never collides with app ops) *)

type t = {
  host : string;
  port : int;
  connect_timeout : float;
  mutex : Mutex.t; (* guards fd + next_id: one call in flight at a time *)
  mutable fd : Unix.file_descr option;
  mutable next_id : int64;
  mutable hb_stop : bool ref option; (* set while a background heartbeat thread runs *)
}

let split_addr (addr : string) : string * int =
  match String.rindex_opt addr ':' with
  | None -> failwith (Printf.sprintf "corndogs: address must be host:port, got %s" addr)
  | Some i ->
    let host = String.sub addr 0 i in
    let port_s = String.sub addr (i + 1) (String.length addr - i - 1) in
    (try (host, int_of_string port_s)
     with _ -> failwith (Printf.sprintf "corndogs: address must be host:port, got %s" addr))

(* [connect addr] builds a transport for "host:port". Dialing is lazy: no
   socket is opened until the first [call]. *)
let connect ?(connect_timeout = default_connect_timeout) (addr : string) : t =
  let host, port = split_addr addr in
  { host; port; connect_timeout; mutex = Mutex.create (); fd = None; next_id = 0L; hb_stop = None }

(* --- socket I/O -------------------------------------------------------- *)

let dial (host : string) (port : int) (timeout : float) : Unix.file_descr =
  let he =
    try Unix.gethostbyname host
    with Not_found -> failwith (Printf.sprintf "corndogs: cannot resolve host %s" host)
  in
  if Array.length he.Unix.h_addr_list = 0 then
    failwith (Printf.sprintf "corndogs: cannot resolve host %s" host);
  let sockaddr = Unix.ADDR_INET (he.Unix.h_addr_list.(0), port) in
  let fd = Unix.socket Unix.PF_INET Unix.SOCK_STREAM 0 in
  (try
     Unix.set_nonblock fd;
     (try Unix.connect fd sockaddr with
      | Unix.Unix_error (Unix.EINPROGRESS, _, _) -> (
        let _, writable, _ = Unix.select [] [ fd ] [] timeout in
        match writable with
        | [] -> failwith "corndogs: connect timeout"
        | _ -> (
          match Unix.getsockopt_error fd with
          | Some err -> raise (Unix.Unix_error (err, "connect", host))
          | None -> ())));
     Unix.clear_nonblock fd;
     Unix.setsockopt fd Unix.TCP_NODELAY true;
     fd
   with e ->
     (try Unix.close fd with _ -> ());
     raise e)

let rec write_all (fd : Unix.file_descr) (b : bytes) (off : int) (len : int) : unit =
  if off < len then
    let n = try Unix.write fd b off (len - off) with Unix.Unix_error (Unix.EINTR, _, _) -> 0 in
    write_all fd b (off + n) len

(* Reads exactly [n] bytes, or returns [None] on a clean EOF before any byte of
   this read (a "connection closed" signal, mirroring the Go/Python carriers). *)
let read_exact (fd : Unix.file_descr) (n : int) : bytes option =
  let buf = Bytes.create n in
  let rec loop off =
    if off >= n then Some buf
    else
      let r = try Unix.read fd buf off (n - off) with Unix.Unix_error (Unix.EINTR, _, _) -> -1 in
      if r = 0 then None
      else if r < 0 then loop off
      else loop (off + r)
  in
  loop 0

let write_frame (fd : Unix.file_descr) (payload : bytes) : unit =
  let len = Bytes.length payload in
  if len > max_frame then failwith (Printf.sprintf "corndogs: frame too large (%d)" len);
  let prefix = Bytes.create 4 in
  Bytes.set prefix 0 (Char.chr ((len lsr 24) land 0xff));
  Bytes.set prefix 1 (Char.chr ((len lsr 16) land 0xff));
  Bytes.set prefix 2 (Char.chr ((len lsr 8) land 0xff));
  Bytes.set prefix 3 (Char.chr (len land 0xff));
  write_all fd prefix 0 4;
  write_all fd payload 0 len

let read_frame (fd : Unix.file_descr) : bytes option =
  match read_exact fd 4 with
  | None -> None
  | Some hdr ->
    let byte i = Char.code (Bytes.get hdr i) in
    let len = (byte 0 lsl 24) lor (byte 1 lsl 16) lor (byte 2 lsl 8) lor byte 3 in
    if len > max_frame then failwith (Printf.sprintf "corndogs: frame too large (%d)" len);
    read_exact fd len

(* --- envelope ------------------------------------------------------------ *)

let build_request ~(service : string) ~(op : string) ~(id : int64) ~(payload : bytes) : bytes =
  Cbor.encode
    (Cbor.Map
       [
         (Cbor.Text "v", Cbor.Uint 1L);
         (Cbor.Text "service", Cbor.Text service);
         (Cbor.Text "op", Cbor.Text op);
         (Cbor.Text "id", Cbor.Uint id);
         (Cbor.Text "payload", Cbor.Tag (tag_encoded_cbor, Cbor.Bytes payload));
       ])

let map_get (kvs : (Cbor.t * Cbor.t) list) (key : string) : Cbor.t option =
  List.find_map (fun (k, v) -> if k = Cbor.Text key then Some v else None) kvs

(* Decodes one response frame into the inner reply payload, or a
   human-readable error string. A non-zero transport [status] or a
   "ServiceError" [variant] both surface as [Error]. *)
let parse_response (frame : bytes) : (bytes, string) result =
  match Cbor.decode frame with
  | Error e -> Error ("corndogs: decode response envelope: " ^ e)
  | Ok (Cbor.Map kvs) -> (
    let status = match map_get kvs "status" with Some v -> ( try Cbor.to_i64 v with _ -> 0L) | None -> 0L in
    if Int64.compare status 0L <> 0 then
      let msg = match map_get kvs "error" with Some v -> ( try Cbor.to_text v with _ -> "") | None -> "" in
      Error (Printf.sprintf "corndogs: transport status %Ld: %s" status msg)
    else
      match map_get kvs "payload" with
      | None -> Ok Bytes.empty (* control replies (e.g. $pong) carry no payload *)
      | Some pv -> (
        let pv = match pv with Cbor.Tag (_, inner) -> inner | other -> other in
        match pv with
        | Cbor.Bytes inner -> (
          match map_get kvs "variant" with
          | Some (Cbor.Text "ServiceError") -> (
            try
              let se = Codec.decode_service_error_bytes inner in
              Error (Printf.sprintf "corndogs: service error %Ld: %s" se.Types.code se.Types.message)
            with Failure m -> Error ("corndogs: undecodable ServiceError payload: " ^ m))
          | _ -> Ok inner)
        | _ -> Error "corndogs: response payload is not a tag-24 byte string"))
  | Ok _ -> Error "corndogs: malformed response envelope"

(* --- connection lifecycle ------------------------------------------------ *)

let ensure_fd (t : t) : Unix.file_descr =
  match t.fd with
  | Some fd -> fd
  | None ->
    let fd = dial t.host t.port t.connect_timeout in
    t.fd <- Some fd;
    fd

let reset (t : t) : unit =
  (match t.fd with
   | Some fd -> ( try Unix.close fd with _ -> ())
   | None -> ());
  t.fd <- None

(* [call t ~service ~op ~payload] performs one CSIL-RPC round trip and returns
   the inner reply payload bytes (or an error). Matches the generated
   [Client.client]'s [call] seam exactly, so `Transport.call tr` partially
   applies straight into [Client.make_client ~call:(Transport.call tr)].
   Thread-safe: calls are serialized (one in flight); a failed call tears
   down the connection so the next call re-dials. *)
let call (t : t) ~(service : string) ~(op : string) ~(payload : bytes) : (bytes, string) result =
  Mutex.lock t.mutex;
  Fun.protect
    ~finally:(fun () -> Mutex.unlock t.mutex)
    (fun () ->
      match
        (try Ok (ensure_fd t) with
         | Failure m -> Error m
         | Unix.Unix_error (e, fn, _) -> Error (Printf.sprintf "corndogs: %s: %s" fn (Unix.error_message e)))
      with
      | Error e -> Error e
      | Ok fd -> (
        t.next_id <- Int64.add t.next_id 1L;
        let id = t.next_id in
        let env = build_request ~service ~op ~id ~payload in
        try
          write_frame fd env;
          match read_frame fd with
          | None ->
            reset t;
            Error "corndogs: connection closed"
          | Some resp -> parse_response resp
        with
        | Failure m ->
          reset t;
          Error m
        | Unix.Unix_error (e, fn, _) ->
          reset t;
          Error (Printf.sprintf "corndogs: %s: %s" fn (Unix.error_message e))))

(* --- heartbeat: one-shot, sync (blocking), and async (background thread) - *)

(* Sends a single control-plane heartbeat; [Error _] means the server is
   unreachable. Cheap; keeps an idle connection alive and detects a dead
   server. *)
let ping (t : t) : (unit, string) result =
  match call t ~service:control_service ~op:op_ping ~payload:Bytes.empty with
  | Ok _ -> Ok ()
  | Error e -> Error e

(* SYNCHRONOUS heartbeat start: blocks, pinging every [interval] seconds,
   until [stop] is set to [true] (returns [None]) or a ping fails (returns
   [Some error]). Run it in a [Thread] you own, or use [start_heartbeat] for
   the background form. *)
let run_heartbeat ?(interval = default_heartbeat_interval) ?(stop : bool ref option) (t : t) : string option =
  let stopped () = match stop with Some r -> !r | None -> false in
  let rec loop () =
    if stopped () then None
    else begin
      Thread.delay interval;
      if stopped () then None
      else
        match ping t with
        | Ok () -> loop ()
        | Error e -> Some e
    end
  in
  loop ()

(* ASYNCHRONOUS heartbeat start: runs the heartbeat on a background [Thread]
   and returns a stop function. Only one background heartbeat runs at a
   time — calling this again while one is running returns a stopper for the
   existing one. *)
let start_heartbeat ?(interval = default_heartbeat_interval) (t : t) : unit -> unit =
  match t.hb_stop with
  | Some existing -> fun () -> existing := true
  | None ->
    let stop = ref false in
    t.hb_stop <- Some stop;
    ignore (Thread.create (fun () -> ignore (run_heartbeat ~interval ~stop t)) ());
    fun () ->
      stop := true;
      t.hb_stop <- None

(* Shuts the transport down: stops any background heartbeat and closes the
   connection. *)
let close (t : t) : unit =
  (match t.hb_stop with
   | Some stop ->
     stop := true;
     t.hb_stop <- None
   | None -> ());
  Mutex.lock t.mutex;
  reset t;
  Mutex.unlock t.mutex

(* Convenience: wires a ready [Client.client] over a fresh transport for
   [addr] (host:port). Equivalent to [Client.make_client ~call:(call
   (connect addr))], returned alongside the transport so callers can still
   drive the heartbeat / [close] it. *)
let client (t : t) : Client.client = Client.make_client ~call:(call t)
