//! The domain registry (ADR 0125 §3). It stores the domains, the
//! definition of the graph (node types, link types, lifecycles, algorithms, one per namespace, ADR 0013), in its own
//! database, and serves them over Connect (`registry.v1.DomainService`). It goes with the graph, which loads its type
//! catalogue from it, and names no methodology: publishing a domain emits an event, and the methodology registry checks
//! its methodologies again.
//!
//! - [`Service`]: the rules (drafts editable, published versions immutable and valid, ABAC on the "domain" resource);
//!   its `changed` hook ([`ChangedFn`], [`changed_fn`]) and its [`Publisher`] of events.
//! - [`Store`]: [`MemoryStore`] and [`SqlStore`] (SQLite for the local mode, PostgreSQL), with their migrations
//!   [`MIGRATIONS`] / [`SQLITE_MIGRATIONS`].
//! - [`Handler`]: the Connect `DomainService` (`goap_gen::registry::v1::domain_service_router`).
//!
//! Differences from the Go package: Go's `SQLStore{DB, Dollar}` is the enum [`SqlStore`]; the import of a domain to
//! publish that has issues returns [`Error::Draft`] (the draft record and the issues inside the error, where Go
//! returns the three values); [`Service::seed_domains`] returns a [`SeedError`] carrying the versions loaded before
//! the failure; `ExportDomain` returns the bytes and the file name.

mod algorithm;
mod error;
mod handler;
mod service;
mod sqlstore;
mod store;

pub use error::{BoxError, ERR_IMMUTABLE, ERR_INVALID, ERR_NOT_FOUND, Error};
pub use handler::{Handler, to_connect};
pub use service::{ChangedFn, ChangedFuture, Clock, Publisher, SeedError, Service, changed_fn};
pub use sqlstore::{MIGRATIONS, SQLITE_MIGRATIONS, SqlStore};
pub use store::{MemoryStore, Store};
