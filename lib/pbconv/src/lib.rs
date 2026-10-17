//! Port of `lib/pbconv` (see docs/rust-port.md): converts between domain types and their protobuf messages.
//!
//! Naming: `Struct` / `Map` are [`to_struct`] / [`to_map`] (with [`struct_to_map`], [`json_to_value`],
//! [`value_to_json`]), `Time` / `FromTime` are [`time`] / [`from_time`] (and [`opt_time`] / [`from_opt_time`] for an
//! `Option` time, `None` being Go's zero time). The `*_to_pb` functions borrow the model; the `*_from_pb` functions
//! borrow the message, those whose Go version accepts a nil pointer take an `Option<&_>` (or have an `opt_` twin).

mod board;
mod decision;
mod execution;
mod flow;
mod flowdiff;
mod form;
mod graph;
mod log;
mod merge;
mod review;

pub use board::*;
pub use decision::*;
pub use execution::*;
pub use flow::*;
pub use flowdiff::*;
pub use form::*;
pub use graph::*;
pub use log::*;
pub use merge::*;
pub use review::*;
