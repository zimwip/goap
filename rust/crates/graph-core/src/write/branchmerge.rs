//! The three-way merge of a branch into another one (Go's `PlanMerge` / `MergeBranch`, `merge.go`; ADR 0032): the
//! plan compares what the source branch changed since its fork with the head of the target; the merge is a change of
//! its own, one impact per merged node accepted by one review of `graph.merge`, a node changed on one side only joining
//! the target as is, a node changed on both sides getting a merge version, then a baseline of kind merge.
use std::collections::{BTreeMap, BTreeSet, HashMap};

use graph_catalog::catalog::{STRUCTURE_ORGANISATION, STRUCTURE_PROJECT};
use serde_json::{Map, Value};

use super::{
    change::{STATUS_ACTIVE, STATUS_APPLIED},
    events::{CHANGE_APPLIED, CHANGE_MERGED, EventNode},
    tx::Tx,
};
use crate::{
    GraphError, Result,
    change::Change,
    gojson,
    impact::{
        ChangeImpact, INTENT_CREATED, INTENT_MODIFIED, ImpactEvent, ImpactOp, REVIEW_ACCEPTED, REVIEW_PROPOSED, Review,
    },
    model::{
        BRANCH_MERGED, BRANCH_OPEN, Baseline, BaselineId, ChangeId, Link, LinkId, MAIN_BRANCH, Node, NodeId, NodeRef,
        Props, REASON_MERGE, Target, Version, branch_of, namespace_of,
    },
    read::{self, stored_node},
    store::{ReadTx, WriteTx},
};

/// Reported as a conflict when one side deleted the node and the other one modified it.
pub const DELETED_KEY: &str = "(deleted)";
/// A node created on the source branch.
pub const MERGE_ADDED: &str = "added";
/// A node unchanged on the target branch.
pub const MERGE_FAST_FORWARD: &str = "fast_forward";
/// A node changed on both branches.
pub const MERGE_THREE_WAY: &str = "merge";
/// The kind of the baseline a merge leaves (`domain.BaselineMerge`).
const BASELINE_MERGE: &str = "merge";
const MERGE_PRODUCER: &str = "graph.merge";
/// The versions of a node read at once to find a common ancestor.
const VERSION_PAGE: usize = 500;

/// One node of a merge plan.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct MergeCandidate {
    pub node: NodeId,
    pub key: String,
    pub typ: String,
    /// added | fast_forward | merge
    pub kind: String,
    pub ancestor: Option<NodeRef>,
    pub ours: Option<NodeRef>,
    pub theirs: NodeRef,
    pub deleted: bool,
    pub base: Props,
    pub ours_props: Props,
    pub theirs_props: Props,
    pub merged: Props,
    pub conflicts: Vec<String>,
}

/// What merging a branch into another one would do.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct MergePlan {
    pub from: String,
    pub into: String,
    pub into_head: BaselineId,
    pub candidates: Vec<MergeCandidate>,
}

/// How a conflict is resolved: the properties to keep, or the target's version left as is.
#[derive(Debug, Clone, Default)]
pub struct Resolution {
    pub props: Props,
    pub skip: bool,
}

/// A merge of a branch into another one.
#[derive(Debug, Clone, Default)]
pub struct MergeRequest {
    pub from: String,
    pub into: String,
    pub title: String,
    pub namespace: String,
    pub resolutions: HashMap<NodeId, Resolution>,
    /// The unit holding the merge change (empty: the root unit).
    pub owner_org: String,
    /// The project the merge change acts in (required).
    pub project_id: String,
}

/// The merge change, the baseline it left on the target with its state, and the plan it followed.
#[derive(Debug, Clone, Default)]
pub struct MergeResult {
    pub change: Change,
    pub baseline: Baseline,
    pub state: HashMap<NodeId, Version>,
    pub plan: MergePlan,
    /// The versions the merge wrote or joined on the target (ADR 0159).
    pub merged: Vec<Node>,
}

