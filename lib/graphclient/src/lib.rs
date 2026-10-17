//! The client of the graph service, implementing
//! `goap_graphapi::Port` (and so `goap_access::Graph`, `goap_graphsnap::Graph`) over the generated client.

mod client;

pub use client::*;
