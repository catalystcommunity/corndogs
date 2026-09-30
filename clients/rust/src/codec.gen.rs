//! Generated self-contained canonical-CBOR codec from CSIL specification.
//!
//! CSIL is the CBOR Service Interface Language; this codec owns the payload
//! wire (a CBOR map keyed by the verbatim CSIL field name in canonical RFC
//! 8949 order) so the generated types need no serde derive. One
//! `encode_`/`decode_` pair is emitted per record type.
#![allow(dead_code, clippy::vec_init_then_push)]

use super::types::*;

/// A decode failure: the CBOR was malformed or did not match the expected shape.
#[derive(Debug, Clone, PartialEq)]
pub struct CsilCborError(pub String);

impl std::fmt::Display for CsilCborError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for CsilCborError {}

/// A minimal canonical-CBOR value tree: a closed set of variants the generated codec
/// builds and walks. A map is an ordered list of pairs, so the encoder controls the
/// wire order of a record's keys explicitly (laid down in canonical order).
#[derive(Debug, Clone, PartialEq)]
pub enum CsilCborValue {
    Uint(u64),
    Int(i64),
    Bool(bool),
    Float(f64),
    Null,
    Text(String),
    Bytes(Vec<u8>),
    Array(Vec<CsilCborValue>),
    Map(Vec<(CsilCborValue, CsilCborValue)>),
    Tag(u64, Box<CsilCborValue>),
}

fn cbor_int(x: i64) -> CsilCborValue {
    CsilCborValue::Int(x)
}
fn cbor_uint(x: u64) -> CsilCborValue {
    CsilCborValue::Uint(x)
}
fn cbor_float(x: f64) -> CsilCborValue {
    CsilCborValue::Float(x)
}
fn cbor_bool(x: bool) -> CsilCborValue {
    CsilCborValue::Bool(x)
}
fn cbor_text(x: &str) -> CsilCborValue {
    CsilCborValue::Text(x.to_string())
}
fn cbor_bytes(x: &[u8]) -> CsilCborValue {
    CsilCborValue::Bytes(x.to_vec())
}

/// Serialize a value tree to canonical CBOR bytes.
fn cbor_encode(v: &CsilCborValue) -> Vec<u8> {
    let mut out = Vec::new();
    cbor_enc(v, &mut out);
    out
}

fn cbor_head(major: u8, n: u64, out: &mut Vec<u8>) {
    let mt = major << 5;
    if n < 24 {
        out.push(mt | n as u8);
    } else if n < 0x100 {
        out.push(mt | 24);
        out.push(n as u8);
    } else if n < 0x10000 {
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

fn cbor_enc(v: &CsilCborValue, out: &mut Vec<u8>) {
    match v {
        CsilCborValue::Uint(x) => cbor_head(0, *x, out),
        // A non-negative `Int` rides major type 0 so it is byte-identical to a `Uint`
        // of the same magnitude; only a genuinely negative value uses major type 1.
        CsilCborValue::Int(x) => {
            if *x >= 0 {
                cbor_head(0, *x as u64, out);
            } else {
                cbor_head(1, (-(*x + 1)) as u64, out);
            }
        }
        CsilCborValue::Bool(x) => out.push(if *x { 0xf5 } else { 0xf4 }),
        CsilCborValue::Null => out.push(0xf6),
        CsilCborValue::Float(x) => {
            out.push(0xfb);
            out.extend_from_slice(&x.to_bits().to_be_bytes());
        }
        CsilCborValue::Text(s) => {
            let bytes = s.as_bytes();
            cbor_head(3, bytes.len() as u64, out);
            out.extend_from_slice(bytes);
        }
        CsilCborValue::Bytes(b) => {
            cbor_head(2, b.len() as u64, out);
            out.extend_from_slice(b);
        }
        CsilCborValue::Array(items) => {
            cbor_head(4, items.len() as u64, out);
            for item in items {
                cbor_enc(item, out);
            }
        }
        CsilCborValue::Map(entries) => {
            cbor_head(5, entries.len() as u64, out);
            for (k, val) in entries {
                cbor_enc(k, out);
                cbor_enc(val, out);
            }
        }
        CsilCborValue::Tag(num, inner) => {
            cbor_head(6, *num, out);
            cbor_enc(inner, out);
        }
    }
}

/// Parse a full CBOR item and reject trailing bytes, so a payload that is not
/// exactly one value is an error rather than a silently-truncated read.
fn cbor_decode(b: &[u8]) -> Result<CsilCborValue, CsilCborError> {
    let mut pos = 0usize;
    let v = cbor_dec(b, &mut pos, 0)?;
    if pos != b.len() {
        return Err(CsilCborError(format!(
            "csil cbor: {} trailing bytes",
            b.len() - pos
        )));
    }
    Ok(v)
}

fn cbor_read_arg(b: &[u8], pos: &mut usize, low: u8) -> Result<u64, CsilCborError> {
    if low < 24 {
        *pos += 1;
        return Ok(low as u64);
    }
    let width = match low {
        24 => 1usize,
        25 => 2,
        26 => 4,
        27 => 8,
        _ => {
            return Err(CsilCborError(format!(
                "csil cbor: reserved additional info {low}"
            )))
        }
    };
    if *pos >= b.len() || width > b.len() - *pos - 1 {
        return Err(CsilCborError("csil cbor: truncated argument".to_string()));
    }
    let mut v = 0u64;
    for &byte in &b[*pos + 1..*pos + 1 + width] {
        v = (v << 8) | byte as u64;
    }
    *pos += 1 + width;
    Ok(v)
}

/// The most elements a decoded array or map reserves before it reads them. The
/// declared length is checked against the remaining input, but one input byte can
/// become a much larger value, so reserving the full declared length lets a small
/// frame reserve a large multiple of its size at every nesting level. Past this
/// bound, the vector grows only as elements are actually read.
const CSIL_CBOR_PREALLOC_LIMIT: usize = 1024;

fn cbor_dec(b: &[u8], pos: &mut usize, depth: usize) -> Result<CsilCborValue, CsilCborError> {
    if depth > 64 {
        return Err(CsilCborError(
            "csil cbor: nesting limit exceeded".to_string(),
        ));
    }
    if *pos >= b.len() {
        return Err(CsilCborError(
            "csil cbor: unexpected end of input".to_string(),
        ));
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
                let bits = cbor_read_arg(b, pos, low)?;
                Ok(CsilCborValue::Float(f32::from_bits(bits as u32) as f64))
            }
            27 => {
                let bits = cbor_read_arg(b, pos, low)?;
                Ok(CsilCborValue::Float(f64::from_bits(bits)))
            }
            _ => Err(CsilCborError(format!(
                "csil cbor: unsupported simple value {low}"
            ))),
        };
    }
    let arg = cbor_read_arg(b, pos, low)?;
    match major {
        0 => Ok(CsilCborValue::Uint(arg)),
        1 => {
            if arg > i64::MAX as u64 {
                return Err(CsilCborError(
                    "csil cbor: negative integer out of range".to_string(),
                ));
            }
            Ok(CsilCborValue::Int(-1 - arg as i64))
        }
        2 => {
            if arg > (b.len() - *pos) as u64 {
                return Err(CsilCborError(
                    "csil cbor: truncated byte string".to_string(),
                ));
            }
            let n = arg as usize;
            let slice = b[*pos..*pos + n].to_vec();
            *pos += n;
            Ok(CsilCborValue::Bytes(slice))
        }
        3 => {
            if arg > (b.len() - *pos) as u64 {
                return Err(CsilCborError(
                    "csil cbor: truncated text string".to_string(),
                ));
            }
            let n = arg as usize;
            let s = std::str::from_utf8(&b[*pos..*pos + n])
                .map_err(|e| CsilCborError(format!("csil cbor: invalid utf-8: {e}")))?
                .to_string();
            *pos += n;
            Ok(CsilCborValue::Text(s))
        }
        4 => {
            if arg > (b.len() - *pos) as u64 {
                return Err(CsilCborError(
                    "csil cbor: array length exceeds remaining input".to_string(),
                ));
            }
            let n = arg as usize;
            let mut items = Vec::with_capacity(n.min(CSIL_CBOR_PREALLOC_LIMIT));
            for _ in 0..n {
                items.push(cbor_dec(b, pos, depth + 1)?);
            }
            Ok(CsilCborValue::Array(items))
        }
        5 => {
            if arg > (b.len() - *pos) as u64 {
                return Err(CsilCborError(
                    "csil cbor: map length exceeds remaining input".to_string(),
                ));
            }
            let n = arg as usize;
            let mut entries = Vec::with_capacity(n.min(CSIL_CBOR_PREALLOC_LIMIT));
            for _ in 0..n {
                let k = cbor_dec(b, pos, depth + 1)?;
                let val = cbor_dec(b, pos, depth + 1)?;
                entries.push((k, val));
            }
            Ok(CsilCborValue::Map(entries))
        }
        6 => {
            let inner = cbor_dec(b, pos, depth + 1)?;
            Ok(CsilCborValue::Tag(arg, Box::new(inner)))
        }
        _ => Err(CsilCborError(format!(
            "csil cbor: unexpected major type {major}"
        ))),
    }
}

/// Map a typed slice to a CBOR array via the per-element encoder.
fn cbor_enc_array<E>(xs: &[E], f: impl Fn(&E) -> CsilCborValue) -> CsilCborValue {
    CsilCborValue::Array(xs.iter().map(f).collect())
}

/// Map a typed map to a CBOR map. Rust `HashMap` iteration is unordered, so the inner
/// map's entry order is not canonicalized; the record's own keys (laid down at
/// generation time) are what the cross-language wire contract pins.
fn cbor_enc_map<K, V>(
    m: &std::collections::HashMap<K, V>,
    kf: impl Fn(&K) -> CsilCborValue,
    vf: impl Fn(&V) -> CsilCborValue,
) -> CsilCborValue {
    CsilCborValue::Map(m.iter().map(|(k, v)| (kf(k), vf(v))).collect())
}

fn cbor_dec_array<E>(
    v: &CsilCborValue,
    f: impl Fn(&CsilCborValue) -> Result<E, CsilCborError>,
) -> Result<Vec<E>, CsilCborError> {
    cbor_as_array(v)?.iter().map(f).collect()
}

fn cbor_dec_map<K: std::cmp::Eq + std::hash::Hash, V>(
    v: &CsilCborValue,
    kf: impl Fn(&CsilCborValue) -> Result<K, CsilCborError>,
    vf: impl Fn(&CsilCborValue) -> Result<V, CsilCborError>,
) -> Result<std::collections::HashMap<K, V>, CsilCborError> {
    let entries = cbor_as_map(v)?;
    let mut out = std::collections::HashMap::with_capacity(entries.len());
    for (k, val) in entries {
        out.insert(kf(k)?, vf(val)?);
    }
    Ok(out)
}

fn cbor_map_get<'a>(v: &'a CsilCborValue, key: &str) -> Option<&'a CsilCborValue> {
    if let CsilCborValue::Map(entries) = v {
        for (k, val) in entries {
            if matches!(k, CsilCborValue::Text(name) if name == key) {
                return Some(val);
            }
        }
    }
    None
}

