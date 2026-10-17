//! Port of `platform/prefssvc` (see docs/rust-port.md): the personal preferences of the users, kept outside the graph
//! (ADR 0038): one JSON document per user, read and written by that user alone, saved as they change it.
//!
//! - [`Service`]: the rules (an identified subject, a JSON object of at most [`MAX_DOCUMENT_BYTES`]).
//! - [`Store`]: [`MemoryStore`] and [`SqlStore`] (SQLite for the local mode, PostgreSQL), with their migrations
//!   [`MIGRATIONS`] / [`SQLITE_MIGRATIONS`].
//! - [`Handler`]: the Connect `PreferencesService` (`goap_gen::preferences::v1::preferences_service_router`).
//!
//! Differences from the Go package: Go's `SQLStore{DB, Dollar}` is the enum [`SqlStore`] (one variant per dialect);
//! a document that is not JSON (a Go `func` value) cannot be built in Rust, so only the size is checked.

mod handler;
mod service;
mod store;

pub use handler::Handler;
pub use service::{Error, MAX_DOCUMENT_BYTES, Service};
pub use store::{MIGRATIONS, MemoryStore, Prefs, SQLITE_MIGRATIONS, SqlStore, Store, UpdateFn};
