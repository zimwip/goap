//! What the reads ask of a store: one trait, implemented per dialect by `graph-store`. Each method is one statement
//! (or a few batched ones), never a walk of a history or of a namespace: the reads of a view cost what they ask.
use std::{collections::HashMap, future::Future};

use crate::{
    change::{Change, ChangesFilter},
    error::Result,
    model::{
        Baseline, BaselineId, Branch, ChangeId, Link, LinkId, Node, NodeId, NodePage, NodeQuery, NodeRef, Version,
    },
};

/// What [`ReadTx::committed_versions`] answers for a node whose last commit reverts it to the fork of its branch.
pub const REVERTED: Version = -1;

/// The reads of a store, in one session (a read-only connection: PostgreSQL with no transaction around the
/// statements, SQLite a reader of the WAL).
pub trait ReadTx: Sync {
    /// What the transaction read of the platform graph when another graph holds the platform's namespaces (a content
    /// graph, ADR 0154 §9): the reads of the organisation, the projects and the methodologies take it; none, they
    /// read the store.
    fn platform(&self) -> Option<&crate::platform::Platform> {
        None
    }

    /// The type catalogue of the domains in force the session was opened with (the registry's, ADR 0167); none, the
    /// built-in domains of a checkout (tests).
    fn catalog(&self) -> Option<std::sync::Arc<graph_catalog::Catalog>> {
        None
    }

    /// The order of the last commit on a branch (0: none).
    fn last_commit(&self, branch: &str) -> impl Future<Output = Result<i64>> + Send;

    /// The whole header of a change; none when the store holds none.
    fn change(&self, id: &ChangeId) -> impl Future<Output = Result<Option<Change>>> + Send;

    /// The entries of the log of a change after an order whose type starts with one of the prefixes, in order.
    fn log(
        &self,
        change: &ChangeId,
        after: i64,
        prefixes: &[&str],
    ) -> impl Future<Output = Result<Vec<crate::impact::LogEntry>>> + Send;

    /// A branch of a namespace; none when the store holds none of that name.
    fn branch(&self, namespace: &str, name: &str) -> impl Future<Output = Result<Option<Branch>>> + Send;

    /// The header of a baseline.
    fn baseline_header(&self, id: &BaselineId) -> impl Future<Output = Result<Option<Baseline>>> + Send;

    /// The headers of the baselines of a namespace, oldest first.
    fn baseline_headers(&self, namespace: &str) -> impl Future<Output = Result<Vec<Baseline>>> + Send;

    /// The branches of a namespace, oldest first.
    fn branches(&self, namespace: &str) -> impl Future<Output = Result<Vec<Branch>>> + Send;

    /// The headers of baselines of a namespace by id, in the order they committed; an id of no baseline of the
    /// namespace is left out.
    fn baseline_headers_of(
        &self,
        namespace: &str,
        ids: &[BaselineId],
    ) -> impl Future<Output = Result<Vec<Baseline>>> + Send;

    /// The headers of the baselines of a branch committed before the order before (0: the newest), newest first, at
    /// most limit.
    fn baseline_page(
        &self,
        namespace: &str,
        branch: &str,
        before: i64,
        limit: i64,
    ) -> impl Future<Output = Result<Vec<Baseline>>> + Send;

    /// The headers of the baselines some changes left in a namespace, in the order they committed.
    fn change_baseline_headers(
        &self,
        namespace: &str,
        changes: &[ChangeId],
    ) -> impl Future<Output = Result<Vec<Baseline>>> + Send;

    /// The headers of the baselines of a branch, in the order they committed.
    fn branch_baseline_headers(
        &self,
        namespace: &str,
        branch: &str,
    ) -> impl Future<Output = Result<Vec<Baseline>>> + Send;

    /// The rows of a namespace in the store, by measure (`stats::STORE_MEASURES`) then by key (`Tx.StoreCounts`).
    fn store_counts(
        &self,
        namespace: &str,
    ) -> impl Future<Output = Result<HashMap<String, HashMap<String, i64>>>> + Send;

    /// The number of baselines of each branch of a namespace.
    fn baseline_counts(&self, namespace: &str) -> impl Future<Output = Result<HashMap<String, i64>>> + Send;

    /// The last commit of each node on a branch of a namespace in the orders `(after, up_to]` (ADR 0140, 0143), for
    /// some nodes (None: every node): its version, 0 when that version deletes the node, [`REVERTED`] when it reverts
    /// the node to the version the branch was forked from (ADR 0102: only the branch of a change holds one).
    fn committed_versions(
        &self,
        namespace: &str,
        branch: &str,
        after: i64,
        up_to: i64,
        ids: Option<&[NodeId]>,
    ) -> impl Future<Output = Result<HashMap<NodeId, Version>>> + Send;

