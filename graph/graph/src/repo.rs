//! The persistence primitives the storage backends implement.
//!
//! Go's `Repo.InTx(ctx, fn)` runs a closure in a transaction; in Rust a transaction is a value: [`Repo::begin`] opens
//! one, [`Tx::commit`] commits it, and dropping it without committing rolls it back (what Go does when `fn` returns
//! an error). Every method of a [`Tx`] takes `&mut self`: one transaction is used by one task at a time.

use std::any::Any;
use std::collections::BTreeMap;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{
    Baseline, BaselineID, Branch, Change, ChangeID, ChangeImpact, ChangeImpactID, Link, LinkID,
    LogEntry, LogFilter, Node, NodeID, NodeRef, Tag, TagFilter, TagID, Version,
};

use crate::Error;

/// Result is the result of the graph's operations.
pub type Result<T, E = Error> = std::result::Result<T, E>;

/// Repo is the persistence primitive implemented by storage backends.
#[async_trait]
pub trait Repo: Send + Sync {
    /// Begin opens a transaction. It is rolled back when dropped without [`Tx::commit`].
    async fn begin(&self, ctx: &Ctx) -> Result<Box<dyn Tx>>;
    /// The repository as `Any`, so that a test can reach the store under a graph (its pool).
    fn as_any(&self) -> &dyn Any;
}

/// BaselineHeader is what compaction needs of a stored baseline (`baselinedelta.go`).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct BaselineHeader {
    pub id: BaselineID,
    pub parent: BaselineID,
    pub depth: i64,
}

/// BaselineEntry is one stored entry of a baseline: a node at a version, or, in a delta, a node that left it
/// (removed, version is the one it had).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct BaselineEntry {
    pub node: NodeID,
    pub version: Version,
    pub removed: bool,
}

