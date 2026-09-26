//! I/O deadline tests against a fake TCP server.

mod common;

use std::io::Write;
use std::net::{TcpListener, TcpStream};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};

use corndogs::client::Transport as _;
use corndogs::transport::{ConnectOptions, Transport};
use corndogs::ClientError;

/// Starts a fake server. `handler(n, stream)` runs on its own thread for the
/// n-th accepted connection (0-based). Returns the address and the accept
/// count.
fn serve<F>(handler: F) -> (String, Arc<AtomicUsize>)
where
    F: Fn(usize, TcpStream) + Send + Sync + 'static,
{
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap().to_string();
    let accepts = Arc::new(AtomicUsize::new(0));
    let count = accepts.clone();
    let handler = Arc::new(handler);
    thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(stream) = stream else { break };
            let n = count.fetch_add(1, Ordering::SeqCst);
            let h = handler.clone();
            thread::spawn(move || h(n, stream));
        }
    });
    (addr, accepts)
}

/// Answers every request on the stream with its own id and `payload`.
fn answer_all(mut s: TcpStream, payload: &[u8]) {
    while let Ok(Some(f)) = common::read_frame(&mut s) {
        let req = common::parse_request(&f);
        if common::write_frame(&mut s, &common::reply(req.id, Some(payload))).is_err() {
            return;
        }
    }
}

fn transport_msg(r: Result<Vec<u8>, ClientError>) -> String {
    match r {
        Err(ClientError::Transport(m)) => m,
        other => panic!("expected a transport error, got {other:?}"),
    }
}

const SVC: &str = "CorndogsService";

#[test]
fn no_deadline_connect_still_works() {
    let (addr, _) = serve(|_, s| answer_all(s, b"ok"));
    let tr = Transport::connect(addr.clone()).unwrap();
    assert_eq!(tr.call(SVC, "Anything", b"x").unwrap(), b"ok");
    tr.ping().unwrap();
    let tr = Transport::connect_timeout(addr, Duration::from_secs(1)).unwrap();
    assert_eq!(tr.call(SVC, "Anything", b"x").unwrap(), b"ok");
}

#[test]
fn silent_server_times_out_and_next_call_redials() {
    let (addr, accepts) = serve(|n, mut s| {
        if n == 0 {
            // Read the request, then never answer. Keep the socket open.
            let _ = common::read_frame(&mut s);
            thread::sleep(Duration::from_secs(10));
        } else {
            answer_all(s, b"second");
        }
    });
    let tr = Transport::connect_with(addr, Duration::from_secs(2), Some(Duration::from_millis(300))).unwrap();

    let start = Instant::now();
    let msg = transport_msg(tr.call(SVC, "First", b"1"));
    let took = start.elapsed();
    assert!(msg.contains("timed out"), "message: {msg}");
    assert!(took >= Duration::from_millis(250), "returned too early: {took:?}");
    assert!(took < Duration::from_millis(2000), "returned too late: {took:?}");

    assert_eq!(tr.call(SVC, "Second", b"2").unwrap(), b"second");
    assert_eq!(accepts.load(Ordering::SeqCst), 2, "the second call must re-dial");
}

#[test]
fn ping_uses_the_deadline() {
    let (addr, _) = serve(|_, mut s| {
        let _ = common::read_frame(&mut s);
        thread::sleep(Duration::from_secs(10));
    });
    let tr = Transport::connect_with(addr, Duration::from_secs(2), Some(Duration::from_millis(200))).unwrap();
    let start = Instant::now();
    match tr.ping() {
        Err(ClientError::Transport(m)) => assert!(m.contains("timed out"), "message: {m}"),
        other => panic!("expected timeout, got {other:?}"),
    }
    assert!(start.elapsed() < Duration::from_millis(1500));
}

#[test]
fn trickling_server_cannot_extend_the_deadline() {
    let (addr, _) = serve(|_, mut s| {
        let Ok(Some(f)) = common::read_frame(&mut s) else {
            return;
        };
        let req = common::parse_request(&f);
        let bytes = common::frame(&common::reply(req.id, Some(&[7u8; 64])));
        // One byte each 50 ms: each read makes progress, the full reply
        // takes several seconds.
        for b in bytes {
            if s.write_all(&[b]).is_err() {
                return;
            }
            thread::sleep(Duration::from_millis(50));
        }
    });
    let tr = Transport::connect_with(addr, Duration::from_secs(2), Some(Duration::from_millis(400))).unwrap();
    let start = Instant::now();
    let msg = transport_msg(tr.call(SVC, "Slow", b""));
    let took = start.elapsed();
    assert!(msg.contains("timed out"), "message: {msg}");
    assert!(took < Duration::from_millis(1200), "deadline was extended: {took:?}");
}

#[test]
fn late_reply_is_not_read_by_the_next_call() {
    let (addr, accepts) = serve(|n, mut s| {
        let Ok(Some(f)) = common::read_frame(&mut s) else {
            return;
        };
        let req = common::parse_request(&f);
        if n == 0 {
            // Answer call 1 after its deadline.
            thread::sleep(Duration::from_millis(600));
            let _ = common::write_frame(&mut s, &common::reply(req.id, Some(b"late-one")));
            thread::sleep(Duration::from_secs(2));
        } else {
            let _ = common::write_frame(&mut s, &common::reply(req.id, Some(b"two")));
            answer_all(s, b"more");
        }
    });
    let tr = Transport::connect_with(addr, Duration::from_secs(2), Some(Duration::from_millis(300))).unwrap();
    let msg = transport_msg(tr.call(SVC, "One", b"1"));
    assert!(msg.contains("timed out"), "message: {msg}");
    // Wait until the late reply was sent on the old connection.
    thread::sleep(Duration::from_millis(500));
    assert_eq!(tr.call(SVC, "Two", b"2").unwrap(), b"two");
    assert_eq!(accepts.load(Ordering::SeqCst), 2);
}

#[test]
fn mismatched_reply_id_drops_the_connection() {
    let (addr, accepts) = serve(|n, mut s| {
        if n == 0 {
            let Ok(Some(f)) = common::read_frame(&mut s) else {
                return;
            };
            let req = common::parse_request(&f);
            let _ = common::write_frame(&mut s, &common::reply(req.id + 100, Some(b"wrong")));
            thread::sleep(Duration::from_secs(2));
        } else {
            answer_all(s, b"right");
        }
    });
    let tr = Transport::connect_with(addr, Duration::from_secs(2), Some(Duration::from_secs(2))).unwrap();
    let msg = transport_msg(tr.call(SVC, "One", b"1"));
    assert!(msg.contains("does not match"), "message: {msg}");
    assert_eq!(tr.call(SVC, "Two", b"2").unwrap(), b"right");
    assert_eq!(accepts.load(Ordering::SeqCst), 2);
}

#[test]
fn redial_respects_the_call_deadline() {
    // A listener that never accepts: the kernel completes the TCP connect
    // (backlog), so the dial works, and the call then times out.
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap().to_string();
    let opts = ConnectOptions::new()
        .connect_timeout(Duration::from_secs(2))
        .io_timeout(Duration::from_millis(200));
    let tr = Transport::connect_options(addr, opts).unwrap();
    let start = Instant::now();
    let msg = transport_msg(tr.call(SVC, "X", b""));
    assert!(msg.contains("timed out"), "message: {msg}");
    let msg = transport_msg(tr.call(SVC, "X", b""));
    assert!(msg.contains("timed out"), "message: {msg}");
    assert!(start.elapsed() < Duration::from_millis(1500));
    drop(listener);
}