fn same(a: Option<&Value>, b: Option<&Value>) -> bool {
    match (a, b) {
        (None, None) => true,
        (Some(a), Some(b)) => gojson::to_string(a) == gojson::to_string(b),
        _ => false,
    }
}

/// Merges two property maps against their common ancestor, key by key (Go's `MergeProps`): equal sides win; a side
/// equal to the ancestor takes the other one; otherwise the key is a conflict and ours is kept. A key absent from a
/// side is a deletion on that side.
pub fn merge_props(anc: &Props, ours: &Props, theirs: &Props) -> (Props, Vec<String>) {
    let empty = Map::new();
    let (a, o, t) = (
        anc.as_ref().unwrap_or(&empty),
        ours.as_ref().unwrap_or(&empty),
        theirs.as_ref().unwrap_or(&empty),
    );
    let keys: BTreeSet<&String> = a.keys().chain(o.keys()).chain(t.keys()).collect();
    let mut merged = Map::new();
    let mut conflicts = Vec::new();
    for k in keys {
        let (av, ov, tv) = (a.get(k), o.get(k), t.get(k));
        let v = if same(ov, tv) {
            ov
        } else if same(ov, av) {
            tv
        } else if same(tv, av) {
            ov
        } else {
            conflicts.push(k.clone());
            ov
        };
        if let Some(v) = v {
            merged.insert(k.clone(), v.clone());
        }
    }
    (Some(merged), conflicts)
}

fn link_key(l: &Link) -> String {
    match &l.to {
        Target::External(u) => format!("{}\0{}", l.typ, u.logical()),
        Target::Node(r) => format!("{}\0{}", l.typ, r.id),
    }
}

/// Merges outgoing link sets keyed by type and target node (Go's `MergeLinks`): a link is kept when present on both
/// sides, or added on one side since the ancestor; it is dropped when removed on one side. Ours is preferred.
pub fn merge_links(anc: &[Link], ours: &[Link], theirs: &[Link]) -> Vec<Link> {
    let index = |ls: &[Link]| -> HashMap<String, Link> { ls.iter().map(|l| (link_key(l), l.clone())).collect() };
    let (a, o, t) = (index(anc), index(ours), index(theirs));
    let keys: BTreeSet<&String> = o.keys().chain(t.keys()).collect();
    let mut out = Vec::new();
    for k in keys {
        let in_a = a.contains_key(k);
        match (o.get(k), t.get(k)) {
            (Some(ol), Some(_)) => out.push(ol.clone()),
            (Some(ol), None) if !in_a => out.push(ol.clone()),
            (None, Some(tl)) if !in_a => out.push(tl.clone()),
            _ => {}
        }
    }
    out
}

/// The most recent common ancestor of two versions of a node, a version being its own (Go's `CommonAncestor`); none
/// when they have none.
pub async fn common_ancestor<T: ReadTx>(tx: &T, id: &NodeId, a: Version, b: Version) -> Result<Option<NodeRef>> {
    let mut parents: HashMap<Version, Vec<Version>> = HashMap::new();
    let mut before = 0;
    loop {
        let page = tx.version_range(id, before, VERSION_PAGE).await?;
        let n = page.len();
        let Some(first) = page.first() else { break };
        before = first.version;
        for v in page {
            parents.insert(v.version, v.parents);
        }
        if n < VERSION_PAGE || before <= 1 {
            break;
        }
    }
    let ancestors = |v: Version| {
        let mut seen = BTreeSet::new();
        let mut stack = vec![v];
        while let Some(x) = stack.pop() {
            if seen.insert(x) {
                stack.extend(parents.get(&x).into_iter().flatten().copied());
            }
        }
        seen
    };
    let (sa, sb) = (ancestors(a), ancestors(b));
    Ok(sa
        .intersection(&sb)
        .copied()
        .filter(|&v| v > 0)
        .max()
        .map(|v| NodeRef::new(id.clone(), v)))
}

