//! The reads of the current graph through a view (ADR 0157), read by node: what a read costs follows what it asks,
//! never the size of the namespace.
//!
//! A branch view is a baseline (the head of the branch when the view names none) whose state is a selection: the last
//! commit of each node on its branch up to its order, else, past the fork, on the branch it was forked from (ADR 0143).
//! A change view (its fork, its commits, its drafts folded from its log) is read here for one node at a time
//! ([`World::open_node`]), on the flow it names (else the active option): the impacts and drafts that flow sees; a
//! personal change answers [`crate::error::Kind::Unimplemented`].
use std::collections::{HashMap, HashSet};

use crate::{
    change::Change,
    error::{GraphError, Result},
    impact::{ChangeImpact, Draft, REVIEW_ACCEPTED, REVIEW_REJECTED},
    model::{
        BRANCH_OPEN, Baseline, BaselineId, Branch, Link, MAIN_BRANCH, MAX_BROWSE_LIMIT, Neighbourhood, Node, NodeId,
        NodePage, NodeQuery, NodeRef, NodeView, Target, Version, branch_of, namespace_of,
    },
    page::page_nodes,
    store::{REVERTED, ReadTx},
    view::View,
    write::{change::change_branch_origin, work::fold_change},
};

/// The level of a change view (Go's `ViewWritten`, `ViewAccepted`, `ViewCommitted`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ViewLevel {
    Written,
    Accepted,
    Committed,
}

impl ViewLevel {
    /// A level by its name, the written view when none is named.
    pub fn parse(s: &str) -> Result<ViewLevel> {
        match s {
            "" | "written" => Ok(ViewLevel::Written),
            "accepted" => Ok(ViewLevel::Accepted),
            "committed" => Ok(ViewLevel::Committed),
            _ => Err(GraphError::invalid(format!("unknown view level {s:?}"))),
        }
    }

    pub fn as_str(self) -> &'static str {
        match self {
            ViewLevel::Written => "written",
            ViewLevel::Accepted => "accepted",
            ViewLevel::Committed => "committed",
        }
    }
}

/// The whole state of a baseline (none for the empty state).
pub async fn whole_state<T: ReadTx>(tx: &T, h: &Baseline) -> Result<HashMap<NodeId, Version>> {
    if h.id.is_empty() {
        return Ok(HashMap::new());
    }
    select(tx, h, None).await
}

/// The number of nodes one statement selects the versions of.
pub const SELECT_BATCH: usize = 2000;

/// The empty state of a branch: no change committed in its namespace yet (ADR 0056).
fn empty_head(namespace: &str, branch: &str) -> Baseline {
    Baseline {
        namespace: namespace.into(),
        branch: branch.into(),
        ..Baseline::default()
    }
}

fn ephemeral(name: &str) -> GraphError {
    GraphError::invalid(format!(
        "branch {name} is the branch of a change: it holds no baseline, its state is its fork with what committed on it"
    ))
}

/// The header of a baseline; the empty id is the empty state.
pub async fn header_of<T: ReadTx>(tx: &T, id: &BaselineId) -> Result<Baseline> {
    if id.is_empty() {
        return Ok(Baseline::default());
    }
    tx.baseline_header(id)
        .await?
        .ok_or_else(|| GraphError::not_found(format!("baseline {id}")))
}

/// The head of a branch without its state: the baseline its last commit left, else, for main, the last baseline a
/// change left on it, else the empty state (empty id).
pub async fn branch_head<T: ReadTx>(tx: &T, namespace: &str, name: &str) -> Result<Baseline> {
    let (ns, name) = (namespace_of(namespace), branch_of(name));
    let b = tx.branch(ns, name).await?;
    match &b {
        Some(b) if b.ephemeral => return Err(ephemeral(&b.name)),
        Some(b) if !b.head.is_empty() => return header_of(tx, &b.head).await,
        None if name != MAIN_BRANCH => return Err(GraphError::not_found(format!("branch {name}"))),
        _ => {}
    }
    let headers = tx.baseline_headers(ns).await?;
    Ok(headers
        .into_iter()
        .rev()
        .find(|h| branch_of(&h.branch) == name)
        .unwrap_or_else(|| empty_head(ns, name)))
}

/// A branch of a namespace; main exists implicitly, open at its head, before any change made it a row.
pub async fn branch<T: ReadTx>(tx: &T, namespace: &str, name: &str) -> Result<Branch> {
    let (ns, name) = (namespace_of(namespace), branch_of(name));
    match tx.branch(ns, name).await? {
        Some(b) => Ok(b),
        None if name == MAIN_BRANCH => {
            let head = branch_head(tx, ns, name).await?;
            Ok(Branch {
                name: name.into(),
                namespace: ns.into(),
                head: head.id,
                status: BRANCH_OPEN.into(),
                ..Branch::default()
            })
        }
        None => Err(GraphError::not_found(format!("branch {name}"))),
    }
}

/// The baseline a lasting branch was forked from: none for main or a branch the store does not know.
async fn branch_fork<T: ReadTx>(tx: &T, namespace: &str, branch: &str) -> Result<BaselineId> {
    if branch_of(branch) == MAIN_BRANCH {
        return Ok(BaselineId::default());
    }
    Ok(tx
        .branch(namespace_of(namespace), branch_of(branch))
        .await?
        .map(|b| b.fork_baseline)
        .unwrap_or_default())
}

