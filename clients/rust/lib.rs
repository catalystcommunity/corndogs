//! corndogs Rust client: generated code (`gen/`) + a CBOR-over-HTTP transport.
//!
//! ```ignore
//! use corndogs_client::{CorndogsClient, CborHttpTransport, SubmitTaskRequest};
//! let client = CorndogsClient::new(CborHttpTransport::new("https://corndogs.example.com"));
//! let resp = client.submit_task(SubmitTaskRequest { queue: "q".into(), ..Default::default() })?;
//! # Ok::<(), corndogs_client::ClientError>(())
//! ```
#[path = "gen/mod.rs"]
pub mod gen;
pub use gen::*;

mod transport;
pub use transport::CborHttpTransport;
