# corndogs (Rust client)

The official Rust client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with a
built-in heartbeat) — connect and go. Dependency-free: std only, no CBOR crate.

```toml
[dependencies]
corndogs = { path = "../corndogs" } # TODO: point at the published/vendored crate
```

## Usage

```rust
use corndogs::{CorndogsClient, SubmitTaskRequest, GetNextTaskRequest};
use corndogs::transport::Transport;

fn main() {
    let tr = Transport::connect("localhost:5080").expect("connect"); // your corndogs server's TCP address
    let client = CorndogsClient::new(tr);

    // Submit a task, then claim the next one from the queue.
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

`Transport` is cheap to `Clone` (it's an `Arc` handle to one shared connection),
so you can keep a clone around to run a heartbeat while the client owns another:

```rust
let tr = Transport::connect("localhost:5080").expect("connect");
let hb = tr.clone();
let client = CorndogsClient::new(tr);
let stop = hb.start_heartbeat(std::time::Duration::from_secs(15));
// ...
stop.stop();
```

## Heartbeat (keep the connection alive)

Both a sync (blocking) and an async — meaning background-thread — start are
provided:

```rust
use std::time::Duration;

let stop = tr.start_heartbeat(Duration::from_secs(15)); // background: std::thread, returns a handle
stop.stop();

// ... or run it yourself, blocking, on a thread you control:
let tr2 = tr.clone();
std::thread::spawn(move || tr2.run_heartbeat(Duration::from_secs(15))); // blocks until a ping fails
tr.ping().expect("ping");                                               // or a single one-shot heartbeat
```

## Clustered deployments

Point the client at any node; a write that lands on a follower is transparently
redirected to the leader.

## Notes

- **Transport:** CSIL-RPC over TCP (4-byte big-endian length-prefix framing). HTTP
  is not used for RPC — the server serves RPC on its TCP port; HTTP is only for
  health/metrics.
- **Concurrency:** `Transport` serializes calls on the connection (one request in
  flight at a time, guarded by a mutex) — simple and dependency-free. Share a
  `Transport` across threads (it's `Clone` + `Send + Sync`) and calls queue up
  rather than racing.
- Errors: a service error is `corndogs::ClientError::Service { code, message }`; a
  transport failure is `corndogs::ClientError::Transport(String)`.
- **Async:** an async transport isn't shipped yet — the generated async client
  (`CorndogsAsyncClient` / `AsyncTransport`) is present, but this crate currently
  has no async runtime dependency, and adding one (e.g. tokio) just for the
  carrier isn't a call this client makes for you. Use the sync `Transport` above,
  or implement `AsyncTransport` yourself against whichever runtime your
  application already uses.
