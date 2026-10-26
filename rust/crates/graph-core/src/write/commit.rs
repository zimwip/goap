//! The commit of a change (Go's `CommitChange`, `apply.go`, `changeimpact_apply.go`, `linkset.go`; ADR 0079, 0102,
//! 0140, 0141): the accepted modifications of its main flow become versions on the branch of the change, each with its
//! outgoing links (a set shared with the version it was checked out from, or a delta over an earlier one), then one
//! `committed` entry for them all. The change stays open; the branch of a change keeps no baseline, the header answered
//! is the commit as its event tells it.
use std::collections::{HashMap, HashSet};

use serde_json::Value;

use super::scripts;
use super::{
    change::{STATUS_ACTIVE, STATUS_DRAFT},
    check,
    events::{CHANGE_COMMITTED, EventNode},
    review::draft_changed,
    tx::Tx,
    validators::Validated,
    work::node_of,
};
use crate::{
    GraphError, Result,
    change::Change,
    guardian::{activity_of, judged},
    impact::{ChangeImpact, Draft, ImpactEvent, ImpactOp, REVIEW_ACCEPTED},
    jsontext,
    model::{
        Baseline, BaselineId, Branch, ChangeId, Link, LinkId, Node, NodeId, NodeRef, REASON_CREATE, REASON_DERIVE,
        REASON_REBASE, REASON_REVERT, REASON_REVISE, Target, Version, branch_of, namespace_of,
    },
    read::{branch_state, branch_versions, fork_versions, stored_node},
    store::{LinkSet, ReadTx, WriteTx},
};
use graph_catalog::def::Lifecycle;

/// How many deltas deep a link set may be before a version writes its links whole (ADR 0141).
const MAX_LINK_DEPTH: i32 = 16;

/// The kind of the baseline a commit tells (`domain.BaselineCommit`).
const BASELINE_COMMIT: &str = "commit";

/// The kind of the baseline of a commit that reverts nodes (`domain.BaselineRevert`).
const BASELINE_REVERT: &str = "revert";
/// The kind of the commit of the merges of a rebase (`domain.BaselineRebase`).
const BASELINE_REBASE: &str = "rebase";

/// The comment of a version reverting a node withdrawn after its commit.
const REVERT_COMMENT: &str = "reverted: its impact was withdrawn from the change";

/// `graphapi.ErrNothingToCommit`: an invalid request the caller tells apart (`lib/rpcerr`'s reason).
pub const NOTHING_TO_COMMIT: &str = "nothing to commit";