    /// The nodes of a namespace, by id.
    fn namespace_node_ids(&self, namespace: &str) -> impl Future<Output = Result<Vec<NodeId>>> + Send;

    /// A version of a node.
    fn node(&self, r: &NodeRef) -> impl Future<Output = Result<Option<Node>>> + Send;

    /// The latest version of a node on a branch.
    fn latest_on(&self, id: &NodeId, branch: &str) -> impl Future<Output = Result<Option<Node>>> + Send;

    /// The node of a key in a namespace.
    fn node_id_by_key(&self, namespace: &str, key: &str) -> impl Future<Output = Result<Option<NodeId>>> + Send;

    /// Versions of nodes, of one type when `typ` is not empty, by key.
    fn nodes_at(&self, refs: &[NodeRef], typ: &str) -> impl Future<Output = Result<Vec<Node>>> + Send;

    /// A link row by its id, none when the store holds none (a row dropping a link left out).
    fn link(&self, id: &LinkId) -> impl Future<Output = Result<Option<Link>>> + Send;

    /// The links some versions hold (their link sets, ADR 0141), of some types (None: every type), by id.
    fn links_from(&self, refs: &[NodeRef], types: Option<&[String]>) -> impl Future<Output = Result<Vec<Link>>> + Send;

    /// The links some versions hold into a node, by id.
    fn links_from_into(&self, refs: &[NodeRef], to: &NodeId) -> impl Future<Output = Result<Vec<Link>>> + Send;

    /// The links entering a version, one per version of their source holding it, by id then source version.
    fn links_to(&self, r: &NodeRef) -> impl Future<Output = Result<Vec<Link>>> + Send;

    /// The nodes a link row names a node as its target from, each once.
    fn link_sources(&self, id: &NodeId) -> impl Future<Output = Result<Vec<NodeId>>> + Send;

    /// A page of some versions, by key, with the counts per type.
    fn node_page(&self, refs: &[NodeRef], q: &NodeQuery) -> impl Future<Output = Result<NodePage>> + Send;

    /// The last `limit` versions of a node below `before` (0: from its latest), oldest first, with the branches each
    /// joined.
    fn version_range(
        &self,
        id: &NodeId,
        before: Version,
        limit: usize,
    ) -> impl Future<Output = Result<Vec<Node>>> + Send;

    /// The nodes the impacts of a change act on; none when the store holds no such change.
    fn change_nodes(&self, change: &ChangeId) -> impl Future<Output = Result<Option<Vec<NodeId>>>> + Send;

    /// The headers of the changes matching the parts of a filter a column answers (namespace, unit, statuses,
    /// project, parent, dependency, ids), oldest first; the text query is the caller's ([`ChangesFilter::matches`]).
    fn change_heads(&self, f: &ChangesFilter) -> impl Future<Output = Result<Vec<Change>>> + Send;

    /// The last `limit` changes holding an impact on a node, each with the order of its first one, below `before`
    /// (0: any), newest first.
    fn node_change_page(
        &self,
        node: &NodeId,
        before: i64,
        limit: usize,
    ) -> impl Future<Output = Result<Vec<(ChangeId, i64)>>> + Send;

    /// The nodes a branch holds a version of: written on it or joined to it (ADR 0137).
    fn branch_node_ids(&self, branch: &str) -> impl Future<Output = Result<Vec<NodeId>>> + Send;

    /// The branches the versions of a node joined as is, with the change that joined each, by version then branch
    /// (`Tx.VersionJoins`).
    fn version_joins(&self, id: &NodeId) -> impl Future<Output = Result<Vec<(Version, String, ChangeId)>>> + Send;

    /// The versions the impacts of a change name (before, after, committed), in the order they were written.
    fn impact_refs(&self, change: &ChangeId) -> impl Future<Output = Result<Vec<NodeRef>>> + Send;
}

/// How a version holds its links (ADR 0141): its own rows (`of` its version, `base` 0), the set of an earlier version
/// as is (`of`), or its rows as a delta over the set of version `base`, `depth` deltas deep, `seq` its order.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct LinkSet {
    pub of: Version,
    pub base: Version,
    pub depth: i32,
    pub seq: i64,
}

/// An event of the outbox of the graph (ADR 0136): written in the transaction of its fact, delivered by a relay.
#[derive(Debug, Clone, PartialEq)]
pub struct OutboxMessage {
    pub subject: String,
    /// The JSON of the event, as Go writes it.
    pub data: String,
    pub headers: std::collections::BTreeMap<String, String>,
    pub created_at: chrono::DateTime<chrono::Utc>,
}

