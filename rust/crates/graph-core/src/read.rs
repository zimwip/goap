//! The reads of the current graph through a view (ADR 0157), read by node: what a read costs follows what it asks,
//! never the size of the namespace.
//!
//! A branch view is a baseline (the head of the branch when the view names none) whose state is a selection: the last
//! commit of each node on its branch up to its order, else, past the fork, on the branch it was forked from (ADR 0143).
//! The views of a change (its fork, its commits, its drafts folded from its log) are not served here yet: they answer
//! [`crate::error::Kind::Unimplemented`].
use std::collections::{HashMap, HashSet};

use crate::{
    error::{GraphError, Result},
    model::{
        BRANCH_OPEN, Baseline, BaselineId, Branch, Link, MAIN_BRANCH, MAX_BROWSE_LIMIT, Neighbourhood, Node, NodeId,
        NodePage, NodeQuery, NodeRef, NodeView, Target, Version, branch_of, namespace_of,
    },
    page::page_nodes,
    store::ReadTx,
    view::View,
};

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
    /// The baseline the view reads (empty: the empty state).
    pub baseline: Baseline,
    pub namespace: String,
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
        })
    }

    /// The versions some nodes have in the view (absent: not in it).
    pub async fn at(&self, ids: &[NodeId]) -> Result<HashMap<NodeId, Version>> {
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
        let id = if id.is_empty() {
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
