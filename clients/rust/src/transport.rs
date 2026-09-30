//! Corndogs' own, ready-to-use client transport carrier: CSIL-RPC over a
//! persistent TCP connection, framed with the canonical 4-byte big-endian
//! length prefix (the CSIL "StreamCarrier"). This is the single official
//! Corndogs Rust carrier — you do not need to write one or read csilgen docs.
//!
//! ## Connect
//!
//! Use [`Transport::connect_with`] with an I/O timeout in production:
//!
//! ```no_run
//! use corndogs::{CorndogsClient, SubmitTaskRequest};
//! use corndogs::transport::Transport;
//! use std::time::Duration;
//!
//! let tr = Transport::connect_with(
//!     "localhost:5080",
//!     Duration::from_secs(5),        // connect timeout
//!     Some(Duration::from_secs(10)), // deadline for each call
//! ).expect("connect");
//! let stop = tr.start_heartbeat(Duration::from_secs(15)); // keep the connection alive
//! let client = CorndogsClient::new(tr);
//! let _ = client.submit_task(SubmitTaskRequest {
//!     queue: "emails".into(), current_state: "submitted".into(),
//!     auto_target_state: "sending".into(), timeout: -1, payload: b"...".to_vec(), priority: 0,
//! });
//! stop.stop();
//! ```
//!
//! [`Transport::connect`] and [`Transport::connect_timeout`] set no I/O
//! deadline. A call on such a transport waits with no limit if the server
//! stops answering while TCP stays up.
//!
//! [`ConnectOptions`] holds all connect settings (connect timeout, I/O
//! timeout, and TLS with the `tls` cargo feature). Use it with
//! [`Transport::connect_options`].
//!
//! ## Deadlines
//!
//! - The connect timeout limits the TCP connect. With TLS, it also limits the
//!   TLS handshake.
//! - The I/O timeout is one deadline for the full call: the re-dial (if
//!   necessary), the request write, and the full reply read. A server that
//!   sends bytes slowly cannot make the call go past the deadline.
//! - When a call times out, the transport returns
//!   [`ClientError::Transport`] with a message that contains "timed out".
//! - After a timeout or any other I/O error, the transport drops the
//!   connection. The next call dials a new connection. Thus a late reply to
//!   an old call cannot become the reply to a new call.
//! - The transport also compares the reply `id` with the request `id`. If
//!   they are different, the transport drops the connection and returns
//!   [`ClientError::Transport`].
//! - [`Transport::ping`] and the heartbeat use the same deadline.
//!
//! ## Concurrency
//!
//! One `Transport` is one connection. It sends one call at a time and holds a
//! `Mutex` for the full round trip. The deadline starts when a call gets the
//! mutex. A caller that waits on the mutex behind a stuck call can wait up to
//! the full deadline of the stuck call, plus its own deadline. Give each role
//! or thread its own `Transport`, or use a small pool of them.
//!
//! `Transport` is cheap to `Clone`. All clones share one connection and one
//! heartbeat. Thus you can give one clone to a `CorndogsClient` and keep
//! another clone for a heartbeat.
//!
//! This carrier implements the generated [`crate::client::Transport`] trait's
//! `call(service, op, req) -> Result<Vec<u8>, ClientError>` seam. Without the
//! `tls` feature it has no dependencies: std only, no cbor crate.
//!
//! ### Why this file hand-rolls a few CBOR primitives
//!
//! `codec.gen.rs` owns per-message (de)serialization (`encode_submit_task_request`,
//! …) and publishes the value-tree type [`crate::CsilCborValue`], but its
//! low-level encode/decode helpers (`cbor_encode`, `cbor_decode`, `cbor_text`,
//! …) are *private* to that module. The Go carrier can reuse the equivalent
//! generated helpers because Go visibility is package-wide; Rust's module
//! privacy is stricter, and `codec.gen.rs` is generated code this carrier must
//! not edit just to add `pub`. So this file implements the small amount of
//! canonical-CBOR encode/decode needed for the fixed-shape envelope itself (a
//! 5-key request map; a handful of response fields) — the same canonical
//! encoding rules as the generated codec, producing byte-identical wire
//! output. Message *payloads* stay fully opaque bytes produced/consumed by
//! the generated `encode_*`/`decode_*` functions; the only generated decoder
//! this file calls is [`crate::decode_service_error`], to surface a typed
//! service error.

