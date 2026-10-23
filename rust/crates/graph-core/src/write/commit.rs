//! The commit of a change (Go's `CommitChange`, `apply.go`, `changeimpact_apply.go`, `linkset.go`; ADR 0079, 0102,
//! 0140, 0141): the accepted modifications of its main flow become versions on the branch of the change, each with its
//! outgoing links (a set shared with the version it was checked out from, or a delta over an earlier one), then one
//! `committed` entry for them all. The change stays open; the branch of a change keeps no baseline, the header answered
//! is the commit as its event tells it.
use std::collections::HashMap;

use serde_json::Value;

use super::{
    change::{STATUS_ACTIVE, STATUS_DRAFT},
    check,
    events::{CHANGE_COMMITTED, EventNode},
    review::draft_changed,
    tx::Tx,
};
use crate::{
    GraphError, Result,
    change::Change,
    gojson,
    impact::{ChangeImpact, Draft, INTENT_CREATED, ImpactEvent, ImpactOp, REVIEW_ACCEPTED},
    model::{
        Baseline, BaselineId, ChangeId, Link, LinkId, Node, NodeRef, REASON_CREATE, REASON_DERIVE, REASON_REBASE,
        REASON_REVISE, Target, Version, branch_of, namespace_of,
    },
    store::{LinkSet, WriteTx},
};

/// How many deltas deep a link set may be before a version writes its links whole (ADR 0141).
const MAX_LINK_DEPTH: i32 = 16;

/// The kind of the baseline a commit tells (`domain.BaselineCommit`).
const BASELINE_COMMIT: &str = "commit";

/// `graphapi.ErrNothingToCommit`: an invalid request the caller tells apart (`lib/rpcerr`'s reason).
pub const NOTHING_TO_COMMIT: &str = "nothing to commit";

fn unserved(what: &str) -> GraphError {
    GraphError::unimplemented(format!("{what}: not served here yet"))
}

/// What makes two links the same link of two versions of a node (Go's `linkKey`): type, target and properties.
fn link_key(l: &Link) -> String {
    let to = match &l.to {
        Target::Node(r) => r.to_string(),
        Target::External(u) => u.to_string(),
    };
    let props = match &l.props {
        Some(m) if !m.is_empty() => gojson::to_string(&Value::Object(m.clone())),
        _ => String::new(),
    };
    format!("{}\0{to}\0{props}", l.typ)
}

/// The links to add to the ones a version holds to get the ones wanted, and the held ones to drop (Go's
/// `diffLinks`).
fn diff_links(held: &[Link], want: &[Link]) -> (Vec<Link>, Vec<Link>) {
    let mut unmatched: HashMap<String, std::collections::VecDeque<&Link>> = HashMap::new();
    for l in held {
        unmatched.entry(link_key(l)).or_default().push_back(l);
    }
    let mut adds = Vec::new();
    for l in want {
        match unmatched.get_mut(&link_key(l)) {
            Some(ls) if !ls.is_empty() => {
                ls.pop_front();
            }
            _ => adds.push(l.clone()),
        }
    }
    let mut drops = Vec::new();
    for l in held {
        if let Some(ls) = unmatched.get_mut(&link_key(l))
            && ls.front().is_some_and(|f| f.id == l.id)
        {
            ls.pop_front();
            drops.push(l.clone());
        }
    }
    (adds, drops)
}

