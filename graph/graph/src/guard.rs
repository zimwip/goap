//! The guard (ADR 0054) is the rule of the graph, in Rust, whatever the storage: a database with constraints repeats
//! it, a storage without any still gets it. Every repository transaction of a Graph goes through it ([`Graph::begin`]
//! returns a [`GraphTx`]):
//!
//!   - every node version, link, branch membership and node origin names the change that writes it, and that change
//!     exists;
//!   - every node version is owned by an organisational unit and every node was created in a project, nodes of the
//!     structures in force; a version that names neither gets them from the version it follows, else from the change
//!     that writes it (the unit holding it, the project it acts in); the project of a node never changes;
//!   - every change names the unit holding it and the project it acts in, nodes of the structures in force;
//!   - every baseline is the result of a change (ADR 0056); what precedes the first change of a namespace is the empty
//!     state, the empty baseline id, which nothing stores;
//!   - a version is immutable once written (ADR 0079): the working state of a node in a change is a draft, the version
//!     is written when the change lands, with its outgoing links, in the same transaction; no version is ever edited,
//!     frozen or dropped, and only a version written in the transaction gets links.
//!
//! What names a change, a unit or a project is checked when the transaction ends, before it commits (as a deferred
//! foreign key): the bootstrap writes the root unit and the root project, each referencing the other and itself, in
//! one transaction.
//!
//! Go stacks two wrappers on the store's transaction: the guard, and the observer of `Graph.Observe` (events.go) on
//! top of it. [`GraphTx`] is both: its observer part records what the transaction wrote when the graph has an event
//! sink, and [`GraphTx::commit`] builds the events (inside the transaction), verifies the guard, commits, then
//! publishes (best effort), in Go's order.

use std::collections::{BTreeMap, HashMap, HashSet};

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{
    Baseline, BaselineID, Branch, Change, ChangeID, ChangeImpact, ChangeImpactID, Link, LinkID,
    LogEntry, LogFilter, Node, NodeID, NodeRef, STRUCTURE_ORGANISATION, STRUCTURE_PROJECT, Tag,
    TagFilter, TagID, Version, namespace_of,
};

use crate::events::Published;
use crate::repo::{BaselineEntry, BaselineHeader, Tx};
use crate::{Error, ErrorKind, Graph, Result};

/// What the observer of a transaction records (Go's `observedTx`): the versions written, the baselines created, the
/// changes touched (header, impacts or log), the headers written, the changes whose index document must be
/// published, the changes purged.
#[derive(Default)]
pub(crate) struct ObsState {
    pub(crate) nodes: Vec<NodeRef>,
    pub(crate) baselines: Vec<Baseline>,
    pub(crate) changes: Vec<ChangeID>,
    pub(crate) headers: HashMap<ChangeID, Change>,
    pub(crate) indexed: Vec<ChangeID>,
    pub(crate) purged: Vec<ChangeID>,
}

impl ObsState {
    pub(crate) fn index(&mut self, id: &ChangeID) {
        if !id.is_empty() && !self.indexed.contains(id) {
            self.indexed.push(id.clone());
        }
    }

    pub(crate) fn touch(&mut self, id: &ChangeID) {
        if !id.is_empty() && !self.changes.contains(id) {
            self.changes.push(id.clone());
        }
    }
}

/// GraphTx is a transaction of the graph: the store's transaction under the guard (and the observer when the graph
/// has an event sink). Dropping it without [`GraphTx::commit`] rolls it back.
pub struct GraphTx<'g> {
    inner: Option<Box<dyn Tx>>,
    pub(crate) g: &'g Graph,
    // what the transaction wrote that names a change, a unit or a project, checked at its end
    change_refs: BTreeMap<ChangeID, String>,
    owners: BTreeMap<NodeID, String>,
    projects: BTreeMap<NodeID, String>,
    changes: BTreeMap<ChangeID, Change>,
    // headers caches the changes read to stamp versions
    headers: HashMap<ChangeID, Change>,
    // written are the versions the transaction wrote
    written: HashSet<NodeRef>,
    // logged are the changes whose log the transaction appended to
    logged: HashSet<ChangeID>,
    // locked are the changes and branches whose lock the transaction holds (ADR 0127)
    locked: HashSet<String>,
    pub(crate) obs: Option<ObsState>,
}