use std::io::{self, Read, Write};
use std::net::{TcpStream, ToSocketAddrs};
use std::sync::{Arc, Condvar, Mutex};
use std::thread;
use std::time::{Duration, Instant};

use crate::client::ClientError;
use crate::CsilCborValue;

#[cfg(feature = "tls")]
pub use crate::tls::TlsOptions;

const TAG_ENCODED_CBOR: u64 = 24; // RFC 8949 §3.4.5.1 — embedded encoded CBOR data item
const CONTROL_SERVICE: &str = "CorndogsService";
const OP_PING: &str = "$ping"; // control-plane heartbeat op (never collides with an app op)
const MAX_FRAME: usize = 1025 << 20; // payload hard maximum plus RPC envelope allowance
const DEFAULT_CONNECT_TIMEOUT: Duration = Duration::from_secs(5);

// --- deadline-aware socket ------------------------------------------------

fn timed_out() -> io::Error {
    io::Error::new(io::ErrorKind::TimedOut, "corndogs: timed out")
}

fn is_timeout(e: &io::Error) -> bool {
    matches!(e.kind(), io::ErrorKind::TimedOut | io::ErrorKind::WouldBlock)
}

/// A `TcpStream` with an absolute deadline. Before each read or write it sets
/// the socket timeout to the time that remains. Thus a peer that sends one
/// byte at a time cannot extend the operation past the deadline. With no
/// deadline, the socket blocks with no limit.
struct DeadlineStream {
    tcp: TcpStream,
    deadline: Option<Instant>,
}

impl DeadlineStream {
    fn new(tcp: TcpStream) -> Self {
        Self { tcp, deadline: None }
    }

    fn set_deadline(&mut self, deadline: Option<Instant>) -> io::Result<()> {
        if deadline.is_none() && self.deadline.is_some() {
            self.tcp.set_read_timeout(None)?;
            self.tcp.set_write_timeout(None)?;
        }
        self.deadline = deadline;
        Ok(())
    }

    /// Returns the time left before the deadline, or a timeout error.
    fn remaining(&self) -> io::Result<Option<Duration>> {
        match self.deadline {
            None => Ok(None),
            Some(d) => {
                let now = Instant::now();
                if now >= d {
                    Err(timed_out())
                } else {
                    Ok(Some(d - now))
                }
            }
        }
    }
}

impl Read for DeadlineStream {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        if let Some(left) = self.remaining()? {
            self.tcp.set_read_timeout(Some(left))?;
        }
        self.tcp
            .read(buf)
            .map_err(|e| if is_timeout(&e) { timed_out() } else { e })
    }
}

impl Write for DeadlineStream {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        if let Some(left) = self.remaining()? {
            self.tcp.set_write_timeout(Some(left))?;
        }
        self.tcp
            .write(buf)
            .map_err(|e| if is_timeout(&e) { timed_out() } else { e })
    }

    fn flush(&mut self) -> io::Result<()> {
        self.tcp.flush()
    }
}

/// One live connection: plain TCP, or TLS over TCP.
enum Conn {
    Plain(DeadlineStream),
    #[cfg(feature = "tls")]
    Tls(Box<rustls::StreamOwned<rustls::ClientConnection, DeadlineStream>>),
}

impl Conn {
    fn set_deadline(&mut self, deadline: Option<Instant>) -> io::Result<()> {
        match self {
            Conn::Plain(s) => s.set_deadline(deadline),
            #[cfg(feature = "tls")]
            Conn::Tls(s) => s.sock.set_deadline(deadline),
        }
    }
}

impl Read for Conn {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        match self {
            Conn::Plain(s) => s.read(buf),
            #[cfg(feature = "tls")]
            Conn::Tls(s) => s.read(buf),
        }
    }
}