/// The writes of a store, in one transaction on its writer (Go's `Tx`): what a write reads it reads through the same
/// transaction ([`ReadTx`]), and nothing is kept unless it commits.
pub trait WriteTx: ReadTx {
    /// Serializes the writers of the tree of a change (ADR 0127): PostgreSQL takes an advisory lock on its root.
    fn lock_change(&self, id: &ChangeId) -> impl Future<Output = Result<()>> + Send;

    /// Serializes the writers of a branch.
    fn lock_branch(&self, namespace: &str, name: &str) -> impl Future<Output = Result<()>> + Send;

    fn put_change(&self, c: &Change) -> impl Future<Output = Result<()>> + Send;

    fn put_branch(&self, b: &Branch) -> impl Future<Output = Result<()>> + Send;

    fn put_baseline(&self, b: &Baseline) -> impl Future<Output = Result<()>> + Send;

    /// Appends an entry to the log of its change and returns its order.
    fn append_log(&self, e: &crate::impact::LogEntry) -> impl Future<Output = Result<i64>> + Send;

    /// Writes the projection of an impact (`change_impact`).
    fn put_change_impact(
        &self,
        change: &ChangeId,
        cn: &crate::impact::ChangeImpact,
    ) -> impl Future<Output = Result<()>> + Send;

    fn delete_change_impact(&self, change: &ChangeId, id: &str) -> impl Future<Output = Result<()>> + Send;

    /// The keys and types of the first impacts of a change, at most `limit`.
    fn change_impact_keys(
        &self,
        change: &ChangeId,
        limit: usize,
    ) -> impl Future<Output = Result<Vec<(String, String)>>> + Send;

    fn append_outbox(&self, msgs: &[OutboxMessage]) -> impl Future<Output = Result<()>> + Send;

    /// Writes a version (and the node, at its first), committed at an order.
    fn put_node(&self, n: &Node, committed: i64) -> impl Future<Output = Result<()>> + Send;

    /// Writes link rows.
    fn put_links(&self, ls: &[Link]) -> impl Future<Output = Result<()>> + Send;

    /// Writes the row dropping a link from the set of a version.
    fn drop_link(&self, from: &NodeRef, row: &LinkId, l: &Link) -> impl Future<Output = Result<()>> + Send;

    fn put_link_set(&self, from: &NodeRef, set: LinkSet) -> impl Future<Output = Result<()>> + Send;

    /// How some versions hold their links.
    fn link_sets_of(&self, refs: &[NodeRef]) -> impl Future<Output = Result<HashMap<NodeRef, LinkSet>>> + Send;

    /// Makes a version join a branch, at the order of the baseline it commits in.
    fn join_branch(
        &self,
        r: &NodeRef,
        branch: &str,
        change: &ChangeId,
        committed: i64,
    ) -> impl Future<Output = Result<()>> + Send;

    /// Gives a version the change and impact it commits for, with the comment of that impact (a version a sub-change
    /// wrote, merged into its parent and then into its target: Go's `SetNodeOrigin`).
    fn set_node_origin(
        &self,
        r: &NodeRef,
        change: &ChangeId,
        impact: &str,
        comment: &str,
    ) -> impl Future<Output = Result<()>> + Send;

    /// Takes the next position of the log of the changes and of their impacts and leaves them unused: what a
    /// transaction of the Go service declaring an impact then rolling back takes (PostgreSQL's sequences are not
    /// rolled back; SQLite's are), so that both services number alike while they are compared (ADR 0163).
    fn skip_declaration(&self) -> impl Future<Output = Result<()>> + Send;

    /// Takes the next positions of the impacts and leaves them unused: what the verdicts of a review batch the Go
    /// service wrote before one of them failed take (its projection is written verdict by verdict), so that both
    /// services number alike while they are compared (ADR 0163).
    fn skip_impact_positions(&self, n: usize) -> impl Future<Output = Result<()>> + Send;

    /// Marks the point a rehearsal of the Go service starts from (Go's `gated`: a pass it rolls back, ADR 0163).
    fn savepoint(&self) -> impl Future<Output = Result<()>> + Send;

    /// Undoes what the transaction wrote since its savepoint; the sequences of PostgreSQL keep the positions taken.
    fn rollback_to_savepoint(&self) -> impl Future<Output = Result<()>> + Send;

    /// The next order of a commit.
    fn next_commit(&self) -> impl Future<Output = Result<i64>> + Send;

    /// The types of the entries of the log of a change.
    fn log_types(&self, change: &ChangeId) -> impl Future<Output = Result<Vec<String>>> + Send;

    /// Deletes a change that wrote nothing the graph uses, with what it wrote and its branch (`branch` not empty).
    fn delete_change(&self, id: &ChangeId, namespace: &str, branch: &str) -> impl Future<Output = Result<()>> + Send;
}
