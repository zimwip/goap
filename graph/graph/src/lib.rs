//! goap-graph: the versioned graph of GOAP (the domain axis: nodes, links, branches, baselines; the change axis:
//! changes, their impacts and their log), ported from the Go package `graph/graph`: the repositories (memory, SQLite,
//! PostgreSQL), the guard, the log, the states of baselines, the events, the structures and the bootstrap; the work on
//! a change (drafts, checkouts, impacts and their reviews, lifecycles, merge and split, flows, options, decisions,
//! the views); the commit, the rebase and the merge of a change, sub-changes, attach / detach, dependencies, moves
//! and upgrades, tags, purges, snapshots and the `goap_graphapi::Port` of the graph.

mod algorithms;
mod api;
mod apply;
mod attach;
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
mod commit;
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
mod move_change;
mod operation;
mod option;
mod port;
mod postgres;
mod purge;
mod rebase;
mod repo;
mod restructure;
mod snapshot;
mod sqlbuild;
mod sqlite;
mod sqltx;
mod state;
mod structure;
mod subchange;
mod tag;
mod transition;
mod upgrade;
mod validate;
mod validator;

#[cfg(test)]
mod tests;
#[cfg(test)]
pub(crate) mod testsupport;

pub use goap_graphapi::{Error, ErrorKind};

pub use api::*;
pub use attach::{HEADER_GUARDIANS, HEADER_PARENT_ID};
pub use baselinediff::{BaselineDiff, DIFF_ADDED, DIFF_CHANGED, DIFF_REMOVED};
pub use browse::{
    LinkPage, LinkQuery, MAX_BROWSE_LIMIT, Neighbourhood, NodePage, NodeQuery, TypeCount,
};
pub use changebranch::SharedNode;
pub use changeview::{VIEW_ACCEPTED, VIEW_LANDED, VIEW_WRITTEN};
pub use dependency::HEADER_DEPENDS_ON;
pub use draftcache::DEFAULT_DRAFT_CACHE_SIZE;
pub use events::{EventSink, GraphEvent, Published};
pub use graph::{
    CallerFn, ChangeLifecycles, ChangeTransitionAuthorizer, Facets, Graph, ItemAuthorizer,
    ItemPolicy, OperationAuthorizer, ProjectMoveGate, SubChangeValidator, TransitionAuthorizer,
    TypeCatalog, TypesFn,
};
pub use guard::GraphTx;
pub use memory::Memory;
pub use merge::{
    DELETED_KEY, MERGE_ADDED, MERGE_FAST_FORWARD, MERGE_THREE_WAY, MergeRequest, MergeResult,
    NewBranch, common_ancestor, merge_links, merge_props,
};
pub use move_change::HEADER_PROJECT_ID;
pub use option::{BlackboardFacet, LogFacet, LogFacetFold};
pub use postgres::{MIGRATIONS, Postgres};
pub use rebase::{CommitState, HEADER_BASELINE_ID};
pub use repo::{BaselineEntry, BaselineHeader, Repo, Result, Tx};
pub use snapshot::{
    SNAPSHOT_FORMAT, Snapshot, SnapshotGate, SnapshotImport, SnapshotImportResult, SnapshotLink,
    SnapshotNode, SnapshotProvenance, parse_snapshot,
};
pub use sqlite::{SQLITE_MIGRATIONS, Sqlite, ts_parse, ts_text};
pub use state::{StateCache, default_materialize_every, set_default_materialize_every};
pub use upgrade::HEADER_METHODOLOGY_VERSION;
