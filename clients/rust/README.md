# corndogs (Rust client)

The official Rust client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. The crate includes a transport (CSIL-RPC over TCP, with a
heartbeat). The default build has no dependencies: std only, no CBOR crate. TLS is
optional (cargo feature `tls`).

```toml
[dependencies]
corndogs = { path = "../corndogs" } # TODO: point at the published/vendored crate
```

## Usage

Use `Transport::connect_with` with an I/O timeout in production:

```rust
use std::time::Duration;
use corndogs::{CorndogsClient, SubmitTaskRequest, GetNextTaskRequest};
use corndogs::transport::Transport;

fn main() {
    let tr = Transport::connect_with(
        "localhost:5080",              // the TCP address of your corndogs server
        Duration::from_secs(5),        // connect timeout
        Some(Duration::from_secs(10)), // deadline for each call
    ).expect("connect");
    let client = CorndogsClient::new(tr);

    // Submit a task, then claim the next task from the queue.
    client.submit_task(SubmitTaskRequest {
        queue: "emails".into(), current_state: "submitted".into(),
        auto_target_state: "sending".into(), timeout: -1, payload: b"...".to_vec(), priority: 0,
    }).expect("submit_task");

    let got = client.get_next_task(GetNextTaskRequest {
        queue: "emails".into(), current_state: "submitted".into(),
        override_timeout: 0, override_current_state: String::new(), override_auto_target_state: String::new(),
    }).expect("get_next_task");
    let _delivery = got.delivery;
}
```

`Transport::connect(addr)` and `Transport::connect_timeout(addr, t)` also work. They
set no I/O deadline. If the server stops answering while TCP stays up, a call on such
a transport waits with no limit.

`ConnectOptions` holds all connect settings. Use it with `Transport::connect_options`:

```rust
use corndogs::transport::{ConnectOptions, Transport};

let opts = ConnectOptions::new()
    .connect_timeout(Duration::from_secs(5))
    .io_timeout(Duration::from_secs(10));
let tr = Transport::connect_options("localhost:5080", opts).expect("connect");
```

## Timeouts

- The connect timeout limits the TCP connect. With TLS, it also limits the TLS
  handshake.
- The I/O timeout is one deadline for the full call. The deadline includes the
  re-dial (if necessary), the request write, and the full reply read. A server that
  sends bytes slowly cannot make the call go past the deadline.
- A call that times out returns `ClientError::Transport`. The message contains
  "timed out".
- After a timeout or any other I/O error, the transport drops the connection. The
  next call dials a new connection. Thus a late reply to an old call cannot become the
  reply to a new call.
- The transport compares the reply id with the request id. If they are different,
  the transport drops the connection and returns `ClientError::Transport`.
- `ping` and the heartbeat use the same deadline.

## TLS

The server uses TLS on its RPC TCP port when the operator sets
`CORNDOGS_TLS_CERT_FILE` and `CORNDOGS_TLS_KEY_FILE`. Enable the `tls` feature to
connect to such a server:

```toml
[dependencies]
corndogs = { path = "../corndogs", features = ["tls"] }
```

The feature adds `rustls` (with the `ring` provider), `rustls-pki-types`, and
`rustls-native-certs`. It does not need a C toolchain.

Verify the server with a CA file (PEM, one or more certificates):

```rust
use corndogs::transport::{ConnectOptions, TlsOptions, Transport};

let opts = ConnectOptions::new()
    .connect_timeout(Duration::from_secs(5))
    .io_timeout(Duration::from_secs(10))
    .tls(TlsOptions::ca_file("/etc/corndogs/ca.pem"));
let tr = Transport::connect_options("corndogs.internal:5080", opts).expect("connect");
```

Verify the server with the system root store:

```rust
let tr = Transport::connect_tls("corndogs.example.com:5080", TlsOptions::system_roots())
    .expect("connect");
```

`connect_tls` sets no I/O deadline. Use `connect_options` to set one.

The client uses the host part of the address as the server name. The server
certificate must be valid for that name. To connect to an IP address with a
certificate for a DNS name, set the name:

```rust
let tls = TlsOptions::ca_file("/etc/corndogs/ca.pem").server_name("corndogs.internal");
```

The server does not ask for a client certificate. A re-dial after a dropped
connection does a new TLS handshake.

## Heartbeat (keep the connection alive)

`Transport` is cheap to `Clone`. All clones share one connection. Keep one clone to
run a heartbeat, and give one clone to the client:

```rust
let hb = tr.clone();
let client = CorndogsClient::new(tr);
let stop = hb.start_heartbeat(Duration::from_secs(15)); // background std::thread
// ...
stop.stop();
```

You can also run the heartbeat on a thread that you control:

```rust
let tr2 = tr.clone();
std::thread::spawn(move || tr2.run_heartbeat(Duration::from_secs(15))); // blocks until a ping fails
tr.ping().expect("ping");                                               // one heartbeat
```

## Update a task

In `UpdateTaskRequest`, `payload` and `priority` are optional:

- `payload: None` keeps the stored payload.
- `priority: None` keeps the stored priority. `Some(p)` sets the priority to `p`.

```rust
use corndogs::UpdateTaskRequest;

client.update_task(UpdateTaskRequest {
    uuid: task.uuid.clone(), queue: "emails".into(),
    current_state: task.current_state.clone(), auto_target_state: "sending".into(),
    timeout: -1, new_state: "retry".into(), payload: None, priority: None,
}).expect("update_task");
```

## Clustered deployments

Point the client at any node. The server sends a write that lands on a follower to
the leader.

## Notes

- **Transport:** CSIL-RPC over TCP (4-byte big-endian length-prefix framing). With
  the `tls` feature, the same framing goes inside TLS. The client does not use HTTP
  for RPC. The server uses HTTP only for health and metrics.
- **Concurrency:** one `Transport` is one connection. It sends one call at a time and
  holds a mutex for the full call. The deadline starts when a call gets the mutex. A
  caller that waits behind a stuck call can wait up to the deadline of the stuck
  call, plus its own deadline. Give each role or thread its own `Transport`, or use a
  small pool of them.
- **Errors:** a service error is `corndogs::ClientError::Service { code, message }`.
  A transport failure (and a timeout) is `corndogs::ClientError::Transport(String)`.
- **Async:** the crate has no async transport. The generated async client
  (`CorndogsAsyncClient` / `AsyncTransport`) is present, but the crate has no async
  runtime dependency. Use the sync `Transport`, or implement `AsyncTransport` with
  the runtime of your application.
