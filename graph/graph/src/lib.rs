//! goap-graph: the versioned graph of GOAP (the domain axis: nodes, links, branches, baselines; the change axis:
//! changes, their impacts and their log), ported from the Go package `graph/graph`. This crate holds the foundation
//! (slice G1: the repositories, the guard, the log, the states of baselines, the events, the structures and the
//! bootstrap, the checks of values) and the work on a change before it lands (slice G2: drafts, checkouts, impacts
//! and their reviews, lifecycles, merge and split, flows, options, decisions, the views); PORTING.md lists what slice
//! G3 adds (the commit and the merge) and how it plugs in.

// The foundation holds crate-private pieces the later slices of the port call (the impact events, the operation
// check, the views of a guard, the transition algorithms, ...): unused until they land.
#![allow(dead_code)]

mod algorithms;
mod api;
mod apply;
mod baselinedelta;
mod baselinediff;
mod bootstrap;
mod browse;
mod changebranch;
mod changeimpact;
mod changeimpact_apply;
mod changeimpact_sql;
mod changelog;
mod changeview;
mod checkout;
mod decision;
mod dependency;
mod draft;
mod draftcache;
mod events;
mod flow;
mod flowdiff;
mod flownodes;
mod graph;
mod guard;
mod impactevents;
mod impactvalidation;
mod itemtypes;
mod lifecycle;
mod memory;
mod merge;
mod mergecheck;
mod modification;
mod operation;
mod option;
mod postgres;
mod rebase;
mod repo;
mod restructure;
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
pub use browse::{
    LinkPage, LinkQuery, MAX_BROWSE_LIMIT, Neighbourhood, NodePage, NodeQuery, TypeCount,
};
pub use changeview::{VIEW_ACCEPTED, VIEW_LANDED, VIEW_WRITTEN};
pub use draftcache::DEFAULT_DRAFT_CACHE_SIZE;
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