/// Computes the three-way merge of branch `from` into branch `into`, both of a namespace (Go's `PlanMerge`).
pub async fn plan_merge<T: ReadTx>(tx: &T, namespace: &str, from: &str, into: &str) -> Result<MergePlan> {
    Ok(plan(tx, namespace, from, into).await?.0)
}

/// The plan, with the state of the head of the target it was computed on.
async fn plan<T: ReadTx>(
    tx: &T,
    namespace: &str,
    from: &str,
    into: &str,
) -> Result<(MergePlan, HashMap<NodeId, Version>)> {
    let (ns, into) = (namespace_of(namespace), branch_of(into));
    let fb = tx
        .branch(ns, from)
        .await?
        .ok_or_else(|| GraphError::not_found(format!("branch {from}")))?;
    if fb.status != BRANCH_OPEN {
        return Err(GraphError::conflict(format!("branch {from} is {}", fb.status)));
    }
    // the state the branch was forked from and the one it holds: its fork and head baselines, or, for the branch of a
    // change, which holds none, its fork with what committed on it (Go's `branchStates`)
    let (fork, from_head) = if fb.ephemeral {
        (read::fork_state(tx, &fb).await?, read::branch_state(tx, &fb).await?)
    } else {
        let f = read::header_of(tx, &fb.fork_baseline).await?;
        let h = read::header_of(tx, &fb.head).await?;
        (read::whole_state(tx, &f).await?, read::whole_state(tx, &h).await?)
    };
    let head = read::branch_head(tx, ns, into).await?;
    let into_state = read::whole_state(tx, &head).await?;
    let mut plan = MergePlan {
        from: from.to_string(),
        into: into.to_string(),
        into_head: head.id.clone(),
        candidates: Vec::new(),
    };
    let mut theirs: Vec<Node> = Vec::new();
    for (id, &v) in &from_head {
        if fork.get(id) != Some(&v) {
            theirs.push(stored_node(tx, &NodeRef::new(id.clone(), v)).await?);
        }
    }
    for id in fork.keys() {
        if from_head.contains_key(id) {
            continue;
        }
        if let Some(n) = tx.latest_on(id, from).await?
            && n.deleted
        {
            theirs.push(n);
        }
    }
    theirs.sort_by(|a, b| a.key.cmp(&b.key));
    for t in theirs {
        let mut c = MergeCandidate {
            node: t.id.clone(),
            key: t.key.clone(),
            typ: t.typ.clone(),
            theirs: t.node_ref(),
            deleted: t.deleted,
            theirs_props: t.props.clone(),
            ..MergeCandidate::default()
        };
        let in_fork = fork.get(&t.id).copied();
        match (into_state.get(&t.id).copied(), in_fork) {
            (None, None) => {
                if t.deleted {
                    continue;
                }
                c.kind = MERGE_ADDED.into();
                c.merged = t.props.clone();
            }
            (None, Some(fv)) => {
                // deleted on the target branch, changed on the source branch
                if t.deleted {
                    continue;
                }
                c.kind = MERGE_THREE_WAY.into();
                c.ancestor = Some(NodeRef::new(t.id.clone(), fv));
                c.merged = t.props.clone();
                c.conflicts = vec![DELETED_KEY.into()];
            }
            (Some(ov), _) => {
                let ours = stored_node(tx, &NodeRef::new(t.id.clone(), ov)).await?;
                c.ours = Some(ours.node_ref());
                c.ours_props = ours.props.clone();
                let Some(anc) = common_ancestor(tx, &t.id, ov, t.version).await? else {
                    // the node created on both lines of work, each its own history: a merge with no base
                    c.kind = MERGE_THREE_WAY.into();
                    (c.merged, c.conflicts) = merge_props(&None, &ours.props, &t.props);
                    if t.deleted {
                        c.conflicts = vec![DELETED_KEY.into()];
                    }
                    plan.candidates.push(c);
                    continue;
                };
                if anc.version == t.version {
                    continue; // already merged
                }
                let an = stored_node(tx, &anc).await?;
                c.ancestor = Some(anc.clone());
                c.base = an.props.clone();
                if anc.version == ov {
                    c.kind = MERGE_FAST_FORWARD.into();
                    c.merged = t.props.clone();
                } else {
                    c.kind = MERGE_THREE_WAY.into();
                    (c.merged, c.conflicts) = merge_props(&an.props, &ours.props, &t.props);
                    if t.deleted {
                        c.conflicts = vec![DELETED_KEY.into()];
                    }
                }
            }
        }
        plan.candidates.push(c);
    }
    Ok((plan, into_state))
}