/// What makes two links the same link of two versions of a node (Go's `linkKey`): type, target and properties.
fn link_key(l: &Link) -> String {
    let to = match &l.to {
        Target::Node(r) => r.to_string(),
        Target::External(u) => u.to_string(),
    };
    let props = match &l.props {
        Some(m) if !m.is_empty() => jsontext::to_string(&Value::Object(m.clone())),
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

    pub(super) async fn out_links(&self, r: &NodeRef) -> Result<Vec<Link>> {
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
    pub(super) async fn put_version_links(&mut self, n: &Node, links: &[Link], like: &[NodeRef]) -> Result<()> {
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
                // a node only abandoned changes or other lines of work wrote, taken over by the creation
                // (`take_key`): its next version, still a creation
                Some(_) => {
                    self.take_key(c, &n.id, &n.key, &n.typ).await?;
                    let last = self
                        .store
                        .version_range(&n.id, 0, 1)
                        .await?
                        .pop()
                        .map_or(0, |v| v.version);
                    n.version = last + 1;
                }
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

    /// Commits a change on its own branch (Go's `CommitChange` for a root change of the main flow, no methodology to
    /// ask: the committable states of its lifecycles are its guardian): the header of the commit.
    pub async fn commit_change(&mut self, id: &ChangeId, baseline_name: &str) -> Result<Baseline> {
        self.commit_change_nodes(id, baseline_name, &[]).await
    }

    /// A partial commit (Go's `CommitChangeNodes`): the commit of the nodes named only, each an accepted modification of
    /// the change or a node withdrawn since its commit (an impact merged or split via one named follows it); none named:
    /// every one.
    pub async fn commit_change_nodes(
        &mut self,
        id: &ChangeId,
        baseline_name: &str,
        nodes: &[NodeId],
    ) -> Result<Baseline> {
        self.commit_rebased(id, baseline_name, nodes).await?
    }

    /// The commit of a change once its drafts are rebased onto the head (ADR 0165): the inner error when the rebase left
    /// conflicts, the rebase kept by the caller that commits the transaction (Go runs it again in a transaction of its
    /// own).
    pub async fn commit_rebased(
        &mut self,
        id: &ChangeId,
        baseline_name: &str,
        nodes: &[NodeId],
    ) -> Result<std::result::Result<Baseline, GraphError>> {
        let (target, left) = self.rebase_drafts(id, nodes).await?;
        if !left.is_empty() {
            return Ok(Err(super::rebase_drafts::rebase_conflict(id, &target, &left)));
        }
        Ok(Ok(self.commit_on_branch(id, baseline_name, false, nodes).await?.0))
    }

    /// Writes the accepted modifications of a change on its own branch and reverts the nodes it no longer changes (Go's
    /// `commitOnBranch` with its guardian): the header of the commit and the versions written. A rebase (mechanical)
    /// writes its merges and checks nothing of what they mean (ADR 0146), its commit told by the rebase.
    pub(crate) async fn commit_on_branch(
        &mut self,
        id: &ChangeId,
        baseline_name: &str,
        mechanical: bool,
        only: &[NodeId],
    ) -> Result<(Baseline, Vec<Node>)> {
        let w = self.work_on(id, crate::flow::MAIN_FLOW).await?;
        let c = w.c.clone();
        let branch = w.branch.clone();
        let own = self.store.branch(&c.namespace, &c.branch).await?.unwrap_or_default();
        let fold = self.views[id].fold.clone();
        let impacts: Vec<&ChangeImpact> = fold
            .impacts
            .iter()
            .filter(|cn| cn.flow.is_empty() && !cn.superseded)
            .collect();

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
            if !takes_impact(only, &impacts, cn) {
                continue;
            }
            super::review::check_origins(&impacts, cn, d)?;
            todo.push(((*cn).clone(), d.clone()));
        }
        let mut withdrawn = withdrawn_since(self.store, &own, &impacts).await?;
        if !only.is_empty() {
            withdrawn.retain(|(n, _)| only.contains(n));
            let taken: HashSet<&NodeId> = todo
                .iter()
                .map(|(_, d)| &d.node)
                .chain(withdrawn.iter().map(|(n, _)| n))
                .collect();
            let mut left: Vec<&str> = only.iter().filter(|n| !taken.contains(n)).map(|n| n.as_str()).collect();
            left.sort();
            left.dedup();
            if !left.is_empty() {
                return Err(GraphError::invalid(format!(
                    "change {id}: nothing to commit for node(s) {}: neither an accepted modification of the change nor a node withdrawn since its commit",
                    left.join(", ")
                )));
            }
        }
        if todo.is_empty() && withdrawn.is_empty() {
            return Err(GraphError::invalid(format!(
                "change {id}: no accepted change impact modifies a node, and nothing it committed is withdrawn: {NOTHING_TO_COMMIT}"
            )));
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
                    links.push(Link {
                        id: dl.id.clone(),
                        typ: dl.typ.clone(),
                        from: NodeRef::default(),
                        to: Target::External(dl.target.clone()),
                        props: dl.props.clone(),
                        change_id: ChangeId::default(),
                    });
                    continue;
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
        // 3. the reverts of the nodes withdrawn since their commit (ADR 0102)
        let reverted = self.revert_withdrawn(&c, &branch, &own, &withdrawn).await?;
        // the versions are what a version must be (Go's `walkChangeImpacts`)
        for (n, links) in nodes.iter().zip(&wrote) {
            if n.deleted || mechanical {
                continue;
            }
            let mut ls = Vec::with_capacity(links.len());
            for l in links {
                let to = match &l.to {
                    Target::Node(r) if !self.catalog().link_type_validators(&l.typ).is_empty() => {
                        scripts::stored_target(self.store, r).await?
                    }
                    _ => None,
                };
                ls.push((l.typ.as_str(), l.props.as_ref(), to));
            }
            let subject = scripts::node(n.id.as_str(), n.version, &n.key, &n.typ, &n.state, &n.props);
            check::version(self.catalog(), &subject, &ls)?;
            if let Some(lc) = self.catalog().lifecycle(&n.typ)
                && !n.state.is_empty()
            {
                let cn = &todo
                    .iter()
                    .find(|(_, d)| d.node == n.id)
                    .expect("a version of the commit")
                    .0;
                walk_transitions(&self.views[id].events, cn, n, lc)?;
            }
        }
        if !mechanical {
            let mut validated = Vec::with_capacity(nodes.len());
            for ((cn, _), n) in todo.iter().zip(&nodes) {
                let pre = match &cn.pre {
                    Some(r) => Some(stored_node(self.store, r).await?),
                    None => None,
                };
                validated.push(Validated { post: n, pre });
            }
            let store = self.store;
            let state = async || {
                let mut at = branch_state(store, &own).await?;
                for n in nodes.iter().chain(&reverted) {
                    at.insert(n.id.clone(), n.version);
                }
                Ok(at)
            };
            self.run_node_validators(state, &validated).await?;
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
            kind: if mechanical {
                BASELINE_REBASE
            } else if reverted.is_empty() {
                BASELINE_COMMIT
            } else {
                BASELINE_REVERT
            }
            .into(),
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
        if !mechanical {
            self.check_commit(id, &own, &nodes).await?;
        }
        let mut c = self.change_head(id).await?;
        if c.status == STATUS_DRAFT {
            c.status = STATUS_ACTIVE.into();
        }
        self.put_change(&c).await?;
        let wrote: Vec<Node> = nodes.into_iter().chain(reverted).collect();
        if !mechanical {
            let ev: Vec<EventNode> = wrote.iter().map(EventNode::of).collect();
            self.raise(CHANGE_COMMITTED, &c, Some(&result), branch_of(&branch), &ev);
        }
        Ok((result, wrote))
    }
}

/// Whether a partial commit writes the node of an impact (Go's `takesImpact`): its node is named, or the impact it was
/// merged or split via is; nothing named: every one.
pub(super) fn takes_impact(only: &[NodeId], impacts: &[&ChangeImpact], cn: &ChangeImpact) -> bool {
    if only.is_empty() || node_of(cn).is_some_and(|n| only.contains(&n)) {
        return true;
    }
    !cn.via.is_empty()
        && impacts
            .iter()
            .find(|p| p.id == cn.via)
            .is_some_and(|p| takes_impact(only, impacts, p))
}

/// The nodes the branch of a change changed that no impact of it holds any more, withdrawn after their commit, each with
/// the version of the fork a commit reverts it to (none: the fork does not hold it), by id (Go's `withdrawnSince`).
pub async fn withdrawn_since<T: ReadTx>(
    tx: &T,
    own: &Branch,
    impacts: &[&ChangeImpact],
) -> Result<Vec<(NodeId, Option<Version>)>> {
    if own.name.is_empty() {
        return Ok(Vec::new());
    }
    let held: HashSet<NodeId> = impacts.iter().filter_map(|cn| node_of(cn)).collect();
    // only a node the branch holds a version of can differ from the fork (ADR 0137)
    let mut ids: Vec<NodeId> = tx
        .branch_node_ids(&own.name)
        .await?
        .into_iter()
        .filter(|n| !held.contains(n))
        .collect();
    if ids.is_empty() {
        return Ok(Vec::new());
    }
    ids.sort();
    let fork = fork_versions(tx, own, &ids).await?;
    let at = branch_versions(tx, own, &ids).await?;
    Ok(ids
        .into_iter()
        .filter(|n| at.get(n).is_some_and(|v| fork.get(n) != Some(v)))
        .map(|n| {
            let v = fork.get(&n).copied();
            (n, v)
        })
        .collect())
}

impl<S: WriteTx> Tx<'_, S> {
    /// Writes on the branch of a change a revert of each node withdrawn since its commit (Go's `revertWithdrawn`): a
    /// version with the properties and links of the version of the fork, sharing its link set, which the branch reads as
    /// that version (ADR 0140, 0141); a deletion for a node the fork does not hold. It answers the versions written.
    async fn revert_withdrawn(
        &mut self,
        c: &Change,
        branch: &str,
        own: &Branch,
        withdrawn: &[(NodeId, Option<Version>)],
    ) -> Result<Vec<Node>> {
        let mut wrote = Vec::with_capacity(withdrawn.len());
        for (id, fork) in withdrawn {
            let latest = self
                .store
                .latest_on(id, &own.name)
                .await?
                .ok_or_else(|| GraphError::not_found(format!("node {id} on branch {}", own.name)))?;
            let last = self.store.version_range(id, 0, 1).await?;
            let mut n = Node {
                version: last.last().map_or(0, |l| l.version) + 1,
                branch: branch.to_string(),
                change_id: c.id.clone(),
                reason: REASON_REVERT.into(),
                parents: vec![latest.version],
                comment: REVERT_COMMENT.into(),
                created_at: Some(self.env.now()),
                origins: Vec::new(),
                deleted: false,
                change_impact: String::new(),
                execution: String::new(),
                joined: Vec::new(),
                ..latest.clone()
            };
            let (links, like) = match fork {
                Some(v) => {
                    let src = NodeRef::new(id.clone(), *v);
                    let f = self
                        .store
                        .node(&src)
                        .await?
                        .ok_or_else(|| GraphError::not_found(format!("node {id} v{v}")))?;
                    n.props = f.props.clone();
                    n.state = f.state.clone();
                    n.owner = f.owner.clone();
                    n.origins = f.origins.clone();
                    let links = self.out_links(&src).await?;
                    (links, vec![src, latest.node_ref()])
                }
                None => {
                    n.deleted = true;
                    (Vec::new(), vec![latest.node_ref()])
                }
            };
            self.put_node(&n).await?;
            self.put_version_links(&n, &links, &like).await?;
            wrote.push(n);
        }
        Ok(wrote)
    }

    /// The guardian of a commit (Go's `checkCommit`, ADR 0146): the say of the methodology of the change on what it
    /// committed — the goal of the Activity the change is scoped to, else the committable states of the domain — then
    /// the outer guardians of its chain, innermost first (ADR 0106), both asked of the guardian. A change scoped to no
    /// Activity and guarded by its own methodology alone is judged by the committable states, with nothing to ask.
    async fn check_commit(&mut self, id: &ChangeId, own: &Branch, wrote: &[Node]) -> Result<()> {
        let c = self.change_head(id).await?;
        let Some(g) = self.store.guardian() else {
            return self.check_committable(id, own, wrote).await;
        };
        let scoped = !activity_of(&c).is_empty();
        if !scoped && !c.guardians.iter().any(|r| r.methodology != c.methodology) {
            return self.check_committable(id, own, wrote).await;
        }
        let board = judged(self.store, self.catalog(), &c, self.env.now()).await?;
        // the goal of the Activity the change is scoped to decides, in place of the committable states (ADR 0066)
        match scoped {
            true => match g.commit(&c, &board).await? {
                (true, true) => {}
                (true, false) => {
                    return Err(GraphError::invalid(
                        "the change does not satisfy the goal of its methodology",
                    ));
                }
                (false, _) => self.check_committable(id, own, wrote).await?,
            },
            false => self.check_committable(id, own, wrote).await?,
        }
        self.check_guardians(&c, &board, "").await
    }

    /// The guardian of a commit with no methodology to ask (Go's `ownGuardian`, ADR 0078, 0146): what the change
    /// committed, this commit and the ones before, rests in a committable state.
    async fn check_committable(&mut self, id: &ChangeId, own: &Branch, wrote: &[Node]) -> Result<()> {
        let impacts: Vec<ChangeImpact> = self.views[id]
            .fold
            .impacts
            .iter()
            .filter(|cn| cn.flow.is_empty() && !cn.superseded && cn.committed.is_some())
            .cloned()
            .collect();
        let ids: Vec<NodeId> = impacts
            .iter()
            .filter_map(|cn| cn.committed.as_ref().map(|r| r.id.clone()))
            .collect();
        let head = branch_versions(self.store, own, &ids).await?;
        let mut stuck = Vec::new();
        for cn in &impacts {
            let nid = &cn.committed.as_ref().expect("filtered").id;
            let Some(&at) = head.get(nid) else { continue };
            let n = match wrote.iter().find(|n| &n.id == nid && n.version == at) {
                Some(n) => n.clone(),
                None => stored_node(self.store, &NodeRef::new(nid.clone(), at)).await?,
            };
            if n.state.is_empty() {
                continue;
            }
            if let Some(lc) = self.catalog().lifecycle(&n.typ)
                && !lc.committable(&n.state)
            {
                stuck.push(format!("{} ({}) in {}", n.key, n.typ, n.state));
            }
        }
        if stuck.is_empty() {
            return Ok(());
        }
        stuck.sort();
        Err(GraphError::invalid(format!(
            "the change leaves nodes in a state that cannot commit, move them to a committable state first: {}",
            stuck.join(", ")
        )))
    }
}

/// Replays the moves the log of the change records for the draft a version was written from (Go's
/// `walkTransitions`): each move leaves the state the draft was in and is one of its lifecycle, and the chain ends in
/// the state of the version.
fn walk_transitions(events: &[ImpactEvent], cn: &ChangeImpact, n: &Node, lc: &Lifecycle) -> Result<()> {
    let norm = |s: &str| {
        if s.is_empty() {
            lc.initial.clone()
        } else {
            s.to_string()
        }
    };
    let (mut at, mut tracked) = (String::new(), false);
    for e in events.iter().filter(|e| e.impact == cn.id && e.flow.is_empty()) {
        if e.starts_draft() {
            at = norm(&e.draft.as_ref().expect("a draft").state);
            tracked = true;
            continue;
        }
        if e.op != ImpactOp::Transitioned {
            continue;
        }
        let Some(st) = e.patch.as_ref().and_then(|p| p.get("state")).filter(|v| !v.is_null()) else {
            continue;
        };
        let from = st.get("from").and_then(Value::as_str).unwrap_or_default();
        let to = st.get("to").and_then(Value::as_str).unwrap_or_default();
        if tracked && norm(from) != at {
            return Err(GraphError::invalid(format!(
                "{} ({}) was moved from {from} while it was in {at}",
                n.key, n.typ
            )));
        }
        let f = norm(from);
        if !lc.transitions.iter().any(|t| t.from == f && t.to == to) {
            return Err(GraphError::invalid(format!(
                "{} ({}) cannot go from {from} to {to}",
                n.key, n.typ
            )));
        }
        at = to.to_string();
        tracked = true;
    }
    if tracked && norm(&n.state) != at {
        return Err(GraphError::invalid(format!(
            "{} ({}) is in {}, the moves recorded for it end in {at}",
            n.key, n.typ, n.state
        )));
    }
    Ok(())
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