impl Write for Conn {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        match self {
            Conn::Plain(s) => s.write(buf),
            #[cfg(feature = "tls")]
            Conn::Tls(s) => s.write(buf),
        }
    }

    fn flush(&mut self) -> io::Result<()> {
        match self {
            Conn::Plain(s) => s.flush(),
            #[cfg(feature = "tls")]
            Conn::Tls(s) => s.flush(),
        }
    }
}

// --- options ----------------------------------------------------------------

/// Connect settings for [`Transport::connect_options`].
///
/// ```no_run
/// use corndogs::transport::{ConnectOptions, Transport};
/// use std::time::Duration;
///
/// let opts = ConnectOptions::new()
///     .connect_timeout(Duration::from_secs(5))
///     .io_timeout(Duration::from_secs(10));
/// let tr = Transport::connect_options("localhost:5080", opts).expect("connect");
/// ```
#[derive(Clone, Debug)]
pub struct ConnectOptions {
    connect_timeout: Duration,
    io_timeout: Option<Duration>,
    #[cfg(feature = "tls")]
    tls: Option<TlsOptions>,
}

impl Default for ConnectOptions {
    fn default() -> Self {
        Self::new()
    }
}

impl ConnectOptions {
    /// Makes options with a 5 second connect timeout, no I/O timeout, and no
    /// TLS.
    pub fn new() -> Self {
        Self {
            connect_timeout: DEFAULT_CONNECT_TIMEOUT,
            io_timeout: None,
            #[cfg(feature = "tls")]
            tls: None,
        }
    }

    /// Sets the limit for the TCP connect and the TLS handshake.
    pub fn connect_timeout(mut self, timeout: Duration) -> Self {
        self.connect_timeout = timeout;
        self
    }

    /// Sets the deadline for each full call (re-dial, write, and read).
    pub fn io_timeout(mut self, timeout: Duration) -> Self {
        self.io_timeout = Some(timeout);
        self
    }

    /// Sets or removes the I/O deadline. `None` means no limit.
    pub fn io_timeout_opt(mut self, timeout: Option<Duration>) -> Self {
        self.io_timeout = timeout;
        self
    }

    /// Enables TLS with these options.
    #[cfg(feature = "tls")]
    pub fn tls(mut self, tls: TlsOptions) -> Self {
        self.tls = Some(tls);
        self
    }
}

// --- connection state -------------------------------------------------------

struct ConnState {
    stream: Option<Conn>,
    next_id: u64,
}

/// An `Event`-like stop signal: `wait` sleeps up to a timeout but wakes early
/// (and returns `true`) as soon as `set` is called from another thread.
struct StopFlag {
    stopped: Mutex<bool>,
    cv: Condvar,
}

impl StopFlag {
    fn new() -> Self {
        Self {
            stopped: Mutex::new(false),
            cv: Condvar::new(),
        }
    }

    /// Sleeps up to `timeout`; returns `true` if `set` fired (early or not).
    fn wait(&self, timeout: Duration) -> bool {
        let guard = self.stopped.lock().unwrap();
        let (guard, _) = self.cv.wait_timeout_while(guard, timeout, |s| !*s).unwrap();
        *guard
    }

    fn set(&self) {
        *self.stopped.lock().unwrap() = true;
        self.cv.notify_all();
    }
}

/// A stop handle for a background heartbeat started by [`Transport::start_heartbeat`].
/// Calling `stop` (or dropping every clone of the owning [`Transport`]) ends it.
#[derive(Clone)]
pub struct HeartbeatHandle {
    flag: Arc<StopFlag>,
}

impl HeartbeatHandle {
    /// Stops the background heartbeat thread. Idempotent.
    pub fn stop(&self) {
        self.flag.set();
    }
}

struct TransportInner {
    addr: String,
    connect_timeout: Duration,
    io_timeout: Option<Duration>,
    #[cfg(feature = "tls")]
    tls: Option<crate::tls::TlsSetup>,
    state: Mutex<ConnState>,
    hb_stop: Mutex<Option<HeartbeatHandle>>,
}