impl Graph {
    /// Begin opens a transaction of the graph: the repository's, under the guard (Go's `g.repo.InTx`). Commit it
    /// with [`GraphTx::commit`]; dropping it rolls it back.
    pub async fn begin(&self, ctx: &Ctx) -> Result<GraphTx<'_>> {
        let inner = self.repo.begin(ctx).await?;
        Ok(GraphTx {
            inner: Some(inner),
            g: self,
            change_refs: BTreeMap::new(),
            owners: BTreeMap::new(),
            projects: BTreeMap::new(),
            changes: BTreeMap::new(),
            headers: HashMap::new(),
            written: HashSet::new(),
            logged: HashSet::new(),
            locked: HashSet::new(),
            obs: self.sink.as_ref().map(|_| ObsState::default()),
        })
    }

    /// changeTx opens a transaction holding the lock of the tree of change id (ADR 0127): the operations that write
    /// a change take it before they read what they check, so that it still holds when they write.
    pub(crate) async fn change_tx(&self, ctx: &Ctx, id: &ChangeID) -> Result<GraphTx<'_>> {
        let mut tx = self.begin(ctx).await?;
        tx.lock_change(ctx, id).await?;
        Ok(tx)
    }
}

impl<'g> GraphTx<'g> {
    /// The store's transaction under the guard.
    pub(crate) fn inner(&mut self) -> &mut dyn Tx {
        self.inner
            .as_deref_mut()
            .expect("a transaction of the graph is used after its end")
    }

    /// Commit ends the transaction: the events of what it wrote are built (inside it), the guard verifies it, the
    /// store commits it, then the events are published (best effort: the index rebuilds from the graph).
    pub async fn commit(mut self, ctx: &Ctx) -> Result<()> {
        let out: Vec<Published> = match self.obs.take() {
            Some(obs) => {
                let g = self.g;
                g.events_of(ctx, &mut self, &obs).await?
            }
            None => Vec::new(),
        };
        self.verify(ctx).await?;
        let inner = self
            .inner
            .take()
            .expect("a transaction of the graph is committed once");
        inner.commit(ctx).await?;
        if let Some(sink) = &self.g.sink {
            for p in &out {
                let _ = sink.publish(ctx, &p.subject, &p.event).await;
            }
        }
        Ok(())
    }

    fn lock_key(&mut self, key: String) {
        self.locked.insert(key);
    }

