//! Generated Rust code from CSIL specification

pub mod types;
pub use types::*;

#[path = "codec.gen.rs"]
pub mod codec;
pub use codec::*;

pub mod services;
pub use services::*;

pub mod client;
pub use client::*;

pub mod client_async;
pub use client_async::*;

// Corndogs' own carrier (hand-written, not generated): CSIL-RPC over a TCP
// StreamCarrier. Not glob-exported at the crate root — `client::Transport`
// (the generated seam trait) already claims that name there — so it lives at
// `corndogs::transport::Transport`. See src/transport.rs for the full design.
pub mod transport;