/// The versions some nodes (None: every node) have in a baseline (ADR 0143), the nodes it does not hold left out:
/// the last commit of each on its branch up to its order, then, for those not decided, on the branch it was forked
/// from at the fork, and so on. A whole state reads the node list of the namespace at the root of the chain, then
/// selects it in batches: its cost is the number of nodes, never the length of the history.
pub async fn select<T: ReadTx>(tx: &T, header: &Baseline, ids: Option<&[NodeId]>) -> Result<HashMap<NodeId, Version>> {
    if header.id.is_empty() {
        return Ok(HashMap::new());
    }
    let mut decided: HashMap<NodeId, Version> = HashMap::new(); // 0: the baseline does not hold the node
    let mut pending: Option<HashSet<NodeId>> = ids.map(|ids| ids.iter().cloned().collect());
    let mut b = header.clone();
    loop {
        let next = branch_fork(tx, &b.namespace, &b.branch).await?;
        if pending.is_none() && next.is_empty() {
            let all = tx.namespace_node_ids(&b.namespace).await?;
            pending = Some(all.into_iter().filter(|n| !decided.contains_key(n)).collect());
        }
        if pending.as_ref().is_some_and(|p| p.is_empty()) {
            break;
        }
        match &mut pending {
            None => {
                for (n, v) in tx.committed_versions(&b.namespace, &b.branch, 0, b.seq, None).await? {
                    decided.entry(n).or_insert(v);
                }
            }
            Some(p) => {
                let mut want: Vec<NodeId> = p.iter().cloned().collect();
                want.sort();
                for batch in want.chunks(SELECT_BATCH) {
                    for (n, v) in tx
                        .committed_versions(&b.namespace, &b.branch, 0, b.seq, Some(batch))
                        .await?
                    {
                        if p.remove(&n) {
                            decided.insert(n, v);
                        }
                    }
                }
            }
        }
        if next.is_empty() {
            break;
        }
        b = header_of(tx, &next).await?;
    }
    decided.retain(|_, v| *v > 0);
    Ok(decided)
}

/// A view opened for a read.
pub struct World<'a, T> {
    tx: &'a T,
    view: View,
    /// The baseline the view reads (empty: the empty state); for a change view, its fork.
    pub baseline: Baseline,
    pub namespace: String,
    /// What a change view lays over its fork.
    change: Option<ChangeWorld>,
}

/// What a change view adds to its fork (Go's `world` of a change): the change, the fold of its log, its own branch,
/// the versions it wrote over its fork (a draft is version 0) and the nodes it deleted.
struct ChangeWorld {
    change: Change,
    /// the impacts the view's flow sees, and the draft it sees for each
    impacts: Vec<ChangeImpact>,
    seen: HashMap<String, Draft>,
    own: Option<Branch>,
    over: HashMap<NodeId, Version>,
    gone: HashSet<NodeId>,
}

impl ChangeWorld {
    /// The draft the view holds for a node: the one of the last impact of the node that has one (Go's
    /// `draftReader.get`).
    fn draft(&self, id: &NodeId) -> Option<&Draft> {
        self.impacts
            .iter()
            .filter_map(|cn| self.seen.get(&cn.id))
            .rfind(|d| &d.node == id)
    }

    /// The drafts of the view, one per node, by key.
    fn drafts(&self) -> Vec<&Draft> {
        let mut by: HashMap<&NodeId, &Draft> = HashMap::new();
        for cn in &self.impacts {
            if let Some(d) = self.seen.get(&cn.id) {
                by.insert(&d.node, d);
            }
        }
        let mut out: Vec<&Draft> = by.into_values().collect();
        out.sort_by(|a, b| a.key.cmp(&b.key));
        out
    }

    /// A reference to the version a node was checked out from, pointed at the draft of the node.
    fn normalize(&self, r: &NodeRef) -> NodeRef {
        match self.draft(&r.id) {
            Some(d) if d.base.as_ref() == Some(r) => d.draft_ref(),
            _ => r.clone(),
        }
    }

    /// A draft as a node (Go's `draftNode`).
    fn draft_node(&self, d: &Draft) -> Node {
        Node {
            id: d.node.clone(),
            version: 0,
            namespace: namespace_of(&self.change.namespace).to_string(),
            key: d.key.clone(),
            typ: d.typ.clone(),
            props: d.props.clone(),
            state: d.state.clone(),
            owner: d.owner.clone(),
            origins: d.origins.clone(),
            change_id: self.change.id.clone(),
            change_impact: d.impact.clone(),
            execution: d.execution.clone(),
            reason: d.reason().to_string(),
            branch: if d.flow.is_empty() {
                branch_of(&self.change.branch).to_string()
            } else {
                crate::write::work::flow_branch(&d.flow)
            },
            ..Node::default()
        }
    }