impl TransportInner {
    /// Dials a new connection (and does the TLS handshake if TLS is on). The
    /// connect timeout limits the dial. `call_deadline`, if set, also limits
    /// it.
    fn dial(&self, call_deadline: Option<Instant>) -> Result<Conn, ClientError> {
        let mut deadline = Instant::now() + self.connect_timeout;
        if let Some(cd) = call_deadline {
            deadline = deadline.min(cd);
        }
        let addrs: Vec<_> = self
            .addr
            .to_socket_addrs()
            .map_err(|e| ClientError::Transport(format!("corndogs: resolve {}: {e}", self.addr)))?
            .collect();
        if addrs.is_empty() {
            return Err(ClientError::Transport(format!(
                "corndogs: no address for {}",
                self.addr
            )));
        }

        let mut last_err: Option<io::Error> = None;
        let mut tcp = None;
        for addr in &addrs {
            let now = Instant::now();
            if now >= deadline {
                last_err = Some(timed_out());
                break;
            }
            match TcpStream::connect_timeout(addr, deadline - now) {
                Ok(s) => {
                    tcp = Some(s);
                    break;
                }
                Err(e) => last_err = Some(if is_timeout(&e) { timed_out() } else { e }),
            }
        }
        let tcp = match tcp {
            Some(s) => s,
            None => {
                let e = last_err.unwrap_or_else(timed_out);
                return Err(ClientError::Transport(format!("corndogs: connect {}: {e}", self.addr)));
            }
        };
        tcp.set_nodelay(true).ok();
        let sock = DeadlineStream::new(tcp);

        #[cfg(feature = "tls")]
        if let Some(tls) = &self.tls {
            return self.handshake(tls, sock, deadline);
        }
        Ok(Conn::Plain(sock))
    }

    #[cfg(feature = "tls")]
    fn handshake(
        &self,
        tls: &crate::tls::TlsSetup,
        mut sock: DeadlineStream,
        deadline: Instant,
    ) -> Result<Conn, ClientError> {
        let err = |e: io::Error| ClientError::Transport(format!("corndogs: tls handshake with {}: {e}", self.addr));
        sock.set_deadline(Some(deadline)).map_err(err)?;
        let mut stream = rustls::StreamOwned::new(tls.connection()?, sock);
        while stream.conn.is_handshaking() {
            let (rd, wr) = stream.conn.complete_io(&mut stream.sock).map_err(err)?;
            if rd == 0 && wr == 0 && stream.conn.is_handshaking() {
                return Err(err(io::Error::new(
                    io::ErrorKind::UnexpectedEof,
                    "connection closed during handshake",
                )));
            }
        }
        Ok(Conn::Tls(Box::new(stream)))
    }

    /// Sends one request and blocks for its correlated response. Holds the
    /// connection mutex for the whole round trip: one call in flight on this
    /// connection at a time. The I/O deadline (if set) starts when the call
    /// gets the mutex and covers the re-dial, the write, and the read. An I/O
    /// error, a timeout, an undecodable reply, or a reply with a different id
    /// drops the connection so the next call re-dials.
    fn call(&self, service: &str, op: &str, req: &[u8]) -> Result<Vec<u8>, ClientError> {
        let mut state = self.state.lock().unwrap();
        let deadline = self.io_timeout.map(|t| Instant::now() + t);
        if state.stream.is_none() {
            state.stream = Some(self.dial(deadline)?);
        }
        state.next_id += 1;
        let id = state.next_id;
        let env = encode_request(id, service, op, req);

        let outcome: io::Result<Vec<u8>> = (|| {
            let stream = state.stream.as_mut().expect("dialed above");
            stream.set_deadline(deadline)?;
            write_frame(stream, &env)?;
            read_frame(stream)?
                .ok_or_else(|| io::Error::new(io::ErrorKind::UnexpectedEof, "corndogs: connection closed"))
        })();

        let frame = match outcome {
            Ok(frame) => frame,
            Err(e) => {
                // The request may have reached the server. The call is not
                // sent again: a legacy mutation could run twice.
                state.stream = None; // torn down; the next call re-dials
                if e.kind() == io::ErrorKind::TimedOut {
                    let limit = self.io_timeout.unwrap_or_default();
                    return Err(ClientError::Transport(format!(
                        "corndogs: outcome uncertain: {service}/{op} timed out after {limit:?}"
                    )));
                }
                return Err(ClientError::Transport(format!("corndogs: outcome uncertain: {e}")));
            }
        };

        let val = match decode_cbor(&frame).and_then(|v| check_id(&v, id).map(|()| v)) {
            Ok(v) => v,
            Err(e) => {
                state.stream = None; // unknown stream state; the next call re-dials
                return Err(ClientError::Transport(format!("corndogs: bad response envelope: {e}")));
            }
        };
        drop(state);
        parse_response(&val)
    }

