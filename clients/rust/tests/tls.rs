//! TLS tests against an in-process rustls server with a test CA.
#![cfg(feature = "tls")]

mod common;

use std::net::{TcpListener, TcpStream};
use std::path::PathBuf;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};

use corndogs::client::Transport as _;
use corndogs::transport::{ConnectOptions, TlsOptions, Transport};
use corndogs::ClientError;
use rcgen::{BasicConstraints, CertificateParams, CertifiedIssuer, DnType, IsCa, KeyPair};
use rustls::pki_types::{PrivateKeyDer, PrivatePkcs8KeyDer};
use rustls::{ServerConfig, ServerConnection, StreamOwned};

struct TestPki {
    ca_file: PathBuf,
    server: Arc<ServerConfig>,
}

/// Makes a CA and a server certificate for "localhost", "corndogs.test", and
/// 127.0.0.1. Writes the CA PEM to a file.
fn pki(tag: &str) -> TestPki {
    let mut ca_params = CertificateParams::new(Vec::<String>::new()).unwrap();
    ca_params.is_ca = IsCa::Ca(BasicConstraints::Unconstrained);
    ca_params
        .distinguished_name
        .push(DnType::CommonName, "corndogs test CA");
    let ca = CertifiedIssuer::self_signed(ca_params, KeyPair::generate().unwrap()).unwrap();

    let srv_key = KeyPair::generate().unwrap();
    let srv_params = CertificateParams::new(vec![
        "localhost".to_string(),
        "corndogs.test".to_string(),
        "127.0.0.1".to_string(),
    ])
    .unwrap();
    let srv_cert = srv_params.signed_by(&srv_key, &ca).unwrap();

    let dir = PathBuf::from(env!("CARGO_TARGET_TMPDIR"));
    let ca_file = dir.join(format!("corndogs-test-ca-{tag}-{}.pem", std::process::id()));
    std::fs::write(&ca_file, ca.pem()).unwrap();

    let key = PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(srv_key.serialize_der()));
    let server = ServerConfig::builder_with_provider(Arc::new(rustls::crypto::ring::default_provider()))
        .with_safe_default_protocol_versions()
        .unwrap()
        .with_no_client_auth()
        .with_single_cert(vec![srv_cert.der().clone()], key)
        .unwrap();
    TestPki {
        ca_file,
        server: Arc::new(server),
    }
}

/// Starts a TLS server that answers each request with its own id and no
/// payload (a valid `$ping` reply). If `silent_first` is true, the first
/// connection reads one request and never answers.
fn serve_tls(cfg: Arc<ServerConfig>, silent_first: bool) -> (u16, Arc<AtomicUsize>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    let accepts = Arc::new(AtomicUsize::new(0));
    let count = accepts.clone();
    thread::spawn(move || {
        for tcp in listener.incoming() {
            let Ok(tcp) = tcp else { break };
            let n = count.fetch_add(1, Ordering::SeqCst);
            let cfg = cfg.clone();
            thread::spawn(move || handle(cfg, tcp, silent_first && n == 0));
        }
    });
    (port, accepts)
}

fn handle(cfg: Arc<ServerConfig>, tcp: TcpStream, silent: bool) {
    let conn = ServerConnection::new(cfg).unwrap();
    let mut s = StreamOwned::new(conn, tcp);
    while let Ok(Some(f)) = common::read_frame(&mut s) {
        if silent {
            thread::sleep(Duration::from_secs(10));
            return;
        }
        let req = common::parse_request(&f);
        let body = if req.op == "$ping" { None } else { Some(&b"tls-ok"[..]) };
        if common::write_frame(&mut s, &common::reply(req.id, body)).is_err() {
            return;
        }
    }
}

fn err_msg<T: std::fmt::Debug>(r: Result<T, ClientError>) -> String {
    match r {
        Err(ClientError::Transport(m)) => m,
        other => panic!("expected a transport error, got {other:?}"),
    }
}

#[test]
fn ca_file_connects_and_pings() {
    let p = pki("ok");
    let (port, _) = serve_tls(p.server.clone(), false);

    // Default server name: the host part of the address.
    let tr = Transport::connect_tls(format!("localhost:{port}"), TlsOptions::ca_file(&p.ca_file)).unwrap();
    tr.ping().unwrap();
    assert_eq!(tr.call("CorndogsService", "Op", b"x").unwrap(), b"tls-ok");

    // Connect by IP, verify a DNS name.
    let tls = TlsOptions::ca_file(&p.ca_file).server_name("corndogs.test");
    let opts = ConnectOptions::new().io_timeout(Duration::from_secs(2)).tls(tls);
    let tr = Transport::connect_options(format!("127.0.0.1:{port}"), opts).unwrap();
    tr.ping().unwrap();
}

#[test]
fn system_roots_reject_the_test_ca() {
    let p = pki("sys");
    let (port, _) = serve_tls(p.server.clone(), false);
    let msg = err_msg(Transport::connect_tls(
        format!("localhost:{port}"),
        TlsOptions::system_roots(),
    ));
    eprintln!("system roots error: {msg}");
    assert!(msg.contains("tls"), "message: {msg}");
    assert!(
        msg.contains("certificate") || msg.contains("root"),
        "expected a verification error, got: {msg}"
    );
}

#[test]
fn wrong_server_name_is_rejected() {
    let p = pki("name");
    let (port, _) = serve_tls(p.server.clone(), false);
    let tls = TlsOptions::ca_file(&p.ca_file).server_name("other.example");
    let msg = err_msg(Transport::connect_tls(format!("127.0.0.1:{port}"), tls));
    assert!(msg.contains("certificate"), "message: {msg}");
}

#[test]
fn tls_timeout_drops_and_redial_redoes_handshake() {
    let p = pki("timeout");
    let (port, accepts) = serve_tls(p.server.clone(), true);
    let opts = ConnectOptions::new()
        .connect_timeout(Duration::from_secs(2))
        .io_timeout(Duration::from_millis(300))
        .tls(TlsOptions::ca_file(&p.ca_file));
    let tr = Transport::connect_options(format!("localhost:{port}"), opts).unwrap();

    let start = Instant::now();
    let msg = err_msg(tr.ping());
    assert!(msg.contains("timed out"), "message: {msg}");
    assert!(start.elapsed() < Duration::from_millis(1500));

    tr.ping().unwrap(); // new connection, new handshake
    assert_eq!(accepts.load(Ordering::SeqCst), 2);
}

#[test]
fn handshake_respects_the_connect_timeout() {
    // A plain TCP peer that never sends a TLS ServerHello.
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    thread::spawn(move || {
        let _held: Vec<_> = listener.incoming().take(1).collect();
        thread::sleep(Duration::from_secs(10));
    });
    let p = pki("hs");
    let opts = ConnectOptions::new()
        .connect_timeout(Duration::from_millis(300))
        .tls(TlsOptions::ca_file(&p.ca_file));
    let start = Instant::now();
    let msg = err_msg(Transport::connect_options(format!("127.0.0.1:{port}"), opts));
    assert!(msg.contains("timed out"), "message: {msg}");
    assert!(start.elapsed() < Duration::from_millis(1500));
}

#[test]
fn missing_ca_file_is_an_error() {
    let msg = err_msg(Transport::connect_tls(
        "127.0.0.1:1",
        TlsOptions::ca_file("/nonexistent/corndogs-ca.pem"),
    ));
    assert!(msg.contains("CA file"), "message: {msg}");
}