    pub(crate) fn need_change(&mut self, id: &ChangeID, what: &str) -> Result<()> {
        if id.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("{what} is written outside of a change (ADR 0049, 0054)"),
            ));
        }
        self.change_refs
            .entry(id.clone())
            .or_insert_with(|| what.to_string());
        Ok(())
    }

    /// header reads the header of a change (cached for the transaction; Go's `guardTx.change`).
    pub(crate) async fn header(&mut self, ctx: &Ctx, id: &ChangeID) -> Result<Change> {
        if let Some(c) = self.headers.get(id) {
            return Ok(c.clone());
        }
        let c = self.inner().change(ctx, id).await?;
        self.headers.insert(id.clone(), c.clone());
        Ok(c)
    }

    /// resolve returns the id of the node of a structure a change names by key; the node being written is its own
    /// reference (the bootstrap).
    async fn resolve(&mut self, ctx: &Ctx, kind: &str, key: &str, me: &Node) -> Result<NodeID> {
        let st = self.g.structure(kind);
        if key == me.key && st.namespace == namespace_of(&me.namespace) {
            return Ok(me.id.clone());
        }
        match self.inner().node_id_by_key(ctx, &st.namespace, key).await {
            Err(e) if e.is_not_found() => Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "node {}: {kind} {key:?} of its change does not exist",
                    me.key
                ),
            )),
            r => r,
        }
    }

    /// verify checks, at the end of the transaction, that what it wrote names changes, units and projects that exist.
    async fn verify(&mut self, ctx: &Ctx) -> Result<()> {
        let refs: Vec<(ChangeID, String)> = self
            .change_refs
            .iter()
            .map(|(k, v)| (k.clone(), v.clone()))
            .collect();
        for (id, what) in refs {
            if self.changes.contains_key(&id) {
                continue;
            }
            match self.header(ctx, &id).await {
                Err(e) if e.is_not_found() => {
                    return Err(Error::wrap(
                        ErrorKind::Invalid,
                        format!("{what} names change {id}, which does not exist"),
                    ));
                }
                Err(e) => return Err(e),
                Ok(_) => {}
            }
        }
        let owners: Vec<(NodeID, String)> = self
            .owners
            .iter()
            .map(|(k, v)| (k.clone(), v.clone()))
            .collect();
        for (id, what) in owners {
            self.in_structure(ctx, STRUCTURE_ORGANISATION, &id, &format!("{what}: owner"))
                .await?;
        }
        let projects: Vec<(NodeID, String)> = self
            .projects
            .iter()
            .map(|(k, v)| (k.clone(), v.clone()))
            .collect();
        for (id, what) in projects {
            self.in_structure(ctx, STRUCTURE_PROJECT, &id, &format!("{what}: project"))
                .await?;
        }
        let changes: Vec<Change> = self.changes.values().cloned().collect();
        for c in changes {
            for (kind, key, what) in [
                (STRUCTURE_ORGANISATION, &c.owner_org, "owner"),
                (STRUCTURE_PROJECT, &c.project_id, "project"),
            ] {
                let st = self.g.structure(kind);
                let id = match self.inner().node_id_by_key(ctx, &st.namespace, key).await {
                    Err(e) if e.is_not_found() => {
                        return Err(Error::wrap(
                            ErrorKind::Invalid,
                            format!(
                                "change {}: {what} {key:?} is not a node of namespace {}",
                                c.id, st.namespace
                            ),
                        ));
                    }
                    r => r?,
                };
                self.in_structure(ctx, kind, &id, &format!("change {}: {what}", c.id))
                    .await?;
            }
        }
        Ok(())
    }

    /// inStructure checks that a node exists (on any branch) and belongs to the structure of a kind.
    async fn in_structure(&mut self, ctx: &Ctx, kind: &str, id: &NodeID, what: &str) -> Result<()> {
        let st = self.g.structure(kind);
        let vs = match self.inner().versions(ctx, id).await {
            Err(e) if e.is_not_found() => Vec::new(),
            r => r?,
        };
        let Some(n) = vs.last() else {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("{what} {id} does not exist"),
            ));
        };
        if !self.g.in_structure(n, &st) {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "{what} {} is a {} of namespace {}, not a {}",
                    n.key,
                    n.r#type,
                    namespace_of(&n.namespace),
                    st.r#type
                ),
            ));
        }
        Ok(())
    }
}

impl GraphTx<'_> {
    /// Rollback rolls the transaction back now (what dropping it does too, in the background).
    pub async fn rollback(mut self) -> Result<()> {
        match self.inner.take() {
            Some(inner) => inner.rollback().await,
            None => Ok(()),
        }
    }
}

/// A transaction of the graph dropped without a commit (an operation failed) is rolled back at once, in the
/// background: sqlx would only do it when the connection is next used, and PostgreSQL would keep the locks of the
/// transaction (FOR UPDATE, the advisory locks of lock_change / lock_branch) meanwhile.
impl Drop for GraphTx<'_> {
    fn drop(&mut self) {
        if let Some(inner) = self.inner.take() {
            if !inner.rolls_back_late() {
                return; // dropping it rolls it back
            }
            match tokio::runtime::Handle::try_current() {
                Ok(h) => {
                    h.spawn(async move {
                        let _ = inner.rollback().await;
                    });
                }
                Err(_) => drop(inner),
            }
        }
    }
}

