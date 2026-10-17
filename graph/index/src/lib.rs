//! Port of `graph/index` (see docs/rust-port.md): the node and change index (ADR 0026, 0095) and its stores: memory,
//! SQLite (FTS5, exact cosine scan) and PostgreSQL (tsvector, pgvector).
mod error;
mod index;
mod indexer;
mod memory;
mod postgres;
mod sqlite;

pub use error::*;
pub use index::*;
pub use indexer::*;
pub use memory::*;
pub use postgres::*;
pub use sqlite::*;

#[cfg(test)]
mod tests;
