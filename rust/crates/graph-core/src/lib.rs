//! The core of the Rust graph service (ADR 0163): the model of the graph, its views and the reads through them, over
//! a store it knows only as the [`store::ReadTx`] trait. No I/O of its own: `graph-store` implements the trait per
//! SQL dialect, `graph-service` answers the RPCs from what the reads return.
pub mod access;
pub mod board;
pub mod change;
pub mod codetext;
pub mod criticality;
pub mod decision;
pub mod dependency;
pub mod draftmerge;
pub mod error;
pub mod flow;
pub mod flowdiff;
pub mod godecode;
pub mod gojson;
pub mod goyaml;
pub mod impact;
pub mod items;
pub mod lnk;
pub mod model;
pub mod okf;
pub mod operations;
pub mod page;
pub mod prov;
pub mod query;
pub mod reach;
pub mod read;
pub mod snapshot;
pub mod sources;
pub mod stats;
pub mod store;
pub mod view;
pub mod write;

pub use error::{GraphError, Kind, Result};