fn cbor_expect_value(v: &CsilCborValue, expected: &CsilCborValue) -> Result<(), CsilCborError> {
    if v == expected {
        Ok(())
    } else {
        Err(CsilCborError(format!(
            "csil cbor: expected literal {expected:?}, got {v:?}"
        )))
    }
}

fn cbor_require<'a>(v: &'a CsilCborValue, key: &str) -> Result<&'a CsilCborValue, CsilCborError> {
    cbor_map_get(v, key).ok_or_else(|| CsilCborError(format!("csil cbor: missing field {key:?}")))
}

fn cbor_as_i64(v: &CsilCborValue) -> Result<i64, CsilCborError> {
    match v {
        CsilCborValue::Uint(x) => i64::try_from(*x)
            .map_err(|_| CsilCborError("csil cbor: integer overflows i64".to_string())),
        CsilCborValue::Int(x) => Ok(*x),
        _ => Err(CsilCborError("csil cbor: expected integer".to_string())),
    }
}

fn cbor_as_u64(v: &CsilCborValue) -> Result<u64, CsilCborError> {
    match v {
        CsilCborValue::Uint(x) => Ok(*x),
        CsilCborValue::Int(x) if *x >= 0 => Ok(*x as u64),
        CsilCborValue::Int(_) => Err(CsilCborError(
            "csil cbor: negative integer where unsigned expected".to_string(),
        )),
        _ => Err(CsilCborError(
            "csil cbor: expected unsigned integer".to_string(),
        )),
    }
}

fn cbor_as_f64(v: &CsilCborValue) -> Result<f64, CsilCborError> {
    match v {
        CsilCborValue::Float(x) => Ok(*x),
        CsilCborValue::Uint(x) => Ok(*x as f64),
        CsilCborValue::Int(x) => Ok(*x as f64),
        _ => Err(CsilCborError("csil cbor: expected float".to_string())),
    }
}

fn cbor_as_bool(v: &CsilCborValue) -> Result<bool, CsilCborError> {
    match v {
        CsilCborValue::Bool(b) => Ok(*b),
        _ => Err(CsilCborError("csil cbor: expected bool".to_string())),
    }
}

fn cbor_as_text(v: &CsilCborValue) -> Result<String, CsilCborError> {
    match v {
        CsilCborValue::Text(s) => Ok(s.clone()),
        _ => Err(CsilCborError("csil cbor: expected text".to_string())),
    }
}

fn cbor_as_bytes(v: &CsilCborValue) -> Result<Vec<u8>, CsilCborError> {
    match v {
        CsilCborValue::Bytes(b) => Ok(b.clone()),
        _ => Err(CsilCborError("csil cbor: expected byte string".to_string())),
    }
}

fn cbor_as_array(v: &CsilCborValue) -> Result<&[CsilCborValue], CsilCborError> {
    match v {
        CsilCborValue::Array(a) => Ok(a),
        _ => Err(CsilCborError("csil cbor: expected array".to_string())),
    }
}

fn cbor_as_map(v: &CsilCborValue) -> Result<&[(CsilCborValue, CsilCborValue)], CsilCborError> {
    match v {
        CsilCborValue::Map(m) => Ok(m),
        _ => Err(CsilCborError("csil cbor: expected map".to_string())),
    }
}