    /// The outgoing links of a draft as links of a node view.
    fn out_links(&self, d: &Draft) -> Vec<Link> {
        d.links
            .iter()
            .map(|l| Link {
                id: l.id.clone(),
                typ: l.typ.clone(),
                from: d.draft_ref(),
                to: if l.is_external() {
                    Target::External(l.target.clone())
                } else {
                    Target::Node(self.normalize(&l.to))
                },
                props: l.props.clone(),
                change_id: self.change.id.clone(),
            })
            .collect()
    }
}

/// The versions some nodes have on the branch of a change: its fork, then what committed on it (Go's
/// `branchVersions`).
pub async fn branch_versions<T: ReadTx>(tx: &T, own: &Branch, ids: &[NodeId]) -> Result<HashMap<NodeId, Version>> {
    chain_versions(tx, own, Some(ids), true).await
}

/// The whole state of the branch of a change.
pub async fn branch_state<T: ReadTx>(tx: &T, own: &Branch) -> Result<HashMap<NodeId, Version>> {
    chain_versions(tx, own, None, true).await
}

/// The whole state the branch of a change was forked from.
pub async fn fork_state<T: ReadTx>(tx: &T, own: &Branch) -> Result<HashMap<NodeId, Version>> {
    chain_versions(tx, own, None, false).await
}

/// The versions some nodes have in the fork of the branch of a change (Go's `forkVersions`).
pub async fn fork_versions<T: ReadTx>(tx: &T, own: &Branch, ids: &[NodeId]) -> Result<HashMap<NodeId, Version>> {
    chain_versions(tx, own, Some(ids), false).await
}

/// The state of the branch of a change, or of its fork (Go's `chainVersions`, ADR 0103, 0140): the lasting baseline
/// under the chain of the branches it is forked from, then the commits of each branch of the chain, outermost first,
/// each up to the order the branch under it was forked at.
async fn chain_versions<T: ReadTx>(
    tx: &T,
    b: &Branch,
    ids: Option<&[NodeId]>,
    own: bool,
) -> Result<HashMap<NodeId, Version>> {
    let mut layers: Vec<(String, i64)> = Vec::new(); // the innermost first
    let mut up_to = i64::MAX;
    if own {
        layers.push((b.name.clone(), up_to));
    }
    let mut cur = b.clone();
    let mut hops = 0;
    while cur.fork_seq > 0 {
        hops += 1;
        if hops > 64 {
            return Err(GraphError::invalid(format!(
                "branch {}: the chain of the branches it is forked from does not end",
                b.name
            )));
        }
        up_to = up_to.min(cur.fork_seq);
        let p = branch(tx, &cur.namespace, &cur.parent).await?;
        layers.push((p.name.clone(), up_to));
        cur = p;
    }
    let fork = header_of(tx, &b.fork_baseline).await?;
    let mut out = match ids {
        Some(ids) => select(tx, &fork, Some(ids)).await?,
        None => whole_state(tx, &fork).await?,
    };
    for (name, up_to) in layers.iter().rev() {
        lay_commits(
            &mut out,
            tx.committed_versions(&b.namespace, name, 0, *up_to, ids).await?,
        );
    }
    Ok(out)
}

/// Lays the commits of the branch of a change over its fork: a deletion removes the node, a revert gives it back the
/// version of the fork (Go's `chainVersions`).
fn lay_commits(state: &mut HashMap<NodeId, Version>, commits: HashMap<NodeId, Version>) {
    for (n, v) in commits {
        match v {
            0 => {
                state.remove(&n);
            }
            REVERTED => {}
            v => {
                state.insert(n, v);
            }
        }
    }
}