/// A node the merge takes.
struct Merged {
    cand: MergeCandidate,
    from: Node,
    ours: Option<Node>,
    anc: Option<Node>,
    next: NodeRef,
    /// the source version commits as is (added or fast-forward, no resolution), no merge version (ADR 0032)
    join: bool,
}

impl<S: WriteTx> Tx<'_, S> {
    /// Merges a branch into another one (Go's `MergeBranch`): a merge change with one impact per merged node, the merge
    /// versions written on the target, the source branch marked merged. Conflicts without a resolution are refused.
    pub async fn merge_branch(&mut self, inp: MergeRequest) -> Result<MergeResult> {
        let ns = namespace_of(&inp.namespace).to_string();
        let (plan, into_state) = plan(self.store, &ns, &inp.from, &inp.into).await?;
        let unresolved: Vec<&str> = plan
            .candidates
            .iter()
            .filter(|c| !c.conflicts.is_empty() && !inp.resolutions.contains_key(&c.node))
            .map(|c| c.key.as_str())
            .collect();
        if !unresolved.is_empty() {
            return Err(GraphError::conflict(format!(
                "merge {} into {}: unresolved conflicts on {}",
                inp.from,
                plan.into,
                unresolved.join(", ")
            )));
        }
        let title = if inp.title.is_empty() {
            format!("merge {} into {}", inp.from, plan.into)
        } else {
            inp.title.clone()
        };
        let owner = self.structure_ref(STRUCTURE_ORGANISATION, &inp.owner_org)?;
        let project = self.structure_ref(STRUCTURE_PROJECT, &inp.project_id)?;
        let mut data = Map::new();
        let mut merge = Map::new();
        merge.insert("from".into(), Value::String(inp.from.clone()));
        merge.insert("into".into(), Value::String(plan.into.clone()));
        data.insert("merge".into(), Value::Object(merge));
        // the merge is a change of the platform (journal): one impact per merged node, whose version records the
        // origin; the merge versions are written here, not proposed
        let mut c = Change {
            id: ChangeId(self.env.new_id()),
            title: title.clone(),
            intent: title.clone(),
            status: STATUS_ACTIVE.into(),
            namespace: ns.clone(),
            baseline_id: plan.into_head.clone(),
            branch: plan.into.clone(),
            created_at: Some(self.env.now()),
            data: Some(data),
            owner_org: owner,
            project_id: project,
            ..Change::default()
        };
        self.scope_change(&mut c).await?;
        self.put_change(&c).await?;
        let base = read::header_of(self.store, &plan.into_head).await?;
        let mut fb = self
            .store
            .branch(&ns, &inp.from)
            .await?
            .ok_or_else(|| GraphError::not_found(format!("branch {}", inp.from)))?;
        let mut target = into_state.clone();
        let mut todo: Vec<Merged> = Vec::new();
        for cand in &plan.candidates {
            let r = inp.resolutions.get(&cand.node);
            if r.is_some_and(|r| r.skip) || (cand.ours.is_none() && cand.ancestor.is_some()) {
                continue; // kept as is on the target (or deleted there)
            }
            let resolved = r.and_then(|r| r.props.clone());
            let mut from = stored_node(self.store, &cand.theirs).await?;
            let join = (cand.kind == MERGE_ADDED || cand.kind == MERGE_FAST_FORWARD) && resolved.is_none();
            let mut props = resolved.or_else(|| cand.merged.clone());
            let mut m = Merged {
                cand: cand.clone(),
                from: Node::default(),
                ours: None,
                anc: None,
                next: NodeRef::default(),
                join,
            };
            if let Some(o) = &cand.ours {
                if target.get(&o.id) != Some(&o.version) {
                    return Err(GraphError::conflict(format!(
                        "node {o} is not in the reference baseline"
                    )));
                }
                if let Some(head) = self.store.latest_on(&o.id, &plan.into).await?
                    && head.version != o.version
                {
                    return Err(GraphError::conflict(format!(
                        "node {o} was modified on {} since the reference baseline (now v{})",
                        plan.into, head.version
                    )));
                }
                let ours = stored_node(self.store, o).await?;
                let anc = match &cand.ancestor {
                    Some(a) => Some(a.clone()),
                    None => common_ancestor(self.store, &from.id, o.version, from.version).await?,
                };
                if let Some(a) = &anc {
                    m.anc = Some(stored_node(self.store, a).await?);
                }
                if props.is_none() {
                    let base = m.anc.as_ref().and_then(|a| a.props.clone());
                    props = merge_props(&base, &ours.props, &from.props).0;
                }
                m.ours = Some(ours);
            } else {
                if target.contains_key(&from.id) {
                    return Err(GraphError::conflict(format!(
                        "node {} is already in the reference baseline, merge needs its version there",
                        from.id
                    )));
                }
                if props.is_none() {
                    props = from.props.clone();
                }
            }
            from.props = props; // the merged properties, carried by the version below
            m.from = from;
            todo.push(m);
        }
        // 1. the merge versions, with the impact that explains each: a node changed on one side only joins the target
        // as is (ADR 0032), a node changed on both sides gets a merge version
        let baseline_id = BaselineId(self.env.new_id());
        let why_of = |m: &Merged| {
            let mut why = format!("merge of {} into {}", inp.from, plan.into);
            if !m.cand.conflicts.is_empty() {
                why.push_str(&format!(" (conflicts on {}, resolved)", m.cand.conflicts.join(", ")));
            }
            why
        };
        let mut impact_ids = Vec::with_capacity(todo.len());
        let (mut review_key, mut cause) = (String::new(), String::new());
        if !todo.is_empty() {
            let mut entries = Vec::with_capacity(todo.len());
            for m in &todo {
                let id = self.env.new_id();
                entries.push((id.clone(), why_of(m)));
                impact_ids.push(id);
            }
            // the merge accepts what it commits in one review, recorded before the impacts it decides (ADR 0105)
            (review_key, cause) = self
                .put_review_record(
                    &c.id,
                    MERGE_PRODUCER,
                    &format!("merge of {} into {}", inp.from, plan.into),
                    &entries,
                )
                .await?;
        }
        let into = branch_of(&plan.into).to_string();
        let mut stuck = Vec::new();
        let mut phases: [Vec<ImpactEvent>; 4] = Default::default();
        let mut merged_nodes: Vec<Node> = Vec::new();
        for (i, m) in todo.iter_mut().enumerate() {
            let why = why_of(m);
            let mut cn = ChangeImpact {
                id: impact_ids[i].clone(),
                key: m.from.key.clone(),
                typ: m.from.typ.clone(),
                intent: INTENT_CREATED.into(),
                rationale: why.clone(),
                review: REVIEW_ACCEPTED.into(),
                produced_by: MERGE_PRODUCER.into(),
                created_at: self.env.now(),
                ..ChangeImpact::default()
            };
            if m.ours.is_some() {
                cn.intent = INTENT_MODIFIED.into();
                cn.pre = m.cand.ours.clone();
            }
            let n = if m.join {
                let n = m.from.clone();
                self.join_branch(&n.node_ref(), &plan.into, &c.id).await?;
                n
            } else {
                let mut n = m.from.clone();
                n.reason = REASON_MERGE.into();
                n.parents = vec![m.from.version];
                let mut deleted = m.from.deleted;
                if let Some(o) = &m.ours {
                    n.parents = vec![o.version, m.from.version];
                    deleted = deleted || o.deleted;
                }
                let last = self
                    .store
                    .version_range(&n.id, 0, 1)
                    .await?
                    .pop()
                    .ok_or_else(|| GraphError::not_found(format!("node {}", n.id)))?;
                n.version = last.version + 1;
                n.branch = plan.into.clone();
                n.deleted = deleted;
                n.change_id = c.id.clone();
                n.change_impact = cn.id.clone();
                n.comment = why.clone();
                n.created_at = Some(self.env.now());
                n.joined = Vec::new();
                self.put_node(&n).await?;
                n
            };
            m.next = n.node_ref();
            // the merge records its impact as events, accepted and committed at once: a node it creates on the target
            // is a created event (the impact and its version), one it modifies is proposed, then written
            let mut declared = cn.clone();
            declared.review = REVIEW_PROPOSED.into();
            let review = Review {
                status: REVIEW_ACCEPTED.into(),
                by: MERGE_PRODUCER.into(),
                comment: why.clone(),
                at: self.env.now(),
                review_id: review_key.clone(),
                ..Review::default()
            };
            let created = cn.intent == INTENT_CREATED;
            let event = |op: ImpactOp| ImpactEvent {
                change: c.id.clone(),
                impact: cn.id.clone(),
                op,
                by: MERGE_PRODUCER.into(),
                ..ImpactEvent::default()
            };
            // by phase, so that the verdicts of the review and the versions of the commit are one entry each (ADR 0144)
            phases[0].push(ImpactEvent {
                state: Some(declared),
                post: created.then(|| m.next.clone()),
                ..event(if created { ImpactOp::Created } else { ImpactOp::Proposed })
            });
            phases[1].push(ImpactEvent {
                review: Some(review),
                causation: cause.clone(),
                ..event(ImpactOp::Reviewed)
            });
            if !created {
                phases[2].push(ImpactEvent {
                    post: Some(m.next.clone()),
                    ..event(ImpactOp::Transitioned)
                });
            }
            phases[3].push(ImpactEvent {
                committed: Some(m.next.clone()),
                baseline: baseline_id.clone(),
                branch: into.clone(),
                ..event(ImpactOp::Committed)
            });
            if n.deleted {
                target.remove(&n.id);
            } else {
                target.insert(n.id.clone(), n.version);
                if let Some(lc) = self.catalog().lifecycle(&n.typ)
                    && !lc.committable(&n.state)
                {
                    stuck.push(format!("{} ({}) in {}", n.key, n.typ, n.state));
                }
            }
            merged_nodes.push(n);
        }
        for events in phases {
            self.emit(events).await?;
        }
        if !stuck.is_empty() {
            return Err(GraphError::invalid(format!(
                "the change leaves nodes in a state that cannot commit, move them to a committable state first: {}",
                stuck.join(", ")
            )));
        }
        // 2. their links: a three-way merge keyed by type and target node, retargeted to the merged versions
        let new_ref: HashMap<NodeId, NodeRef> = todo.iter().map(|m| (m.next.id.clone(), m.next.clone())).collect();
        for m in &todo {
            if m.join || target.get(&m.next.id) != Some(&m.next.version) {
                continue; // deleted, or joined with the links it has
            }
            let theirs = self.out_links(&m.from.node_ref()).await?;
            let (ours, anc) = match &m.ours {
                // a node new on the target: the links of the source
                None => (theirs.clone(), theirs.clone()),
                Some(o) => {
                    let ours = self.out_links(&o.node_ref()).await?;
                    // created on both lines with no common version: the links of both
                    let anc = match &m.anc {
                        Some(a) => self.out_links(&a.node_ref()).await?,
                        None => Vec::new(),
                    };
                    (ours, anc)
                }
            };
            let mut links = Vec::new();
            for l in merge_links(&anc, &ours, &theirs) {
                let to = match &l.to {
                    Target::External(_) => l.to.clone(),
                    Target::Node(to) => match new_ref.get(&to.id) {
                        Some(r) => {
                            if target.get(&r.id) != Some(&r.version) {
                                continue; // the target was deleted
                            }
                            Target::Node(r.clone())
                        }
                        None if !target.contains_key(&to.id) => continue,
                        None => l.to.clone(),
                    },
                };
                links.push(Link {
                    id: LinkId(self.env.new_id()),
                    typ: l.typ.clone(),
                    from: NodeRef::default(),
                    to,
                    props: l.props.clone(),
                    change_id: ChangeId::default(),
                });
            }
            let mut like = vec![m.from.node_ref()];
            if let Some(o) = &m.ours {
                like.insert(0, o.node_ref());
            }
            let written = Node {
                id: m.next.id.clone(),
                version: m.next.version,
                change_id: c.id.clone(),
                ..Node::default()
            };
            self.put_version_links(&written, &links, &like).await?;
        }
        // 3. the baseline of the target, its head moved; on main its event carries what it changed
        let delta = (into == MAIN_BRANCH).then(|| {
            let mut d: BTreeMap<NodeId, i32> = BTreeMap::new();
            for (id, &v) in &target {
                if into_state.get(id) != Some(&v) {
                    d.insert(id.clone(), v);
                }
            }
            for id in into_state.keys() {
                if !target.contains_key(id) {
                    d.insert(id.clone(), 0);
                }
            }
            d
        });
        let res = Baseline {
            id: baseline_id,
            name: title,
            namespace: ns.clone(),
            branch: plan.into.clone(),
            parent_id: base.id.clone(),
            merged_from: fb.head.clone(),
            change_id: c.id.clone(),
            kind: BASELINE_MERGE.into(),
            created_at: Some(self.env.now()),
            ..Baseline::default()
        };
        let res = self.put_baseline(&res, delta).await?;
        self.advance_branch(&ns, &plan.into, &res).await?;
        c.status = STATUS_APPLIED.into();
        c.result_baseline_id = res.id.clone();
        self.put_change(&c).await?;
        let ev: Vec<EventNode> = merged_nodes.iter().map(EventNode::of).collect();
        // the change as the merge made it, never read from its log (Go's `Change.Seq` 0)
        self.raise_at(CHANGE_MERGED, &c, 0, Some(&res), &into, &ev);
        self.raise_at(CHANGE_APPLIED, &c, 0, Some(&res), "", &[]);
        fb.status = BRANCH_MERGED.into();
        self.put_branch(&fb).await?;
        Ok(MergeResult {
            change: c,
            baseline: res,
            state: target,
            plan,
            merged: merged_nodes,
        })
    }
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    fn props(v: Value) -> Props {
        v.as_object().cloned()
    }

    #[test]
    fn merges_properties_key_by_key() {
        let anc = props(json!({"a": 1, "b": 1, "c": 1, "d": 1}));
        let ours = props(json!({"a": 2, "b": 1, "c": 3, "e": 1}));
        let theirs = props(json!({"a": 1, "b": 2, "c": 4, "d": 1}));
        let (m, conflicts) = merge_props(&anc, &ours, &theirs);
        assert_eq!(m, props(json!({"a": 2, "b": 2, "c": 3, "e": 1})));
        assert_eq!(conflicts, vec!["c".to_string()]);
    }

    #[test]
    fn merges_links_by_type_and_target() {
        let l = |typ: &str, to: &str| Link {
            id: LinkId(format!("{typ}-{to}")),
            typ: typ.into(),
            from: NodeRef::default(),
            to: Target::Node(NodeRef::new(NodeId(to.into()), 1)),
            props: None,
            change_id: ChangeId::default(),
        };
        let anc = [l("t", "a"), l("t", "b")];
        let ours = [l("t", "a"), l("t", "c")];
        let theirs = [l("t", "b"), l("t", "d")];
        let keys: Vec<String> = merge_links(&anc, &ours, &theirs)
            .iter()
            .map(|l| l.id.0.clone())
            .collect();
        assert_eq!(keys, vec!["t-c", "t-d"]);
    }
}