    fn ping(&self) -> Result<(), ClientError> {
        self.call(CONTROL_SERVICE, OP_PING, &[])?;
        Ok(())
    }
}

/// Sync CSIL-RPC transport over one persistent TCP connection (the canonical
/// Corndogs Rust carrier). Implements the generated [`crate::client::Transport`]
/// trait. Cheap to `Clone`: every clone shares the same underlying connection
/// and heartbeat state (it is an `Arc` handle), so you can hand one clone to a
/// `CorndogsClient` and keep another to drive a heartbeat.
#[derive(Clone)]
pub struct Transport(Arc<TransportInner>);

impl Transport {
    /// Dials `addr` ("host:port") and returns a ready transport. It uses a
    /// 5 second connect timeout and no I/O deadline.
    pub fn connect(addr: impl Into<String>) -> Result<Self, ClientError> {
        Self::connect_options(addr, ConnectOptions::new())
    }

    /// Like [`Transport::connect`], with an explicit dial timeout. It sets no
    /// I/O deadline.
    pub fn connect_timeout(addr: impl Into<String>, connect_timeout: Duration) -> Result<Self, ClientError> {
        Self::connect_options(addr, ConnectOptions::new().connect_timeout(connect_timeout))
    }

    /// Dials `addr` with a connect timeout and an optional I/O deadline for
    /// each call. This is the recommended form for production. See the
    /// module docs for the deadline rules.
    pub fn connect_with(
        addr: impl Into<String>,
        connect_timeout: Duration,
        io_timeout: Option<Duration>,
    ) -> Result<Self, ClientError> {
        Self::connect_options(
            addr,
            ConnectOptions::new()
                .connect_timeout(connect_timeout)
                .io_timeout_opt(io_timeout),
        )
    }

    /// Dials `addr` over TLS with a 5 second connect timeout and no I/O
    /// deadline. Use [`Transport::connect_options`] to also set an I/O
    /// deadline.
    #[cfg(feature = "tls")]
    pub fn connect_tls(addr: impl Into<String>, tls: TlsOptions) -> Result<Self, ClientError> {
        Self::connect_options(addr, ConnectOptions::new().tls(tls))
    }

    /// Dials `addr` with all settings in `opts`.
    pub fn connect_options(addr: impl Into<String>, opts: ConnectOptions) -> Result<Self, ClientError> {
        let addr = addr.into();
        #[cfg(feature = "tls")]
        let tls = match &opts.tls {
            Some(t) => Some(t.resolve(&addr)?),
            None => None,
        };
        let inner = TransportInner {
            addr,
            connect_timeout: opts.connect_timeout,
            io_timeout: opts.io_timeout,
            #[cfg(feature = "tls")]
            tls,
            state: Mutex::new(ConnState {
                stream: None,
                next_id: 0,
            }),
            hb_stop: Mutex::new(None),
        };
        let stream = inner.dial(None)?;
        inner.state.lock().unwrap().stream = Some(stream);
        Ok(Transport(Arc::new(inner)))
    }

    /// Sends one control-plane heartbeat (`CorndogsService/$ping`) and returns
    /// an error if the server is unreachable. Cheap; keeps an idle connection
    /// alive and detects a dead server. It uses the I/O deadline.
    pub fn ping(&self) -> Result<(), ClientError> {
        self.0.ping()
    }

    /// SYNCHRONOUS heartbeat: blocks the calling thread, pinging every
    /// `interval`, until a ping fails — then returns that error. Run it on a
    /// thread you own (e.g. `thread::spawn(move || tr.run_heartbeat(iv))`), or
    /// use [`Transport::start_heartbeat`] for the background form.
    pub fn run_heartbeat(&self, interval: Duration) -> ClientError {
        loop {
            thread::sleep(interval);
            if let Err(e) = self.ping() {
                return e;
            }
        }
    }