/// Build the canonical CBOR value tree for a Task.
fn csil_enc_task(csil_v: &Task) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(8);
    csil_entries.push((cbor_text("uuid"), cbor_text(&csil_v.uuid)));
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((cbor_text("timeout"), cbor_int(csil_v.timeout)));
    csil_entries.push((cbor_text("priority"), cbor_int(csil_v.priority)));
    csil_entries.push((cbor_text("submit_time"), cbor_int(csil_v.submit_time)));
    csil_entries.push((cbor_text("update_time"), cbor_int(csil_v.update_time)));
    csil_entries.push((cbor_text("current_state"), cbor_text(&csil_v.current_state)));
    csil_entries.push((
        cbor_text("auto_target_state"),
        cbor_text(&csil_v.auto_target_state),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a Task from a decoded CBOR value tree.
fn csil_dec_task(csil_root: &CsilCborValue) -> Result<Task, CsilCborError> {
    let uuid = {
        let csil_field = cbor_require(csil_root, "uuid")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let current_state = {
        let csil_field = cbor_require(csil_root, "current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let auto_target_state = {
        let csil_field = cbor_require(csil_root, "auto_target_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let submit_time = {
        let csil_field = cbor_require(csil_root, "submit_time")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let update_time = {
        let csil_field = cbor_require(csil_root, "update_time")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let timeout = {
        let csil_field = cbor_require(csil_root, "timeout")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let priority = {
        let csil_field = cbor_require(csil_root, "priority")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    Ok(Task {
        uuid,
        queue,
        current_state,
        auto_target_state,
        submit_time,
        update_time,
        timeout,
        priority,
    })
}

/// Encode a Task to canonical CSIL CBOR bytes.
pub fn encode_task(csil_v: &Task) -> Vec<u8> {
    cbor_encode(&csil_enc_task(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a Task.
pub fn decode_task(csil_data: &[u8]) -> Result<Task, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_task(&csil_root)
}

/// Build the canonical CBOR value tree for a TaskDelivery.
fn csil_enc_task_delivery(csil_v: &TaskDelivery) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("task"), csil_enc_task(&csil_v.task)));
    csil_entries.push((cbor_text("payload"), cbor_bytes(&csil_v.payload)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a TaskDelivery from a decoded CBOR value tree.
fn csil_dec_task_delivery(csil_root: &CsilCborValue) -> Result<TaskDelivery, CsilCborError> {
    let task = {
        let csil_field = cbor_require(csil_root, "task")?;
        let csil_decode = csil_dec_task;
        csil_decode(csil_field)?
    };
    let payload = {
        let csil_field = cbor_require(csil_root, "payload")?;
        let csil_decode = cbor_as_bytes;
        csil_decode(csil_field)?
    };
    Ok(TaskDelivery { task, payload })
}

/// Encode a TaskDelivery to canonical CSIL CBOR bytes.
pub fn encode_task_delivery(csil_v: &TaskDelivery) -> Vec<u8> {
    cbor_encode(&csil_enc_task_delivery(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a TaskDelivery.
pub fn decode_task_delivery(csil_data: &[u8]) -> Result<TaskDelivery, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_task_delivery(&csil_root)
}

/// Build the canonical CBOR value tree for a SubmitTaskRequest.
fn csil_enc_submit_task_request(csil_v: &SubmitTaskRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(6);
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((cbor_text("payload"), cbor_bytes(&csil_v.payload)));
    csil_entries.push((cbor_text("timeout"), cbor_int(csil_v.timeout)));
    csil_entries.push((cbor_text("priority"), cbor_int(csil_v.priority)));
    csil_entries.push((cbor_text("current_state"), cbor_text(&csil_v.current_state)));
    csil_entries.push((
        cbor_text("auto_target_state"),
        cbor_text(&csil_v.auto_target_state),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a SubmitTaskRequest from a decoded CBOR value tree.
fn csil_dec_submit_task_request(
    csil_root: &CsilCborValue,
) -> Result<SubmitTaskRequest, CsilCborError> {
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let current_state = {
        let csil_field = cbor_require(csil_root, "current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let auto_target_state = {
        let csil_field = cbor_require(csil_root, "auto_target_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let timeout = {
        let csil_field = cbor_require(csil_root, "timeout")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let payload = {
        let csil_field = cbor_require(csil_root, "payload")?;
        let csil_decode = cbor_as_bytes;
        csil_decode(csil_field)?
    };
    let priority = {
        let csil_field = cbor_require(csil_root, "priority")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    Ok(SubmitTaskRequest {
        queue,
        current_state,
        auto_target_state,
        timeout,
        payload,
        priority,
    })
}

/// Encode a SubmitTaskRequest to canonical CSIL CBOR bytes.
pub fn encode_submit_task_request(csil_v: &SubmitTaskRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_submit_task_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a SubmitTaskRequest.
pub fn decode_submit_task_request(csil_data: &[u8]) -> Result<SubmitTaskRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_submit_task_request(&csil_root)
}

/// Build the canonical CBOR value tree for a SubmitTaskResponse.
fn csil_enc_submit_task_response(csil_v: &SubmitTaskResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    if let Some(csil_inner) = &csil_v.task {
        csil_entries.push((cbor_text("task"), csil_enc_task(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a SubmitTaskResponse from a decoded CBOR value tree.
fn csil_dec_submit_task_response(
    csil_root: &CsilCborValue,
) -> Result<SubmitTaskResponse, CsilCborError> {
    let task = match cbor_map_get(csil_root, "task") {
        Some(csil_field) => {
            let csil_decode = csil_dec_task;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(SubmitTaskResponse { task })
}

/// Encode a SubmitTaskResponse to canonical CSIL CBOR bytes.
pub fn encode_submit_task_response(csil_v: &SubmitTaskResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_submit_task_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a SubmitTaskResponse.
pub fn decode_submit_task_response(csil_data: &[u8]) -> Result<SubmitTaskResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_submit_task_response(&csil_root)
}

/// Build the canonical CBOR value tree for a GetTaskStateByIDRequest.
fn csil_enc_get_task_state_by_id_request(csil_v: &GetTaskStateByIDRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("uuid"), cbor_text(&csil_v.uuid)));
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetTaskStateByIDRequest from a decoded CBOR value tree.
fn csil_dec_get_task_state_by_id_request(
    csil_root: &CsilCborValue,
) -> Result<GetTaskStateByIDRequest, CsilCborError> {
    let uuid = {
        let csil_field = cbor_require(csil_root, "uuid")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(GetTaskStateByIDRequest { uuid, queue })
}

/// Encode a GetTaskStateByIDRequest to canonical CSIL CBOR bytes.
pub fn encode_get_task_state_by_id_request(csil_v: &GetTaskStateByIDRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_get_task_state_by_id_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetTaskStateByIDRequest.
pub fn decode_get_task_state_by_id_request(
    csil_data: &[u8],
) -> Result<GetTaskStateByIDRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_task_state_by_id_request(&csil_root)
}

/// Build the canonical CBOR value tree for a GetTaskStateByIDResponse.
fn csil_enc_get_task_state_by_id_response(csil_v: &GetTaskStateByIDResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    if let Some(csil_inner) = &csil_v.task {
        csil_entries.push((cbor_text("task"), csil_enc_task(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetTaskStateByIDResponse from a decoded CBOR value tree.
fn csil_dec_get_task_state_by_id_response(
    csil_root: &CsilCborValue,
) -> Result<GetTaskStateByIDResponse, CsilCborError> {
    let task = match cbor_map_get(csil_root, "task") {
        Some(csil_field) => {
            let csil_decode = csil_dec_task;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(GetTaskStateByIDResponse { task })
}

/// Encode a GetTaskStateByIDResponse to canonical CSIL CBOR bytes.
pub fn encode_get_task_state_by_id_response(csil_v: &GetTaskStateByIDResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_get_task_state_by_id_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetTaskStateByIDResponse.
pub fn decode_get_task_state_by_id_response(
    csil_data: &[u8],
) -> Result<GetTaskStateByIDResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_task_state_by_id_response(&csil_root)
}

/// Build the canonical CBOR value tree for a GetNextTaskRequest.
fn csil_enc_get_next_task_request(csil_v: &GetNextTaskRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(5);
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((cbor_text("current_state"), cbor_text(&csil_v.current_state)));
    csil_entries.push((
        cbor_text("override_timeout"),
        cbor_int(csil_v.override_timeout),
    ));
    csil_entries.push((
        cbor_text("override_current_state"),
        cbor_text(&csil_v.override_current_state),
    ));
    csil_entries.push((
        cbor_text("override_auto_target_state"),
        cbor_text(&csil_v.override_auto_target_state),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetNextTaskRequest from a decoded CBOR value tree.
fn csil_dec_get_next_task_request(
    csil_root: &CsilCborValue,
) -> Result<GetNextTaskRequest, CsilCborError> {
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let current_state = {
        let csil_field = cbor_require(csil_root, "current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let override_timeout = {
        let csil_field = cbor_require(csil_root, "override_timeout")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let override_current_state = {
        let csil_field = cbor_require(csil_root, "override_current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let override_auto_target_state = {
        let csil_field = cbor_require(csil_root, "override_auto_target_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(GetNextTaskRequest {
        queue,
        current_state,
        override_timeout,
        override_current_state,
        override_auto_target_state,
    })
}

/// Encode a GetNextTaskRequest to canonical CSIL CBOR bytes.
pub fn encode_get_next_task_request(csil_v: &GetNextTaskRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_get_next_task_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetNextTaskRequest.
pub fn decode_get_next_task_request(csil_data: &[u8]) -> Result<GetNextTaskRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_next_task_request(&csil_root)
}

/// Build the canonical CBOR value tree for a GetNextTaskResponse.
fn csil_enc_get_next_task_response(csil_v: &GetNextTaskResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    if let Some(csil_inner) = &csil_v.delivery {
        csil_entries.push((cbor_text("delivery"), csil_enc_task_delivery(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetNextTaskResponse from a decoded CBOR value tree.
fn csil_dec_get_next_task_response(
    csil_root: &CsilCborValue,
) -> Result<GetNextTaskResponse, CsilCborError> {
    let delivery = match cbor_map_get(csil_root, "delivery") {
        Some(csil_field) => {
            let csil_decode = csil_dec_task_delivery;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(GetNextTaskResponse { delivery })
}

/// Encode a GetNextTaskResponse to canonical CSIL CBOR bytes.
pub fn encode_get_next_task_response(csil_v: &GetNextTaskResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_get_next_task_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetNextTaskResponse.
pub fn decode_get_next_task_response(
    csil_data: &[u8],
) -> Result<GetNextTaskResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_next_task_response(&csil_root)
}

/// Build the canonical CBOR value tree for a GetNextTaskGroupRequest.
fn csil_enc_get_next_task_group_request(csil_v: &GetNextTaskGroupRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(5);
    csil_entries.push((
        cbor_text("queues"),
        cbor_enc_array(&csil_v.queues, |csil_elem| cbor_text(csil_elem)),
    ));
    csil_entries.push((cbor_text("current_state"), cbor_text(&csil_v.current_state)));
    csil_entries.push((
        cbor_text("override_timeout"),
        cbor_int(csil_v.override_timeout),
    ));
    csil_entries.push((
        cbor_text("override_current_state"),
        cbor_text(&csil_v.override_current_state),
    ));
    csil_entries.push((
        cbor_text("override_auto_target_state"),
        cbor_text(&csil_v.override_auto_target_state),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetNextTaskGroupRequest from a decoded CBOR value tree.
fn csil_dec_get_next_task_group_request(
    csil_root: &CsilCborValue,
) -> Result<GetNextTaskGroupRequest, CsilCborError> {
    let queues = {
        let csil_field = cbor_require(csil_root, "queues")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, cbor_as_text);
        csil_decode(csil_field)?
    };
    let current_state = {
        let csil_field = cbor_require(csil_root, "current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let override_timeout = {
        let csil_field = cbor_require(csil_root, "override_timeout")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let override_current_state = {
        let csil_field = cbor_require(csil_root, "override_current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let override_auto_target_state = {
        let csil_field = cbor_require(csil_root, "override_auto_target_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(GetNextTaskGroupRequest {
        queues,
        current_state,
        override_timeout,
        override_current_state,
        override_auto_target_state,
    })
}

/// Encode a GetNextTaskGroupRequest to canonical CSIL CBOR bytes.
pub fn encode_get_next_task_group_request(csil_v: &GetNextTaskGroupRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_get_next_task_group_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetNextTaskGroupRequest.
pub fn decode_get_next_task_group_request(
    csil_data: &[u8],
) -> Result<GetNextTaskGroupRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_next_task_group_request(&csil_root)
}

/// Build the canonical CBOR value tree for a GetNextTaskGroupResponse.
fn csil_enc_get_next_task_group_response(csil_v: &GetNextTaskGroupResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    if let Some(csil_inner) = &csil_v.delivery {
        csil_entries.push((cbor_text("delivery"), csil_enc_task_delivery(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetNextTaskGroupResponse from a decoded CBOR value tree.
fn csil_dec_get_next_task_group_response(
    csil_root: &CsilCborValue,
) -> Result<GetNextTaskGroupResponse, CsilCborError> {
    let delivery = match cbor_map_get(csil_root, "delivery") {
        Some(csil_field) => {
            let csil_decode = csil_dec_task_delivery;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(GetNextTaskGroupResponse { delivery })
}

/// Encode a GetNextTaskGroupResponse to canonical CSIL CBOR bytes.
pub fn encode_get_next_task_group_response(csil_v: &GetNextTaskGroupResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_get_next_task_group_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetNextTaskGroupResponse.
pub fn decode_get_next_task_group_response(
    csil_data: &[u8],
) -> Result<GetNextTaskGroupResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_next_task_group_response(&csil_root)
}

/// Build the canonical CBOR value tree for a CompleteTaskRequest.
fn csil_enc_complete_task_request(csil_v: &CompleteTaskRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("uuid"), cbor_text(&csil_v.uuid)));
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((cbor_text("current_state"), cbor_text(&csil_v.current_state)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CompleteTaskRequest from a decoded CBOR value tree.
fn csil_dec_complete_task_request(
    csil_root: &CsilCborValue,
) -> Result<CompleteTaskRequest, CsilCborError> {
    let uuid = {
        let csil_field = cbor_require(csil_root, "uuid")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let current_state = {
        let csil_field = cbor_require(csil_root, "current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(CompleteTaskRequest {
        uuid,
        queue,
        current_state,
    })
}

/// Encode a CompleteTaskRequest to canonical CSIL CBOR bytes.
pub fn encode_complete_task_request(csil_v: &CompleteTaskRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_complete_task_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CompleteTaskRequest.
pub fn decode_complete_task_request(
    csil_data: &[u8],
) -> Result<CompleteTaskRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_complete_task_request(&csil_root)
}

/// Build the canonical CBOR value tree for a CompleteTaskResponse.
fn csil_enc_complete_task_response(csil_v: &CompleteTaskResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    if let Some(csil_inner) = &csil_v.task {
        csil_entries.push((cbor_text("task"), csil_enc_task(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CompleteTaskResponse from a decoded CBOR value tree.
fn csil_dec_complete_task_response(
    csil_root: &CsilCborValue,
) -> Result<CompleteTaskResponse, CsilCborError> {
    let task = match cbor_map_get(csil_root, "task") {
        Some(csil_field) => {
            let csil_decode = csil_dec_task;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(CompleteTaskResponse { task })
}

/// Encode a CompleteTaskResponse to canonical CSIL CBOR bytes.
pub fn encode_complete_task_response(csil_v: &CompleteTaskResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_complete_task_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CompleteTaskResponse.
pub fn decode_complete_task_response(
    csil_data: &[u8],
) -> Result<CompleteTaskResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_complete_task_response(&csil_root)
}

/// Build the canonical CBOR value tree for a UpdateTaskRequest.
fn csil_enc_update_task_request(csil_v: &UpdateTaskRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(8);
    csil_entries.push((cbor_text("uuid"), cbor_text(&csil_v.uuid)));
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    if let Some(csil_inner) = &csil_v.payload {
        csil_entries.push((cbor_text("payload"), cbor_bytes(csil_inner)));
    }
    csil_entries.push((cbor_text("timeout"), cbor_int(csil_v.timeout)));
    if let Some(csil_inner) = &csil_v.priority {
        csil_entries.push((cbor_text("priority"), cbor_int(*csil_inner)));
    }
    csil_entries.push((cbor_text("new_state"), cbor_text(&csil_v.new_state)));
    csil_entries.push((cbor_text("current_state"), cbor_text(&csil_v.current_state)));
    csil_entries.push((
        cbor_text("auto_target_state"),
        cbor_text(&csil_v.auto_target_state),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a UpdateTaskRequest from a decoded CBOR value tree.
fn csil_dec_update_task_request(
    csil_root: &CsilCborValue,
) -> Result<UpdateTaskRequest, CsilCborError> {
    let uuid = {
        let csil_field = cbor_require(csil_root, "uuid")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let current_state = {
        let csil_field = cbor_require(csil_root, "current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let auto_target_state = {
        let csil_field = cbor_require(csil_root, "auto_target_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let timeout = {
        let csil_field = cbor_require(csil_root, "timeout")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let new_state = {
        let csil_field = cbor_require(csil_root, "new_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let payload = match cbor_map_get(csil_root, "payload") {
        Some(csil_field) => {
            let csil_decode = cbor_as_bytes;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let priority = match cbor_map_get(csil_root, "priority") {
        Some(csil_field) => {
            let csil_decode = cbor_as_i64;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(UpdateTaskRequest {
        uuid,
        queue,
        current_state,
        auto_target_state,
        timeout,
        new_state,
        payload,
        priority,
    })
}

/// Encode a UpdateTaskRequest to canonical CSIL CBOR bytes.
pub fn encode_update_task_request(csil_v: &UpdateTaskRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_update_task_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a UpdateTaskRequest.
pub fn decode_update_task_request(csil_data: &[u8]) -> Result<UpdateTaskRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_update_task_request(&csil_root)
}

/// Build the canonical CBOR value tree for a UpdateTaskResponse.
fn csil_enc_update_task_response(csil_v: &UpdateTaskResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    if let Some(csil_inner) = &csil_v.task {
        csil_entries.push((cbor_text("task"), csil_enc_task(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a UpdateTaskResponse from a decoded CBOR value tree.
fn csil_dec_update_task_response(
    csil_root: &CsilCborValue,
) -> Result<UpdateTaskResponse, CsilCborError> {
    let task = match cbor_map_get(csil_root, "task") {
        Some(csil_field) => {
            let csil_decode = csil_dec_task;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(UpdateTaskResponse { task })
}

/// Encode a UpdateTaskResponse to canonical CSIL CBOR bytes.
pub fn encode_update_task_response(csil_v: &UpdateTaskResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_update_task_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a UpdateTaskResponse.
pub fn decode_update_task_response(csil_data: &[u8]) -> Result<UpdateTaskResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_update_task_response(&csil_root)
}

/// Build the canonical CBOR value tree for a CancelTaskRequest.
fn csil_enc_cancel_task_request(csil_v: &CancelTaskRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("uuid"), cbor_text(&csil_v.uuid)));
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((cbor_text("current_state"), cbor_text(&csil_v.current_state)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CancelTaskRequest from a decoded CBOR value tree.
fn csil_dec_cancel_task_request(
    csil_root: &CsilCborValue,
) -> Result<CancelTaskRequest, CsilCborError> {
    let uuid = {
        let csil_field = cbor_require(csil_root, "uuid")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let current_state = {
        let csil_field = cbor_require(csil_root, "current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(CancelTaskRequest {
        uuid,
        queue,
        current_state,
    })
}

/// Encode a CancelTaskRequest to canonical CSIL CBOR bytes.
pub fn encode_cancel_task_request(csil_v: &CancelTaskRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_cancel_task_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CancelTaskRequest.
pub fn decode_cancel_task_request(csil_data: &[u8]) -> Result<CancelTaskRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_cancel_task_request(&csil_root)
}

/// Build the canonical CBOR value tree for a CancelTaskResponse.
fn csil_enc_cancel_task_response(csil_v: &CancelTaskResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    if let Some(csil_inner) = &csil_v.task {
        csil_entries.push((cbor_text("task"), csil_enc_task(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CancelTaskResponse from a decoded CBOR value tree.
fn csil_dec_cancel_task_response(
    csil_root: &CsilCborValue,
) -> Result<CancelTaskResponse, CsilCborError> {
    let task = match cbor_map_get(csil_root, "task") {
        Some(csil_field) => {
            let csil_decode = csil_dec_task;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(CancelTaskResponse { task })
}

/// Encode a CancelTaskResponse to canonical CSIL CBOR bytes.
pub fn encode_cancel_task_response(csil_v: &CancelTaskResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_cancel_task_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CancelTaskResponse.
pub fn decode_cancel_task_response(csil_data: &[u8]) -> Result<CancelTaskResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_cancel_task_response(&csil_root)
}

/// Build the canonical CBOR value tree for a CleanUpTimedOutRequest.
fn csil_enc_clean_up_timed_out_request(csil_v: &CleanUpTimedOutRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((cbor_text("at_time"), cbor_int(csil_v.at_time)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CleanUpTimedOutRequest from a decoded CBOR value tree.
fn csil_dec_clean_up_timed_out_request(
    csil_root: &CsilCborValue,
) -> Result<CleanUpTimedOutRequest, CsilCborError> {
    let at_time = {
        let csil_field = cbor_require(csil_root, "at_time")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(CleanUpTimedOutRequest { at_time, queue })
}

/// Encode a CleanUpTimedOutRequest to canonical CSIL CBOR bytes.
pub fn encode_clean_up_timed_out_request(csil_v: &CleanUpTimedOutRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_clean_up_timed_out_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CleanUpTimedOutRequest.
pub fn decode_clean_up_timed_out_request(
    csil_data: &[u8],
) -> Result<CleanUpTimedOutRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_clean_up_timed_out_request(&csil_root)
}

/// Build the canonical CBOR value tree for a CleanUpTimedOutResponse.
fn csil_enc_clean_up_timed_out_response(csil_v: &CleanUpTimedOutResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    csil_entries.push((cbor_text("timed_out"), cbor_int(csil_v.timed_out)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CleanUpTimedOutResponse from a decoded CBOR value tree.
fn csil_dec_clean_up_timed_out_response(
    csil_root: &CsilCborValue,
) -> Result<CleanUpTimedOutResponse, CsilCborError> {
    let timed_out = {
        let csil_field = cbor_require(csil_root, "timed_out")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    Ok(CleanUpTimedOutResponse { timed_out })
}

/// Encode a CleanUpTimedOutResponse to canonical CSIL CBOR bytes.
pub fn encode_clean_up_timed_out_response(csil_v: &CleanUpTimedOutResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_clean_up_timed_out_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CleanUpTimedOutResponse.
pub fn decode_clean_up_timed_out_response(
    csil_data: &[u8],
) -> Result<CleanUpTimedOutResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_clean_up_timed_out_response(&csil_root)
}

/// Build the canonical CBOR value tree for a GetQueuesRequest.
fn csil_enc_get_queues_request(_csil_v: &GetQueuesRequest) -> CsilCborValue {
    CsilCborValue::Map(Vec::new())
}

/// Reconstruct a GetQueuesRequest from a decoded CBOR value tree.
fn csil_dec_get_queues_request(
    _csil_root: &CsilCborValue,
) -> Result<GetQueuesRequest, CsilCborError> {
    Ok(GetQueuesRequest {})
}

/// Encode a GetQueuesRequest to canonical CSIL CBOR bytes.
pub fn encode_get_queues_request(csil_v: &GetQueuesRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_get_queues_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetQueuesRequest.
pub fn decode_get_queues_request(csil_data: &[u8]) -> Result<GetQueuesRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_queues_request(&csil_root)
}

/// Build the canonical CBOR value tree for a GetQueuesResponse.
fn csil_enc_get_queues_response(csil_v: &GetQueuesResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((
        cbor_text("queues"),
        cbor_enc_array(&csil_v.queues, |csil_elem| cbor_text(csil_elem)),
    ));
    csil_entries.push((
        cbor_text("total_task_count"),
        cbor_int(csil_v.total_task_count),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetQueuesResponse from a decoded CBOR value tree.
fn csil_dec_get_queues_response(
    csil_root: &CsilCborValue,
) -> Result<GetQueuesResponse, CsilCborError> {
    let queues = {
        let csil_field = cbor_require(csil_root, "queues")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, cbor_as_text);
        csil_decode(csil_field)?
    };
    let total_task_count = {
        let csil_field = cbor_require(csil_root, "total_task_count")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    Ok(GetQueuesResponse {
        queues,
        total_task_count,
    })
}

/// Encode a GetQueuesResponse to canonical CSIL CBOR bytes.
pub fn encode_get_queues_response(csil_v: &GetQueuesResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_get_queues_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetQueuesResponse.
pub fn decode_get_queues_response(csil_data: &[u8]) -> Result<GetQueuesResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_queues_response(&csil_root)
}

/// Build the canonical CBOR value tree for a GetQueueTaskCountsRequest.
fn csil_enc_get_queue_task_counts_request(_csil_v: &GetQueueTaskCountsRequest) -> CsilCborValue {
    CsilCborValue::Map(Vec::new())
}

/// Reconstruct a GetQueueTaskCountsRequest from a decoded CBOR value tree.
fn csil_dec_get_queue_task_counts_request(
    _csil_root: &CsilCborValue,
) -> Result<GetQueueTaskCountsRequest, CsilCborError> {
    Ok(GetQueueTaskCountsRequest {})
}

/// Encode a GetQueueTaskCountsRequest to canonical CSIL CBOR bytes.
pub fn encode_get_queue_task_counts_request(csil_v: &GetQueueTaskCountsRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_get_queue_task_counts_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetQueueTaskCountsRequest.
pub fn decode_get_queue_task_counts_request(
    csil_data: &[u8],
) -> Result<GetQueueTaskCountsRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_queue_task_counts_request(&csil_root)
}

/// Build the canonical CBOR value tree for a GetQueueTaskCountsResponse.
fn csil_enc_get_queue_task_counts_response(csil_v: &GetQueueTaskCountsResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((
        cbor_text("queue_counts"),
        cbor_enc_map(
            &csil_v.queue_counts,
            |csil_mk| cbor_text(csil_mk),
            |csil_mv| cbor_int(*csil_mv),
        ),
    ));
    csil_entries.push((
        cbor_text("total_task_count"),
        cbor_int(csil_v.total_task_count),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetQueueTaskCountsResponse from a decoded CBOR value tree.
fn csil_dec_get_queue_task_counts_response(
    csil_root: &CsilCborValue,
) -> Result<GetQueueTaskCountsResponse, CsilCborError> {
    let queue_counts = {
        let csil_field = cbor_require(csil_root, "queue_counts")?;
        let csil_decode = |csil_v| cbor_dec_map(csil_v, cbor_as_text, cbor_as_i64);
        csil_decode(csil_field)?
    };
    let total_task_count = {
        let csil_field = cbor_require(csil_root, "total_task_count")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    Ok(GetQueueTaskCountsResponse {
        queue_counts,
        total_task_count,
    })
}

/// Encode a GetQueueTaskCountsResponse to canonical CSIL CBOR bytes.
pub fn encode_get_queue_task_counts_response(csil_v: &GetQueueTaskCountsResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_get_queue_task_counts_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetQueueTaskCountsResponse.
pub fn decode_get_queue_task_counts_response(
    csil_data: &[u8],
) -> Result<GetQueueTaskCountsResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_queue_task_counts_response(&csil_root)
}

/// Build the canonical CBOR value tree for a GetTaskStateCountsRequest.
fn csil_enc_get_task_state_counts_request(csil_v: &GetTaskStateCountsRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetTaskStateCountsRequest from a decoded CBOR value tree.
fn csil_dec_get_task_state_counts_request(
    csil_root: &CsilCborValue,
) -> Result<GetTaskStateCountsRequest, CsilCborError> {
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(GetTaskStateCountsRequest { queue })
}

/// Encode a GetTaskStateCountsRequest to canonical CSIL CBOR bytes.
pub fn encode_get_task_state_counts_request(csil_v: &GetTaskStateCountsRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_get_task_state_counts_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetTaskStateCountsRequest.
pub fn decode_get_task_state_counts_request(
    csil_data: &[u8],
) -> Result<GetTaskStateCountsRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_task_state_counts_request(&csil_root)
}

/// Build the canonical CBOR value tree for a GetTaskStateCountsResponse.
fn csil_enc_get_task_state_counts_response(csil_v: &GetTaskStateCountsResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("count"), cbor_int(csil_v.count)));
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((
        cbor_text("state_counts"),
        cbor_enc_map(
            &csil_v.state_counts,
            |csil_mk| cbor_text(csil_mk),
            |csil_mv| cbor_int(*csil_mv),
        ),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetTaskStateCountsResponse from a decoded CBOR value tree.
fn csil_dec_get_task_state_counts_response(
    csil_root: &CsilCborValue,
) -> Result<GetTaskStateCountsResponse, CsilCborError> {
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let count = {
        let csil_field = cbor_require(csil_root, "count")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let state_counts = {
        let csil_field = cbor_require(csil_root, "state_counts")?;
        let csil_decode = |csil_v| cbor_dec_map(csil_v, cbor_as_text, cbor_as_i64);
        csil_decode(csil_field)?
    };
    Ok(GetTaskStateCountsResponse {
        queue,
        count,
        state_counts,
    })
}

/// Encode a GetTaskStateCountsResponse to canonical CSIL CBOR bytes.
pub fn encode_get_task_state_counts_response(csil_v: &GetTaskStateCountsResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_get_task_state_counts_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetTaskStateCountsResponse.
pub fn decode_get_task_state_counts_response(
    csil_data: &[u8],
) -> Result<GetTaskStateCountsResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_task_state_counts_response(&csil_root)
}

/// Build the canonical CBOR value tree for a QueueAndStateCounts.
fn csil_enc_queue_and_state_counts(csil_v: &QueueAndStateCounts) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    csil_entries.push((cbor_text("count"), cbor_int(csil_v.count)));
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((
        cbor_text("state_counts"),
        cbor_enc_map(
            &csil_v.state_counts,
            |csil_mk| cbor_text(csil_mk),
            |csil_mv| cbor_int(*csil_mv),
        ),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a QueueAndStateCounts from a decoded CBOR value tree.
fn csil_dec_queue_and_state_counts(
    csil_root: &CsilCborValue,
) -> Result<QueueAndStateCounts, CsilCborError> {
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let count = {
        let csil_field = cbor_require(csil_root, "count")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let state_counts = {
        let csil_field = cbor_require(csil_root, "state_counts")?;
        let csil_decode = |csil_v| cbor_dec_map(csil_v, cbor_as_text, cbor_as_i64);
        csil_decode(csil_field)?
    };
    Ok(QueueAndStateCounts {
        queue,
        count,
        state_counts,
    })
}

/// Encode a QueueAndStateCounts to canonical CSIL CBOR bytes.
pub fn encode_queue_and_state_counts(csil_v: &QueueAndStateCounts) -> Vec<u8> {
    cbor_encode(&csil_enc_queue_and_state_counts(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a QueueAndStateCounts.
pub fn decode_queue_and_state_counts(
    csil_data: &[u8],
) -> Result<QueueAndStateCounts, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_queue_and_state_counts(&csil_root)
}

/// Build the canonical CBOR value tree for a GetQueueAndStateCountsRequest.
fn csil_enc_get_queue_and_state_counts_request(
    _csil_v: &GetQueueAndStateCountsRequest,
) -> CsilCborValue {
    CsilCborValue::Map(Vec::new())
}

/// Reconstruct a GetQueueAndStateCountsRequest from a decoded CBOR value tree.
fn csil_dec_get_queue_and_state_counts_request(
    _csil_root: &CsilCborValue,
) -> Result<GetQueueAndStateCountsRequest, CsilCborError> {
    Ok(GetQueueAndStateCountsRequest {})
}

/// Encode a GetQueueAndStateCountsRequest to canonical CSIL CBOR bytes.
pub fn encode_get_queue_and_state_counts_request(
    csil_v: &GetQueueAndStateCountsRequest,
) -> Vec<u8> {
    cbor_encode(&csil_enc_get_queue_and_state_counts_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetQueueAndStateCountsRequest.
pub fn decode_get_queue_and_state_counts_request(
    csil_data: &[u8],
) -> Result<GetQueueAndStateCountsRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_queue_and_state_counts_request(&csil_root)
}

/// Build the canonical CBOR value tree for a GetQueueAndStateCountsResponse.
fn csil_enc_get_queue_and_state_counts_response(
    csil_v: &GetQueueAndStateCountsResponse,
) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    csil_entries.push((
        cbor_text("queue_and_state_counts"),
        cbor_enc_map(
            &csil_v.queue_and_state_counts,
            |csil_mk| cbor_text(csil_mk),
            csil_enc_queue_and_state_counts,
        ),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetQueueAndStateCountsResponse from a decoded CBOR value tree.
fn csil_dec_get_queue_and_state_counts_response(
    csil_root: &CsilCborValue,
) -> Result<GetQueueAndStateCountsResponse, CsilCborError> {
    let queue_and_state_counts = {
        let csil_field = cbor_require(csil_root, "queue_and_state_counts")?;
        let csil_decode =
            |csil_v| cbor_dec_map(csil_v, cbor_as_text, csil_dec_queue_and_state_counts);
        csil_decode(csil_field)?
    };
    Ok(GetQueueAndStateCountsResponse {
        queue_and_state_counts,
    })
}

/// Encode a GetQueueAndStateCountsResponse to canonical CSIL CBOR bytes.
pub fn encode_get_queue_and_state_counts_response(
    csil_v: &GetQueueAndStateCountsResponse,
) -> Vec<u8> {
    cbor_encode(&csil_enc_get_queue_and_state_counts_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetQueueAndStateCountsResponse.
pub fn decode_get_queue_and_state_counts_response(
    csil_data: &[u8],
) -> Result<GetQueueAndStateCountsResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_queue_and_state_counts_response(&csil_root)
}

/// Build the canonical CBOR value tree for a GetServerInfoRequest.
fn csil_enc_get_server_info_request(_csil_v: &GetServerInfoRequest) -> CsilCborValue {
    CsilCborValue::Map(Vec::new())
}

/// Reconstruct a GetServerInfoRequest from a decoded CBOR value tree.
fn csil_dec_get_server_info_request(
    _csil_root: &CsilCborValue,
) -> Result<GetServerInfoRequest, CsilCborError> {
    Ok(GetServerInfoRequest {})
}

/// Encode a GetServerInfoRequest to canonical CSIL CBOR bytes.
pub fn encode_get_server_info_request(csil_v: &GetServerInfoRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_get_server_info_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetServerInfoRequest.
pub fn decode_get_server_info_request(
    csil_data: &[u8],
) -> Result<GetServerInfoRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_server_info_request(&csil_root)
}

/// Build the canonical CBOR value tree for a GetServerInfoResponse.
fn csil_enc_get_server_info_response(csil_v: &GetServerInfoResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(5);
    csil_entries.push((
        cbor_text("features"),
        cbor_enc_array(&csil_v.features, |csil_elem| cbor_text(csil_elem)),
    ));
    csil_entries.push((
        cbor_text("server_version"),
        cbor_text(&csil_v.server_version),
    ));
    csil_entries.push((
        cbor_text("task_guard_policy"),
        cbor_text(&csil_v.task_guard_policy),
    ));
    csil_entries.push((
        cbor_text("submission_key_policy"),
        cbor_text(&csil_v.submission_key_policy),
    ));
    csil_entries.push((
        cbor_text("receipt_retention_seconds"),
        cbor_int(csil_v.receipt_retention_seconds),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetServerInfoResponse from a decoded CBOR value tree.
fn csil_dec_get_server_info_response(
    csil_root: &CsilCborValue,
) -> Result<GetServerInfoResponse, CsilCborError> {
    let server_version = {
        let csil_field = cbor_require(csil_root, "server_version")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let features = {
        let csil_field = cbor_require(csil_root, "features")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, cbor_as_text);
        csil_decode(csil_field)?
    };
    let submission_key_policy = {
        let csil_field = cbor_require(csil_root, "submission_key_policy")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let task_guard_policy = {
        let csil_field = cbor_require(csil_root, "task_guard_policy")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let receipt_retention_seconds = {
        let csil_field = cbor_require(csil_root, "receipt_retention_seconds")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    Ok(GetServerInfoResponse {
        server_version,
        features,
        submission_key_policy,
        task_guard_policy,
        receipt_retention_seconds,
    })
}

/// Encode a GetServerInfoResponse to canonical CSIL CBOR bytes.
pub fn encode_get_server_info_response(csil_v: &GetServerInfoResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_get_server_info_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetServerInfoResponse.
pub fn decode_get_server_info_response(
    csil_data: &[u8],
) -> Result<GetServerInfoResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_server_info_response(&csil_root)
}

/// Build the canonical CBOR value tree for a GuardedTask.
fn csil_enc_guarded_task(csil_v: &GuardedTask) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(4);
    csil_entries.push((cbor_text("task"), csil_enc_task(&csil_v.task)));
    csil_entries.push((cbor_text("guarded"), cbor_bool(csil_v.guarded)));
    csil_entries.push((cbor_text("revision"), cbor_int(csil_v.revision)));
    csil_entries.push((cbor_text("terminal"), cbor_bool(csil_v.terminal)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GuardedTask from a decoded CBOR value tree.
fn csil_dec_guarded_task(csil_root: &CsilCborValue) -> Result<GuardedTask, CsilCborError> {
    let task = {
        let csil_field = cbor_require(csil_root, "task")?;
        let csil_decode = csil_dec_task;
        csil_decode(csil_field)?
    };
    let guarded = {
        let csil_field = cbor_require(csil_root, "guarded")?;
        let csil_decode = cbor_as_bool;
        csil_decode(csil_field)?
    };
    let revision = {
        let csil_field = cbor_require(csil_root, "revision")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let terminal = {
        let csil_field = cbor_require(csil_root, "terminal")?;
        let csil_decode = cbor_as_bool;
        csil_decode(csil_field)?
    };
    Ok(GuardedTask {
        task,
        guarded,
        revision,
        terminal,
    })
}

/// Encode a GuardedTask to canonical CSIL CBOR bytes.
pub fn encode_guarded_task(csil_v: &GuardedTask) -> Vec<u8> {
    cbor_encode(&csil_enc_guarded_task(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GuardedTask.
pub fn decode_guarded_task(csil_data: &[u8]) -> Result<GuardedTask, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_guarded_task(&csil_root)
}

/// Build the canonical CBOR value tree for a SubmissionReceipt.
fn csil_enc_submission_receipt(csil_v: &SubmissionReceipt) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(6);
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((cbor_text("guarded"), cbor_bool(csil_v.guarded)));
    csil_entries.push((cbor_text("task_uuid"), cbor_text(&csil_v.task_uuid)));
    csil_entries.push((cbor_text("expires_at"), cbor_int(csil_v.expires_at)));
    csil_entries.push((cbor_text("accepted_at"), cbor_int(csil_v.accepted_at)));
    csil_entries.push((
        cbor_text("submission_key"),
        cbor_text(&csil_v.submission_key),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a SubmissionReceipt from a decoded CBOR value tree.
fn csil_dec_submission_receipt(
    csil_root: &CsilCborValue,
) -> Result<SubmissionReceipt, CsilCborError> {
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let submission_key = {
        let csil_field = cbor_require(csil_root, "submission_key")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let task_uuid = {
        let csil_field = cbor_require(csil_root, "task_uuid")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let accepted_at = {
        let csil_field = cbor_require(csil_root, "accepted_at")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let expires_at = {
        let csil_field = cbor_require(csil_root, "expires_at")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let guarded = {
        let csil_field = cbor_require(csil_root, "guarded")?;
        let csil_decode = cbor_as_bool;
        csil_decode(csil_field)?
    };
    Ok(SubmissionReceipt {
        queue,
        submission_key,
        task_uuid,
        accepted_at,
        expires_at,
        guarded,
    })
}

/// Encode a SubmissionReceipt to canonical CSIL CBOR bytes.
pub fn encode_submission_receipt(csil_v: &SubmissionReceipt) -> Vec<u8> {
    cbor_encode(&csil_enc_submission_receipt(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a SubmissionReceipt.
pub fn decode_submission_receipt(csil_data: &[u8]) -> Result<SubmissionReceipt, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_submission_receipt(&csil_root)
}

/// Build the canonical CBOR value tree for a SubmitKeyedTaskRequest.
fn csil_enc_submit_keyed_task_request(csil_v: &SubmitKeyedTaskRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(8);
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((cbor_text("guarded"), cbor_bool(csil_v.guarded)));
    csil_entries.push((cbor_text("payload"), cbor_bytes(&csil_v.payload)));
    csil_entries.push((cbor_text("timeout"), cbor_int(csil_v.timeout)));
    csil_entries.push((cbor_text("priority"), cbor_int(csil_v.priority)));
    csil_entries.push((cbor_text("current_state"), cbor_text(&csil_v.current_state)));
    csil_entries.push((
        cbor_text("submission_key"),
        cbor_text(&csil_v.submission_key),
    ));
    csil_entries.push((
        cbor_text("auto_target_state"),
        cbor_text(&csil_v.auto_target_state),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a SubmitKeyedTaskRequest from a decoded CBOR value tree.
fn csil_dec_submit_keyed_task_request(
    csil_root: &CsilCborValue,
) -> Result<SubmitKeyedTaskRequest, CsilCborError> {
    let submission_key = {
        let csil_field = cbor_require(csil_root, "submission_key")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let guarded = {
        let csil_field = cbor_require(csil_root, "guarded")?;
        let csil_decode = cbor_as_bool;
        csil_decode(csil_field)?
    };
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let current_state = {
        let csil_field = cbor_require(csil_root, "current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let auto_target_state = {
        let csil_field = cbor_require(csil_root, "auto_target_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let timeout = {
        let csil_field = cbor_require(csil_root, "timeout")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let payload = {
        let csil_field = cbor_require(csil_root, "payload")?;
        let csil_decode = cbor_as_bytes;
        csil_decode(csil_field)?
    };
    let priority = {
        let csil_field = cbor_require(csil_root, "priority")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    Ok(SubmitKeyedTaskRequest {
        submission_key,
        guarded,
        queue,
        current_state,
        auto_target_state,
        timeout,
        payload,
        priority,
    })
}

/// Encode a SubmitKeyedTaskRequest to canonical CSIL CBOR bytes.
pub fn encode_submit_keyed_task_request(csil_v: &SubmitKeyedTaskRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_submit_keyed_task_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a SubmitKeyedTaskRequest.
pub fn decode_submit_keyed_task_request(
    csil_data: &[u8],
) -> Result<SubmitKeyedTaskRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_submit_keyed_task_request(&csil_root)
}

/// Build the canonical CBOR value tree for a SubmitKeyedTaskResponse.
fn csil_enc_submit_keyed_task_response(csil_v: &SubmitKeyedTaskResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(3);
    if let Some(csil_inner) = &csil_v.task {
        csil_entries.push((cbor_text("task"), csil_enc_guarded_task(csil_inner)));
    }
    csil_entries.push((
        cbor_text("receipt"),
        csil_enc_submission_receipt(&csil_v.receipt),
    ));
    csil_entries.push((cbor_text("replayed"), cbor_bool(csil_v.replayed)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a SubmitKeyedTaskResponse from a decoded CBOR value tree.
fn csil_dec_submit_keyed_task_response(
    csil_root: &CsilCborValue,
) -> Result<SubmitKeyedTaskResponse, CsilCborError> {
    let receipt = {
        let csil_field = cbor_require(csil_root, "receipt")?;
        let csil_decode = csil_dec_submission_receipt;
        csil_decode(csil_field)?
    };
    let replayed = {
        let csil_field = cbor_require(csil_root, "replayed")?;
        let csil_decode = cbor_as_bool;
        csil_decode(csil_field)?
    };
    let task = match cbor_map_get(csil_root, "task") {
        Some(csil_field) => {
            let csil_decode = csil_dec_guarded_task;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(SubmitKeyedTaskResponse {
        receipt,
        replayed,
        task,
    })
}

/// Encode a SubmitKeyedTaskResponse to canonical CSIL CBOR bytes.
pub fn encode_submit_keyed_task_response(csil_v: &SubmitKeyedTaskResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_submit_keyed_task_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a SubmitKeyedTaskResponse.
pub fn decode_submit_keyed_task_response(
    csil_data: &[u8],
) -> Result<SubmitKeyedTaskResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_submit_keyed_task_response(&csil_root)
}

/// Build the canonical CBOR value tree for a LookupSubmissionRequest.
fn csil_enc_lookup_submission_request(csil_v: &LookupSubmissionRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((
        cbor_text("submission_key"),
        cbor_text(&csil_v.submission_key),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a LookupSubmissionRequest from a decoded CBOR value tree.
fn csil_dec_lookup_submission_request(
    csil_root: &CsilCborValue,
) -> Result<LookupSubmissionRequest, CsilCborError> {
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let submission_key = {
        let csil_field = cbor_require(csil_root, "submission_key")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(LookupSubmissionRequest {
        queue,
        submission_key,
    })
}

/// Encode a LookupSubmissionRequest to canonical CSIL CBOR bytes.
pub fn encode_lookup_submission_request(csil_v: &LookupSubmissionRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_lookup_submission_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a LookupSubmissionRequest.
pub fn decode_lookup_submission_request(
    csil_data: &[u8],
) -> Result<LookupSubmissionRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_lookup_submission_request(&csil_root)
}

/// Build the canonical CBOR value tree for a LookupSubmissionResponse.
fn csil_enc_lookup_submission_response(csil_v: &LookupSubmissionResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    if let Some(csil_inner) = &csil_v.task {
        csil_entries.push((cbor_text("task"), csil_enc_guarded_task(csil_inner)));
    }
    if let Some(csil_inner) = &csil_v.receipt {
        csil_entries.push((
            cbor_text("receipt"),
            csil_enc_submission_receipt(csil_inner),
        ));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a LookupSubmissionResponse from a decoded CBOR value tree.
fn csil_dec_lookup_submission_response(
    csil_root: &CsilCborValue,
) -> Result<LookupSubmissionResponse, CsilCborError> {
    let receipt = match cbor_map_get(csil_root, "receipt") {
        Some(csil_field) => {
            let csil_decode = csil_dec_submission_receipt;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let task = match cbor_map_get(csil_root, "task") {
        Some(csil_field) => {
            let csil_decode = csil_dec_guarded_task;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(LookupSubmissionResponse { receipt, task })
}

/// Encode a LookupSubmissionResponse to canonical CSIL CBOR bytes.
pub fn encode_lookup_submission_response(csil_v: &LookupSubmissionResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_lookup_submission_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a LookupSubmissionResponse.
pub fn decode_lookup_submission_response(
    csil_data: &[u8],
) -> Result<LookupSubmissionResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_lookup_submission_response(&csil_root)
}

/// Build the canonical CBOR value tree for a ClaimGuardedTaskRequest.
fn csil_enc_claim_guarded_task_request(csil_v: &ClaimGuardedTaskRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(6);
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((cbor_text("operation_id"), cbor_text(&csil_v.operation_id)));
    csil_entries.push((cbor_text("current_state"), cbor_text(&csil_v.current_state)));
    csil_entries.push((
        cbor_text("override_timeout"),
        cbor_int(csil_v.override_timeout),
    ));
    csil_entries.push((
        cbor_text("override_current_state"),
        cbor_text(&csil_v.override_current_state),
    ));
    csil_entries.push((
        cbor_text("override_auto_target_state"),
        cbor_text(&csil_v.override_auto_target_state),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ClaimGuardedTaskRequest from a decoded CBOR value tree.
fn csil_dec_claim_guarded_task_request(
    csil_root: &CsilCborValue,
) -> Result<ClaimGuardedTaskRequest, CsilCborError> {
    let operation_id = {
        let csil_field = cbor_require(csil_root, "operation_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let current_state = {
        let csil_field = cbor_require(csil_root, "current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let override_timeout = {
        let csil_field = cbor_require(csil_root, "override_timeout")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let override_current_state = {
        let csil_field = cbor_require(csil_root, "override_current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let override_auto_target_state = {
        let csil_field = cbor_require(csil_root, "override_auto_target_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(ClaimGuardedTaskRequest {
        operation_id,
        queue,
        current_state,
        override_timeout,
        override_current_state,
        override_auto_target_state,
    })
}

/// Encode a ClaimGuardedTaskRequest to canonical CSIL CBOR bytes.
pub fn encode_claim_guarded_task_request(csil_v: &ClaimGuardedTaskRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_claim_guarded_task_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ClaimGuardedTaskRequest.
pub fn decode_claim_guarded_task_request(
    csil_data: &[u8],
) -> Result<ClaimGuardedTaskRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_claim_guarded_task_request(&csil_root)
}

/// Build the canonical CBOR value tree for a GuardedDelivery.
fn csil_enc_guarded_delivery(csil_v: &GuardedDelivery) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("task"), csil_enc_guarded_task(&csil_v.task)));
    csil_entries.push((cbor_text("payload"), cbor_bytes(&csil_v.payload)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GuardedDelivery from a decoded CBOR value tree.
fn csil_dec_guarded_delivery(csil_root: &CsilCborValue) -> Result<GuardedDelivery, CsilCborError> {
    let task = {
        let csil_field = cbor_require(csil_root, "task")?;
        let csil_decode = csil_dec_guarded_task;
        csil_decode(csil_field)?
    };
    let payload = {
        let csil_field = cbor_require(csil_root, "payload")?;
        let csil_decode = cbor_as_bytes;
        csil_decode(csil_field)?
    };
    Ok(GuardedDelivery { task, payload })
}

/// Encode a GuardedDelivery to canonical CSIL CBOR bytes.
pub fn encode_guarded_delivery(csil_v: &GuardedDelivery) -> Vec<u8> {
    cbor_encode(&csil_enc_guarded_delivery(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GuardedDelivery.
pub fn decode_guarded_delivery(csil_data: &[u8]) -> Result<GuardedDelivery, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_guarded_delivery(&csil_root)
}

/// Build the canonical CBOR value tree for a ClaimGuardedTaskResponse.
fn csil_enc_claim_guarded_task_response(csil_v: &ClaimGuardedTaskResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    if let Some(csil_inner) = &csil_v.delivery {
        csil_entries.push((cbor_text("delivery"), csil_enc_guarded_delivery(csil_inner)));
    }
    csil_entries.push((cbor_text("replayed"), cbor_bool(csil_v.replayed)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ClaimGuardedTaskResponse from a decoded CBOR value tree.
fn csil_dec_claim_guarded_task_response(
    csil_root: &CsilCborValue,
) -> Result<ClaimGuardedTaskResponse, CsilCborError> {
    let delivery = match cbor_map_get(csil_root, "delivery") {
        Some(csil_field) => {
            let csil_decode = csil_dec_guarded_delivery;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let replayed = {
        let csil_field = cbor_require(csil_root, "replayed")?;
        let csil_decode = cbor_as_bool;
        csil_decode(csil_field)?
    };
    Ok(ClaimGuardedTaskResponse { delivery, replayed })
}

/// Encode a ClaimGuardedTaskResponse to canonical CSIL CBOR bytes.
pub fn encode_claim_guarded_task_response(csil_v: &ClaimGuardedTaskResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_claim_guarded_task_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ClaimGuardedTaskResponse.
pub fn decode_claim_guarded_task_response(
    csil_data: &[u8],
) -> Result<ClaimGuardedTaskResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_claim_guarded_task_response(&csil_root)
}

/// Build the canonical CBOR value tree for a ClaimGuardedTaskGroupRequest.
fn csil_enc_claim_guarded_task_group_request(
    csil_v: &ClaimGuardedTaskGroupRequest,
) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(6);
    csil_entries.push((
        cbor_text("queues"),
        cbor_enc_array(&csil_v.queues, |csil_elem| cbor_text(csil_elem)),
    ));
    csil_entries.push((cbor_text("operation_id"), cbor_text(&csil_v.operation_id)));
    csil_entries.push((cbor_text("current_state"), cbor_text(&csil_v.current_state)));
    csil_entries.push((
        cbor_text("override_timeout"),
        cbor_int(csil_v.override_timeout),
    ));
    csil_entries.push((
        cbor_text("override_current_state"),
        cbor_text(&csil_v.override_current_state),
    ));
    csil_entries.push((
        cbor_text("override_auto_target_state"),
        cbor_text(&csil_v.override_auto_target_state),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ClaimGuardedTaskGroupRequest from a decoded CBOR value tree.
fn csil_dec_claim_guarded_task_group_request(
    csil_root: &CsilCborValue,
) -> Result<ClaimGuardedTaskGroupRequest, CsilCborError> {
    let operation_id = {
        let csil_field = cbor_require(csil_root, "operation_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let queues = {
        let csil_field = cbor_require(csil_root, "queues")?;
        let csil_decode = |csil_v| cbor_dec_array(csil_v, cbor_as_text);
        csil_decode(csil_field)?
    };
    let current_state = {
        let csil_field = cbor_require(csil_root, "current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let override_timeout = {
        let csil_field = cbor_require(csil_root, "override_timeout")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let override_current_state = {
        let csil_field = cbor_require(csil_root, "override_current_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let override_auto_target_state = {
        let csil_field = cbor_require(csil_root, "override_auto_target_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(ClaimGuardedTaskGroupRequest {
        operation_id,
        queues,
        current_state,
        override_timeout,
        override_current_state,
        override_auto_target_state,
    })
}

/// Encode a ClaimGuardedTaskGroupRequest to canonical CSIL CBOR bytes.
pub fn encode_claim_guarded_task_group_request(csil_v: &ClaimGuardedTaskGroupRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_claim_guarded_task_group_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ClaimGuardedTaskGroupRequest.
pub fn decode_claim_guarded_task_group_request(
    csil_data: &[u8],
) -> Result<ClaimGuardedTaskGroupRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_claim_guarded_task_group_request(&csil_root)
}

/// Build the canonical CBOR value tree for a ClaimGuardedTaskGroupResponse.
fn csil_enc_claim_guarded_task_group_response(
    csil_v: &ClaimGuardedTaskGroupResponse,
) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    if let Some(csil_inner) = &csil_v.delivery {
        csil_entries.push((cbor_text("delivery"), csil_enc_guarded_delivery(csil_inner)));
    }
    csil_entries.push((cbor_text("replayed"), cbor_bool(csil_v.replayed)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ClaimGuardedTaskGroupResponse from a decoded CBOR value tree.
fn csil_dec_claim_guarded_task_group_response(
    csil_root: &CsilCborValue,
) -> Result<ClaimGuardedTaskGroupResponse, CsilCborError> {
    let delivery = match cbor_map_get(csil_root, "delivery") {
        Some(csil_field) => {
            let csil_decode = csil_dec_guarded_delivery;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let replayed = {
        let csil_field = cbor_require(csil_root, "replayed")?;
        let csil_decode = cbor_as_bool;
        csil_decode(csil_field)?
    };
    Ok(ClaimGuardedTaskGroupResponse { delivery, replayed })
}

/// Encode a ClaimGuardedTaskGroupResponse to canonical CSIL CBOR bytes.
pub fn encode_claim_guarded_task_group_response(csil_v: &ClaimGuardedTaskGroupResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_claim_guarded_task_group_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ClaimGuardedTaskGroupResponse.
pub fn decode_claim_guarded_task_group_response(
    csil_data: &[u8],
) -> Result<ClaimGuardedTaskGroupResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_claim_guarded_task_group_response(&csil_root)
}

/// Build the canonical CBOR value tree for a UpdateGuardedTaskRequest.
fn csil_enc_update_guarded_task_request(csil_v: &UpdateGuardedTaskRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(10);
    csil_entries.push((cbor_text("uuid"), cbor_text(&csil_v.uuid)));
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    if let Some(csil_inner) = &csil_v.payload {
        csil_entries.push((cbor_text("payload"), cbor_bytes(csil_inner)));
    }
    csil_entries.push((cbor_text("timeout"), cbor_int(csil_v.timeout)));
    if let Some(csil_inner) = &csil_v.priority {
        csil_entries.push((cbor_text("priority"), cbor_int(*csil_inner)));
    }
    csil_entries.push((cbor_text("new_state"), cbor_text(&csil_v.new_state)));
    csil_entries.push((cbor_text("operation_id"), cbor_text(&csil_v.operation_id)));
    if let Some(csil_inner) = &csil_v.expected_state {
        csil_entries.push((cbor_text("expected_state"), cbor_text(csil_inner)));
    }
    csil_entries.push((
        cbor_text("auto_target_state"),
        cbor_text(&csil_v.auto_target_state),
    ));
    csil_entries.push((
        cbor_text("expected_revision"),
        cbor_int(csil_v.expected_revision),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a UpdateGuardedTaskRequest from a decoded CBOR value tree.
fn csil_dec_update_guarded_task_request(
    csil_root: &CsilCborValue,
) -> Result<UpdateGuardedTaskRequest, CsilCborError> {
    let operation_id = {
        let csil_field = cbor_require(csil_root, "operation_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let uuid = {
        let csil_field = cbor_require(csil_root, "uuid")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let expected_revision = {
        let csil_field = cbor_require(csil_root, "expected_revision")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let expected_state = match cbor_map_get(csil_root, "expected_state") {
        Some(csil_field) => {
            let csil_decode = cbor_as_text;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let new_state = {
        let csil_field = cbor_require(csil_root, "new_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let auto_target_state = {
        let csil_field = cbor_require(csil_root, "auto_target_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let timeout = {
        let csil_field = cbor_require(csil_root, "timeout")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let payload = match cbor_map_get(csil_root, "payload") {
        Some(csil_field) => {
            let csil_decode = cbor_as_bytes;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    let priority = match cbor_map_get(csil_root, "priority") {
        Some(csil_field) => {
            let csil_decode = cbor_as_i64;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(UpdateGuardedTaskRequest {
        operation_id,
        uuid,
        queue,
        expected_revision,
        expected_state,
        new_state,
        auto_target_state,
        timeout,
        payload,
        priority,
    })
}

/// Encode a UpdateGuardedTaskRequest to canonical CSIL CBOR bytes.
pub fn encode_update_guarded_task_request(csil_v: &UpdateGuardedTaskRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_update_guarded_task_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a UpdateGuardedTaskRequest.
pub fn decode_update_guarded_task_request(
    csil_data: &[u8],
) -> Result<UpdateGuardedTaskRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_update_guarded_task_request(&csil_root)
}

/// Build the canonical CBOR value tree for a UpdateGuardedTaskResponse.
fn csil_enc_update_guarded_task_response(csil_v: &UpdateGuardedTaskResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("task"), csil_enc_guarded_task(&csil_v.task)));
    csil_entries.push((cbor_text("replayed"), cbor_bool(csil_v.replayed)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a UpdateGuardedTaskResponse from a decoded CBOR value tree.
fn csil_dec_update_guarded_task_response(
    csil_root: &CsilCborValue,
) -> Result<UpdateGuardedTaskResponse, CsilCborError> {
    let task = {
        let csil_field = cbor_require(csil_root, "task")?;
        let csil_decode = csil_dec_guarded_task;
        csil_decode(csil_field)?
    };
    let replayed = {
        let csil_field = cbor_require(csil_root, "replayed")?;
        let csil_decode = cbor_as_bool;
        csil_decode(csil_field)?
    };
    Ok(UpdateGuardedTaskResponse { task, replayed })
}

/// Encode a UpdateGuardedTaskResponse to canonical CSIL CBOR bytes.
pub fn encode_update_guarded_task_response(csil_v: &UpdateGuardedTaskResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_update_guarded_task_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a UpdateGuardedTaskResponse.
pub fn decode_update_guarded_task_response(
    csil_data: &[u8],
) -> Result<UpdateGuardedTaskResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_update_guarded_task_response(&csil_root)
}

/// Build the canonical CBOR value tree for a CompleteGuardedTaskRequest.
fn csil_enc_complete_guarded_task_request(csil_v: &CompleteGuardedTaskRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(5);
    csil_entries.push((cbor_text("uuid"), cbor_text(&csil_v.uuid)));
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((cbor_text("operation_id"), cbor_text(&csil_v.operation_id)));
    if let Some(csil_inner) = &csil_v.expected_state {
        csil_entries.push((cbor_text("expected_state"), cbor_text(csil_inner)));
    }
    csil_entries.push((
        cbor_text("expected_revision"),
        cbor_int(csil_v.expected_revision),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CompleteGuardedTaskRequest from a decoded CBOR value tree.
fn csil_dec_complete_guarded_task_request(
    csil_root: &CsilCborValue,
) -> Result<CompleteGuardedTaskRequest, CsilCborError> {
    let operation_id = {
        let csil_field = cbor_require(csil_root, "operation_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let uuid = {
        let csil_field = cbor_require(csil_root, "uuid")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let expected_revision = {
        let csil_field = cbor_require(csil_root, "expected_revision")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let expected_state = match cbor_map_get(csil_root, "expected_state") {
        Some(csil_field) => {
            let csil_decode = cbor_as_text;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(CompleteGuardedTaskRequest {
        operation_id,
        uuid,
        queue,
        expected_revision,
        expected_state,
    })
}

/// Encode a CompleteGuardedTaskRequest to canonical CSIL CBOR bytes.
pub fn encode_complete_guarded_task_request(csil_v: &CompleteGuardedTaskRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_complete_guarded_task_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CompleteGuardedTaskRequest.
pub fn decode_complete_guarded_task_request(
    csil_data: &[u8],
) -> Result<CompleteGuardedTaskRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_complete_guarded_task_request(&csil_root)
}

/// Build the canonical CBOR value tree for a CompleteGuardedTaskResponse.
fn csil_enc_complete_guarded_task_response(csil_v: &CompleteGuardedTaskResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("task"), csil_enc_guarded_task(&csil_v.task)));
    csil_entries.push((cbor_text("replayed"), cbor_bool(csil_v.replayed)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CompleteGuardedTaskResponse from a decoded CBOR value tree.
fn csil_dec_complete_guarded_task_response(
    csil_root: &CsilCborValue,
) -> Result<CompleteGuardedTaskResponse, CsilCborError> {
    let task = {
        let csil_field = cbor_require(csil_root, "task")?;
        let csil_decode = csil_dec_guarded_task;
        csil_decode(csil_field)?
    };
    let replayed = {
        let csil_field = cbor_require(csil_root, "replayed")?;
        let csil_decode = cbor_as_bool;
        csil_decode(csil_field)?
    };
    Ok(CompleteGuardedTaskResponse { task, replayed })
}

/// Encode a CompleteGuardedTaskResponse to canonical CSIL CBOR bytes.
pub fn encode_complete_guarded_task_response(csil_v: &CompleteGuardedTaskResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_complete_guarded_task_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CompleteGuardedTaskResponse.
pub fn decode_complete_guarded_task_response(
    csil_data: &[u8],
) -> Result<CompleteGuardedTaskResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_complete_guarded_task_response(&csil_root)
}

/// Build the canonical CBOR value tree for a CancelGuardedTaskRequest.
fn csil_enc_cancel_guarded_task_request(csil_v: &CancelGuardedTaskRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(5);
    csil_entries.push((cbor_text("uuid"), cbor_text(&csil_v.uuid)));
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((cbor_text("operation_id"), cbor_text(&csil_v.operation_id)));
    if let Some(csil_inner) = &csil_v.expected_state {
        csil_entries.push((cbor_text("expected_state"), cbor_text(csil_inner)));
    }
    csil_entries.push((
        cbor_text("expected_revision"),
        cbor_int(csil_v.expected_revision),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CancelGuardedTaskRequest from a decoded CBOR value tree.
fn csil_dec_cancel_guarded_task_request(
    csil_root: &CsilCborValue,
) -> Result<CancelGuardedTaskRequest, CsilCborError> {
    let operation_id = {
        let csil_field = cbor_require(csil_root, "operation_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let uuid = {
        let csil_field = cbor_require(csil_root, "uuid")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let expected_revision = {
        let csil_field = cbor_require(csil_root, "expected_revision")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let expected_state = match cbor_map_get(csil_root, "expected_state") {
        Some(csil_field) => {
            let csil_decode = cbor_as_text;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(CancelGuardedTaskRequest {
        operation_id,
        uuid,
        queue,
        expected_revision,
        expected_state,
    })
}

/// Encode a CancelGuardedTaskRequest to canonical CSIL CBOR bytes.
pub fn encode_cancel_guarded_task_request(csil_v: &CancelGuardedTaskRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_cancel_guarded_task_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CancelGuardedTaskRequest.
pub fn decode_cancel_guarded_task_request(
    csil_data: &[u8],
) -> Result<CancelGuardedTaskRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_cancel_guarded_task_request(&csil_root)
}

/// Build the canonical CBOR value tree for a CancelGuardedTaskResponse.
fn csil_enc_cancel_guarded_task_response(csil_v: &CancelGuardedTaskResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("task"), csil_enc_guarded_task(&csil_v.task)));
    csil_entries.push((cbor_text("replayed"), cbor_bool(csil_v.replayed)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a CancelGuardedTaskResponse from a decoded CBOR value tree.
fn csil_dec_cancel_guarded_task_response(
    csil_root: &CsilCborValue,
) -> Result<CancelGuardedTaskResponse, CsilCborError> {
    let task = {
        let csil_field = cbor_require(csil_root, "task")?;
        let csil_decode = csil_dec_guarded_task;
        csil_decode(csil_field)?
    };
    let replayed = {
        let csil_field = cbor_require(csil_root, "replayed")?;
        let csil_decode = cbor_as_bool;
        csil_decode(csil_field)?
    };
    Ok(CancelGuardedTaskResponse { task, replayed })
}

/// Encode a CancelGuardedTaskResponse to canonical CSIL CBOR bytes.
pub fn encode_cancel_guarded_task_response(csil_v: &CancelGuardedTaskResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_cancel_guarded_task_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a CancelGuardedTaskResponse.
pub fn decode_cancel_guarded_task_response(
    csil_data: &[u8],
) -> Result<CancelGuardedTaskResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_cancel_guarded_task_response(&csil_root)
}

/// Build the canonical CBOR value tree for a GetGuardedTaskRequest.
fn csil_enc_get_guarded_task_request(csil_v: &GetGuardedTaskRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("uuid"), cbor_text(&csil_v.uuid)));
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetGuardedTaskRequest from a decoded CBOR value tree.
fn csil_dec_get_guarded_task_request(
    csil_root: &CsilCborValue,
) -> Result<GetGuardedTaskRequest, CsilCborError> {
    let uuid = {
        let csil_field = cbor_require(csil_root, "uuid")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(GetGuardedTaskRequest { uuid, queue })
}

/// Encode a GetGuardedTaskRequest to canonical CSIL CBOR bytes.
pub fn encode_get_guarded_task_request(csil_v: &GetGuardedTaskRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_get_guarded_task_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetGuardedTaskRequest.
pub fn decode_get_guarded_task_request(
    csil_data: &[u8],
) -> Result<GetGuardedTaskRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_guarded_task_request(&csil_root)
}

/// Build the canonical CBOR value tree for a GetGuardedTaskResponse.
fn csil_enc_get_guarded_task_response(csil_v: &GetGuardedTaskResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    if let Some(csil_inner) = &csil_v.task {
        csil_entries.push((cbor_text("task"), csil_enc_guarded_task(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a GetGuardedTaskResponse from a decoded CBOR value tree.
fn csil_dec_get_guarded_task_response(
    csil_root: &CsilCborValue,
) -> Result<GetGuardedTaskResponse, CsilCborError> {
    let task = match cbor_map_get(csil_root, "task") {
        Some(csil_field) => {
            let csil_decode = csil_dec_guarded_task;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(GetGuardedTaskResponse { task })
}

/// Encode a GetGuardedTaskResponse to canonical CSIL CBOR bytes.
pub fn encode_get_guarded_task_response(csil_v: &GetGuardedTaskResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_get_guarded_task_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a GetGuardedTaskResponse.
pub fn decode_get_guarded_task_response(
    csil_data: &[u8],
) -> Result<GetGuardedTaskResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_get_guarded_task_response(&csil_root)
}

/// Build the canonical CBOR value tree for a OperationReceipt.
fn csil_enc_operation_receipt(csil_v: &OperationReceipt) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(8);
    csil_entries.push((cbor_text("at"), cbor_int(csil_v.at)));
    csil_entries.push((cbor_text("op"), cbor_text(&csil_v.op)));
    csil_entries.push((cbor_text("queue"), cbor_text(&csil_v.queue)));
    csil_entries.push((cbor_text("task_uuid"), cbor_text(&csil_v.task_uuid)));
    csil_entries.push((cbor_text("expires_at"), cbor_int(csil_v.expires_at)));
    csil_entries.push((cbor_text("operation_id"), cbor_text(&csil_v.operation_id)));
    csil_entries.push((cbor_text("result_state"), cbor_text(&csil_v.result_state)));
    csil_entries.push((
        cbor_text("result_revision"),
        cbor_int(csil_v.result_revision),
    ));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a OperationReceipt from a decoded CBOR value tree.
fn csil_dec_operation_receipt(
    csil_root: &CsilCborValue,
) -> Result<OperationReceipt, CsilCborError> {
    let operation_id = {
        let csil_field = cbor_require(csil_root, "operation_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let op = {
        let csil_field = cbor_require(csil_root, "op")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let task_uuid = {
        let csil_field = cbor_require(csil_root, "task_uuid")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let queue = {
        let csil_field = cbor_require(csil_root, "queue")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let result_revision = {
        let csil_field = cbor_require(csil_root, "result_revision")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let result_state = {
        let csil_field = cbor_require(csil_root, "result_state")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    let at = {
        let csil_field = cbor_require(csil_root, "at")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    let expires_at = {
        let csil_field = cbor_require(csil_root, "expires_at")?;
        let csil_decode = cbor_as_i64;
        csil_decode(csil_field)?
    };
    Ok(OperationReceipt {
        operation_id,
        op,
        task_uuid,
        queue,
        result_revision,
        result_state,
        at,
        expires_at,
    })
}

/// Encode a OperationReceipt to canonical CSIL CBOR bytes.
pub fn encode_operation_receipt(csil_v: &OperationReceipt) -> Vec<u8> {
    cbor_encode(&csil_enc_operation_receipt(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a OperationReceipt.
pub fn decode_operation_receipt(csil_data: &[u8]) -> Result<OperationReceipt, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_operation_receipt(&csil_root)
}

/// Build the canonical CBOR value tree for a LookupOperationRequest.
fn csil_enc_lookup_operation_request(csil_v: &LookupOperationRequest) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    csil_entries.push((cbor_text("operation_id"), cbor_text(&csil_v.operation_id)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a LookupOperationRequest from a decoded CBOR value tree.
fn csil_dec_lookup_operation_request(
    csil_root: &CsilCborValue,
) -> Result<LookupOperationRequest, CsilCborError> {
    let operation_id = {
        let csil_field = cbor_require(csil_root, "operation_id")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(LookupOperationRequest { operation_id })
}

/// Encode a LookupOperationRequest to canonical CSIL CBOR bytes.
pub fn encode_lookup_operation_request(csil_v: &LookupOperationRequest) -> Vec<u8> {
    cbor_encode(&csil_enc_lookup_operation_request(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a LookupOperationRequest.
pub fn decode_lookup_operation_request(
    csil_data: &[u8],
) -> Result<LookupOperationRequest, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_lookup_operation_request(&csil_root)
}

/// Build the canonical CBOR value tree for a LookupOperationResponse.
fn csil_enc_lookup_operation_response(csil_v: &LookupOperationResponse) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(1);
    if let Some(csil_inner) = &csil_v.receipt {
        csil_entries.push((cbor_text("receipt"), csil_enc_operation_receipt(csil_inner)));
    }
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a LookupOperationResponse from a decoded CBOR value tree.
fn csil_dec_lookup_operation_response(
    csil_root: &CsilCborValue,
) -> Result<LookupOperationResponse, CsilCborError> {
    let receipt = match cbor_map_get(csil_root, "receipt") {
        Some(csil_field) => {
            let csil_decode = csil_dec_operation_receipt;
            Some(csil_decode(csil_field)?)
        }
        None => None,
    };
    Ok(LookupOperationResponse { receipt })
}

/// Encode a LookupOperationResponse to canonical CSIL CBOR bytes.
pub fn encode_lookup_operation_response(csil_v: &LookupOperationResponse) -> Vec<u8> {
    cbor_encode(&csil_enc_lookup_operation_response(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a LookupOperationResponse.
pub fn decode_lookup_operation_response(
    csil_data: &[u8],
) -> Result<LookupOperationResponse, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_lookup_operation_response(&csil_root)
}

/// Build the canonical CBOR value tree for a ServiceError.
fn csil_enc_service_error(csil_v: &ServiceError) -> CsilCborValue {
    let mut csil_entries: Vec<(CsilCborValue, CsilCborValue)> = Vec::with_capacity(2);
    csil_entries.push((cbor_text("code"), cbor_uint(csil_v.code)));
    csil_entries.push((cbor_text("message"), cbor_text(&csil_v.message)));
    CsilCborValue::Map(csil_entries)
}

/// Reconstruct a ServiceError from a decoded CBOR value tree.
fn csil_dec_service_error(csil_root: &CsilCborValue) -> Result<ServiceError, CsilCborError> {
    let code = {
        let csil_field = cbor_require(csil_root, "code")?;
        let csil_decode = cbor_as_u64;
        csil_decode(csil_field)?
    };
    let message = {
        let csil_field = cbor_require(csil_root, "message")?;
        let csil_decode = cbor_as_text;
        csil_decode(csil_field)?
    };
    Ok(ServiceError { code, message })
}

/// Encode a ServiceError to canonical CSIL CBOR bytes.
pub fn encode_service_error(csil_v: &ServiceError) -> Vec<u8> {
    cbor_encode(&csil_enc_service_error(csil_v))
}

/// Decode canonical CSIL CBOR bytes into a ServiceError.
pub fn decode_service_error(csil_data: &[u8]) -> Result<ServiceError, CsilCborError> {
    let csil_root = cbor_decode(csil_data)?;
    csil_dec_service_error(&csil_root)
}