#[async_trait]
impl Tx for GraphTx<'_> {
    async fn node(&mut self, ctx: &Ctx, r: &NodeRef) -> Result<Node> {
        self.inner().node(ctx, r).await
    }
    async fn latest_on(&mut self, ctx: &Ctx, id: &NodeID, branch: &str) -> Result<Node> {
        self.inner().latest_on(ctx, id, branch).await
    }
    async fn versions(&mut self, ctx: &Ctx, id: &NodeID) -> Result<Vec<Node>> {
        self.inner().versions(ctx, id).await
    }
    async fn derived_nodes(&mut self, ctx: &Ctx, r: &NodeRef) -> Result<Vec<Node>> {
        self.inner().derived_nodes(ctx, r).await
    }
    async fn branch(&mut self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Branch> {
        self.inner().branch(ctx, namespace, name).await
    }
    async fn branches(&mut self, ctx: &Ctx, namespace: &str) -> Result<Vec<Branch>> {
        self.inner().branches(ctx, namespace).await
    }
    async fn put_branch(&mut self, ctx: &Ctx, b: Branch) -> Result<()> {
        self.inner().put_branch(ctx, b).await
    }
    async fn node_by_key(&mut self, ctx: &Ctx, namespace: &str, key: &str) -> Result<Node> {
        self.inner().node_by_key(ctx, namespace, key).await
    }
    async fn node_id_by_key(&mut self, ctx: &Ctx, namespace: &str, key: &str) -> Result<NodeID> {
        self.inner().node_id_by_key(ctx, namespace, key).await
    }
    async fn nodes_in(
        &mut self,
        ctx: &Ctx,
        baseline: &BaselineID,
        node_type: &str,
    ) -> Result<Vec<Node>> {
        self.guarded_nodes_in(ctx, baseline, node_type).await
    }
    async fn latest_nodes(
        &mut self,
        ctx: &Ctx,
        namespace: &str,
        branch: &str,
    ) -> Result<Vec<Node>> {
        self.inner().latest_nodes(ctx, namespace, branch).await
    }
    async fn namespaces(&mut self, ctx: &Ctx) -> Result<Vec<String>> {
        self.inner().namespaces(ctx).await
    }
    async fn out_links(&mut self, ctx: &Ctx, r: &NodeRef) -> Result<Vec<Link>> {
        self.inner().out_links(ctx, r).await
    }
    async fn in_links(&mut self, ctx: &Ctx, r: &NodeRef) -> Result<Vec<Link>> {
        self.inner().in_links(ctx, r).await
    }
    async fn baseline(&mut self, ctx: &Ctx, id: &BaselineID) -> Result<Baseline> {
        self.guarded_baseline(ctx, id).await
    }
    async fn baselines(&mut self, ctx: &Ctx, namespace: &str) -> Result<Vec<Baseline>> {
        self.guarded_baselines(ctx, namespace).await
    }
    async fn change(&mut self, ctx: &Ctx, id: &ChangeID) -> Result<Change> {
        self.inner().change(ctx, id).await
    }
    async fn changes(&mut self, ctx: &Ctx) -> Result<Vec<Change>> {
        self.inner().changes(ctx).await
    }

    async fn put_node(&mut self, ctx: &Ctx, mut n: Node) -> Result<()> {
        let what = format!("node {} v{}", n.key, n.version);
        self.need_change(&n.change_id, &what)?;
        let mut prev: Option<Node> = None;
        if n.version > 1 {
            let vs = self.inner().versions(ctx, &n.id).await?;
            if let Some(last) = vs.last() {
                let mut p = last.clone();
                if let Some(&first) = n.parents.first()
                    && first >= 1
                    && (first as usize) <= vs.len()
                {
                    p = vs[first as usize - 1].clone();
                }
                prev = Some(p);
                if !n.project.is_empty() && n.project != vs[0].project {
                    return Err(Error::wrap(
                        ErrorKind::Invalid,
                        format!(
                            "{what}: the project of a node never changes ({}, not {})",
                            vs[0].project, n.project
                        ),
                    ));
                }
                n.project = vs[0].project.clone();
            }
        }
        if n.owner.is_empty()
            && let Some(p) = &prev
        {
            n.owner = p.owner.clone();
        }
        if n.owner.is_empty() || n.project.is_empty() {
            let c = match self.header(ctx, &n.change_id).await {
                Err(e) if e.is_not_found() => {
                    return Err(Error::wrap(
                        ErrorKind::Invalid,
                        format!("{what}: change {} does not exist", n.change_id),
                    ));
                }
                r => r?,
            };
            if n.owner.is_empty() {
                n.owner = self
                    .resolve(ctx, STRUCTURE_ORGANISATION, &c.owner_org, &n)
                    .await?;
            }
            if n.project.is_empty() {
                n.project = self
                    .resolve(ctx, STRUCTURE_PROJECT, &c.project_id, &n)
                    .await?;
            }
        }
        self.owners.insert(n.owner.clone(), what.clone());
        self.projects.insert(n.project.clone(), what);
        let r = n.r#ref();
        self.inner().put_node(ctx, n).await?;
        self.written.insert(r.clone());
        if let Some(o) = &mut self.obs {
            o.nodes.push(r);
        }
        Ok(())
    }

    async fn put_link(&mut self, ctx: &Ctx, l: Link) -> Result<()> {
        self.need_change(&l.change_id, &format!("link {}", l.r#type))?;
        if !self.written.contains(&l.from) {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "link {}: version {} is not written by this transaction, a version never changes (ADR 0079)",
                    l.r#type, l.from
                ),
            ));
        }
        self.inner().put_link(ctx, l).await
    }
    async fn link(&mut self, ctx: &Ctx, id: &LinkID) -> Result<Link> {
        self.inner().link(ctx, id).await
    }
    async fn put_baseline(&mut self, ctx: &Ctx, b: Baseline) -> Result<()> {
        let stored = self.guarded_put_baseline(ctx, b).await?;
        if let Some(o) = &mut self.obs {
            o.baselines.push(stored);
        }
        Ok(())
    }
    async fn branch_joins(
        &mut self,
        ctx: &Ctx,
        namespace: &str,
        branch: &str,
        change: &ChangeID,
    ) -> Result<Vec<NodeRef>> {
        self.inner()
            .branch_joins(ctx, namespace, branch, change)
            .await
    }
    async fn materialize_baseline(
        &mut self,
        ctx: &Ctx,
        id: &BaselineID,
        nodes: &BTreeMap<NodeID, Version>,
    ) -> Result<()> {
        self.inner().materialize_baseline(ctx, id, nodes).await
    }
    async fn put_tag(&mut self, ctx: &Ctx, t: Tag) -> Result<()> {
        self.need_change(&t.change_id, &format!("tag {}", t.name))?;
        self.inner().put_tag(ctx, t).await
    }
    async fn delete_tag(&mut self, ctx: &Ctx, id: &TagID) -> Result<()> {
        self.inner().delete_tag(ctx, id).await
    }
    async fn tags(&mut self, ctx: &Ctx, f: &TagFilter) -> Result<Vec<Tag>> {
        self.inner().tags(ctx, f).await
    }

    async fn put_change(&mut self, ctx: &Ctx, c: Change) -> Result<()> {
        if c.owner_org.is_empty() || c.project_id.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "change {} names no owner unit or no project (ADR 0054)",
                    c.id
                ),
            ));
        }
        // a new sub-change is written with its parent's tree (it forks the parent's branch)
        if !c.parent_id.is_empty() {
            self.lock_change(ctx, &c.parent_id).await?;
        }
        self.lock_change(ctx, &c.id).await?;
        let mut h = c.clone();
        h.items = Vec::new();
        h.nodes = Vec::new();
        self.inner().put_change(ctx, c).await?;
        self.changes.insert(h.id.clone(), h.clone());
        self.headers.remove(&h.id);
        if let Some(o) = &mut self.obs {
            let id = h.id.clone();
            o.headers.insert(id.clone(), h);
            o.touch(&id);
            o.index(&id);
        }
        Ok(())
    }

    async fn append_log(&mut self, ctx: &Ctx, e: LogEntry) -> Result<LogEntry> {
        self.lock_change(ctx, &e.change).await?;
        self.logged.insert(e.change.clone());
        let e = self.inner().append_log(ctx, e).await?;
        if let Some(o) = &mut self.obs {
            o.touch(&e.change);
        }
        Ok(e)
    }
    async fn log(&mut self, ctx: &Ctx, f: &LogFilter) -> Result<Vec<LogEntry>> {
        self.inner().log(ctx, f).await
    }
    async fn log_counts(&mut self, ctx: &Ctx, f: &LogFilter) -> Result<BTreeMap<String, i64>> {
        self.inner().log_counts(ctx, f).await
    }
    async fn open_change_ids(&mut self, ctx: &Ctx) -> Result<Vec<ChangeID>> {
        self.inner().open_change_ids(ctx).await
    }

    async fn put_change_impact(
        &mut self,
        ctx: &Ctx,
        change: &ChangeID,
        cn: ChangeImpact,
    ) -> Result<()> {
        self.lock_change(ctx, change).await?;
        self.inner().put_change_impact(ctx, change, cn).await?;
        if let Some(o) = &mut self.obs {
            o.touch(change);
            o.index(change);
        }
        Ok(())
    }
    async fn delete_change_impact(
        &mut self,
        ctx: &Ctx,
        change: &ChangeID,
        id: &ChangeImpactID,
    ) -> Result<()> {
        self.lock_change(ctx, change).await?;
        self.inner().delete_change_impact(ctx, change, id).await
    }
    async fn change_impacts(&mut self, ctx: &Ctx, change: &ChangeID) -> Result<Vec<ChangeImpact>> {
        self.inner().change_impacts(ctx, change).await
    }
    async fn node_change_impacts(&mut self, ctx: &Ctx, node: &NodeID) -> Result<Vec<ChangeID>> {
        self.inner().node_change_impacts(ctx, node).await
    }
    async fn set_node_origin(
        &mut self,
        ctx: &Ctx,
        r: &NodeRef,
        change: &ChangeID,
        cn: &ChangeImpactID,
        comment: &str,
    ) -> Result<()> {
        self.need_change(change, &format!("the origin of node {r}"))?;
        self.inner()
            .set_node_origin(ctx, r, change, cn, comment)
            .await
    }
    async fn join_branch(
        &mut self,
        ctx: &Ctx,
        r: &NodeRef,
        branch: &str,
        change: &ChangeID,
    ) -> Result<()> {
        self.need_change(change, &format!("node {r} joining {branch}"))?;
        self.inner().join_branch(ctx, r, branch, change).await
    }

    async fn delete_change(
        &mut self,
        ctx: &Ctx,
        id: &ChangeID,
        namespace: &str,
        branch: &str,
    ) -> Result<()> {
        // the store checks that no baseline holds what the change wrote from the entries: every baseline of the
        // namespace must have its own (a baseline kept as a header only is materialised first)
        let bs = self.inner().baselines(ctx, namespace).await?;
        for b in bs {
            self.materialize(ctx, &b.id).await?;
        }
        self.lock_change(ctx, id).await?;
        self.inner()
            .delete_change(ctx, id, namespace, branch)
            .await?;
        self.changes.remove(id);
        self.headers.remove(id);
        if let Some(o) = &mut self.obs {
            o.indexed.retain(|x| x != id);
            if !o.purged.contains(id) {
                o.purged.push(id.clone());
            }
        }
        Ok(())
    }

    /// LockChange takes the lock of the tree of a change once per transaction (ADR 0127). Every write naming a change
    /// takes it (the log, the header, the projection of the impacts); an operation that reads before it writes takes
    /// it first, so that what it checked still holds when it writes.
    async fn lock_change(&mut self, ctx: &Ctx, id: &ChangeID) -> Result<()> {
        let key = format!("change:{id}");
        if id.is_empty() || self.locked.contains(&key) {
            return Ok(());
        }
        self.inner().lock_change(ctx, id).await?;
        self.lock_key(key);
        Ok(())
    }

    /// LockBranch takes the lock of a branch once per transaction (ADR 0127).
    async fn lock_branch(&mut self, ctx: &Ctx, namespace: &str, name: &str) -> Result<()> {
        let key = format!("branch:{}/{name}", namespace_of(namespace));
        if self.locked.contains(&key) {
            return Ok(());
        }
        self.inner().lock_branch(ctx, namespace, name).await?;
        self.lock_key(key);
        Ok(())
    }

    async fn commit(self: Box<Self>, ctx: &Ctx) -> Result<()> {
        GraphTx::commit(*self, ctx).await
    }

    async fn rollback(self: Box<Self>) -> Result<()> {
        GraphTx::rollback(*self).await
    }

    fn rolls_back_late(&self) -> bool {
        self.inner.as_ref().is_some_and(|t| t.rolls_back_late())
    }

    fn wrote_log(&self, id: &ChangeID) -> bool {
        self.logged.contains(id)
    }

    // rewriting a baseline's storage writes no node, link or change: straight to the store (Go's rewriterOf)
    async fn baseline_headers(&mut self, ctx: &Ctx) -> Result<Option<Vec<BaselineHeader>>> {
        self.inner().baseline_headers(ctx).await
    }
    async fn rewrite_baseline(
        &mut self,
        ctx: &Ctx,
        id: &BaselineID,
        depth: i64,
        entries: &[BaselineEntry],
    ) -> Result<()> {
        self.inner().rewrite_baseline(ctx, id, depth, entries).await
    }
}