/// Tx gives access to storage primitives inside a transaction.
#[async_trait]
pub trait Tx: Send {
    /// Node returns an exact node version, or the latest one on main when ref.version is 0.
    async fn node(&mut self, ctx: &Ctx, r: &NodeRef) -> Result<Node>;
    /// LatestOn returns the latest version of a node on a branch: written there or joined (not found if none).
    async fn latest_on(&mut self, ctx: &Ctx, id: &NodeID, branch: &str) -> Result<Node>;
    /// Versions returns every version of a node, all branches, by version, with the branches each one joined.
    async fn versions(&mut self, ctx: &Ctx, id: &NodeID) -> Result<Vec<Node>>;
    /// DerivedNodes returns the versions that name r among their origins (ADR 0077): the first versions of the
    /// successors of a merge or split; version 0 matches any version of the node.
    async fn derived_nodes(&mut self, ctx: &Ctx, r: &NodeRef) -> Result<Vec<Node>>;
    async fn branch(&mut self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Branch>;
    async fn branches(&mut self, ctx: &Ctx, namespace: &str) -> Result<Vec<Branch>>;
    async fn put_branch(&mut self, ctx: &Ctx, b: Branch) -> Result<()>;
    async fn node_by_key(&mut self, ctx: &Ctx, namespace: &str, key: &str) -> Result<Node>;
    /// NodeIDByKey resolves a key whatever the branch the node lives on.
    async fn node_id_by_key(&mut self, ctx: &Ctx, namespace: &str, key: &str) -> Result<NodeID>;
    async fn nodes_in(
        &mut self,
        ctx: &Ctx,
        baseline: &BaselineID,
        node_type: &str,
    ) -> Result<Vec<Node>>;
    /// LatestNodes returns the latest version of every node of a namespace on a branch.
    async fn latest_nodes(&mut self, ctx: &Ctx, namespace: &str, branch: &str)
    -> Result<Vec<Node>>;
    /// Namespaces returns the distinct namespaces holding at least one node (admin/reindex use).
    async fn namespaces(&mut self, ctx: &Ctx) -> Result<Vec<String>>;
    async fn out_links(&mut self, ctx: &Ctx, r: &NodeRef) -> Result<Vec<Link>>;
    async fn in_links(&mut self, ctx: &Ctx, r: &NodeRef) -> Result<Vec<Link>>;
    async fn baseline(&mut self, ctx: &Ctx, id: &BaselineID) -> Result<Baseline>;
    async fn baselines(&mut self, ctx: &Ctx, namespace: &str) -> Result<Vec<Baseline>>;
    async fn change(&mut self, ctx: &Ctx, id: &ChangeID) -> Result<Change>;
    async fn changes(&mut self, ctx: &Ctx) -> Result<Vec<Change>>;

    /// PutNode writes a node version. A version is written once and never changes (ADR 0079): the working state of a
    /// node in a change is a draft, the version is written when the change lands.
    async fn put_node(&mut self, ctx: &Ctx, n: Node) -> Result<()>;
    /// PutLink writes a link of a version written in the same transaction; Link reads one.
    async fn put_link(&mut self, ctx: &Ctx, l: Link) -> Result<()>;
    async fn link(&mut self, ctx: &Ctx, id: &LinkID) -> Result<Link>;
    /// PutBaseline stores a baseline: its entries when b.gap is 0, else only its header (ADR 0056); a Baseline read
    /// back with a gap holds no nodes, the graph computes them.
    async fn put_baseline(&mut self, ctx: &Ctx, b: Baseline) -> Result<()>;
    /// BranchJoins lists the node versions a change joined to a branch (ADR 0032: a fast-forward), of the nodes of a
    /// namespace.
    async fn branch_joins(
        &mut self,
        ctx: &Ctx,
        namespace: &str,
        branch: &str,
        change: &ChangeID,
    ) -> Result<Vec<NodeRef>>;
    /// MaterializeBaseline stores the state of a baseline stored with a gap: whole, its gap becomes 0.
    async fn materialize_baseline(
        &mut self,
        ctx: &Ctx,
        id: &BaselineID,
        nodes: &BTreeMap<NodeID, Version>,
    ) -> Result<()>;
    /// PutTag stores a tag (ADR 0056); DeleteTag removes it (not found when absent); Tags lists the tags matching
    /// f, oldest first.
    async fn put_tag(&mut self, ctx: &Ctx, t: Tag) -> Result<()>;
    async fn delete_tag(&mut self, ctx: &Ctx, id: &TagID) -> Result<()>;
    async fn tags(&mut self, ctx: &Ctx, f: &TagFilter) -> Result<Vec<Tag>>;
    /// PutChange inserts or updates the change header (items are ignored).
    async fn put_change(&mut self, ctx: &Ctx, c: Change) -> Result<()>;
    /// AppendLog appends an entry to the log of its change (ADR 0030: facts, journal records and impact events in
    /// one order) and returns it with its seq. The log is insert-only: nothing updates or deletes an entry.
    async fn append_log(&mut self, ctx: &Ctx, e: LogEntry) -> Result<LogEntry>;
    /// Log returns the entries matching f, in the order of the log.
    async fn log(&mut self, ctx: &Ctx, f: &LogFilter) -> Result<Vec<LogEntry>>;
    /// LogCounts counts the entries matching f by type (the limit and the position aside).
    async fn log_counts(&mut self, ctx: &Ctx, f: &LogFilter) -> Result<BTreeMap<String, i64>>;

    /// OpenChangeIDs lists the changes that are neither applied nor abandoned.
    async fn open_change_ids(&mut self, ctx: &Ctx) -> Result<Vec<ChangeID>>;
    /// PutChangeImpact inserts or updates a change impact of a change (ADR 0024). The table is the projection of the
    /// impact log (ADR 0029): only `Graph::emit` writes it.
    async fn put_change_impact(
        &mut self,
        ctx: &Ctx,
        change: &ChangeID,
        cn: ChangeImpact,
    ) -> Result<()>;
    /// DeleteChangeImpact removes a change impact from the projection (an event removed it: ADR 0076).
    async fn delete_change_impact(
        &mut self,
        ctx: &Ctx,
        change: &ChangeID,
        id: &ChangeImpactID,
    ) -> Result<()>;
    /// ChangeImpacts lists the change impacts of a change, in creation order.
    async fn change_impacts(&mut self, ctx: &Ctx, change: &ChangeID) -> Result<Vec<ChangeImpact>>;
    /// NodeChangeImpacts lists the changes holding a change impact on a node, oldest first.
    async fn node_change_impacts(&mut self, ctx: &Ctx, node: &NodeID) -> Result<Vec<ChangeID>>;
    /// SetNodeOrigin records on a node version the change and change impact that produced it and its comment.
    async fn set_node_origin(
        &mut self,
        ctx: &Ctx,
        r: &NodeRef,
        change: &ChangeID,
        cn: &ChangeImpactID,
        comment: &str,
    ) -> Result<()>;

    /// JoinBranch makes a node version part of a branch it was not written on (a merge that lands it as is, ADR
    /// 0032): the version is not copied and keeps the branch it was written on. change is the change whose merge
    /// makes it join (ADR 0054: no membership without a change).
    async fn join_branch(
        &mut self,
        ctx: &Ctx,
        r: &NodeRef,
        branch: &str,
        change: &ChangeID,
    ) -> Result<()>;

    /// DeleteChange removes a change that landed nothing (ADR 0037): its log, impacts, node versions and links, the
    /// nodes it alone created, and its branch when it has one of its own (empty: none). It refuses (conflict) when
    /// what the change wrote is used: a baseline holds one of its versions, a later version or a link of another
    /// change builds on one, or it produced a baseline.
    async fn delete_change(
        &mut self,
        ctx: &Ctx,
        id: &ChangeID,
        namespace: &str,
        branch: &str,
    ) -> Result<()>;

    /// LockChange holds, until the transaction ends, the lock of the tree of changes id belongs to (its root change and
    /// every sub-change under it, ADR 0127): two transactions writing changes of one tree run one after the other. A
    /// change not stored yet is its own tree. LockBranch holds the lock of a branch the same way, taken before its head
    /// is read to be moved. A storage whose transactions are serialized already does nothing.
    async fn lock_change(&mut self, ctx: &Ctx, id: &ChangeID) -> Result<()>;
    async fn lock_branch(&mut self, ctx: &Ctx, namespace: &str, name: &str) -> Result<()>;

    /// Commit commits the transaction.
    async fn commit(self: Box<Self>, ctx: &Ctx) -> Result<()>;

    /// Rollback rolls the transaction back now. Dropping a transaction rolls it back too, but a SQL store only does it
    /// when its connection is next used, keeping its locks (row and advisory ones, ADR 0127) meanwhile: the graph rolls
    /// back explicitly a transaction dropped without a commit ([`crate::GraphTx`]). The default does nothing (a store
    /// whose drop rolls back at once).
    async fn rollback(self: Box<Self>) -> Result<()> {
        Ok(())
    }

    /// RollsBackLate reports a store whose dropped transaction is only rolled back later (sqlx: when the connection
    /// is next used), so that the graph rolls it back explicitly ([`Tx::rollback`]) when it is dropped.
    fn rolls_back_late(&self) -> bool {
        false
    }

    /// WroteLog reports whether the transaction appended to the log of a change (Go's `logWriter`); a transaction
    /// that cannot say is taken to have (the draft cache, ADR 0079, never publishes what a transaction that may roll
    /// back has seen of its own writes).
    fn wrote_log(&self, _id: &ChangeID) -> bool {
        true
    }

    /// BaselineHeaders reads the header of every materialised baseline, for the stores that keep baselines as deltas
    /// (Go's `baselineRewriter`, the SQL ones); `None` on a store that keeps whole maps.
    async fn baseline_headers(&mut self, _ctx: &Ctx) -> Result<Option<Vec<BaselineHeader>>> {
        Ok(None)
    }

    /// RewriteBaseline replaces the stored entries of a baseline, whose content does not change (the SQL stores).
    async fn rewrite_baseline(
        &mut self,
        _ctx: &Ctx,
        id: &BaselineID,
        _depth: i64,
        _entries: &[BaselineEntry],
    ) -> Result<()> {
        Err(Error::msg(format!(
            "baseline {id}: the store keeps no delta to rewrite"
        )))
    }
}
