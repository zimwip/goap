//! The migrations, applied as the Go services apply them (`lib/platform` `Migrate` / `MigrateSQLite`): each file once,
//! in name order, in a transaction with its record in `schema_migrations` (PostgreSQL: by name, under an advisory
//! lock so that replicas start together; SQLite: by component and name).
use crate::migrations::*;

/// The migrations of the graph process, by component (the SQLite component names of `goap-dev`).
pub fn sets(postgres: bool) -> [(&'static str, &'static [(&'static str, &'static str)]); 3] {
    if postgres {
        [
            ("graph", GRAPH_PG),
            ("registry", DOMAINS_PG),
            ("smarttags", SMARTTAGS_PG),
        ]
    } else {
        [
            ("graph", GRAPH_SQLITE),
            ("registry", DOMAINS_SQLITE),
            ("smarttags", SMARTTAGS_SQLITE),
        ]
    }
}