    /// ASYNCHRONOUS heartbeat: starts the heartbeat on a background
    /// `std::thread`, pinging every `interval`, and returns a handle whose
    /// `stop()` ends it. Only one background heartbeat runs at a time per
    /// underlying connection (calling this again returns a handle to the
    /// existing one).
    pub fn start_heartbeat(&self, interval: Duration) -> HeartbeatHandle {
        let mut hb = self.0.hb_stop.lock().unwrap();
        if let Some(existing) = hb.clone() {
            return existing;
        }
        let handle = HeartbeatHandle {
            flag: Arc::new(StopFlag::new()),
        };
        *hb = Some(handle.clone());
        drop(hb);

        let inner = self.0.clone();
        let flag = handle.flag.clone();
        thread::spawn(move || {
            loop {
                if flag.wait(interval) {
                    break; // stopped
                }
                if inner.ping().is_err() {
                    break; // dead server; stop retrying, matching StreamTransport.RunHeartbeat
                }
            }
            *inner.hb_stop.lock().unwrap() = None;
        });
        handle
    }

    /// Shuts the transport down: stops any background heartbeat and closes
    /// the connection. A later call re-dials.
    pub fn close(&self) {
        if let Some(hb) = self.0.hb_stop.lock().unwrap().take() {
            hb.stop();
        }
        self.0.state.lock().unwrap().stream = None;
    }
}

impl crate::client::Transport for Transport {
    fn call(&self, service: &str, op: &str, req: &[u8]) -> Result<Vec<u8>, ClientError> {
        self.0.call(service, op, req)
    }
}

impl std::fmt::Debug for Transport {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let mut d = f.debug_struct("Transport");
        d.field("addr", &self.0.addr)
            .field("connect_timeout", &self.0.connect_timeout)
            .field("io_timeout", &self.0.io_timeout);
        #[cfg(feature = "tls")]
        d.field("tls", &self.0.tls.is_some());
        d.finish()
    }
}

// --- envelope + framing --------------------------------------------------

fn encode_request(id: u64, service: &str, op: &str, req: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(32 + service.len() + op.len() + req.len());
    write_head(5, 5, &mut out); // map, 5 pairs
    write_text(&mut out, "v");
    write_uint(&mut out, 1);
    write_text(&mut out, "service");
    write_text(&mut out, service);
    write_text(&mut out, "op");
    write_text(&mut out, op);
    write_text(&mut out, "id");
    write_uint(&mut out, id);
    write_text(&mut out, "payload");
    write_head(6, TAG_ENCODED_CBOR, &mut out); // tag(24, bytes) — embedded encoded CBOR
    write_bytes(&mut out, req);
    out
}

/// Checks that a response envelope answers request `expected`. The server
/// echoes the request id. Only a transport error for an undecodable request
/// (status != 0) can have no id.
fn check_id(val: &CsilCborValue, expected: u64) -> Result<(), String> {
    match map_get(val, "id") {
        Some(CsilCborValue::Uint(got)) if *got == expected => Ok(()),
        Some(CsilCborValue::Uint(got)) => Err(format!("response id {got} does not match request id {expected}")),
        Some(_) => Err("response id is not an unsigned integer".to_string()),
        None => match map_get(val, "status").and_then(as_i64) {
            Some(status) if status != 0 => Ok(()),
            _ => Err(format!("response has no id (request id {expected})")),
        },
    }
}

