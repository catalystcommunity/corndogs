//! TLS options for the corndogs transport (cargo feature `tls`).
//!
//! The corndogs server serves TLS on its RPC TCP port when the operator sets
//! `CORNDOGS_TLS_CERT_FILE` and `CORNDOGS_TLS_KEY_FILE`. The server uses
//! TLS 1.2 or TLS 1.3. The server does not ask for a client certificate. The
//! framing inside TLS is the same as on plain TCP.
//!
//! This module uses rustls with the `ring` crypto provider. The client
//! verifies the server certificate against one of these root sets:
//!
//! - a CA bundle file in PEM format ([`TlsOptions::ca_file`]),
//! - the system root store ([`TlsOptions::system_roots`]).
//!
//! The client uses the host part of the address as the server name for SNI
//! and for certificate verification. Use [`TlsOptions::server_name`] to set a
//! different name. For example, connect to an IP address with a certificate
//! for a DNS name.
//!
//! ```no_run
//! use corndogs::transport::{ConnectOptions, TlsOptions, Transport};
//! use std::time::Duration;
//!
//! let tls = TlsOptions::ca_file("/etc/corndogs/ca.pem").server_name("corndogs.internal");
//! let opts = ConnectOptions::new().io_timeout(Duration::from_secs(10)).tls(tls);
//! let tr = Transport::connect_options("10.0.0.5:5080", opts).expect("connect");
//! ```

use std::path::PathBuf;
use std::sync::Arc;

use rustls::{ClientConfig, ClientConnection, RootCertStore};
use rustls_pki_types::pem::PemObject;
use rustls_pki_types::{CertificateDer, ServerName};

use crate::client::ClientError;

#[derive(Clone, Debug)]
enum Roots {
    System,
    CaFile(PathBuf),
}

/// Tells the transport to use TLS and how to verify the server.
#[derive(Clone, Debug)]
pub struct TlsOptions {
    roots: Roots,
    server_name: Option<String>,
}

impl TlsOptions {
    /// Verifies the server against the system root store.
    pub fn system_roots() -> Self {
        Self {
            roots: Roots::System,
            server_name: None,
        }
    }

    /// Verifies the server against the CA certificates in a PEM file. The
    /// file can contain more than one certificate.
    pub fn ca_file(path: impl Into<PathBuf>) -> Self {
        Self {
            roots: Roots::CaFile(path.into()),
            server_name: None,
        }
    }

    /// Sets the server name for SNI and for certificate verification. If you
    /// do not set it, the transport uses the host part of the address.
    pub fn server_name(mut self, name: impl Into<String>) -> Self {
        self.server_name = Some(name.into());
        self
    }

    /// Loads the roots and builds the rustls client configuration. The
    /// transport does this one time, at connect. Each re-dial uses the result.
    pub(crate) fn resolve(&self, addr: &str) -> Result<TlsSetup, ClientError> {
        let roots = match &self.roots {
            Roots::System => system_root_store()?,
            Roots::CaFile(path) => ca_file_root_store(path)?,
        };
        let provider = Arc::new(rustls::crypto::ring::default_provider());
        let config = ClientConfig::builder_with_provider(provider)
            .with_safe_default_protocol_versions()
            .map_err(|e| tls_err(format!("protocol versions: {e}")))?
            .with_root_certificates(roots)
            .with_no_client_auth();
        let name = match &self.server_name {
            Some(n) => n.clone(),
            None => host_of(addr).to_string(),
        };
        let server_name =
            ServerName::try_from(name.clone()).map_err(|e| tls_err(format!("invalid server name {name:?}: {e}")))?;
        Ok(TlsSetup {
            config: Arc::new(config),
            server_name,
        })
    }
}

/// The resolved TLS state that a transport keeps for its lifetime.
pub(crate) struct TlsSetup {
    config: Arc<ClientConfig>,
    server_name: ServerName<'static>,
}

impl TlsSetup {
    /// Makes a new client session for one connection.
    pub(crate) fn connection(&self) -> Result<ClientConnection, ClientError> {
        ClientConnection::new(self.config.clone(), self.server_name.clone())
            .map_err(|e| tls_err(format!("new session: {e}")))
    }
}

fn tls_err(msg: String) -> ClientError {
    ClientError::Transport(format!("corndogs: tls: {msg}"))
}

fn system_root_store() -> Result<RootCertStore, ClientError> {
    let loaded = rustls_native_certs::load_native_certs();
    let mut store = RootCertStore::empty();
    let (added, _ignored) = store.add_parsable_certificates(loaded.certs);
    if added == 0 {
        let detail = loaded.errors.first().map(|e| format!(": {e}")).unwrap_or_default();
        return Err(tls_err(format!("no usable system root certificates{detail}")));
    }
    Ok(store)
}

fn ca_file_root_store(path: &std::path::Path) -> Result<RootCertStore, ClientError> {
    let shown = path.display();
    let iter = CertificateDer::pem_file_iter(path).map_err(|e| tls_err(format!("read CA file {shown}: {e}")))?;
    let mut store = RootCertStore::empty();
    for cert in iter {
        let cert = cert.map_err(|e| tls_err(format!("parse CA file {shown}: {e}")))?;
        store.add(cert).map_err(|e| tls_err(format!("CA file {shown}: {e}")))?;
    }
    if store.is_empty() {
        return Err(tls_err(format!("CA file {shown} has no certificates")));
    }
    Ok(store)
}

/// Returns the host part of "host:port", "[v6]:port", or a bare host.
fn host_of(addr: &str) -> &str {
    if let Some(rest) = addr.strip_prefix('[') {
        if let Some(end) = rest.find(']') {
            return &rest[..end];
        }
    }
    match addr.rfind(':') {
        // A bare IPv6 address has more than one ':'; keep it whole.
        Some(i) if addr[..i].find(':').is_none() => &addr[..i],
        Some(_) => addr,
        None => addr,
    }
}

#[cfg(test)]
mod tests {
    use super::host_of;

    #[test]
    fn host_part() {
        assert_eq!(host_of("localhost:5080"), "localhost");
        assert_eq!(host_of("10.0.0.5:5080"), "10.0.0.5");
        assert_eq!(host_of("[::1]:5080"), "::1");
        assert_eq!(host_of("corndogs.internal"), "corndogs.internal");
        assert_eq!(host_of("::1"), "::1");
    }
}
