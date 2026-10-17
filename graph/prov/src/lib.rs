//! Port of `graph/prov` (see docs/rust-port.md): the PROV-O JSON-LD export of the log of a change (ADR 0057).
mod gojson;
mod prov;

pub use gojson::{marshal, marshal_indent};
pub use prov::*;

#[cfg(test)]
mod prov_test;
