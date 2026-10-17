//! Port of `merge.go`: the 3-way merge primitives, the branches, and the merge of a branch into another one.

use std::collections::{BTreeMap, BTreeSet, HashMap};

use goap_ctx::Ctx;
use goap_domain::{
    BASELINE_MERGE, BRANCH_ABANDONED, BRANCH_MERGED, BRANCH_OPEN, Baseline, BaselineID, Branch,
    CHANGE_ACTIVE, CHANGE_APPLIED, Change, ChangeID, ChangeImpact, ChangeImpactID, IMPACT_CREATED,
    IMPACT_LANDED, IMPACT_PROPOSED, IMPACT_REVIEWED, IMPACT_TRANSITIONED, INTENT_CREATED,
    INTENT_MODIFIED, ImpactEvent, Link, LinkID, MAIN_BRANCH, Node, NodeID, NodeRef, REASON_MERGE,
    REVIEW_ACCEPTED, REVIEW_OUTCOME_ACCEPT, REVIEW_PROPOSED, Review, ReviewEntry, Version,
    branch_of, json_equal, namespace_of,
};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json};

use crate::api::{MergeCandidate, MergePlan, Resolution};
use crate::apply::next_version;
use crate::changebranch::unresolved;
use crate::lifecycle::invalidf;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

// ---- 3-way merge primitives ----------------------------------------------

/// DeletedKey is reported as a conflict when one side deleted the node and the other side modified it.
pub const DELETED_KEY: &str = "(deleted)";

/// MergeProps merges two property maps against their common ancestor, key by key: equal sides win; a side equal to
/// the ancestor takes the other one; otherwise the key is a conflict and ours is kept. A key absent from a side is a
/// deletion on that side.
pub fn merge_props(
    anc: &Map<String, Value>,
    ours: &Map<String, Value>,
    theirs: &Map<String, Value>,
) -> (Map<String, Value>, Vec<String>) {
    let mut merged = Map::new();
    let mut conflicts = Vec::new();
    let keys: BTreeSet<&String> = anc.keys().chain(ours.keys()).chain(theirs.keys()).collect();
    for k in keys {
        let (a, o, t) = (anc.get(k), ours.get(k), theirs.get(k));
        let v = if same(o, t) || same(t, a) {
            o
        } else if same(o, a) {
            t
        } else {
            conflicts.push(k.clone());
            o
        };
        if let Some(v) = v {
            merged.insert(k.clone(), v.clone());
        }
    }
    (merged, conflicts)
}

fn same(a: Option<&Value>, b: Option<&Value>) -> bool {
    match (a, b) {
        (None, None) => true,
        (Some(a), Some(b)) => json_equal(a, b),
        _ => false,
    }
}

/// MergeLinks merges outgoing link sets keyed by (type, target node): a link is kept when present on both sides, or
/// added on one side since the ancestor; it is dropped when removed on one side. Ours is preferred when both have it.
pub fn merge_links(anc: &[Link], ours: &[Link], theirs: &[Link]) -> Vec<Link> {
    let key = |l: &Link| format!("{}\x00{}", l.r#type, l.to.id);
    let index =
        |ls: &[Link]| -> HashMap<String, Link> { ls.iter().map(|l| (key(l), l.clone())).collect() };
    let (a, o, t) = (index(anc), index(ours), index(theirs));
    let keys: BTreeSet<&String> = o.keys().chain(t.keys()).collect();
    let mut out = Vec::new();
    for k in keys {
        let (ol, tl, in_a) = (o.get(k), t.get(k), a.contains_key(k));
        match (ol, tl) {
            (Some(ol), Some(_)) => out.push(ol.clone()),
            (Some(ol), None) if !in_a => out.push(ol.clone()),
            (None, Some(tl)) if !in_a => out.push(tl.clone()),
            _ => {}
        }
    }
    out
}

/// CommonAncestor returns the most recent common ancestor of two versions of a node (a version is its own ancestor).
pub async fn common_ancestor(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    id: &NodeID,
    a: Version,
    b: Version,
) -> Result<NodeRef> {
    let vs = tx.versions(ctx, id).await?;
    let parents: HashMap<Version, Vec<Version>> =
        vs.into_iter().map(|n| (n.version, n.parents)).collect();
    let ancestors = |v: Version| {
        let mut seen = std::collections::HashSet::new();
        let mut stack = vec![v];
        while let Some(x) = stack.pop() {
            if !seen.insert(x) {
                continue;
            }
            if let Some(ps) = parents.get(&x) {
                stack.extend(ps.iter().copied());
            }
        }
        seen
    };
    let (sa, sb) = (ancestors(a), ancestors(b));
    let best = sa
        .iter()
        .filter(|v| sb.contains(v))
        .copied()
        .max()
        .unwrap_or(0);
    if best == 0 {
        return Err(Error::wrap(
            ErrorKind::NotFound,
            format!("versions {a} and {b} of node {id} have no common ancestor"),
        ));
    }
    Ok(NodeRef {
        id: id.clone(),
        version: best,
    })
}

// ---- Branches --------------------------------------------------------------

/// NewBranch describes a branch to open from a baseline.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct NewBranch {
    pub name: String,
    /// Namespace the branch belongs to (default: the default namespace).
    pub namespace: String,
    pub from: BaselineID,
    /// Origin is the change / option that opens the branch.
    pub origin: String,
    pub description: String,
}

