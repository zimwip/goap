//! Merge and split of nodes, seen from the parent side (Go's `restructure.go`, ADR 0077): merging A and B into C is a
//! modification of the parent P of A and B, whose draft loses the links to A and B and gains one to C; a split loses
//! the link to its source and gains one to each new node. The sources stay as they are, their impacts carrying the
//! impact of their parent (`via`); the successors record their origins.
use std::collections::{HashMap, HashSet};

use super::{
    tx::Tx,
    work::{LinkWrite, Named, NodeCreate, Work, add_link_patch, node_of, remove_link_patch},
};
use crate::{
    GraphError, Result,
    impact::{ChangeImpact, Draft, INTENT_CREATED, ImpactEvent, ImpactOp},
    model::{ChangeId, Link, Node, NodeId, NodeRef, Props, Target, namespace_of},
    read,
    store::WriteTx,
};

/// A node of the change a merge or a split names: a change impact, a node id or a key.
#[derive(Debug, Clone, Default)]
pub struct NodeName {
    pub impact: String,
    pub node: NodeId,
    pub key: String,
}

/// A link of another node to a split source that the split leaves as it is (ADR 0003: suspect).
#[derive(Debug, Clone, Default)]
pub struct SuspectLink {
    pub from: NodeRef,
    pub from_key: String,
    pub typ: String,
    pub to: NodeRef,
    pub to_key: String,
}

/// What a merge or a split did, as the flow sees it.
#[derive(Debug, Clone, Default)]
pub struct Restructured {
    pub successors: Vec<ChangeImpact>,
    pub sources: Vec<ChangeImpact>,
    pub parents: Vec<ChangeImpact>,
    pub suspect: Vec<SuspectLink>,
}

/// A node whose links move: its current state and the links it holds to the sources.
struct Holder {
    node: Node,
    parent: bool,
    links: Vec<Link>,
}

/// What a merge or a split works on (one request).
pub struct Restructure {
    pub sources: Vec<NodeName>,
    pub into: Vec<NodeCreate>,
    pub merge: bool,
    pub rationale: String,
    pub flow: String,
    pub execution: String,
}

