//! The migrations, applied as the Go services apply them (`lib/platform` `Migrate` / `MigrateSQLite`): each file once,
//! in name order, in a transaction with its record in `schema_migrations` (PostgreSQL: by name, under an advisory
//! lock so that replicas start together; SQLite: by component and name).
use crate::migrations::*;

/// What a store holds: the graph process's tables, or the domain registry's (ADR 0167: a service of its own, on a
/// database of its own).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Part {
    /// The graph and its smart tags.
    Graph,
    /// The domain registry.
    Domains,
}

/// The migrations of a part, by component (the SQLite component names of the Go services).
pub fn sets(postgres: bool, part: Part) -> Vec<(&'static str, &'static [(&'static str, &'static str)])> {
    match (part, postgres) {
        (Part::Graph, true) => vec![("graph", GRAPH_PG), ("smarttags", SMARTTAGS_PG)],
        (Part::Graph, false) => vec![("graph", GRAPH_SQLITE), ("smarttags", SMARTTAGS_SQLITE)],
        (Part::Domains, true) => vec![("domains", DOMAINS_PG)],
        (Part::Domains, false) => vec![("domains", DOMAINS_SQLITE)],
    }
}