/// Interprets one decoded response envelope and returns the inner payload
/// bytes, or a `ClientError` for a non-zero transport status or a
/// `"ServiceError"` variant (decoded via the generated
/// [`crate::decode_service_error`]).
fn parse_response(val: &CsilCborValue) -> Result<Vec<u8>, ClientError> {
    if let Some(status) = map_get(val, "status").and_then(as_i64) {
        if status != 0 {
            let msg = map_get(val, "error").and_then(as_text).unwrap_or_default();
            return Err(ClientError::Transport(format!(
                "corndogs: transport status {status}: {msg}"
            )));
        }
    }

    let payload = match map_get(val, "payload") {
        None => return Ok(Vec::new()), // control replies (e.g. $pong) may carry no payload
        Some(p) => p,
    };
    let inner = match payload {
        CsilCborValue::Tag(_, boxed) => as_bytes(boxed),
        other => as_bytes(other),
    }
    .ok_or_else(|| ClientError::Transport("corndogs: response payload is not a byte string".to_string()))?;

    if let Some(variant) = map_get(val, "variant").and_then(as_text) {
        if variant == "ServiceError" {
            return match crate::decode_service_error(&inner) {
                Ok(se) => Err(ClientError::Service {
                    code: se.code as i64,
                    message: se.message,
                }),
                Err(_) => Err(ClientError::Transport(
                    "corndogs: undecodable ServiceError payload".to_string(),
                )),
            };
        }
    }
    Ok(inner)
}

fn map_get<'a>(v: &'a CsilCborValue, key: &str) -> Option<&'a CsilCborValue> {
    if let CsilCborValue::Map(entries) = v {
        for (k, val) in entries {
            if matches!(k, CsilCborValue::Text(name) if name == key) {
                return Some(val);
            }
        }
    }
    None
}

fn as_i64(v: &CsilCborValue) -> Option<i64> {
    match v {
        CsilCborValue::Uint(x) => i64::try_from(*x).ok(),
        CsilCborValue::Int(x) => Some(*x),
        _ => None,
    }
}

fn as_text(v: &CsilCborValue) -> Option<String> {
    match v {
        CsilCborValue::Text(s) => Some(s.clone()),
        _ => None,
    }
}

fn as_bytes(v: &CsilCborValue) -> Option<Vec<u8>> {
    match v {
        CsilCborValue::Bytes(b) => Some(b.clone()),
        _ => None,
    }
}

fn write_head(major: u8, n: u64, out: &mut Vec<u8>) {
    let mt = major << 5;
    if n < 24 {
        out.push(mt | n as u8);
    } else if n < 0x100 {
        out.push(mt | 24);
        out.push(n as u8);
    } else if n < 0x1_0000 {
        out.push(mt | 25);
        out.extend_from_slice(&(n as u16).to_be_bytes());
    } else if n < 0x1_0000_0000 {
        out.push(mt | 26);
        out.extend_from_slice(&(n as u32).to_be_bytes());
    } else {
        out.push(mt | 27);
        out.extend_from_slice(&n.to_be_bytes());
    }
}

fn write_text(out: &mut Vec<u8>, s: &str) {
    write_head(3, s.len() as u64, out);
    out.extend_from_slice(s.as_bytes());
}

fn write_bytes(out: &mut Vec<u8>, b: &[u8]) {
    write_head(2, b.len() as u64, out);
    out.extend_from_slice(b);
}

fn write_uint(out: &mut Vec<u8>, n: u64) {
    write_head(0, n, out);
}

/// Parses one full CBOR item into the shared [`CsilCborValue`] tree. Supports
/// every major type the generated codec's own decoder does (uint, negative
/// int, byte/text string, array, map, tag, bool/null, float) so an
/// envelope carrying fields this carrier doesn't look at still decodes
/// correctly instead of desyncing the byte offset.
fn decode_cbor(b: &[u8]) -> Result<CsilCborValue, String> {
    let mut pos = 0usize;
    let v = decode_value(b, &mut pos)?;
    if pos != b.len() {
        return Err(format!("corndogs: {} trailing bytes", b.len() - pos));
    }
    Ok(v)
}

fn read_arg(b: &[u8], pos: &mut usize, low: u8) -> Result<u64, String> {
    if low < 24 {
        *pos += 1;
        return Ok(low as u64);
    }
    let width = match low {
        24 => 1usize,
        25 => 2,
        26 => 4,
        27 => 8,
        _ => return Err(format!("corndogs: reserved additional info {low}")),
    };
    if *pos + 1 + width > b.len() {
        return Err("corndogs: truncated cbor argument".to_string());
    }
    let mut v = 0u64;
    for &byte in &b[*pos + 1..*pos + 1 + width] {
        v = (v << 8) | byte as u64;
    }
    *pos += 1 + width;
    Ok(v)
}