impl<S: WriteTx> Tx<'_, S> {
    /// Merges nodes into a new one (Go's `ImpactNodeMerge`).
    pub async fn impact_node_merge(
        &mut self,
        id: &ChangeId,
        sources: Vec<NodeName>,
        into: NodeCreate,
        rationale: String,
        flow: String,
        execution: String,
    ) -> Result<Restructured> {
        if sources.len() < 2 {
            return Err(GraphError::invalid("a merge needs at least two nodes"));
        }
        let r = Restructure {
            sources,
            into: vec![into],
            merge: true,
            rationale,
            flow,
            execution,
        };
        self.gated(id, &r).await
    }

    /// Splits a node into new ones (Go's `ImpactNodeSplit`): the links of other types to the source stay, returned as
    /// suspect.
    pub async fn impact_node_split(
        &mut self,
        id: &ChangeId,
        source: NodeName,
        into: Vec<NodeCreate>,
        rationale: String,
        flow: String,
        execution: String,
    ) -> Result<Restructured> {
        if into.len() < 2 {
            return Err(GraphError::invalid("a split needs at least two new nodes"));
        }
        let r = Restructure {
            sources: vec![source],
            into,
            merge: false,
            rationale,
            flow,
            execution,
        };
        self.gated(id, &r).await
    }

    /// Runs a restructuring the way the Go service does (its `gated`): a first pass collecting the node types its gate
    /// is asked about, rolled back, then the pass that stays. A failing first pass is the answer.
    async fn gated(&mut self, id: &ChangeId, r: &Restructure) -> Result<Restructured> {
        let rehearsal = self.rehearse().await?;
        self.restructure(id, r).await?;
        self.rewind(rehearsal).await?;
        self.restructure(id, r).await
    }

    async fn restructure(&mut self, id: &ChangeId, r: &Restructure) -> Result<Restructured> {
        let mut out = Restructured::default();
        for c in &r.into {
            if c.key.is_empty() || c.typ.is_empty() {
                return Err(GraphError::invalid("a node needs a key and a type"));
            }
        }
        let rationale = if r.rationale.is_empty() {
            r.into[0].rationale.clone()
        } else {
            r.rationale.clone()
        };
        let named = |impact: &str, node: &NodeId, key: &str, rationale: &str| Named {
            impact: impact.to_string(),
            node: node.clone(),
            key: key.to_string(),
            rationale: rationale.to_string(),
            execution: r.execution.clone(),
            flow: r.flow.clone(),
            ..Named::default()
        };

        // 1. the sources, as impacts of the change
        let mut srcs: Vec<Node> = Vec::new();
        let mut src_impacts: Vec<String> = Vec::new();
        let mut src_set: HashSet<NodeId> = HashSet::new();
        let mut w: Option<Work> = None;
        for name in &r.sources {
            let sw = self
                .resolve(id, named(&name.impact, &name.node, &name.key, &rationale))
                .await?;
            if sw.cn.intent == INTENT_CREATED {
                return Err(GraphError::invalid(format!(
                    "{} is created by the change: create the node you want instead of merging or splitting it",
                    sw.cn.key
                )));
            }
            let Some(n) = self.base(&sw).await? else {
                return Err(GraphError::invalid(format!(
                    "change impact {} has no version to merge or split",
                    sw.cn.id
                )));
            };
            if self.views[id].seen_draft(&sw.flow, &sw.cn.id).is_some() {
                return Err(GraphError::conflict(format!(
                    "{} is checked out by change impact {}: cancel the checkout first",
                    n.key, sw.cn.id
                )));
            }
            let ns = namespace_of(&n.namespace);
            if ns != sw.ns {
                return Err(GraphError::invalid(format!(
                    "node {} is in namespace {ns}, the change acts on {}",
                    n.key, sw.ns
                )));
            }
            if !src_set.insert(n.id.clone()) {
                return Err(GraphError::invalid(format!("{} is named twice", n.key)));
            }
            for c in &r.into {
                if !self.compatible(&n.typ, &c.typ) {
                    return Err(GraphError::invalid(format!(
                        "{} ({}) cannot be replaced by {} ({}): the types are not compatible",
                        n.key, n.typ, c.key, c.typ
                    )));
                }
            }
            src_impacts.push(sw.cn.id.clone());
            srcs.push(n);
            w = Some(sw);
        }
        let w = w.expect("a restructuring names a source");

        // 2. the nodes holding links to the sources, as the flow sees them: a node the flow holds a draft of through
        // its draft, any other through the links of its stored version
        let drafts = self.drafts_by_node(id, &w);
        let normalize = |to: &NodeRef| match drafts.get(&to.id) {
            Some(d) if d.base.as_ref() == Some(to) => d.draft_ref(),
            _ => to.clone(),
        };
        let mut sorted: Vec<&Draft> = drafts.values().collect();
        sorted.sort_by(|a, b| a.key.cmp(&b.key));
        let mut holders: HashMap<NodeId, Holder> = HashMap::new();
        let mut has_parent: HashSet<NodeId> = HashSet::new();
        let cat = self.catalog();
        let mut hold = |n: Node, l: Link, src: &NodeId| {
            let composes = cat.composes(&l.typ);
            let h = holders.entry(n.id.clone()).or_insert_with(|| Holder {
                node: n,
                parent: false,
                links: Vec::new(),
            });
            if !h.links.iter().any(|x| x.id == l.id) {
                h.links.push(l);
            }
            if composes {
                h.parent = true;
                has_parent.insert(src.clone());
            }
        };
        let not_parts = |parent: &str, part: &str| {
            GraphError::invalid(format!(
                "{parent} is the parent of {part}: merge or split nodes that are not parts of each other"
            ))
        };
        for s in &srcs {
            // the links entering every version of the source, in one read
            for l in self.store.links_to(&NodeRef::new(s.id.clone(), 0)).await? {
                if drafts.contains_key(&l.from.id) {
                    continue; // read through its draft below
                }
                let Some(cur) = self.current_of(&w, &l.from.id).await? else {
                    continue;
                };
                if cur.node_ref() != l.from || cur.deleted {
                    continue; // a link of a version the flow no longer sees
                }
                if src_set.contains(&cur.id) {
                    if cat.composes(&l.typ) {
                        return Err(not_parts(&cur.key, &s.key));
                    }
                    continue;
                }
                hold(cur, l, &s.id);
            }
            for d in &sorted {
                for dl in d.links.iter().filter(|dl| dl.to.id == s.id) {
                    let l = Link {
                        id: dl.id.clone(),
                        typ: dl.typ.clone(),
                        from: d.draft_ref(),
                        to: Target::Node(normalize(&dl.to)),
                        props: dl.props.clone(),
                        change_id: d.change.clone(),
                    };
                    if src_set.contains(&d.node) {
                        if cat.composes(&l.typ) {
                            return Err(not_parts(&d.key, &s.key));
                        }
                        continue;
                    }
                    let n = Node {
                        id: d.node.clone(),
                        key: d.key.clone(),
                        typ: d.typ.clone(),
                        ..Node::default()
                    };
                    hold(n, l, &s.id);
                }
            }
        }
        for s in &srcs {
            if !has_parent.contains(&s.id) {
                return Err(GraphError::invalid(format!(
                    "{} has no parent: a merge or a split is a modification of the parent of the nodes, the node holding a link of a type flagged compose to it",
                    s.key
                )));
            }
        }
        let mut order: Vec<Holder> = holders.into_values().collect();
        order.sort_by(|a, b| a.node.key.cmp(&b.node.key));
        // 3. the successors
        let origins: Vec<NodeRef> = srcs.iter().map(Node::node_ref).collect();
        let mut succ = Vec::with_capacity(r.into.len());
        for c in &r.into {
            let cn = self.create_node(id, c.clone(), &r.flow, &origins).await?;
            succ.push(cn.post.clone().unwrap_or_default());
        }

        // 4. the links move in the drafts of the holders
        let mut via: HashMap<NodeId, String> = HashMap::new();
        let mut holder_impacts = Vec::new();
        for h in &order {
            if !r.merge && !h.parent {
                for l in &h.links {
                    let to = link_to(l);
                    let to_key = match drafts.get(&to.id) {
                        Some(d) if to.is_draft() => d.key.clone(),
                        _ => read::stored_node(self.store, &to)
                            .await
                            .map(|n| n.key)
                            .unwrap_or_default(),
                    };
                    out.suspect.push(SuspectLink {
                        from: l.from.clone(),
                        from_key: h.node.key.clone(),
                        typ: l.typ.clone(),
                        to,
                        to_key,
                    });
                }
                continue;
            }
            let mut hw = self.resolve(id, named("", &h.node.id, "", &rationale)).await?;
            if self.views[id].fold.draft(&hw.cn.id, &hw.flow).is_none() {
                self.checkout(&hw, &r.execution).await?;
                hw = self.resolve(id, named("", &h.node.id, "", "")).await?;
            }
            let d = self.working(&hw)?;
            let mut moves: Vec<(String, Props)> = Vec::new();
            for l in &d.links {
                let composes = self.catalog().composes(&l.typ);
                if !src_set.contains(&l.to.id) || (!r.merge && !composes) {
                    continue;
                }
                let mut patch = serde_json::Map::new();
                patch.insert("removeLink".into(), remove_link_patch(l));
                self.emit_updated(&mut hw, &d.draft_ref(), &r.execution, patch).await?;
                if composes {
                    via.entry(l.to.id.clone()).or_insert_with(|| hw.cn.id.clone());
                }
                if !moves.iter().any(|(t, _)| t == &l.typ) {
                    moves.push((l.typ.clone(), l.props.clone()));
                }
            }
            if moves.is_empty() {
                return Err(GraphError::conflict(format!(
                    "{} no longer holds a link to the nodes",
                    h.node.key
                )));
            }
            for (typ, props) in moves {
                for to in &succ {
                    let lw = LinkWrite {
                        typ: typ.clone(),
                        to: to.clone(),
                        props: props.clone(),
                        ..LinkWrite::default()
                    };
                    let dl = self.new_draft_link(&hw, &d, &lw).await?;
                    let mut patch = serde_json::Map::new();
                    patch.insert("addLink".into(), add_link_patch(&dl));
                    self.emit_updated(&mut hw, &d.draft_ref(), &r.execution, patch).await?;
                }
            }
            holder_impacts.push(hw.cn.id.clone());
        }

        // 5. the impacts of the sources carry the impact of their parent
        for (s, impact) in srcs.iter().zip(&src_impacts) {
            let Some(v) = via.get(&s.id).cloned() else {
                return Err(GraphError::conflict(format!("{}: no parent link was moved", s.key)));
            };
            let sw = self.resolve(id, named(impact, &NodeId::default(), "", "")).await?;
            if sw.cn.via == v {
                continue;
            }
            if !sw.cn.via.is_empty() {
                return Err(GraphError::conflict(format!(
                    "change impact {} of {} is already realized through {}",
                    sw.cn.id, s.key, sw.cn.via
                )));
            }
            let mut cn = sw.cn.clone();
            cn.via = v;
            self.emit(vec![ImpactEvent {
                change: id.clone(),
                impact: cn.id.clone(),
                op: ImpactOp::Proposed,
                flow: cn.flow.clone(),
                execution: cn.execution.clone(),
                state: Some(cn),
                ..ImpactEvent::default()
            }])
            .await?;
        }

        // 6. the result, as the flow sees it
        let w = self.work_on(id, &r.flow).await?;
        let find = |f: &dyn Fn(&ChangeImpact) -> bool| w.seen.iter().find(|i| f(i)).cloned().unwrap_or_default();
        for c in &r.into {
            out.successors
                .push(find(&|i| i.key == c.key && i.intent == INTENT_CREATED));
        }
        for i in &src_impacts {
            out.sources.push(find(&|x| &x.id == i));
        }
        for i in &holder_impacts {
            out.parents.push(find(&|x| &x.id == i));
        }
        Ok(out)
    }

    /// A node of type a may be replaced by one of type b: the same type, or one a subtype of the other.
    fn compatible(&self, a: &str, b: &str) -> bool {
        a == b || self.catalog().is_a(a, b) || self.catalog().is_a(b, a)
    }

    /// The drafts the flow sees, by node: the draft of the last impact of the node it sees (Go's `draftReader`).
    fn drafts_by_node(&self, id: &ChangeId, w: &Work) -> HashMap<NodeId, Draft> {
        let by_impact = self.views[id].seen_drafts(&w.flow);
        let mut out = HashMap::new();
        for cn in &w.seen {
            if let Some(d) = by_impact.get(&cn.id) {
                out.insert(d.node.clone(), d.clone());
            }
        }
        out
    }

    /// The stored version of a node the flow starts from (Go's `currentOf`): its pre version when the change holds
    /// an impact on it, else the version of the reference baseline.
    async fn current_of(&self, w: &Work, nid: &NodeId) -> Result<Option<Node>> {
        if let Some(cn) = w.seen.iter().find(|cn| node_of(cn).as_ref() == Some(nid)) {
            let mut w2 = w.clone();
            self.select_impact(&mut w2, &cn.id)?;
            return self.base(&w2).await;
        }
        let header = read::header_of(self.store, &w.c.baseline_id).await?;
        let at = read::select(self.store, &header, Some(std::slice::from_ref(nid))).await?;
        let Some(v) = at.get(nid) else { return Ok(None) };
        Ok(Some(
            read::stored_node(self.store, &NodeRef::new(nid.clone(), *v)).await?,
        ))
    }
}

fn link_to(l: &Link) -> NodeRef {
    match &l.to {
        Target::Node(r) => r.clone(),
        Target::External(_) => NodeRef::default(),
    }
}
