//! Port of `lib/rpcerr` (see docs/rust-port.md): maps domain errors to Connect codes and back.

mod rpcerr;

pub use rpcerr::{NOT_FOUND, NotFound, REASON_HEADER, from_connect, is_forbidden, to_connect};