fn decode_value(b: &[u8], pos: &mut usize) -> Result<CsilCborValue, String> {
    if *pos >= b.len() {
        return Err("corndogs: unexpected end of cbor input".to_string());
    }
    let ib = b[*pos];
    let major = ib >> 5;
    let low = ib & 0x1f;
    if major == 7 {
        return match low {
            20 => {
                *pos += 1;
                Ok(CsilCborValue::Bool(false))
            }
            21 => {
                *pos += 1;
                Ok(CsilCborValue::Bool(true))
            }
            22 | 23 => {
                *pos += 1;
                Ok(CsilCborValue::Null)
            }
            26 => {
                let bits = read_arg(b, pos, low)?;
                Ok(CsilCborValue::Float(f32::from_bits(bits as u32) as f64))
            }
            27 => {
                let bits = read_arg(b, pos, low)?;
                Ok(CsilCborValue::Float(f64::from_bits(bits)))
            }
            _ => Err(format!("corndogs: unsupported cbor simple value {low}")),
        };
    }
    let arg = read_arg(b, pos, low)?;
    match major {
        0 => Ok(CsilCborValue::Uint(arg)),
        1 => {
            if arg > i64::MAX as u64 {
                return Err("corndogs: negative cbor integer out of range".to_string());
            }
            Ok(CsilCborValue::Int(-1 - arg as i64))
        }
        2 => {
            let n = arg as usize;
            if *pos + n > b.len() {
                return Err("corndogs: truncated cbor byte string".to_string());
            }
            let slice = b[*pos..*pos + n].to_vec();
            *pos += n;
            Ok(CsilCborValue::Bytes(slice))
        }
        3 => {
            let n = arg as usize;
            if *pos + n > b.len() {
                return Err("corndogs: truncated cbor text string".to_string());
            }
            let s = std::str::from_utf8(&b[*pos..*pos + n])
                .map_err(|e| format!("corndogs: invalid utf-8: {e}"))?
                .to_string();
            *pos += n;
            Ok(CsilCborValue::Text(s))
        }
        4 => {
            let n = arg as usize;
            let mut items = Vec::with_capacity(n);
            for _ in 0..n {
                items.push(decode_value(b, pos)?);
            }
            Ok(CsilCborValue::Array(items))
        }
        5 => {
            let n = arg as usize;
            let mut entries = Vec::with_capacity(n);
            for _ in 0..n {
                let k = decode_value(b, pos)?;
                let val = decode_value(b, pos)?;
                entries.push((k, val));
            }
            Ok(CsilCborValue::Map(entries))
        }
        6 => {
            let inner = decode_value(b, pos)?;
            Ok(CsilCborValue::Tag(arg, Box::new(inner)))
        }
        _ => Err(format!("corndogs: unexpected cbor major type {major}")),
    }
}

fn write_frame(w: &mut impl Write, payload: &[u8]) -> io::Result<()> {
    if payload.len() > MAX_FRAME {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("corndogs: frame too large ({} > {MAX_FRAME})", payload.len()),
        ));
    }
    w.write_all(&(payload.len() as u32).to_be_bytes())?;
    w.write_all(payload)?;
    w.flush() // TLS: push the buffered records to the socket
}

/// Reads one length-prefixed frame. Returns `Ok(None)` on a clean EOF at a
/// frame boundary (peer closed the connection).
fn read_frame(r: &mut impl Read) -> io::Result<Option<Vec<u8>>> {
    let mut len_buf = [0u8; 4];
    if let Err(e) = r.read_exact(&mut len_buf) {
        if e.kind() == io::ErrorKind::UnexpectedEof {
            return Ok(None);
        }
        return Err(e);
    }
    let n = u32::from_be_bytes(len_buf) as usize;
    if n > MAX_FRAME {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("corndogs: frame too large ({n})"),
        ));
    }
    let mut buf = vec![0u8; n];
    r.read_exact(&mut buf)?;
    Ok(Some(buf))
}
