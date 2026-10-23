//! The core of the Rust graph service (ADR 0163): the model of the graph, its views and the reads through them, over
//! a store it knows only as the [`store::ReadTx`] trait. No I/O of its own: `graph-store` implements the trait per
//! SQL dialect, `graph-service` answers the RPCs from what the reads return.
pub mod change;
pub mod error;
pub mod gojson;
pub mod impact;
pub mod lnk;
pub mod model;
pub mod page;
pub mod read;
pub mod store;
pub mod view;
pub mod write;

pub use error::{GraphError, Kind, Result};
