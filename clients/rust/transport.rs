//! CBOR-over-HTTP implementation of the generated `Transport` trait.
//!
//! The wire is CBOR in the POST body to `{base_url}/v1alpha1/{service}/{method}`;
//! map/field keys are the CSIL field names verbatim (csilgen
//! docs/cbor-wire-contract.md).
use serde::de::DeserializeOwned;
use serde::Serialize;

use crate::gen::{ClientError, ServiceError, Transport};

pub struct CborHttpTransport {
    base_url: String,
    client: reqwest::blocking::Client,
}

impl CborHttpTransport {
    pub fn new(base_url: impl Into<String>) -> Self {
        Self {
            base_url: base_url.into().trim_end_matches('/').to_string(),
            client: reqwest::blocking::Client::new(),
        }
    }
}

impl Transport for CborHttpTransport {
    fn call<Req, Res>(&self, service: &str, method: &str, req: &Req) -> Result<Res, ClientError>
    where
        Req: Serialize,
        Res: DeserializeOwned,
    {
        let mut body = Vec::new();
        ciborium::into_writer(req, &mut body).map_err(|e| ClientError::Transport(e.to_string()))?;

        let url = format!("{}/v1alpha1/{}/{}", self.base_url, service, method);
        let resp = self
            .client
            .post(&url)
            .header("content-type", "application/cbor")
            .header("accept", "application/cbor")
            .body(body)
            .send()
            .map_err(|e| ClientError::Transport(e.to_string()))?;

        let status = resp.status();
        let bytes = resp
            .bytes()
            .map_err(|e| ClientError::Transport(e.to_string()))?;

        if !status.is_success() {
            if let Ok(se) = ciborium::from_reader::<ServiceError, _>(bytes.as_ref()) {
                return Err(ClientError::Service {
                    code: se.code as i64,
                    message: se.message,
                });
            }
            return Err(ClientError::Transport(format!("http {}", status.as_u16())));
        }

        ciborium::from_reader(bytes.as_ref()).map_err(|e| ClientError::Transport(e.to_string()))
    }
}
