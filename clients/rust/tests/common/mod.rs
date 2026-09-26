//! Test helpers: framing and a small CBOR subset for fake corndogs servers.
#![allow(dead_code)]

use std::io::{self, Read, Write};

/// Reads one length-prefixed frame. Returns `None` at a clean EOF.
pub fn read_frame(r: &mut impl Read) -> io::Result<Option<Vec<u8>>> {
    let mut len = [0u8; 4];
    if let Err(e) = r.read_exact(&mut len) {
        if e.kind() == io::ErrorKind::UnexpectedEof {
            return Ok(None);
        }
        return Err(e);
    }
    let mut buf = vec![0u8; u32::from_be_bytes(len) as usize];
    r.read_exact(&mut buf)?;
    Ok(Some(buf))
}

/// Encodes one frame (length prefix + body).
pub fn frame(body: &[u8]) -> Vec<u8> {
    let mut out = (body.len() as u32).to_be_bytes().to_vec();
    out.extend_from_slice(body);
    out
}

pub fn write_frame(w: &mut impl Write, body: &[u8]) -> io::Result<()> {
    w.write_all(&frame(body))?;
    w.flush()
}

fn head(major: u8, n: u64, out: &mut Vec<u8>) {
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

fn text(out: &mut Vec<u8>, s: &str) {
    head(3, s.len() as u64, out);
    out.extend_from_slice(s.as_bytes());
}

/// Builds a success response envelope:
/// `{"v":1,"id":id,"status":0,"payload":tag24(payload)}`. With `payload`
/// `None`, the envelope has no payload key (a control reply such as `$pong`).
pub fn reply(id: u64, payload: Option<&[u8]>) -> Vec<u8> {
    let mut out = Vec::new();
    head(5, if payload.is_some() { 4 } else { 3 }, &mut out);
    text(&mut out, "v");
    head(0, 1, &mut out);
    text(&mut out, "id");
    head(0, id, &mut out);
    text(&mut out, "status");
    head(0, 0, &mut out);
    if let Some(p) = payload {
        text(&mut out, "payload");
        head(6, 24, &mut out);
        head(2, p.len() as u64, &mut out);
        out.extend_from_slice(p);
    }
    out
}

/// Parsed request envelope fields that the tests use.
#[derive(Debug, Default)]
pub struct Request {
    pub id: u64,
    pub service: String,
    pub op: String,
}

/// Reads the head of one CBOR item: (major type, argument).
fn read_head(b: &[u8], pos: &mut usize) -> (u8, u64) {
    let ib = b[*pos];
    *pos += 1;
    let (major, low) = (ib >> 5, ib & 0x1f);
    let width = match low {
        0..=23 => return (major, low as u64),
        24 => 1,
        25 => 2,
        26 => 4,
        27 => 8,
        _ => panic!("unsupported cbor head {ib:#x}"),
    };
    let mut v = 0u64;
    for &x in &b[*pos..*pos + width] {
        v = (v << 8) | x as u64;
    }
    *pos += width;
    (major, v)
}

/// Skips one CBOR item and returns its text value (major type 3) or its
/// unsigned value (major type 0) if it has one.
fn item(b: &[u8], pos: &mut usize) -> (Option<String>, Option<u64>) {
    let (major, arg) = read_head(b, pos);
    match major {
        0 => (None, Some(arg)),
        2 => {
            *pos += arg as usize;
            (None, None)
        }
        3 => {
            let s = String::from_utf8(b[*pos..*pos + arg as usize].to_vec()).unwrap();
            *pos += arg as usize;
            (Some(s), None)
        }
        6 => item(b, pos),
        _ => panic!("unexpected cbor major type {major} in request"),
    }
}

/// Parses a request envelope (a CBOR map with text keys).
pub fn parse_request(b: &[u8]) -> Request {
    let mut pos = 0;
    let (major, n) = read_head(b, &mut pos);
    assert_eq!(major, 5, "request envelope is not a map");
    let mut req = Request::default();
    for _ in 0..n {
        let (key, _) = item(b, &mut pos);
        let (t, u) = item(b, &mut pos);
        match key.as_deref() {
            Some("id") => req.id = u.expect("id is uint"),
            Some("service") => req.service = t.expect("service is text"),
            Some("op") => req.op = t.expect("op is text"),
            _ => {}
        }
    }
    req
}