impl<S: WriteTx> Tx<'_, S> {
    async fn link_set_of(&self, r: &NodeRef) -> Result<LinkSet> {
        self.store
            .link_sets_of(std::slice::from_ref(r))
            .await?
            .remove(r)
            .ok_or_else(|| GraphError::not_found(format!("link set of {r}")))
    }

    async fn out_links(&self, r: &NodeRef) -> Result<Vec<Link>> {
        self.store.links_from(std::slice::from_ref(r), None).await
    }

    /// The set the set of order `seq` is a delta over (Go's `baseSet`, skip deltas): the one of order `seq` with its
    /// lowest bit cleared, on the chain of the set `prev` holds.
    async fn base_set(&self, prev: &NodeRef, mut set: LinkSet, seq: i64) -> Result<Option<LinkSet>> {
        let want = seq & (seq - 1);
        if want == 0 {
            return Ok(None);
        }
        if set.of != prev.version {
            set = self.link_set_of(&NodeRef::new(prev.id.clone(), set.of)).await?;
        }
        while set.seq > want && set.base != 0 {
            set = self.link_set_of(&NodeRef::new(prev.id.clone(), set.base)).await?;
        }
        Ok((set.seq == want && set.depth < MAX_LINK_DEPTH).then_some(set))
    }

    /// Writes the outgoing links of a version written in the transaction (Go's `putLinks`, ADR 0141): the set of one
    /// of `like` when it holds the same links, else a new set, a delta over an earlier one when that is smaller.
    async fn put_version_links(&mut self, n: &Node, links: &[Link], like: &[NodeRef]) -> Result<()> {
        let from = n.node_ref();
        let mut prev: Option<NodeRef> = None;
        for r in like {
            if r.id != n.id || r.version < 1 || r.version >= n.version {
                continue;
            }
            if prev.is_none() {
                prev = Some(r.clone());
            }
            let held = self.out_links(r).await?;
            let (a, d) = diff_links(&held, links);
            if a.is_empty() && d.is_empty() {
                let set = self.link_set_of(r).await?;
                return self
                    .put_link_set(
                        &from,
                        LinkSet {
                            of: set.of,
                            base: 0,
                            depth: set.depth,
                            seq: set.seq,
                        },
                    )
                    .await;
            }
        }
        if prev.is_none() && n.version > 1 {
            prev = Some(NodeRef::new(n.id.clone(), n.version - 1));
        }
        let stamp = |ls: Vec<Link>| -> Vec<Link> {
            ls.into_iter()
                .map(|mut l| {
                    l.from = from.clone();
                    l.change_id = n.change_id.clone();
                    l
                })
                .collect()
        };
        let mut seq = 1;
        let mut delta = None;
        if let Some(prev) = &prev {
            let set = self.link_set_of(prev).await?;
            seq = set.seq + 1;
            if let Some(b) = self.base_set(prev, set, seq).await?
                && !links.is_empty()
            {
                let held = self.out_links(&NodeRef::new(n.id.clone(), b.of)).await?;
                let (a, d) = diff_links(&held, links);
                if a.len() + d.len() < links.len() {
                    delta = Some((b, a, d));
                }
            }
        }
        let Some((base, adds, drops)) = delta else {
            self.put_link_set(
                &from,
                LinkSet {
                    seq,
                    ..LinkSet::default()
                },
            )
            .await?;
            return self.put_links(&stamp(links.to_vec())).await;
        };
        self.put_link_set(
            &from,
            LinkSet {
                of: 0,
                base: base.of,
                depth: base.depth + 1,
                seq,
            },
        )
        .await?;
        self.put_links(&stamp(adds)).await?;
        for mut l in drops {
            l.change_id = n.change_id.clone();
            let row = LinkId(self.env.new_id());
            self.drop_link(&from, &row, &l).await?;
        }
        Ok(())
    }

    /// Writes the version of a draft on the branch of the change (Go's `writeVersion`).
    async fn write_version(&mut self, c: &Change, branch: &str, cn: &ChangeImpact, d: &Draft) -> Result<Node> {
        let comment = cn.reviews.last().map_or(cn.rationale.clone(), |r| r.comment.clone());
        let mut n = Node {
            id: d.node.clone(),
            branch: branch.to_string(),
            namespace: namespace_of(&c.namespace).to_string(),
            key: d.key.clone(),
            typ: d.typ.clone(),
            props: d.props.clone(),
            state: d.state.clone(),
            change_id: c.id.clone(),
            owner: d.owner.clone(),
            change_impact: cn.id.clone(),
            comment,
            execution: d.execution.clone(),
            origins: d.origins.clone(),
            created_at: Some(self.env.now()),
            ..Node::default()
        };
        let Some(base) = &d.base else {
            match self.store.node_id_by_key(&n.namespace, &n.key).await? {
                Some(id) if id != n.id => {
                    return Err(GraphError::conflict(format!(
                        "{} ({}): the key is taken by a node another change created meanwhile: rename it or withdraw it",
                        d.key, d.typ
                    )));
                }
                Some(_) => return Err(unserved("a creation taking over a key (ADR 0151)")),
                None => n.version = 1,
            }
            n.reason = REASON_CREATE.into();
            self.put_node(&n).await?;
            return Ok(n);
        };
        let last = self
            .store
            .version_range(&d.node, 0, 1)
            .await?
            .pop()
            .ok_or_else(|| GraphError::not_found(format!("node {}", d.node)))?;
        n.version = last.version + 1;
        n.parents = vec![base.version];
        n.reason = REASON_DERIVE.into();
        if let Some(on) = self.store.latest_on(&d.node, branch).await? {
            if on.version != base.version {
                return Err(GraphError::conflict(format!(
                    "{} changed on the branch of change {} since it was checked out (v{}, checked out from v{})",
                    d.key, c.id, on.version, base.version
                )));
            }
            n.reason = REASON_REVISE.into();
        }
        if let Some(m) = &d.merged_with
            && m.version != base.version
        {
            n.parents.push(m.version);
            n.reason = REASON_REBASE.into();
        }
        self.put_node(&n).await?;
        Ok(n)
    }

    /// Commits a change on its own branch (Go's `CommitChange` for a root change of the main flow, no lifecycle, no
    /// guardian): the header of the commit.
    pub async fn commit_change(&mut self, id: &ChangeId, baseline_name: &str) -> Result<Baseline> {
        let w = self.work_on(id, "").await?;
        let c = w.c.clone();
        let branch = w.branch.clone();
        let own = self.store.branch(&c.namespace, &c.branch).await?.unwrap_or_default();
        let fold = self.views[id].fold.clone();
        let impacts: Vec<&ChangeImpact> = fold
            .impacts
            .iter()
            .filter(|cn| cn.flow.is_empty() && !cn.superseded)
            .collect();

        // nothing a commit wrote is withdrawn since: a revert is the Go service's (ADR 0102)
        let held: std::collections::HashSet<_> = impacts
            .iter()
            .filter_map(|cn| cn.post.as_ref().or(cn.pre.as_ref()).map(|r| r.id.clone()))
            .collect();
        if self
            .store
            .branch_node_ids(&own.name)
            .await?
            .iter()
            .any(|n| !held.contains(n))
        {
            return Err(unserved("the revert of a node withdrawn after its commit"));
        }

        let mut todo: Vec<(ChangeImpact, Draft)> = Vec::new();
        for cn in &impacts {
            if cn.review != REVIEW_ACCEPTED {
                continue;
            }
            let Some(d) = fold.draft(&cn.id, "") else { continue };
            if let Some(b) = &d.base
                && !draft_changed(&self.stored_draft(b).await?, d)
            {
                continue;
            }
            if cn.intent == INTENT_CREATED && !d.origins.is_empty() {
                return Err(unserved("the commit of the successor of a merge or a split (ADR 0077)"));
            }
            todo.push(((*cn).clone(), d.clone()));
        }
        if todo.is_empty() {
            return Err(GraphError::invalid(format!(
                "change {id}: no accepted change impact modifies a node, and nothing it committed is withdrawn: {NOTHING_TO_COMMIT}"
            )));
        }
        for (_, d) in &todo {
            if self.catalog().lifecycle(&d.typ).is_some() {
                return Err(unserved("the commit of a node following a lifecycle (ADR 0058, 0078)"));
            }
        }

        // 1. the versions
        let mut written: HashMap<_, Version> = HashMap::new();
        let mut from: HashMap<_, Version> = HashMap::new();
        let mut nodes = Vec::with_capacity(todo.len());
        for (cn, d) in &todo {
            let n = self.write_version(&c, &branch, cn, d).await?;
            written.insert(n.id.clone(), n.version);
            if let Some(b) = &d.base {
                from.insert(n.id.clone(), b.version);
            } else if let Some(p) = &cn.pre {
                from.insert(n.id.clone(), p.version);
            }
            nodes.push(n);
        }
        // 2. their links, to the versions of the change
        let mut wrote = Vec::with_capacity(todo.len());
        for ((_, d), n) in todo.iter().zip(&nodes) {
            let mut links = Vec::with_capacity(d.links.len());
            for dl in &d.links {
                let mut to = dl.to.clone();
                if let Some(&v) = written.get(&to.id)
                    && (to.is_draft()
                        || Some(&to.version) == from.get(&to.id)
                        || (d.base.as_ref().is_some_and(|b| to.version == b.version) && to.id == d.node))
                {
                    to.version = v;
                }
                if dl.is_external() {
                    return Err(unserved("a link to another source (ADR 0154)"));
                }
                if to.is_draft() {
                    return Err(GraphError::invalid(format!(
                        "{} links to the draft of node {}, which the change does not commit",
                        n.key, to.id
                    )));
                }
                links.push(Link {
                    id: dl.id.clone(),
                    typ: dl.typ.clone(),
                    from: NodeRef::default(),
                    to: Target::Node(to),
                    props: dl.props.clone(),
                    change_id: ChangeId::default(),
                });
            }
            let like: Vec<NodeRef> = n.parents.iter().map(|&p| NodeRef::new(n.id.clone(), p)).collect();
            self.put_version_links(n, &links, &like).await?;
            wrote.push(links);
        }
        // the versions are what a version must be (Go's `walkChangeImpacts`)
        for (n, links) in nodes.iter().zip(&wrote) {
            if n.deleted {
                continue;
            }
            let ls: Vec<_> = links.iter().map(|l| (l.typ.as_str(), l.props.as_ref())).collect();
            check::version(self.catalog(), &w.ns, &n.key, &n.typ, n.props.as_ref(), &ls)?;
        }

        let name = if baseline_name.is_empty() {
            c.title.clone()
        } else {
            baseline_name.to_string()
        };
        let result = Baseline {
            id: BaselineId::default(),
            name,
            namespace: c.namespace.clone(),
            branch: branch.clone(),
            parent_id: own.fork_baseline.clone(),
            change_id: c.id.clone(),
            kind: BASELINE_COMMIT.into(),
            seq: self.end_commit(&branch),
            created_at: Some(self.env.now()),
            ..Baseline::default()
        };
        let committed = todo
            .iter()
            .zip(&nodes)
            .map(|((cn, _), n)| ImpactEvent {
                change: c.id.clone(),
                impact: cn.id.clone(),
                op: ImpactOp::Committed,
                committed: Some(n.node_ref()),
                branch: branch_of(&branch).to_string(),
                ..ImpactEvent::default()
            })
            .collect();
        self.emit(committed).await?;
        let mut c = self.change_head(id).await?;
        if c.status == STATUS_DRAFT {
            c.status = STATUS_ACTIVE.into();
        }
        self.put_change(&c).await?;
        let ev: Vec<EventNode> = nodes.iter().map(EventNode::of).collect();
        self.raise(CHANGE_COMMITTED, &c, Some(&result), branch_of(&branch), &ev);
        Ok(result)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::NodeId;

    fn link(id: &str, typ: &str, to: &str) -> Link {
        Link {
            id: LinkId(id.into()),
            typ: typ.into(),
            from: NodeRef::default(),
            to: Target::Node(NodeRef::new(NodeId(to.into()), 1)),
            props: None,
            change_id: ChangeId::default(),
        }
    }

    #[test]
    fn diffs_links_by_type_target_and_properties() {
        let held = vec![link("a", "t", "x"), link("b", "t", "y")];
        let want = vec![link("c", "t", "x"), link("d", "u", "z")];
        let (adds, drops) = diff_links(&held, &want);
        assert_eq!(adds.iter().map(|l| l.id.0.as_str()).collect::<Vec<_>>(), ["d"]);
        assert_eq!(drops.iter().map(|l| l.id.0.as_str()).collect::<Vec<_>>(), ["b"]);
    }
}
