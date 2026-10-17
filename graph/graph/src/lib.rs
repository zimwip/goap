//! goap-graph: the versioned graph of GOAP (the domain axis: nodes, links, branches, baselines; the change axis:
//! changes, their impacts and their log), ported from the Go package `graph/graph`. This crate holds the foundation
//! (slice G1: the repositories, the guard, the log, the states of baselines, the events, the structures and the
//! bootstrap, the checks of values); PORTING.md lists what later slices add and how they plug in.

// The foundation holds crate-private pieces the later slices of the port call (the impact events, the operation
// check, the views of a guard, the transition algorithms, ...): unused until they land.
#![allow(dead_code)]

mod algorithms;
mod api;
mod apply;
mod baselinedelta;
mod baselinediff;
mod bootstrap;
mod changebranch;
mod changeimpact;
mod changeimpact_sql;
mod changelog;
mod events;
mod flownodes;
mod graph;
mod guard;
mod impactevents;
mod itemtypes;
mod lifecycle;
mod memory;
mod merge;
mod operation;
mod option;
mod postgres;
mod repo;
mod sqlbuild;
mod sqlite;
mod sqltx;
mod state;
mod structure;
mod subchange;
mod transition;
mod validate;
mod validator;

#[cfg(test)]
mod tests;
#[cfg(test)]
pub(crate) mod testsupport;

pub use goap_graphapi::{Error, ErrorKind};

pub use api::*;
pub use baselinediff::{BaselineDiff, DIFF_ADDED, DIFF_CHANGED, DIFF_REMOVED};
pub use events::{EventSink, GraphEvent, Published};
pub use graph::{
    CallerFn, ChangeLifecycles, ChangeTransitionAuthorizer, Facets, Graph, ItemAuthorizer,
    ItemPolicy, OperationAuthorizer, ProjectMoveGate, SubChangeValidator, TransitionAuthorizer,
    TypeCatalog, TypesFn,
};
pub use guard::GraphTx;
pub use memory::Memory;
pub use option::{BlackboardFacet, LogFacet, LogFacetFold};
pub use postgres::{MIGRATIONS, Postgres};
pub use repo::{BaselineEntry, BaselineHeader, Repo, Result, Tx};
pub use sqlite::{SQLITE_MIGRATIONS, Sqlite, ts_parse, ts_text};
pub use state::{StateCache, default_materialize_every, set_default_materialize_every};
