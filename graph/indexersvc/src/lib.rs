//! Port of `graph/indexersvc` (see docs/rust-port.md): the node index service (ADR 0026, 0095). It follows the node,
//! baseline and change-document events of the graph ([`SUBJECTS`], [`Service::handle`]; the binary consumes them
//! through `goap_platform::Events::consume_durable`, the all-in-one mode through a [`Sink`]), embeds the documents
//! through the model gateway ([`Embedder`]) and answers searches ([`Handler`], Connect `index.v1.IndexService`),
//! authorized by ABAC and the organisation ([`Access`], implemented by `goap_access::Directory`).
//!
//! Differences from the Go package: `Service.Access` and `Service.Republish` are set with [`Service::set_access`] /
//! [`Service::set_republish`] (the service is shared once built); `Status` returns a [`Status`] struct; the store name
//! is the Rust type name (`Memory`, `Sqlite`, `Postgres`); Go's `EnsureVectorIndex` assertion is the [`VectorIndex`]
//! trait, found by [`Service::new`] for the PostgreSQL store; [`Sink::publish`] takes any serializable event (a
//! composition adapts it to the graph's `EventSink`).

mod handler;
mod service;

pub use handler::Handler;
pub use service::{
    Access, BoxError, Embedder, RETRY_AFTER, RepublishFn, RepublishFuture, SUBJECTS, Service, Sink,
    Status, VectorIndex, republish_fn,
};