// ---- Branch merge ------------------------------------------------------------

/// Merge candidate kinds: a node created on the source branch, unchanged on the target branch, changed on both.
pub const MERGE_ADDED: &str = "added";
pub const MERGE_FAST_FORWARD: &str = "fast_forward";
pub const MERGE_THREE_WAY: &str = "merge";

/// MergeRequest merges a branch into another one.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct MergeRequest {
    pub from: String,
    pub into: String,
    pub title: String,
    /// Namespace of the merge change (default: the default namespace).
    pub namespace: String,
    pub resolutions: BTreeMap<NodeID, Resolution>,
    /// OwnerOrg is the unit holding the merge change and ProjectID the project it acts in (ADR 0054); empty: the root
    /// unit. The project is required (ADR 0091).
    pub owner_org: String,
    pub project_id: String,
}

/// MergeResult is the merge change and the resulting baseline of the target branch.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct MergeResult {
    pub change: Change,
    pub baseline: Baseline,
    pub plan: MergePlan,
}

impl Graph {
    /// CreateBranch opens a branch forked from a baseline.
    pub async fn create_branch(&self, ctx: &Ctx, input: NewBranch) -> Result<Branch> {
        if input.name.is_empty()
            || input.name == MAIN_BRANCH
            || input.name.contains([' ', '\t', '\n'])
        {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("invalid branch name {:?}", input.name),
            ));
        }
        let namespace = namespace_of(&input.namespace).to_string();
        let mut tx = self.begin(ctx).await?;
        match tx.branch(ctx, &namespace, &input.name).await {
            Ok(_) => {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!(
                        "branch {} already exists in namespace {namespace}",
                        input.name
                    ),
                ));
            }
            Err(e) if !e.is_not_found() => return Err(e),
            Err(_) => {}
        }
        let fork = tx.baseline(ctx, &input.from).await?;
        if !fork.id.is_empty() && fork.namespace != namespace {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "baseline {} is of namespace {}, not {namespace}",
                    input.from, fork.namespace
                ),
            ));
        }
        let b = Branch {
            name: input.name.clone(),
            namespace,
            parent: branch_of(&fork.branch).to_string(),
            fork_baseline: fork.id.clone(),
            head: fork.id,
            origin: input.origin,
            status: BRANCH_OPEN.to_string(),
            created_at: self.now(),
            description: input.description,
            ..Default::default()
        };
        tx.put_branch(ctx, b.clone()).await?;
        tx.commit(ctx).await?;
        Ok(b)
    }

    /// Branches lists the branches of a namespace (main appears once it has been applied to).
    pub async fn branches(&self, ctx: &Ctx, namespace: &str) -> Result<Vec<Branch>> {
        let mut tx = self.begin(ctx).await?;
        let bs = tx.branches(ctx, namespace).await?;
        tx.commit(ctx).await?;
        Ok(bs)
    }

    /// Branch returns a branch of a namespace.
    pub async fn branch(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Branch> {
        let mut tx = self.begin(ctx).await?;
        let b = branch_of_tx(ctx, &mut tx, namespace, name).await?;
        tx.commit(ctx).await?;
        Ok(b)
    }

    /// SetBranchStatus closes a branch (merged / abandoned) or reopens it.
    pub async fn set_branch_status(
        &self,
        ctx: &Ctx,
        namespace: &str,
        name: &str,
        status: &str,
    ) -> Result<()> {
        if status != BRANCH_OPEN && status != BRANCH_MERGED && status != BRANCH_ABANDONED {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("invalid branch status {status:?}"),
            ));
        }
        let mut tx = self.begin(ctx).await?;
        let mut b = tx.branch(ctx, namespace, name).await?;
        b.status = status.to_string();
        tx.put_branch(ctx, b).await?;
        tx.commit(ctx).await
    }

    /// SetBranchDescription sets the free-text description of a branch (main included: unlike CreateBranch, this may
    /// target main, persisting its row on first use).
    pub async fn set_branch_description(
        &self,
        ctx: &Ctx,
        namespace: &str,
        name: &str,
        description: &str,
    ) -> Result<()> {
        let mut tx = self.begin(ctx).await?;
        let mut b = branch_of_tx(ctx, &mut tx, namespace, name).await?;
        b.description = description.to_string();
        tx.put_branch(ctx, b).await?;
        tx.commit(ctx).await
    }

    /// PlanMerge computes the 3-way merge of branch from into branch into, both of namespace.
    pub async fn plan_merge(
        &self,
        ctx: &Ctx,
        namespace: &str,
        from: &str,
        into: &str,
    ) -> Result<MergePlan> {
        let mut tx = self.begin(ctx).await?;
        let p = plan_merge(ctx, &mut tx, namespace, from, into).await?;
        tx.commit(ctx).await?;
        Ok(p)
    }

    /// MergeBranch merges branch From into Into: a merge change with one change impact per merged node is created, the
    /// merge versions are written on Into, and From is marked merged. Conflicts without a resolution fail with
    /// Conflict.
    pub async fn merge_branch(&self, ctx: &Ctx, input: MergeRequest) -> Result<MergeResult> {
        self.bootstrap(ctx).await?;
        let mut tx = self.begin(ctx).await?;
        let res = self.merge_branch_tx(ctx, &mut tx, input).await?;
        tx.commit(ctx).await?;
        Ok(res)
    }

    pub(crate) async fn merge_branch_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        input: MergeRequest,
    ) -> Result<MergeResult> {
        let namespace = namespace_of(&input.namespace).to_string();
        let plan = plan_merge(ctx, tx, &namespace, &input.from, &input.into).await?;
        let un = unresolved(&plan, &input.resolutions);
        if !un.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "merge {} into {}: unresolved conflicts on {}",
                    input.from,
                    plan.into,
                    un.join(", ")
                ),
            ));
        }
        let title = if input.title.is_empty() {
            format!("merge {} into {}", input.from, plan.into)
        } else {
            input.title.clone()
        };
        // the merge is a change of the platform (journal): one change impact per merged node, whose version records
        // the origin; the merge versions are written here, not proposed
        let mut data = Map::new();
        data.insert(
            "merge".into(),
            json!({"from": input.from, "into": plan.into}),
        );
        let mut c = Change {
            id: ChangeID::new(self.new_id()),
            title: title.clone(),
            intent: title.clone(),
            status: CHANGE_ACTIVE,
            namespace: namespace.clone(),
            baseline_id: plan.into_head.clone(),
            branch: plan.into.clone(),
            created_at: self.now(),
            data,
            owner_org: input.owner_org.clone(),
            project_id: input.project_id.clone(),
            ..Default::default()
        };
        self.scope_change(ctx, tx, &mut c).await?;
        tx.put_change(ctx, c.clone()).await?;
        let base = tx.baseline(ctx, &plan.into_head).await?;
        let mut fb = tx.branch(ctx, &namespace, &input.from).await?;
        let ix = self.types_at();
        let mut target = base.nodes.clone();
        struct Merged {
            cand: MergeCandidate,
            from: Node,
            ours: Option<Node>,
            anc: Option<Node>,
            next: NodeRef,
            // join: the source version lands as is (added or fast-forward, no resolution), no merge version (ADR
            // 0032)
            join: bool,
        }
        let mut todo: Vec<Merged> = Vec::new();
        for cand in &plan.candidates {
            let r = input.resolutions.get(&cand.node);
            if r.is_some_and(|r| r.skip) || (cand.ours.is_none() && cand.ancestor.is_some()) {
                continue; // kept as is on the target (or deleted there)
            }
            let resolved_props = r.filter(|r| !r.props.is_empty()).map(|r| r.props.clone());
            let mut from = tx.node(ctx, &cand.theirs).await?;
            let join = (cand.kind == MERGE_ADDED || cand.kind == MERGE_FAST_FORWARD)
                && resolved_props.is_none();
            let mut props =
                resolved_props.or_else(|| (!cand.merged.is_empty()).then(|| cand.merged.clone()));
            let mut m = Merged {
                cand: cand.clone(),
                from: Node::default(),
                ours: None,
                anc: None,
                next: NodeRef::default(),
                join,
            };
            if let Some(our_ref) = &cand.ours {
                if target.get(&our_ref.id) != Some(&our_ref.version) {
                    return Err(Error::wrap(
                        ErrorKind::Conflict,
                        format!("node {our_ref} is not in the reference baseline"),
                    ));
                }
                match tx.latest_on(ctx, &our_ref.id, &plan.into).await {
                    Ok(head) if head.version != our_ref.version => {
                        return Err(Error::wrap(
                            ErrorKind::Conflict,
                            format!(
                                "node {our_ref} was modified on {} since the reference baseline (now v{})",
                                plan.into, head.version
                            ),
                        ));
                    }
                    Ok(_) => {}
                    Err(e) if e.is_not_found() => {}
                    Err(e) => return Err(e),
                }
                let ours = tx.node(ctx, our_ref).await?;
                let anc = match &cand.ancestor {
                    Some(a) => a.clone(),
                    None => {
                        common_ancestor(ctx, tx, &from.id, our_ref.version, from.version).await?
                    }
                };
                let an = tx.node(ctx, &anc).await?;
                if props.is_none() {
                    props = Some(merge_props(&an.properties, &ours.properties, &from.properties).0);
                }
                m.ours = Some(ours);
                m.anc = Some(an);
            } else {
                if target.contains_key(&from.id) {
                    return Err(Error::wrap(
                        ErrorKind::Conflict,
                        format!(
                            "node {} is already in the reference baseline, merge needs its version there",
                            from.id
                        ),
                    ));
                }
                if props.is_none() {
                    props = Some(from.properties.clone());
                }
            }
            from.properties = props.unwrap_or_default(); // the merged properties, carried by the version below
            m.from = from;
            todo.push(m);
        }
        // 1. the merge versions, with the change impact that explains each: a node changed on one side only joins the
        // target as is (ADR 0032), a node changed on both sides gets a merge version
        let result_id = BaselineID::new(self.new_id());
        // the merge accepts what it lands in one review, recorded before the impacts it decides (ADR 0105)
        let why_of = |m: &Merged| {
            let mut why = format!("merge of {} into {}", input.from, plan.into);
            if !m.cand.conflicts.is_empty() {
                why += &format!(" (conflicts on {}, resolved)", m.cand.conflicts.join(", "));
            }
            why
        };
        let mut impact_ids = Vec::with_capacity(todo.len());
        let (mut review_key, mut cause) = (String::new(), String::new());
        if !todo.is_empty() {
            let mut entries = Vec::with_capacity(todo.len());
            for m in &todo {
                let id = ChangeImpactID::new(self.new_id());
                entries.push(ReviewEntry {
                    impact: id.to_string(),
                    comment: why_of(m),
                    outcome: REVIEW_OUTCOME_ACCEPT.to_string(),
                });
                impact_ids.push(id);
            }
            (review_key, cause) = self
                .put_review_record(
                    ctx,
                    tx,
                    &c.id,
                    "graph.merge",
                    &format!("merge of {} into {}", input.from, plan.into),
                    entries,
                )
                .await?;
        }
        let mut stuck = Vec::new();
        for (i, m) in todo.iter_mut().enumerate() {
            let mut n = m.from.clone();
            n.reason = REASON_MERGE.to_string();
            n.parents = vec![m.from.version];
            let mut deleted = m.from.deleted;
            if let Some(ours) = &m.ours {
                n.parents = vec![ours.version, m.from.version];
                deleted = deleted || ours.deleted;
            }
            let why = why_of(m);
            let mut cn = ChangeImpact {
                id: impact_ids[i].clone(),
                key: n.key.clone(),
                r#type: n.r#type.clone(),
                intent: INTENT_CREATED,
                rationale: why.clone(),
                review: REVIEW_ACCEPTED,
                produced_by: "graph.merge".into(),
                created_at: self.now(),
                ..Default::default()
            };
            if m.ours.is_some() {
                cn.intent = INTENT_MODIFIED;
                cn.pre = m.cand.ours.clone();
            }
            if m.join {
                n = m.from.clone();
                tx.join_branch(ctx, &n.r#ref(), &plan.into, &c.id).await?;
            } else {
                n.version = next_version(ctx, tx, &n.id).await?;
                n.branch = plan.into.clone();
                n.deleted = deleted;
                n.change_id = c.id.clone();
                n.change_impact = cn.id.clone();
                n.comment = why.clone();
                n.created_at = self.now();
                tx.put_node(ctx, n.clone()).await?;
            }
            m.next = n.r#ref();
            // the merge records its change impact as events, accepted and landed at once: a node the merge creates on
            // the target is a created event (the impact and its version), one it modifies is proposed, then written
            let mut declared = cn.clone();
            declared.review = REVIEW_PROPOSED;
            let review = Review {
                status: REVIEW_ACCEPTED,
                by: "graph.merge".into(),
                comment: why.clone(),
                at: self.now(),
                review_id: review_key.clone(),
                ..Default::default()
            };
            let mut first = ImpactEvent {
                change: c.id.clone(),
                impact: cn.id.clone(),
                op: IMPACT_PROPOSED,
                by: "graph.merge".into(),
                state: Some(declared),
                ..Default::default()
            };
            let mut written = Vec::new();
            if cn.intent == INTENT_CREATED {
                first.op = IMPACT_CREATED;
                first.post = Some(m.next.clone());
            } else {
                written.push(ImpactEvent {
                    change: c.id.clone(),
                    impact: cn.id.clone(),
                    op: IMPACT_TRANSITIONED,
                    by: "graph.merge".into(),
                    post: Some(m.next.clone()),
                    ..Default::default()
                });
            }
            let mut events = vec![
                first,
                ImpactEvent {
                    change: c.id.clone(),
                    impact: cn.id.clone(),
                    op: IMPACT_REVIEWED,
                    by: "graph.merge".into(),
                    review: Some(review),
                    causation: cause.clone(),
                    ..Default::default()
                },
            ];
            events.extend(written);
            events.push(ImpactEvent {
                change: c.id.clone(),
                impact: cn.id.clone(),
                op: IMPACT_LANDED,
                by: "graph.merge".into(),
                landed: Some(m.next.clone()),
                baseline: result_id.clone(),
                branch: branch_of(&plan.into).to_string(),
                ..Default::default()
            });
            self.emit(ctx, tx, events).await?;
            if n.deleted {
                target.remove(&n.id);
                continue;
            }
            target.insert(n.id.clone(), n.version);
            if let Some(lc) = ix.lifecycle_of(&n.r#type)
                && !lc.landable(&n.state)
            {
                stuck.push(format!("{} ({}) in {}", n.key, n.r#type, n.state));
            }
        }
        if !stuck.is_empty() {
            return Err(invalidf(format!(
                "the change leaves nodes in a state that cannot land, move them to a landable state first: {}",
                stuck.join(", ")
            )));
        }
        // 2. their links: a 3-way merge keyed by (type, target node), retargeted to the merged versions
        let new_ref: HashMap<NodeID, NodeRef> = todo
            .iter()
            .map(|m| (m.next.id.clone(), m.next.clone()))
            .collect();
        for m in &todo {
            if target.get(&m.next.id) != Some(&m.next.version) || m.join {
                continue; // deleted, or joined with the links it has
            }
            let theirs = tx.out_links(ctx, &m.from.r#ref()).await?;
            // a node new on the target: take the links of the source
            let (mut ours, mut anc) = (theirs.clone(), theirs.clone());
            if let (Some(a), Some(o)) = (&m.anc, &m.ours) {
                ours = tx.out_links(ctx, &o.r#ref()).await?;
                anc = tx.out_links(ctx, &a.r#ref()).await?;
            }
            for l in merge_links(&anc, &ours, &theirs) {
                let mut to = l.to.clone();
                if let Some(r) = new_ref.get(&to.id) {
                    if target.get(&r.id) != Some(&r.version) {
                        continue; // the target was deleted
                    }
                    to = r.clone();
                } else if !target.contains_key(&to.id) {
                    continue;
                }
                tx.put_link(
                    ctx,
                    Link {
                        id: LinkID::new(self.new_id()),
                        r#type: l.r#type.clone(),
                        from: m.next.clone(),
                        to,
                        properties: l.properties.clone(),
                        change_id: c.id.clone(),
                    },
                )
                .await?;
            }
        }
        let baseline = Baseline {
            id: result_id,
            name: title,
            namespace: namespace.clone(),
            branch: plan.into.clone(),
            parent_id: base.id.clone(),
            merged_from: fb.head.clone(),
            change_id: c.id.clone(),
            kind: BASELINE_MERGE.into(),
            nodes: target,
            created_at: self.now(),
            ..Default::default()
        };
        tx.put_baseline(ctx, baseline.clone()).await?;
        self.advance_branch(ctx, tx, &namespace, &plan.into, &baseline)
            .await?;
        c.status = CHANGE_APPLIED;
        c.result_baseline_id = baseline.id.clone();
        tx.put_change(ctx, c.clone()).await?;
        fb.status = BRANCH_MERGED.to_string();
        tx.put_branch(ctx, fb).await?;
        Ok(MergeResult {
            change: c,
            baseline,
            plan,
        })
    }

    /// BranchHead returns the latest baseline of a branch of a namespace; the head of main before any change landed
    /// is the empty state (empty id, no nodes).
    pub async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> Result<Baseline> {
        let mut tx = self.begin(ctx).await?;
        let b = branch_head(ctx, &mut tx, namespace, name).await?;
        tx.commit(ctx).await?;
        Ok(b)
    }

    /// Versions returns every version of a node across branches (version graph).
    pub async fn versions(&self, ctx: &Ctx, id: &NodeID) -> Result<Vec<Node>> {
        let mut tx = self.begin(ctx).await?;
        let vs = tx.versions(ctx, id).await?;
        tx.commit(ctx).await?;
        Ok(vs)
    }
}

/// branchOf returns a branch of a namespace; main exists implicitly.
pub(crate) async fn branch_of_tx(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    namespace: &str,
    name: &str,
) -> Result<Branch> {
    let (namespace, name) = (namespace_of(namespace), branch_of(name));
    match tx.branch(ctx, namespace, name).await {
        Err(e) if e.is_not_found() && name == MAIN_BRANCH => {
            let head = match branch_head(ctx, tx, namespace, name).await {
                Ok(h) => h,
                Err(e) if e.is_not_found() => Baseline::default(),
                Err(e) => return Err(e),
            };
            Ok(Branch {
                name: name.to_string(),
                namespace: namespace.to_string(),
                head: head.id,
                status: BRANCH_OPEN.to_string(),
                ..Default::default()
            })
        }
        r => r,
    }
}

/// branchHead returns the head baseline of a branch: its head, else (main without applied changes) the latest
/// baseline of main, else the empty state (ADR 0056), the empty baseline id.
pub(crate) async fn branch_head(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    namespace: &str,
    name: &str,
) -> Result<Baseline> {
    let (namespace, name) = (namespace_of(namespace), branch_of(name));
    match tx.branch(ctx, namespace, name).await {
        Ok(b) if !b.head.is_empty() => return tx.baseline(ctx, &b.head).await,
        Ok(_) => {}
        Err(e) if !e.is_not_found() || name != MAIN_BRANCH => return Err(e),
        Err(_) => {}
    }
    // main without applied changes: its latest baseline, if any change left one
    let bs = tx.baselines(ctx, namespace).await?;
    for b in bs.into_iter().rev() {
        if branch_of(&b.branch) == name {
            return Ok(b);
        }
    }
    Ok(Baseline {
        namespace: namespace.to_string(),
        branch: name.to_string(),
        ..Default::default()
    })
}

/// onBranch reports whether a node version is part of a branch: written there, or joined (ADR 0032).
pub(crate) async fn on_branch(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    r: &goap_domain::NodeRef,
    branch: &str,
) -> Result<bool> {
    let vs = tx.versions(ctx, &r.id).await?;
    Ok(vs
        .iter()
        .find(|n| n.version == r.version)
        .is_some_and(|n| n.on(branch)))
}

/// planMerge computes the 3-way merge of branch from into branch into, both of namespace.
pub(crate) async fn plan_merge(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    namespace: &str,
    from: &str,
    into: &str,
) -> Result<MergePlan> {
    let (namespace, into) = (namespace_of(namespace), branch_of(into));
    let fb = tx.branch(ctx, namespace, from).await?;
    if fb.status != BRANCH_OPEN {
        return Err(Error::wrap(
            ErrorKind::Conflict,
            format!("branch {from} is {}", fb.status),
        ));
    }
    let fork = tx.baseline(ctx, &fb.fork_baseline).await?;
    let from_head = tx.baseline(ctx, &fb.head).await?;
    let into_head = branch_head(ctx, tx, namespace, into).await?;
    let mut plan = MergePlan {
        from: from.to_string(),
        into: into.to_string(),
        into_head: into_head.id.clone(),
        ..Default::default()
    };
    let mut theirs = Vec::new();
    for (id, v) in &from_head.nodes {
        if fork.nodes.get(id) != Some(v) {
            theirs.push(
                tx.node(
                    ctx,
                    &NodeRef {
                        id: id.clone(),
                        version: *v,
                    },
                )
                .await?,
            );
        }
    }
    for id in fork.nodes.keys() {
        if from_head.nodes.contains_key(id) {
            continue;
        }
        match tx.latest_on(ctx, id, from).await {
            Ok(n) if n.deleted => theirs.push(n),
            Ok(_) => {}
            Err(e) if e.is_not_found() => {}
            Err(e) => return Err(e),
        }
    }
    theirs.sort_by(|a, b| a.key.cmp(&b.key));
    for t in theirs {
        let mut c = MergeCandidate {
            node: t.id.clone(),
            key: t.key.clone(),
            r#type: t.r#type.clone(),
            theirs: t.r#ref(),
            deleted: t.deleted,
            theirs_props: t.properties.clone(),
            ..Default::default()
        };
        let ov = into_head.nodes.get(&t.id).copied();
        let in_fork = fork.nodes.get(&t.id).copied();
        match (ov, in_fork) {
            (None, None) => {
                if t.deleted {
                    continue;
                }
                c.kind = MERGE_ADDED.into();
                c.merged = t.properties.clone();
            }
            (None, Some(fv)) => {
                // deleted on the target branch, changed on the source branch
                if t.deleted {
                    continue;
                }
                c.kind = MERGE_THREE_WAY.into();
                c.ancestor = Some(NodeRef {
                    id: t.id.clone(),
                    version: fv,
                });
                c.merged = t.properties.clone();
                c.conflicts = vec![DELETED_KEY.to_string()];
            }
            (Some(ov), _) => {
                let ours = tx
                    .node(
                        ctx,
                        &NodeRef {
                            id: t.id.clone(),
                            version: ov,
                        },
                    )
                    .await?;
                let anc = common_ancestor(ctx, tx, &t.id, ov, t.version).await?;
                if anc.version == t.version {
                    continue; // already merged
                }
                let an = tx.node(ctx, &anc).await?;
                c.ours = Some(ours.r#ref());
                c.ancestor = Some(anc.clone());
                c.base = an.properties.clone();
                c.ours_props = ours.properties.clone();
                if anc.version == ov {
                    c.kind = MERGE_FAST_FORWARD.into();
                    c.merged = t.properties.clone();
                } else {
                    c.kind = MERGE_THREE_WAY.into();
                    (c.merged, c.conflicts) =
                        merge_props(&an.properties, &ours.properties, &t.properties);
                    if t.deleted {
                        c.conflicts = vec![DELETED_KEY.to_string()];
                    }
                }
            }
        }
        plan.candidates.push(c);
    }
    Ok(plan)
}
