//! Corndogs' own, ready-to-use client transport carrier: CSIL-RPC over a
//! persistent TCP connection, framed with the canonical 4-byte big-endian
//! length prefix (the CSIL "StreamCarrier"). This is the single official
//! Corndogs Rust carrier — you do not need to write one or read csilgen docs.
//! Point [`Transport::connect`] at a corndogs server's TCP address and hand it
//! to the generated client:
//!
//! ```no_run
//! use corndogs::{CorndogsClient, SubmitTaskRequest};
//! use corndogs::transport::Transport;
//!
//! let tr = Transport::connect("localhost:5080").expect("connect");
//! let stop = tr.start_heartbeat(std::time::Duration::from_secs(15)); // keep the connection alive
//! let client = CorndogsClient::new(tr);
//! let _ = client.submit_task(SubmitTaskRequest {
//!     queue: "emails".into(), current_state: "submitted".into(),
//!     auto_target_state: "sending".into(), timeout: -1, payload: b"...".to_vec(), priority: 0,
//! });
//! stop.stop();
//! ```
//!
//! This carrier implements the generated [`crate::client::Transport`] trait's
//! `call(service, op, req) -> Result<Vec<u8>, ClientError>` seam over one TCP
//! connection: dial (lazily re-dial on failure), write a length-prefixed CBOR
//! envelope, block for the correlated reply. Calls are serialized — one
//! request in flight at a time, guarded by a `Mutex` — matching the sync
//! Python `TcpTransport`. `Transport` is cheap to `Clone` (it is an `Arc`
//! handle to shared connection state), so the same connection can be handed
//! to a `CorndogsClient` and to a background heartbeat at once. Dependency-
//! free: std only, no cbor crate.
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
use std::time::Duration;

use crate::client::ClientError;
use crate::CsilCborValue;

const TAG_ENCODED_CBOR: u64 = 24; // RFC 8949 §3.4.5.1 — embedded encoded CBOR data item
const CONTROL_SERVICE: &str = "CorndogsService";
const OP_PING: &str = "$ping"; // control-plane heartbeat op (never collides with an app op)
const MAX_FRAME: usize = 1025 << 20; // payload hard maximum plus RPC envelope allowance
const DEFAULT_CONNECT_TIMEOUT: Duration = Duration::from_secs(5);

struct ConnState {
    stream: Option<TcpStream>,
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
    state: Mutex<ConnState>,
    hb_stop: Mutex<Option<HeartbeatHandle>>,
}

impl TransportInner {
    fn dial(&self) -> Result<TcpStream, ClientError> {
        let mut addrs = self
            .addr
            .to_socket_addrs()
            .map_err(|e| ClientError::Transport(format!("corndogs: resolve {}: {e}", self.addr)))?;
        let addr = addrs
            .next()
            .ok_or_else(|| ClientError::Transport(format!("corndogs: no address for {}", self.addr)))?;
        let stream = TcpStream::connect_timeout(&addr, self.connect_timeout)
            .map_err(|e| ClientError::Transport(e.to_string()))?;
        stream.set_nodelay(true).ok();
        Ok(stream)
    }

    /// Sends one request and blocks for its correlated response. Holds the
    /// connection mutex for the whole round trip: one call in flight on this
    /// connection at a time. A write/read failure drops the connection so the
    /// next call re-dials.
    fn call(&self, service: &str, op: &str, req: &[u8]) -> Result<Vec<u8>, ClientError> {
        let mut state = self.state.lock().unwrap();
        if state.stream.is_none() {
            state.stream = Some(self.dial()?);
        }
        state.next_id += 1;
        let id = state.next_id;
        let env = encode_request(id, service, op, req);

        let outcome: io::Result<Vec<u8>> = (|| {
            let stream = state.stream.as_mut().expect("dialed above");
            write_frame(stream, &env)?;
            read_frame(stream)?.ok_or_else(|| {
                io::Error::new(io::ErrorKind::UnexpectedEof, "corndogs: connection closed")
            })
        })();

        let frame = match outcome {
            Ok(frame) => frame,
            Err(e) => {
                state.stream = None; // torn down; the next call re-dials
                return Err(ClientError::Transport(e.to_string()));
            }
        };
        drop(state);
        parse_response(&frame)
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
    /// Dials `addr` ("host:port") and returns a ready transport.
    pub fn connect(addr: impl Into<String>) -> Result<Self, ClientError> {
        Self::connect_timeout(addr, DEFAULT_CONNECT_TIMEOUT)
    }

    /// Like [`Transport::connect`], with an explicit dial timeout.
    pub fn connect_timeout(addr: impl Into<String>, connect_timeout: Duration) -> Result<Self, ClientError> {
        let addr = addr.into();
        let inner = TransportInner {
            addr,
            connect_timeout,
            state: Mutex::new(ConnState {
                stream: None,
                next_id: 0,
            }),
            hb_stop: Mutex::new(None),
        };
        let stream = inner.dial()?;
        inner.state.lock().unwrap().stream = Some(stream);
        Ok(Transport(Arc::new(inner)))
    }

    /// Sends one control-plane heartbeat (`CorndogsService/$ping`) and returns
    /// an error if the server is unreachable. Cheap; keeps an idle connection
    /// alive and detects a dead server.
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
        f.debug_struct("Transport").field("addr", &self.0.addr).finish()
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

/// Decodes one response envelope and returns the inner payload bytes, or a
/// `ClientError` for a non-zero transport status or a `"ServiceError"` variant
/// (decoded via the generated [`crate::decode_service_error`]).
fn parse_response(frame: &[u8]) -> Result<Vec<u8>, ClientError> {
    let val = decode_cbor(frame)
        .map_err(|e| ClientError::Transport(format!("corndogs: decode response envelope: {e}")))?;

    if let Some(status) = map_get(&val, "status").and_then(as_i64) {
        if status != 0 {
            let msg = map_get(&val, "error").and_then(as_text).unwrap_or_default();
            return Err(ClientError::Transport(format!(
                "corndogs: transport status {status}: {msg}"
            )));
        }
    }

    let payload = match map_get(&val, "payload") {
        None => return Ok(Vec::new()), // control replies (e.g. $pong) may carry no payload
        Some(p) => p,
    };
    let inner = match payload {
        CsilCborValue::Tag(_, boxed) => as_bytes(boxed),
        other => as_bytes(other),
    }
    .ok_or_else(|| ClientError::Transport("corndogs: response payload is not a byte string".to_string()))?;

    if let Some(variant) = map_get(&val, "variant").and_then(as_text) {
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
    Ok(())
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