impl<'a, T: ReadTx> World<'a, T> {
    /// Opens a view: a branch at a baseline of it, else at its head.
    pub async fn open(tx: &'a T, view: &View) -> Result<World<'a, T>> {
        view.check()?;
        if view.is_change() {
            return Err(GraphError::unimplemented(format!(
                "{view}: the views of a change are read by the Go service"
            )));
        }
        let namespace = namespace_of(&view.namespace).to_string();
        let baseline = if view.baseline.is_empty() {
            branch_head(tx, &namespace, &view.branch).await?
        } else {
            let h = header_of(tx, &view.baseline).await?;
            if h.namespace != namespace || h.branch != view.branch {
                return Err(GraphError::invalid(format!(
                    "baseline {} is of {}/{}, not of {view}",
                    view.baseline, h.namespace, h.branch
                )));
            }
            h
        };
        Ok(World {
            tx,
            view: view.clone(),
            baseline,
            namespace,
            change: None,
        })
    }

    /// Opens a view to read a node in, a change view included (Go's `openWorld`): the change's fork, what it committed
    /// on its branch, its drafts laid over them.
    pub async fn open_node(tx: &'a T, view: &View) -> Result<World<'a, T>> {
        if !view.is_change() {
            return World::open(tx, view).await;
        }
        view.check()?;
        let c = tx
            .change(&view.change)
            .await?
            .ok_or_else(|| GraphError::not_found(format!("change {}", view.change)))?;
        if c.is_personal() {
            return Err(GraphError::unimplemented(
                "a view of a personal change (ADR 0037): not served here yet",
            ));
        }
        let cv = fold_change(tx, &c.id).await?;
        let flow = cv.flows.resolve(&view.flow);
        let impacts = cv.seen(&flow);
        let mut seen = cv.seen_drafts(&flow);
        seen.retain(|id, _| impacts.iter().any(|cn| &cn.id == id));
        let mut over = HashMap::new();
        let mut gone = HashSet::new();
        for cn in &impacts {
            let Some(post) = &cn.post else { continue };
            if cn.review == REVIEW_REJECTED {
                continue;
            }
            if post.is_draft() {
                over.insert(post.id.clone(), 0);
                continue;
            }
            let n = stored_node(tx, post).await?;
            if n.deleted {
                gone.insert(n.id);
            } else {
                over.insert(n.id, n.version);
            }
        }
        let namespace = namespace_of(&c.namespace).to_string();
        let mut own = None;
        if branch_of(&c.branch) != MAIN_BRANCH {
            let b = branch(tx, &c.namespace, &c.branch).await?;
            if b.origin == change_branch_origin(&c.id) {
                own = Some(b);
            }
        }
        let baseline = header_of(tx, &c.baseline_id).await?;
        Ok(World {
            tx,
            view: view.clone(),
            baseline,
            namespace,
            change: Some(ChangeWorld {
                change: c,
                impacts,
                seen,
                own,
                over,
                gone,
            }),
        })
    }

    /// The versions some nodes have in the view (absent: not in it).
    pub async fn at(&self, ids: &[NodeId]) -> Result<HashMap<NodeId, Version>> {
        if let Some(cw) = &self.change {
            let mut at = match &cw.own {
                Some(own) => branch_versions(self.tx, own, ids).await?,
                None if self.baseline.id.is_empty() => HashMap::new(),
                None => select(self.tx, &self.baseline, Some(ids)).await?,
            };
            for id in ids {
                if let Some(&v) = cw.over.get(id) {
                    at.insert(id.clone(), v);
                } else if cw.gone.contains(id) {
                    at.remove(id);
                }
            }
            return Ok(at);
        }
        if self.baseline.id.is_empty() || ids.is_empty() {
            return Ok(HashMap::new());
        }
        select(self.tx, &self.baseline, Some(ids)).await
    }

    async fn node(&self, r: &NodeRef) -> Result<Node> {
        stored_node(self.tx, r).await
    }

    /// Finds a node of the view by id or, when the id is empty, by key: its version in the view.
    pub async fn find(&self, id: &NodeId, key: &str) -> Result<NodeRef> {
        let drafted = match (&self.change, id.is_empty()) {
            (Some(cw), true) => cw.drafts().into_iter().rfind(|d| d.key == key).map(|d| d.node.clone()),
            _ => None,
        };
        let id = if let Some(id) = drafted {
            id
        } else if id.is_empty() {
            let id = self
                .tx
                .node_id_by_key(&self.namespace, key)
                .await?
                .ok_or_else(|| GraphError::not_found(format!("node key {key}")))?;
            // the Go store reads a node by key as its latest version on main: a node main never held is not found
            if self.tx.latest_on(&id, MAIN_BRANCH).await?.is_none() {
                return Err(GraphError::not_found(format!("node {id} on {MAIN_BRANCH}")));
            }
            id
        } else {
            id.clone()
        };
        match self.at(std::slice::from_ref(&id)).await?.get(&id) {
            Some(&v) => Ok(NodeRef::new(id, v)),
            None => {
                let what = if key.is_empty() {
                    id.to_string()
                } else {
                    key.to_string()
                };
                Err(GraphError::not_found(format!("node {what} in {}", self.view)))
            }
        }
    }

    /// The stored links entering a node from the versions of their sources the view holds: the sources first, then
    /// the links their versions hold into the node, so a source with a long history costs one version.
    pub async fn entering(&self, id: &NodeId) -> Result<Vec<Link>> {
        let mut sources = self.tx.link_sources(id).await?;
        if sources.is_empty() {
            return Ok(Vec::new());
        }
        sources.sort();
        let at = self.at(&sources).await?;
        let refs: Vec<NodeRef> = sources
            .into_iter()
            .filter_map(|s| at.get(&s).filter(|&&v| v != 0).map(|&v| NodeRef::new(s, v)))
            .collect();
        if refs.is_empty() {
            return Ok(Vec::new());
        }
        self.tx.links_from_into(&refs, id).await
    }

    /// A node as the view holds it, with its links (ADR 0157).
    pub async fn node_view(&self, id: &NodeId, key: &str) -> Result<NodeView> {
        let r = self.find(id, key).await?;
        if let Some(cw) = &self.change {
            return self.change_node_view(cw, r).await;
        }
        let entering = self.entering(&r.id).await?;
        let r#in = entering.into_iter().filter(|l| l.to_node() == Some(&r)).collect();
        let node = self.node(&r).await?;
        let latest = latest_of(self.tx, &node).await?;
        let out = self.tx.links_from(std::slice::from_ref(&r), None).await?;
        Ok(NodeView {
            node,
            latest,
            out,
            r#in,
        })
    }

    /// A node as a change view holds it (Go's `viewIn` over the draft reader): its draft, or its stored version, with
    /// the links of the draft or of the version, and the links entering it from the versions and the drafts of the view.
    async fn change_node_view(&self, cw: &ChangeWorld, r: NodeRef) -> Result<NodeView> {
        let d = cw.draft(&r.id).filter(|_| r.is_draft());
        let node = match d {
            Some(d) => cw.draft_node(d),
            None => self.node(&r).await?,
        };
        let mut latest = self.tx.latest_on(&r.id, MAIN_BRANCH).await?.map_or(0, |n| n.version);
        if latest == 0 {
            latest = node.version;
        }
        let out = match d {
            Some(d) => cw.out_links(d),
            None => self.tx.links_from(std::slice::from_ref(&r), None).await?,
        };
        let mut r#in = Vec::new();
        let from = match d.and_then(|d| d.base.clone()) {
            Some(base) => base,
            None => r.clone(),
        };
        if !from.is_draft() {
            for l in self.entering(&from.id).await? {
                if l.to_node() != Some(&from) {
                    continue;
                }
                // a link of a version a draft replaces is the draft's now
                if !l.from.is_draft() && cw.draft(&l.from.id).is_some_and(|d| d.base.as_ref() == Some(&l.from)) {
                    continue;
                }
                r#in.push(l);
            }
        }
        for o in cw.drafts() {
            for l in cw.out_links(o) {
                let Target::Node(to) = &l.to else { continue };
                if to.id == r.id && r.is_draft() == to.is_draft() && (r.is_draft() || *to == r) {
                    r#in.push(l);
                }
            }
        }
        Ok(NodeView {
            node,
            latest,
            out,
            r#in,
        })
    }

    /// The nodes a change view's blackboard holds (Go's `BlackboardIn` over its draft reader): every version its
    /// impacts name (pre, post, committed), with their links, and the nodes at the other ends, read in one batch. A
    /// branch view holds none.
    pub async fn blackboard(&self) -> Result<(Vec<NodeView>, Vec<Node>)> {
        let Some(cw) = &self.change else {
            return Ok((Vec::new(), Vec::new()));
        };
        let mut refs: Vec<NodeRef> = Vec::new();
        for cn in &cw.impacts {
            for r in [&cn.pre, &cn.post, &cn.committed].into_iter().flatten() {
                if !r.id.is_empty() && !refs.contains(r) {
                    refs.push(r.clone());
                }
            }
        }
        let mut views = Vec::with_capacity(refs.len());
        let mut near: Vec<NodeRef> = Vec::new();
        for r in refs {
            let v = self.reader_view(cw, &r).await?;
            near.extend(v.out.iter().filter_map(|l| l.to_node().cloned()));
            near.extend(v.r#in.iter().map(|l| l.from.clone()));
            views.push(v);
        }
        let mut neighbours: Vec<Node> = Vec::new();
        let mut stored: Vec<NodeRef> = Vec::new();
        let mut seen = HashSet::new();
        for r in near {
            if !seen.insert(r.clone()) {
                continue;
            }
            if r.is_draft() {
                match cw.draft(&r.id) {
                    Some(d) => neighbours.push(cw.draft_node(d)),
                    None => neighbours.push(stored_node(self.tx, &r).await?),
                }
            } else {
                stored.push(r);
            }
        }
        if !stored.is_empty() {
            neighbours.extend(self.tx.nodes_at(&stored, "").await?);
        }
        Ok((views, neighbours))
    }

    /// A node as the draft reader of a change sees it (Go's `viewIn`): the draft of a draft reference, else the stored
    /// version, the stored links entering the version (one per version of their source holding them; a link of a
    /// version a draft replaces is the draft's) and the links the drafts hold to it.
    async fn reader_view(&self, cw: &ChangeWorld, r: &NodeRef) -> Result<NodeView> {
        let d = cw.draft(&r.id).filter(|_| r.is_draft());
        let node = match d {
            Some(d) => cw.draft_node(d),
            None => self.node(r).await?,
        };
        let latest = latest_of(self.tx, &node).await?;
        let out = match d {
            Some(d) => cw.out_links(d),
            None => self.tx.links_from(std::slice::from_ref(r), None).await?,
        };
        let mut r#in = Vec::new();
        let from = d.and_then(|d| d.base.clone()).unwrap_or_else(|| r.clone());
        if !from.is_draft() {
            for l in self.tx.links_to(&from).await? {
                if !l.from.is_draft() && cw.draft(&l.from.id).is_some_and(|d| d.base.as_ref() == Some(&l.from)) {
                    continue;
                }
                r#in.push(l);
            }
        }
        for o in cw.drafts() {
            for l in cw.out_links(o) {
                let Target::Node(to) = &l.to else { continue };
                if to.id == r.id && r.is_draft() == to.is_draft() && (r.is_draft() || to == r) {
                    r#in.push(l);
                }
            }
        }
        Ok(NodeView {
            node,
            latest,
            out,
            r#in,
        })
    }

    /// The state of a change view at a level (Go's `ChangeView`): its own branch (its fork with what committed on it,
    /// ADR 0140) or the baseline it forked from, the posts of its impacts laid over it (a draft is version 0; a
    /// rejected impact left out, every one not accepted at `accepted`); `committed` is the baseline the change left,
    /// else its fork. The header names the view.
    pub async fn change_state(&self, level: ViewLevel) -> Result<(Baseline, HashMap<NodeId, Version>)> {
        let Some(cw) = &self.change else {
            return Err(GraphError::internal("a change state of a branch view"));
        };
        let c = &cw.change;
        if level == ViewLevel::Committed {
            let from = if c.status == "applied" && !c.result_baseline_id.is_empty() {
                &c.result_baseline_id
            } else {
                &c.baseline_id
            };
            let h = header_of(self.tx, from).await?;
            let state = whole_state(self.tx, &h).await?;
            return Ok((h, state));
        }
        let (base, mut nodes) = match &cw.own {
            Some(own) => {
                let fork = header_of(self.tx, &own.fork_baseline).await?;
                (fork, branch_state(self.tx, own).await?)
            }
            None => (self.baseline.clone(), whole_state(self.tx, &self.baseline).await?),
        };
        for cn in &cw.impacts {
            let Some(post) = &cn.post else { continue };
            if cn.review == REVIEW_REJECTED || (level == ViewLevel::Accepted && cn.review != REVIEW_ACCEPTED) {
                continue;
            }
            if post.is_draft() {
                nodes.insert(post.id.clone(), 0);
                continue;
            }
            let n = stored_node(self.tx, post).await?;
            if n.deleted {
                nodes.remove(&n.id);
            } else {
                nodes.insert(n.id, n.version);
            }
        }
        let header = Baseline {
            name: format!("{} ({})", c.title, level.as_str()),
            namespace: base.namespace.clone(),
            branch: c.branch.clone(),
            parent_id: base.id.clone(),
            change_id: c.id.clone(),
            created_at: base.created_at,
            ..Baseline::default()
        };
        Ok((header, nodes))
    }

    /// The graph of the written view of a change (Go's `viewGraphTx`): its nodes by key, the drafts read through the
    /// change, and the links between them, to the exact version of the view (`by_node`: to the node at any version).
    pub async fn change_graph(&self, by_node: bool) -> Result<(Vec<Node>, Vec<Link>)> {
        let Some(cw) = &self.change else {
            return Err(GraphError::internal("a change graph of a branch view"));
        };
        let (view, state) = self.change_state(ViewLevel::Written).await?;
        let contains = |r: &NodeRef| state.get(&r.id) == Some(&r.version);
        let parent = header_of(self.tx, &view.parent_id).await?;
        let mut refs: Vec<NodeRef> = whole_state(self.tx, &parent)
            .await?
            .into_iter()
            .map(|(n, v)| NodeRef::new(n, v))
            .filter(contains)
            .collect();
        refs.sort();
        let mut nodes = if refs.is_empty() {
            Vec::new()
        } else {
            self.tx.nodes_at(&refs, "").await?
        };
        let held: HashSet<&NodeRef> = refs.iter().collect();
        for (id, &v) in &state {
            let r = NodeRef::new(id.clone(), v);
            if held.contains(&r) {
                continue;
            }
            nodes.push(match cw.draft(&r.id).filter(|_| r.is_draft()) {
                Some(d) => cw.draft_node(d),
                None => self.node(&r).await?,
            });
        }
        nodes.sort_by(|a, b| a.key.cmp(&b.key));
        let mut links = Vec::new();
        for n in &nodes {
            let r = n.node_ref();
            let out = match cw.draft(&r.id).filter(|_| r.is_draft()) {
                Some(d) => cw.out_links(d),
                None => self.tx.links_from(std::slice::from_ref(&r), None).await?,
            };
            for l in out {
                let Target::Node(to) = &l.to else { continue };
                if (by_node && state.contains_key(&to.id)) || to.is_draft() || contains(to) {
                    links.push(l);
                }
            }
        }
        Ok((nodes, links))
    }

    /// The link types asked as the store holds them: as written, and an unqualified one also qualified by the
    /// namespace of the view (None stays None: every type).
    pub fn link_types(&self, types: Option<&[String]>) -> Option<Vec<String>> {
        let mut out: Vec<String> = Vec::new();
        for t in types? {
            out.push(t.clone());
            if !t.contains('@') {
                out.push(format!("{}@{t}", self.namespace));
            }
        }
        out.sort();
        out.dedup();
        Some(out)
    }

    /// A node with its direct neighbours: the nodes and the links between them, the neighbours read in one batch
    /// (ADR 0137). A link is kept when its other end is in the view, at whatever version it names; it is suspect when
    /// it names a version of its target the view does not hold. `types` keeps the links of some types (None: all).
    pub async fn neighbourhood(&self, id: &NodeId, types: Option<&[String]>) -> Result<Neighbourhood> {
        let Some(&v) = self.at(std::slice::from_ref(id)).await?.get(id) else {
            return Err(GraphError::not_found(format!("node {id} in {}", self.view)));
        };
        let r = NodeRef::new(id.clone(), v);
        let node = self.node(&r).await?;
        let keep = |l: &Link| types.is_none_or(|ts| ts.contains(&l.typ));
        let mut out = match types {
            Some([]) => Vec::new(),
            _ => self.tx.links_from(std::slice::from_ref(&r), types).await?,
        };
        let mut r#in = match types {
            Some([]) => Vec::new(),
            _ => self.entering(id).await?,
        };
        out.retain(keep);
        r#in.retain(keep);

        let mut ends: Vec<NodeId> = out
            .iter()
            .filter_map(|l| l.to_node().map(|t| t.id.clone()))
            .chain(r#in.iter().map(|l| l.from.id.clone()))
            .collect();
        ends.sort();
        ends.dedup();
        let at = self.at(&ends).await?;

        // the neighbours are read in one batch, never one by one: a hub has thousands
        let mut refs: Vec<NodeRef> = Vec::new();
        for l in &out {
            if let Some(t) = l.to_node() {
                match at.get(&t.id) {
                    Some(&tv) => refs.push(NodeRef::new(t.id.clone(), tv)),
                    None if !t.is_draft() => refs.push(t.clone()),
                    None => {}
                }
            }
        }
        for l in &r#in {
            if at.get(&l.from.id) == Some(&l.from.version) && l.from.id != *id {
                refs.push(l.from.clone());
            }
        }
        let mut near: HashMap<NodeRef, Node> = self
            .tx
            .nodes_at(&refs, "")
            .await?
            .into_iter()
            .map(|n| (n.node_ref(), n))
            .collect();

        let mut nb = Neighbourhood {
            node,
            ..Neighbourhood::default()
        };
        let mut seen: HashSet<NodeId> = HashSet::from([id.clone()]);
        for l in out {
            let Target::Node(to) = &l.to else {
                nb.links.push(l);
                continue;
            };
            let mut to = to.clone();
            let mut suspect = false;
            match at.get(&to.id) {
                Some(&tv) => {
                    suspect = tv != to.version && tv != 0 && to.version != 0;
                    to.version = tv;
                }
                None if to.is_draft() => continue, // a draft the change no longer holds
                None => {}
            }
            self.meet(&mut nb, &mut seen, &mut near, &to).await?;
            if suspect {
                nb.suspect.push(l.id.clone());
            }
            nb.links.push(l);
        }
        r#in.sort_by_key(|l| l.to_node().map_or(0, |t| t.version));
        let mut taken = HashSet::new();
        for l in r#in {
            let to_version = l.to_node().map_or(0, |t| t.version);
            if l.from.id == *id || taken.contains(&l.id) {
                continue; // a self link is already among the outgoing ones
            }
            if v != 0 && to_version > v {
                continue; // a link into a later version of the node than the view holds
            }
            if at.get(&l.from.id) != Some(&l.from.version) {
                continue; // not the version of its source the view holds
            }
            self.meet(&mut nb, &mut seen, &mut near, &l.from).await?;
            if to_version != 0 && v != 0 && to_version != v {
                nb.suspect.push(l.id.clone());
            }
            taken.insert(l.id.clone());
            nb.links.push(l);
        }
        Ok(nb)
    }

    /// Adds a neighbour once, read from the batch or, when the batch missed it, alone.
    async fn meet(
        &self,
        nb: &mut Neighbourhood,
        seen: &mut HashSet<NodeId>,
        near: &mut HashMap<NodeRef, Node>,
        r: &NodeRef,
    ) -> Result<()> {
        if seen.contains(&r.id) {
            return Ok(());
        }
        let n = match near.remove(r) {
            Some(n) => n,
            None => self.node(r).await?,
        };
        seen.insert(r.id.clone());
        nb.nodes.push(n);
        Ok(())
    }

    /// A page of the nodes of the view ordered by key, matching `q`: the page of its baseline, or the nodes the
    /// impacts of a change act on as the baseline holds them.
    pub async fn page(&self, q: &NodeQuery) -> Result<NodePage> {
        let mut page = if !q.change.is_empty() {
            page_nodes(self.change_nodes(q).await?, q)
        } else if self.baseline.id.is_empty() {
            NodePage::default()
        } else {
            let state = select(self.tx, &self.baseline, None).await?;
            if state.is_empty() {
                NodePage::default()
            } else {
                let mut refs: Vec<NodeRef> = state.into_iter().map(|(n, v)| NodeRef::new(n, v)).collect();
                refs.sort();
                self.tx.node_page(&refs, q).await?
            }
        };
        page.baseline = self.baseline.id.clone();
        Ok(page)
    }

    /// The nodes the impacts of a change act on, as the baseline holds them, by key (ADR 0137): never the whole state.
    async fn change_nodes(&self, q: &NodeQuery) -> Result<Vec<Node>> {
        let mut ids = self
            .tx
            .change_nodes(&q.change)
            .await?
            .ok_or_else(|| GraphError::not_found(format!("change {}", q.change)))?;
        ids.sort();
        ids.dedup();
        let refs: Vec<NodeRef> = self
            .at(&ids)
            .await?
            .into_iter()
            .map(|(n, v)| NodeRef::new(n, v))
            .collect();
        if refs.is_empty() {
            return Ok(Vec::new());
        }
        let mut nodes = self.tx.nodes_at(&refs, "").await?;
        nodes.sort_by(|a, b| a.key.cmp(&b.key));
        Ok(nodes)
    }
}

/// A stored version of a node.
pub async fn stored_node<T: ReadTx>(tx: &T, r: &NodeRef) -> Result<Node> {
    let found = if r.is_draft() {
        tx.latest_on(&r.id, MAIN_BRANCH).await?
    } else {
        tx.node(r).await?
    };
    found.ok_or_else(|| GraphError::not_found(format!("node {r}")))
}

/// The latest version of a node on main, the node's own when main has none (a node a change created).
async fn latest_of<T: ReadTx>(tx: &T, n: &Node) -> Result<Version> {
    Ok(tx.latest_on(&n.id, MAIN_BRANCH).await?.map_or(n.version, |l| l.version))
}

/// A version of a node with its links, read as its history holds it (no view: GetNode naming a version).
pub async fn version_view<T: ReadTx>(tx: &T, r: &NodeRef) -> Result<NodeView> {
    let node = stored_node(tx, r).await?;
    let latest = latest_of(tx, &node).await?;
    let own = node.node_ref();
    let out = tx.links_from(std::slice::from_ref(&own), None).await?;
    let r#in = tx.links_to(&own).await?;
    Ok(NodeView {
        node,
        latest,
        out,
        r#in,
    })
}

/// A page of the history of a node (ADR 0140): its last `limit` versions (0: 50, at most 500) below `before` (0: up
/// to its latest), by version, and the `before` of the next older page (0: none).
pub async fn version_page<T: ReadTx>(
    tx: &T,
    id: &NodeId,
    before: Version,
    limit: usize,
) -> Result<(Vec<Node>, Version)> {
    let limit = if limit == 0 { 50 } else { limit.min(MAX_BROWSE_LIMIT) };
    let vs = tx.version_range(id, before, limit).await?;
    if vs.is_empty() && before == 0 {
        return Err(GraphError::not_found(format!("node {id}")));
    }
    let next = vs.first().map_or(0, |n| if n.version > 1 { n.version } else { 0 });
    Ok((vs, next))
}

/// The size of a page of the nodes of a view: at most [`MAX_BROWSE_LIMIT`], the largest when none is asked.
pub fn view_page_limit(limit: i64) -> usize {
    if limit <= 0 || limit as usize > MAX_BROWSE_LIMIT {
        MAX_BROWSE_LIMIT
    } else {
        limit as usize
    }
}

/// A page of the history of a branch (Go's `BaselineHistory`): its baselines, oldest first, and the baselines they
/// relate to.
#[derive(Debug, Clone, Default)]
pub struct BaselineHistory {
    pub baselines: Vec<Baseline>,
    /// the other baselines of the changes of the page, those of the open branches forked from this one (first page
    /// only), and the parents, merge and rebase sources not listed otherwise
    pub related: Vec<Baseline>,
    /// the order to ask the next page before, 0 when none remains
    pub next: i64,
}

/// The history of a branch, newest commits first by pages of limit, before the order before (0: the newest).
pub async fn baseline_history<T: ReadTx>(
    tx: &T,
    namespace: &str,
    branch: &str,
    before: i64,
    limit: i64,
) -> Result<BaselineHistory> {
    let limit = if limit <= 0 || limit > MAX_BROWSE_LIMIT as i64 {
        100
    } else {
        limit
    };
    let branch = branch_of(branch);
    let mut h = BaselineHistory::default();
    let mut page = tx.baseline_page(namespace, branch, before, limit + 1).await?;
    if page.len() as i64 > limit {
        page.truncate(limit as usize);
        h.next = page[limit as usize - 1].seq;
    }
    page.reverse();
    let mut seen: HashSet<BaselineId> = page.iter().map(|b| b.id.clone()).collect();
    let mut changes: Vec<_> = page
        .iter()
        .filter(|b| !b.change_id.is_empty())
        .map(|b| b.change_id.clone())
        .collect();
    h.baselines = page;
    let add = |h: &mut BaselineHistory, seen: &mut HashSet<BaselineId>, bs: Vec<Baseline>| {
        for b in bs {
            if seen.insert(b.id.clone()) {
                h.related.push(b);
            }
        }
    };
    changes.sort();
    changes.dedup();
    let related = tx.change_baseline_headers(namespace, &changes).await?;
    add(&mut h, &mut seen, related);
    if before == 0 {
        for br in tx.branches(namespace).await? {
            if br.status != BRANCH_OPEN || br.name == branch || branch_of(&br.parent) != branch {
                continue;
            }
            let bs = tx.branch_baseline_headers(namespace, &br.name).await?;
            add(&mut h, &mut seen, bs);
        }
    }
    let mut reach: Vec<BaselineId> = Vec::new();
    for b in h.baselines.iter().chain(&h.related) {
        for id in [&b.parent_id, &b.merged_from, &b.rebased_from] {
            if !id.is_empty() && !seen.contains(id) {
                reach.push(id.clone());
            }
        }
    }
    reach.sort();
    reach.dedup();
    let ghosts = tx.baseline_headers_of(namespace, &reach).await?;
    add(&mut h, &mut seen, ghosts);
    for b in h.baselines.iter_mut().chain(h.related.iter_mut()) {
        b.namespace = namespace_of(namespace).to_string();
    }
    Ok(h)
}
