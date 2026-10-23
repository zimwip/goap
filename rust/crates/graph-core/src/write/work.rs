//! How a change edits nodes (Go's `checkout.go`, `changeimpact.go`, `impactevents.go`; ADR 0029, 0076, 0079): every
//! operation appends impact events to the log of the change and updates the `change_impact` projection, and the view
//! of the change (its impacts and drafts) is the fold of that log, kept by the transaction and brought up by its own
//! writes. A node has no version during a change: a creation or a checkout gives it a draft, the edits patch it.
//!
//! The service serves the simple world here: the main flow of a change with no option, no lifecycle, no sub-change and
//! no execution recorded in its log. Anything else answers [`crate::Kind::Unimplemented`], and the proxy replays the
//! request on the Go service.
use std::collections::{HashMap, HashSet};

use serde_json::{Map, Value};

use super::{
    change::{INTENT_DERIVE, STATUS_ACTIVE, STATUS_DRAFT, change_branch_name, change_branch_origin},
    check,
    tx::Tx,
};
use crate::{
    GraphError, Result,
    change::Change,
    impact::{
        self, BaseCopy, BaseLink, BaseNode, ChangeImpact, Draft, DraftLink, Fold, INTENT_CREATED, INTENT_MODIFIED,
        ImpactEvent, ImpactOp, LOG_FACT, LOG_IMPACT, REVIEW_PROPOSED, Review,
    },
    lnk::Url,
    model::{
        BRANCH_OPEN, Branch, ChangeId, Link, LinkId, MAIN_BRANCH, Node, NodeId, NodeRef, Props, Target, branch_of,
        namespace_of,
    },
    read,
    store::{ReadTx, WriteTx},
};

/// The entries of a log the service folds: the impact events and the review records (ADR 0105).
/// The facts that make the view of a change (its options, its state, the decisions of its options): the Go service's. The
/// others (reviews, artifacts, signals, the records of the use cases) are items of the change only.
const VIEW_FACTS: [&str; 3] = ["fact.flow", "fact.transition", "fact.decision"];

/// The view of a change: the fold of its log, up to a position.
#[derive(Debug, Clone, Default)]
pub struct ChangeView {
    pub fold: Fold,
    pub seq: i64,
    /// the impact events of the log, in order, the checkouts hydrated
    pub events: Vec<ImpactEvent>,
}

/// What an operation on a change impact works on: the change, the branch its versions are written on, and the impact
/// as the main flow sees it.
#[derive(Debug, Clone)]
pub struct Work {
    pub c: Change,
    pub branch: String,
    pub ns: String,
    /// the impacts the main flow sees
    pub seen: Vec<ChangeImpact>,
    /// the stored impact, and as the flow sees it (the same on the main flow)
    pub cn: ChangeImpact,
    pub vcn: ChangeImpact,
    /// a creation names the key of a planned creation (ADR 0121): its draft fulfils that impact
    pub planned: bool,
}

impl Work {
    /// The impact as the operation leaves it, with its post.
    pub fn seen_as(&self, post: &NodeRef) -> ChangeImpact {
        let mut cn = self.vcn.clone();
        cn.post = Some(post.clone());
        cn
    }

    /// Points the version a node of the change was checked out from at its draft (Go's `normalizeTo`).
    fn normalize_to(&self, to: &NodeRef) -> NodeRef {
        for cn in &self.seen {
            if let Some(post) = &cn.post
                && post.is_draft()
                && post.id == to.id
                && cn.pre.as_ref() == Some(to)
            {
                return post.clone();
            }
        }
        to.clone()
    }
}

/// What an operation names: a change impact, or a node (by id, or by key and type).
#[derive(Debug, Clone, Default)]
pub struct Named {
    pub impact: String,
    pub node: NodeId,
    pub key: String,
    pub typ: String,
    /// the operation creates the node: the key must be new
    pub create: bool,
    pub rationale: String,
    pub execution: String,
    pub produced_by: String,
}

/// An outgoing link a write gives a draft: to an exact version or a draft of the change (a URL of another source is
/// the Go service's, ADR 0154).
#[derive(Debug, Clone, Default)]
pub struct LinkWrite {
    pub typ: String,
    pub to: NodeRef,
    pub target: Url,
    pub props: Props,
}

/// A node a change creates.
#[derive(Debug, Clone, Default)]
pub struct NodeCreate {
    pub key: String,
    pub typ: String,
    pub props: Props,
    pub owner: String,
    pub rationale: String,
    pub links: Vec<LinkWrite>,
    pub execution: String,
    pub produced_by: String,
}

/// A node a change checks out.
#[derive(Debug, Clone, Default)]
pub struct NodeCheckout {
    pub impact: String,
    pub node: NodeId,
    pub key: String,
    pub rationale: String,
    pub execution: String,
    pub produced_by: String,
}

/// The flow an operation names: the main flow only here.
fn on_main(flow: &str) -> Result<()> {
    if flow.is_empty() {
        Ok(())
    } else {
        Err(unserved("a write on an option of a change (ADR 0032)"))
    }
}

fn draft_of(cn: &ChangeImpact) -> String {
    format!("change impact {} ({})", cn.id, cn.key)
}

fn unserved(what: &str) -> GraphError {
    GraphError::unimplemented(format!("{what}: not served here yet"))
}

/// The node a stored link points to, and the URL of another source.
fn ends(l: &Link) -> (NodeRef, Url) {
    match &l.to {
        Target::Node(r) => (r.clone(), Url::default()),
        Target::External(u) => (NodeRef::default(), u.clone()),
    }
}

impl<S: WriteTx> Tx<'_, S> {
    /// The view of a change, folded from its log the first time the transaction works on it.
    pub async fn view(&mut self, id: &ChangeId) -> Result<&ChangeView> {
        self.lock_change(id).await?;
        if !self.views.contains_key(id) {
            let v = self.fold_log(id).await?;
            self.views.insert(id.clone(), v);
        }
        Ok(&self.views[id])
    }

    async fn fold_log(&self, id: &ChangeId) -> Result<ChangeView> {
        fold_change(self.store, id).await
    }

    /// Appends events to the log of their change and applies each one to the projection and to the view (Go's
    /// `emit`): the events of one batched operation that follow each other with one envelope are one entry (ADR 0144).
    pub async fn emit(&mut self, mut events: Vec<ImpactEvent>) -> Result<()> {
        let now = self.env.now();
        for e in events.iter_mut() {
            if e.at == crate::gojson::zero_time() {
                e.at = now;
            }
            if e.by.is_empty() {
                e.by = self.env.by.clone();
            }
        }
        while !events.is_empty() {
            let mut n = 1;
            while n < events.len() && events[0].same_entry(&events[n]) {
                n += 1;
            }
            let rest = events.split_off(n);
            self.emit_entry(events, false).await?;
            events = rest;
        }
        Ok(())
    }

    /// Appends events staged together as one entry, a batch even of one (Go's `stage` then `flush`: the verdicts of
    /// a review, ADR 0144); they share the envelope of the first.
    pub async fn emit_staged(&mut self, mut events: Vec<ImpactEvent>) -> Result<()> {
        if events.is_empty() {
            return Ok(());
        }
        let now = self.env.now();
        for e in events.iter_mut() {
            if e.at == crate::gojson::zero_time() {
                e.at = now;
            }
            if e.by.is_empty() {
                e.by = self.env.by.clone();
            }
        }
        if let Some(e) = events.iter().skip(1).find(|e| !events[0].same_entry(e)) {
            return Err(GraphError::internal(format!(
                "change {}: event {} of {} is not staged with {} of {}",
                e.change,
                e.op.as_str(),
                e.impact,
                events[0].op.as_str(),
                events[0].impact
            )));
        }
        self.emit_entry(events, true).await
    }

    async fn emit_entry(&mut self, mut events: Vec<ImpactEvent>, staged: bool) -> Result<()> {
        let change = events[0].change.clone();
        let id = self.env.new_id();
        let at = events[0].at;
        let many = staged || events.len() > 1;
        for (i, e) in events.iter_mut().enumerate() {
            if many {
                e.id = impact::part_id(&id, i);
            } else if e.id.is_empty() {
                e.id = id.clone();
            }
            e.at = at;
            e.validate()
                .map_err(|err| GraphError::invalid(format!("change {}: {err}", e.change)))?;
        }
        self.view(&change).await?;
        let entry = if many {
            impact::batch_entry(&id, &events).map_err(GraphError::internal)?
        } else {
            impact::entry(&events[0])
        };
        let seq = self.append_log(&entry).await?;
        for e in events.iter_mut() {
            e.seq = seq;
        }
        for e in &events {
            self.project(e).await?;
            let v = self.views.get_mut(&change).expect("the view of the change");
            v.fold.apply(e);
            v.seq = seq;
        }
        Ok(())
    }

    /// Applies an event to the `change_impact` projection, the view being the fold before it (Go's `project`).
    async fn project(&mut self, e: &ImpactEvent) -> Result<()> {
        if e.op == ImpactOp::Adopted {
            return Err(unserved("the adoption of an option"));
        }
        let cur: Vec<ChangeImpact> = self.views[&e.change]
            .fold
            .impact(&e.impact)
            .cloned()
            .into_iter()
            .collect();
        let mut next = cur.clone();
        let at = if next.is_empty() { None } else { Some(0) };
        impact::apply_impact_event(&mut next, at, e);
        for c in &cur {
            if !next.iter().any(|n| n.id == c.id) {
                self.delete_change_impact(&e.change, &c.id).await?;
            }
        }
        let changed: Vec<ChangeImpact> = next.into_iter().filter(|n| !cur.iter().any(|c| c == n)).collect();
        for first in [true, false] {
            for cn in changed.iter().filter(|cn| cn.superseded == first) {
                self.put_change_impact(&e.change, cn).await?;
            }
        }
        Ok(())
    }

    /// Opens a change to write in it (Go's `changeOpen`): locked, and still taking impacts.
    pub async fn change_open(&mut self, id: &ChangeId) -> Result<Change> {
        self.lock_change(id).await?;
        let c = self.change_head(id).await?;
        if matches!(c.status.as_str(), "applied" | "abandoned" | "committed") {
            return Err(GraphError::conflict(format!("change {id} is {}", c.status)));
        }
        Ok(c)
    }

    /// Gives a root change the branch its versions are written on when it commits (Go's `ensureOwnBranch`).
    async fn ensure_own_branch(&mut self, mut c: Change) -> Result<Change> {
        if branch_of(&c.branch) != MAIN_BRANCH
            && let Some(b) = self.store.branch(&c.namespace, &c.branch).await?
            && b.origin == change_branch_origin(&c.id)
        {
            return Ok(c);
        }
        if !c.parent_id.is_empty() {
            return Err(GraphError::invalid(format!(
                "change {} is a sub-change without a branch of its own",
                c.id
            )));
        }
        let parent = read::branch(self.store, &c.namespace, &c.branch).await?;
        if parent.status != BRANCH_OPEN {
            return Err(GraphError::conflict(format!(
                "branch {} is {}",
                parent.name, parent.status
            )));
        }
        let fork = read::header_of(self.store, &c.baseline_id).await?;
        let own = Branch {
            ephemeral: true,
            name: change_branch_name(&c.id),
            namespace: c.namespace.clone(),
            parent: parent.name,
            fork_baseline: fork.id,
            origin: change_branch_origin(&c.id),
            intent: INTENT_DERIVE.into(),
            status: BRANCH_OPEN.into(),
            created_at: Some(self.env.now()),
            ..Branch::default()
        };
        self.put_branch(&own).await?;
        c.branch = own.name;
        self.put_change(&c).await?;
        Ok(c)
    }

    /// Opens an operation on a change, on its main flow (Go's `workOn`).
    pub async fn work_on(&mut self, id: &ChangeId, flow: &str) -> Result<Work> {
        on_main(flow)?;
        let c = self.change_open(id).await?;
        if !c.parent_id.is_empty() || !c.lifecycle.is_empty() || !c.guardians.is_empty() {
            return Err(unserved(
                "a sub-change, or a change following a lifecycle (ADR 0058, 0103)",
            ));
        }
        self.view(id).await?;
        let c = self.ensure_own_branch(c).await?;
        let seen = self.views[id].fold.impacts.clone();
        Ok(Work {
            branch: branch_of(&c.branch).to_string(),
            ns: namespace_of(&c.namespace).to_string(),
            c,
            seen,
            cn: ChangeImpact::default(),
            vcn: ChangeImpact::default(),
            planned: false,
        })
    }

    /// Takes the change and what the flow sees again, after a write of the operation (Go's `reread`).
    async fn reread(&mut self, w: &mut Work) -> Result<()> {
        w.c = self.change_open(&w.c.id).await?;
        w.seen = self.views[&w.c.id].fold.impacts.clone();
        Ok(())
    }

    /// Selects the impact an operation works on (Go's `impact` with no lifecycle: nothing is frozen).
    pub fn select_impact(&self, w: &mut Work, id: &str) -> Result<()> {
        let Some(cn) = self.views[&w.c.id].fold.impact(id).cloned() else {
            return Err(GraphError::not_found(format!(
                "change impact {id} of change {}",
                w.c.id
            )));
        };
        let Some(vcn) = w.seen.iter().find(|c| c.id == id).cloned() else {
            return Err(GraphError::not_found(format!(
                "change impact {id} is not on the flow \"\" of change {}",
                w.c.id
            )));
        };
        w.cn = cn;
        w.vcn = vcn;
        Ok(())
    }

    /// Finds the impact an operation names, declaring it when the change holds none (Go's `resolve`).
    pub async fn resolve(&mut self, id: &ChangeId, t: Named) -> Result<Work> {
        let mut w = self.work_on(id, "").await?;
        if !t.impact.is_empty() {
            self.select_impact(&mut w, &t.impact)?;
            return Ok(w);
        }
        if t.node.is_empty() && t.key.is_empty() {
            return Err(GraphError::invalid("name a change impact or a node"));
        }
        if t.create && (t.key.is_empty() || t.typ.is_empty()) {
            return Err(GraphError::invalid("a node needs a key and a type"));
        }
        let found = w
            .seen
            .iter()
            .rev()
            .find(|cn| {
                if !t.node.is_empty() {
                    node_of(cn).as_ref() == Some(&t.node)
                } else {
                    cn.key == t.key && (t.typ.is_empty() || cn.typ == t.typ)
                }
            })
            .cloned();
        if let Some(found) = found {
            if t.create && found.planned_creation() && found.typ == t.typ {
                self.select_impact(&mut w, &found.id)?;
                w.planned = true;
                return Ok(w);
            }
            if t.create {
                return Err(GraphError::conflict(format!(
                    "cannot create {} ({}): the change already holds it as change impact {} ({})",
                    t.key, t.typ, found.id, found.intent
                )));
            }
            self.select_impact(&mut w, &found.id)?;
            return Ok(w);
        }
        let mut nid = t.node.clone();
        if nid.is_empty()
            && let Some(got) = self.store.node_id_by_key(&w.ns, &t.key).await?
        {
            nid = got;
        }
        let mut pre = None;
        if !nid.is_empty() {
            let header = read::header_of(self.store, &w.c.baseline_id).await?;
            let at = read::select(self.store, &header, Some(std::slice::from_ref(&nid))).await?;
            pre = at.get(&nid).map(|v| NodeRef::new(nid.clone(), *v));
        }
        let mut decl = ChangeImpact::default();
        match (&pre, t.create) {
            (Some(_), true) => {
                return Err(GraphError::conflict(format!(
                    "cannot create {} ({}): the reference baseline of change {id} already holds it",
                    t.key, t.typ
                )));
            }
            (None, true) if !nid.is_empty() => {
                return Err(unserved("a creation taking over the key of a node out of the baseline"));
            }
            (None, true) => {
                decl.intent = INTENT_CREATED.into();
                decl.key = t.key.clone();
                decl.typ = t.typ.clone();
            }
            (Some(p), false) => {
                decl.intent = INTENT_MODIFIED.into();
                decl.pre = Some(p.clone());
            }
            (None, false) => {
                let name = if t.node.is_empty() { &t.key } else { t.node.as_str() };
                return Err(GraphError::not_found(format!(
                    "node {name} is in neither the reference baseline nor the change {id}"
                )));
            }
        }
        decl.rationale = t.rationale;
        decl.execution = t.execution;
        decl.produced_by = t.produced_by;
        let was_draft = w.c.status == STATUS_DRAFT;
        let decl = self.declare(id, decl, !t.create).await?;
        if !t.create || was_draft {
            self.reread(&mut w).await?;
        }
        if t.create {
            w.seen.push(decl.clone());
            w.cn = decl.clone();
            w.vcn = decl;
            return Ok(w);
        }
        self.select_impact(&mut w, &decl.id)?;
        Ok(w)
    }

    /// Proposes impacts (Go's `ProposeImpact`): the modifications of existing nodes a change will make, and the
    /// creations it plans (intent created, a key and a type, no draft, ADR 0121), each recorded by a `proposed`
    /// event. The impacts come back as declared.
    pub async fn propose_impacts(&mut self, id: &ChangeId, mut batch: Vec<ChangeImpact>) -> Result<Vec<ChangeImpact>> {
        for cn in &batch {
            if cn.intent == INTENT_CREATED && (cn.key.is_empty() || cn.typ.is_empty()) {
                return Err(GraphError::invalid(format!(
                    "change impact of {:?}: a planned creation needs a key and a type",
                    cn.key
                )));
            }
        }
        if batch.iter().any(|cn| !cn.flow.is_empty() && cn.flow != "main") {
            return Err(unserved("an impact proposed on an option (ADR 0032)"));
        }
        let c = self.change_open(id).await?;
        if !c.parent_id.is_empty() || !c.lifecycle.is_empty() || !c.guardians.is_empty() {
            return Err(unserved(
                "a sub-change, or a change following a lifecycle (ADR 0058, 0103)",
            ));
        }
        self.view(id).await?;
        for cn in batch.iter_mut() {
            if cn.id.is_empty() {
                cn.id = self.env.new_id();
            }
            cn.flow = String::new();
        }
        let ids: Vec<String> = batch.iter().map(|cn| cn.id.clone()).collect();
        let mut out = Vec::with_capacity(batch.len());
        for cn in batch {
            if cn.post.is_some() || cn.committed.is_some() || !cn.reviews.is_empty() || cn.recheck {
                return Err(GraphError::invalid(format!(
                    "change impact {}: post, committed and reviews are set by ImpactNodeCreate / ImpactNodeCheckout, ImpactNodeReview and Apply",
                    cn.id
                )));
            }
            if !cn.review.is_empty() && cn.review != REVIEW_PROPOSED {
                return Err(GraphError::invalid(format!(
                    "change impact {} starts proposed, it is accepted or rejected by ImpactNodeReview",
                    cn.id
                )));
            }
            if !cn.via.is_empty()
                && !ids.contains(&cn.via)
                && !self.views[id].fold.impacts.iter().any(|o| o.id == cn.via)
            {
                return Err(GraphError::invalid(format!(
                    "change impact {}: unknown via {}",
                    cn.id, cn.via
                )));
            }
            out.push(self.declare(id, cn, true).await?);
        }
        Ok(out)
    }

    /// Refuses a planned creation whose key a node of the reference baseline holds, or the change already acts on
    /// (Go's `checkPlannedKey`).
    async fn check_planned_key(&self, c: &Change, cn: &ChangeImpact) -> Result<()> {
        if let Some(nid) = self.store.node_id_by_key(namespace_of(&c.namespace), &cn.key).await? {
            let header = read::header_of(self.store, &c.baseline_id).await?;
            if !header.id.is_empty()
                && read::select(self.store, &header, Some(std::slice::from_ref(&nid)))
                    .await?
                    .contains_key(&nid)
            {
                return Err(GraphError::conflict(format!(
                    "cannot plan {} ({}): the reference baseline of change {} already holds it",
                    cn.key, cn.typ, c.id
                )));
            }
        }
        if let Some(other) = self.views[&c.id]
            .fold
            .impacts
            .iter()
            .find(|o| o.key == cn.key && o.id != cn.id)
        {
            return Err(GraphError::conflict(format!(
                "cannot plan {} ({}): the change already holds it as change impact {}",
                cn.key, cn.typ, other.id
            )));
        }
        Ok(())
    }

    /// Declares an impact (Go's `declareTx` for one): checked, proposed when `propose` (an existing node), else only
    /// returned (a creation, whose created event carries it).
    pub(super) async fn declare(&mut self, id: &ChangeId, mut cn: ChangeImpact, propose: bool) -> Result<ChangeImpact> {
        let mut c = self.change_open(id).await?;
        if cn.id.is_empty() {
            cn.id = self.env.new_id();
        }
        if cn.review.is_empty() {
            cn.review = REVIEW_PROPOSED.into();
        }
        if let Some(pre) = cn.pre.clone() {
            let header = read::header_of(self.store, &c.baseline_id).await?;
            let at = read::select(self.store, &header, Some(std::slice::from_ref(&pre.id))).await?;
            if at.get(&pre.id) != Some(&pre.version) {
                return Err(GraphError::conflict(format!(
                    "change impact {}: pre {pre} is not in baseline {} (the node moved on)",
                    cn.id, header.id
                )));
            }
            if self.views[id]
                .fold
                .impacts
                .iter()
                .any(|o| o.pre.as_ref().is_some_and(|p| p.id == pre.id))
            {
                return Err(GraphError::conflict(format!(
                    "change impact {}: node {} is already in the change",
                    cn.id, pre.id
                )));
            }
            let n = read::stored_node(self.store, &pre).await?;
            if (!cn.key.is_empty() && cn.key != n.key) || (!cn.typ.is_empty() && cn.typ != n.typ) {
                return Err(GraphError::invalid(format!(
                    "change impact {}: key / type do not match node {}",
                    cn.id,
                    n.node_ref()
                )));
            }
            cn.key = n.key.clone();
            cn.typ = n.typ.clone();
            let (got, want) = (namespace_of(&n.namespace), namespace_of(&c.namespace));
            if got != want {
                return Err(GraphError::invalid(format!(
                    "change impact {}: {} belongs to namespace {got:?}, the change acts on {want:?}",
                    cn.id, n.key
                )));
            }
        } else if !cn.typ.is_empty() {
            check::node(self.catalog(), namespace_of(&c.namespace), &cn.typ)
                .map_err(|e| e.context(format!("change impact {}", cn.id)))?;
        }
        if propose && cn.intent == INTENT_CREATED {
            self.check_planned_key(&c, &cn).await?;
        }
        cn.created_at = self.env.now();
        cn.validate()
            .map_err(|e| GraphError::invalid(format!("change impact {}: {e}", cn.id)))?;
        if propose {
            self.emit(vec![ImpactEvent {
                change: id.clone(),
                impact: cn.id.clone(),
                op: ImpactOp::Proposed,
                execution: cn.execution.clone(),
                state: Some(cn.clone()),
                ..ImpactEvent::default()
            }])
            .await?;
        }
        if c.status == STATUS_DRAFT {
            c.status = STATUS_ACTIVE.into();
            self.put_change(&c).await?;
        }
        Ok(cn)
    }

    /// The stored version a draft of the impact starts from (Go's `work.base`): its pre version, the latest of the
    /// node on the branch of the change on the main flow; none for a creation.
    async fn base(&self, w: &Work) -> Result<Option<Node>> {
        let Some(pre) = &w.cn.pre else {
            if w.cn.committed.is_none() {
                return Ok(None);
            }
            let Some(id) = node_of(&w.cn) else { return Ok(None) };
            return self.store.latest_on(&id, &w.branch).await;
        };
        let n = read::stored_node(self.store, pre).await?;
        Ok(Some(self.store.latest_on(&n.id, &w.branch).await?.unwrap_or(n)))
    }

    /// The draft of the impact on the main flow, editable (Go's `working`).
    fn working(&self, w: &Work) -> Result<Draft> {
        self.views[&w.c.id]
            .fold
            .draft(&w.cn.id, "")
            .cloned()
            .ok_or_else(|| GraphError::conflict(format!("{} is not checked out: check it out first", draft_of(&w.cn))))
    }

    /// The type of the node a link of a draft targets: a draft of the change, or a stored version.
    async fn link_target_type(&self, w: &Work, to: &NodeRef) -> Result<String> {
        if to.is_draft() {
            return w
                .seen
                .iter()
                .find(|cn| cn.post.as_ref().is_some_and(|p| p.is_draft() && p.id == to.id))
                .map(|cn| cn.typ.clone())
                .ok_or_else(|| {
                    GraphError::invalid(format!(
                        "a link targets the draft of node {}, which the change holds no draft of on this flow",
                        to.id
                    ))
                });
        }
        Ok(read::stored_node(self.store, to).await?.typ)
    }

    /// The outgoing link a draft gains, checked (Go's `newDraftLink`).
    async fn new_draft_link(&self, w: &Work, from: &Draft, l: &LinkWrite) -> Result<DraftLink> {
        if l.to.id.is_empty() && !l.target.is_zero() && !l.typ.is_empty() {
            return Err(unserved("a link to a URL (ADR 0154)"));
        }
        if l.typ.is_empty() || l.to.id.is_empty() {
            return Err(GraphError::invalid("a link needs a type and a target"));
        }
        let to_type = self.link_target_type(w, &l.to).await?;
        if !l.to.is_draft() && l.to.version == 0 {
            return Err(GraphError::invalid(
                "a link needs an exact target version, or the draft of a node of the change",
            ));
        }
        check::link(self.catalog(), &l.typ, &from.typ, &to_type)?;
        check::link_attributes(self.catalog(), &l.typ, l.props.as_ref())?;
        Ok(DraftLink {
            id: LinkId(self.env.new_id()),
            typ: l.typ.clone(),
            to: w.normalize_to(&l.to),
            target: Url::default(),
            props: l.props.clone(),
        })
    }

    /// Creates a node in a change: one `created` event adds the impact and the draft (Go's `createTx`).
    pub async fn impact_node_create(&mut self, id: &ChangeId, inp: NodeCreate, flow: &str) -> Result<ChangeImpact> {
        on_main(flow)?;
        if inp.key.is_empty() || inp.typ.is_empty() {
            return Err(GraphError::invalid("a node needs a key and a type"));
        }
        if !inp.owner.is_empty() {
            return Err(unserved("a node created with an owner of its own"));
        }
        let w = self
            .resolve(
                id,
                Named {
                    key: inp.key.clone(),
                    typ: inp.typ.clone(),
                    create: true,
                    rationale: inp.rationale.clone(),
                    execution: inp.execution.clone(),
                    produced_by: inp.produced_by.clone(),
                    ..Named::default()
                },
            )
            .await?;
        let mut d = Draft {
            change: w.c.id.clone(),
            impact: w.cn.id.clone(),
            node: NodeId(self.env.new_id()),
            key: inp.key.clone(),
            typ: inp.typ.clone(),
            props: inp.props.clone(),
            execution: inp.execution.clone(),
            ..Draft::default()
        };
        if let Some(lc) = self.catalog().lifecycle(&d.typ) {
            d.state = lc.initial.clone();
        }
        check::attributes(self.catalog(), &d.key, &d.typ, d.props.as_ref())?;
        for l in &inp.links {
            let dl = self.new_draft_link(&w, &d, l).await?;
            d.links.push(dl);
        }
        let r = d.draft_ref();
        if w.planned {
            // the creation was planned: its impact is recorded, the draft fulfils it
            self.emit(vec![ImpactEvent {
                change: w.c.id.clone(),
                impact: w.cn.id.clone(),
                op: ImpactOp::CheckedOut,
                execution: inp.execution.clone(),
                post: Some(r.clone()),
                draft: Some(d),
                ..ImpactEvent::default()
            }])
            .await?;
            return Ok(w.seen_as(&r));
        }
        self.emit(vec![ImpactEvent {
            change: w.c.id.clone(),
            impact: w.cn.id.clone(),
            op: ImpactOp::Created,
            execution: inp.execution.clone(),
            state: Some(w.cn.clone()),
            post: Some(r.clone()),
            draft: Some(d),
            ..ImpactEvent::default()
        }])
        .await?;
        Ok(w.seen_as(&r))
    }

    /// Checks nodes out in a change, each as one checkout, the change locked and read once (Go's
    /// `ImpactNodeCheckoutBatch`; one node: `ImpactNodeCheckout`).
    pub async fn impact_node_checkout(
        &mut self,
        id: &ChangeId,
        flow: &str,
        nodes: Vec<NodeCheckout>,
    ) -> Result<Vec<ChangeImpact>> {
        on_main(flow)?;
        if nodes.is_empty() {
            return Err(GraphError::invalid("no node to check out"));
        }
        let mut out = Vec::with_capacity(nodes.len());
        for n in nodes {
            let t = Named {
                impact: n.impact,
                node: n.node,
                key: n.key,
                rationale: n.rationale,
                execution: n.execution.clone(),
                produced_by: n.produced_by,
                ..Named::default()
            };
            let w = self.resolve(id, t).await?;
            let d = self.checkout(&w, &n.execution).await?;
            out.push(w.seen_as(&d.draft_ref()));
        }
        Ok(out)
    }

    /// The checkout of the impact w selects (Go's `checkoutTx`): a draft copying the version it starts from.
    async fn checkout(&mut self, w: &Work, execution: &str) -> Result<Draft> {
        if self.views[&w.c.id].fold.draft(&w.cn.id, "").is_some() {
            return Err(GraphError::conflict(format!(
                "{} is already checked out",
                draft_of(&w.cn)
            )));
        }
        let Some(base) = self.base(w).await? else {
            if w.cn.planned_creation() {
                // a planned creation checked out: its first draft, bare (ADR 0076: the creation fulfils the plan)
                let mut d = Draft {
                    change: w.c.id.clone(),
                    impact: w.cn.id.clone(),
                    node: NodeId(self.env.new_id()),
                    key: w.cn.key.clone(),
                    typ: w.cn.typ.clone(),
                    execution: execution.to_string(),
                    ..Draft::default()
                };
                if let Some(lc) = self.catalog().lifecycle(&d.typ) {
                    d.state = lc.initial.clone();
                }
                self.emit(vec![ImpactEvent {
                    change: w.c.id.clone(),
                    impact: w.cn.id.clone(),
                    op: ImpactOp::CheckedOut,
                    execution: execution.to_string(),
                    post: Some(d.draft_ref()),
                    draft: Some(d.clone()),
                    ..ImpactEvent::default()
                }])
                .await?;
                return Ok(d);
            }
            return Err(GraphError::invalid(format!(
                "change impact {} has no version to check out",
                w.cn.id
            )));
        };
        let ns = namespace_of(&base.namespace);
        if ns != w.ns {
            return Err(GraphError::invalid(format!(
                "node {} is in namespace {ns}, the change acts on {}",
                base.key, w.ns
            )));
        }
        if !base.project.is_zero() && !w.c.project_id.is_zero() && base.project.key != w.c.project_id.key {
            return Err(unserved("a checkout of a node of another project (ADR 0161)"));
        }
        let mut links = Vec::new();
        let mut copy = BaseCopy::default();
        for l in self
            .store
            .links_from(std::slice::from_ref(&base.node_ref()), None)
            .await?
        {
            let (to, target) = ends(&l);
            if !to.id.is_empty() && w.normalize_to(&to) != to && !copy.drafted.contains(&to) {
                copy.drafted.push(to.clone());
            }
            links.push(BaseLink {
                id: l.id,
                typ: l.typ,
                to,
                target,
                props: l.props,
            });
        }
        let mut e = ImpactEvent {
            id: self.env.new_id(),
            change: w.c.id.clone(),
            impact: w.cn.id.clone(),
            op: ImpactOp::CheckedOut,
            execution: execution.to_string(),
            from_base: Some(copy),
            ..ImpactEvent::default()
        };
        let d = impact::copy_draft(&e, &base_node(&base), &links);
        e.post = Some(d.draft_ref());
        e.draft = Some(d.clone());
        self.emit(vec![e]).await?;
        Ok(d)
    }

    /// Edits the properties of the draft of an impact (Go's `ImpactNodeUpdate`): merged and checked, an `updated`
    /// event with the patch.
    pub async fn impact_node_update(
        &mut self,
        id: &ChangeId,
        impact: &str,
        props: Map<String, Value>,
        owner: &str,
        flow: &str,
        execution: &str,
    ) -> Result<ChangeImpact> {
        on_main(flow)?;
        if props.is_empty() && owner.is_empty() {
            return Err(GraphError::invalid("nothing to update"));
        }
        if !owner.is_empty() {
            return Err(unserved("a move of the owner of a node"));
        }
        let mut w = self
            .resolve(
                id,
                Named {
                    impact: impact.to_string(),
                    execution: execution.to_string(),
                    ..Named::default()
                },
            )
            .await?;
        let d = self.working(&w)?;
        let mut merged = d.props.clone().unwrap_or_default();
        merged.extend(props.clone());
        check::attributes(self.catalog(), &d.key, &d.typ, Some(&merged))?;
        let mut patch = Map::new();
        patch.insert("props".into(), Value::Object(props));
        let r = d.draft_ref();
        self.emit_updated(&mut w, &r, execution, patch).await?;
        Ok(w.seen_as(&r))
    }

    /// Adds an outgoing link to the draft of an impact (Go's `ImpactLinkCreate`).
    pub async fn impact_link_create(
        &mut self,
        id: &ChangeId,
        impact: &str,
        l: LinkWrite,
        flow: &str,
        execution: &str,
    ) -> Result<Link> {
        let mut w = self.work_on(id, flow).await?;
        self.select_impact(&mut w, impact)?;
        let d = self.working(&w)?;
        let dl = self.new_draft_link(&w, &d, &l).await?;
        let link = Link {
            id: dl.id.clone(),
            typ: dl.typ.clone(),
            from: d.draft_ref(),
            to: Target::Node(dl.to.clone()),
            props: dl.props.clone(),
            change_id: id.clone(),
        };
        let mut patch = Map::new();
        patch.insert("addLink".into(), add_link_patch(&dl));
        self.emit_updated(&mut w, &d.draft_ref(), execution, patch).await?;
        Ok(link)
    }

    /// The draft holding a link a change edits and the link as the draft holds it (Go's `linkWork`): the link by its
    /// id, or the stored link of the version the draft was checked out from.
    async fn link_work(&mut self, id: &ChangeId, link: &LinkId, flow: &str) -> Result<(Work, Draft, DraftLink)> {
        let mut w = self.work_on(id, flow).await?;
        let stored = self.store.link(link).await?;
        // the gate of the service reads the type of the source first (Go's `LinkSourceType`)
        if stored.is_none() && !self.views[id].fold.drafts.iter().any(|d| d.link(link).is_some()) {
            return Err(GraphError::not_found(format!("link {link}")));
        }
        let drafts: Vec<Draft> = self.views[id]
            .fold
            .drafts
            .iter()
            .filter(|d| stored.as_ref().is_none_or(|s| d.node == s.from.id))
            .cloned()
            .collect();
        for d in drafts {
            let mut found = d.link(link).cloned();
            if found.is_none()
                && let (Some(s), Some(base)) = (&stored, &d.base)
                && base.id == s.from.id
            {
                let held = self.store.links_from(std::slice::from_ref(base), None).await?;
                if !held.iter().any(|h| h.id == s.id) {
                    continue;
                }
                let (to, target) = ends(s);
                found = d
                    .links
                    .iter()
                    .rfind(|c| c.typ == s.typ && c.to.id == to.id && c.target == target)
                    .cloned();
            }
            let Some(found) = found else { continue };
            self.select_impact(&mut w, &d.impact)?;
            let d = self.working(&w)?;
            return Ok((w, d, found));
        }
        Err(GraphError::conflict(format!(
            "link {link} is not a link of a draft of change {id}"
        )))
    }

    /// Sets the properties of an outgoing link of a draft (Go's `ImpactLinkUpdate`).
    pub async fn impact_link_update(
        &mut self,
        id: &ChangeId,
        link: &LinkId,
        props: Option<Map<String, Value>>,
        flow: &str,
        execution: &str,
    ) -> Result<Link> {
        let (mut w, d, cur) = self.link_work(id, link, flow).await?;
        check::link_attributes(self.catalog(), &cur.typ, props.as_ref())?;
        let props_value = props.clone().map_or(Value::Null, Value::Object);
        let mut m = Map::new();
        m.insert("id".into(), Value::String(cur.id.0.clone()));
        m.insert("type".into(), Value::String(cur.typ.clone()));
        if cur.is_external() {
            m.insert("target".into(), Value::String(cur.target.to_string()));
        } else {
            m.insert("toId".into(), Value::String(cur.to.id.0.clone()));
        }
        m.insert("props".into(), props_value);
        let mut patch = Map::new();
        patch.insert("updateLink".into(), Value::Object(m));
        let to = if cur.is_external() {
            Target::External(cur.target.clone())
        } else {
            Target::Node(cur.to.clone())
        };
        let l = Link {
            id: cur.id.clone(),
            typ: cur.typ.clone(),
            from: d.draft_ref(),
            to,
            props,
            change_id: id.clone(),
        };
        self.emit_updated(&mut w, &d.draft_ref(), execution, patch).await?;
        Ok(l)
    }

    /// Removes an outgoing link of a draft (Go's `ImpactLinkDelete`): removing a child is a modification of its parent.
    pub async fn impact_link_delete(
        &mut self,
        id: &ChangeId,
        link: &LinkId,
        flow: &str,
        execution: &str,
    ) -> Result<()> {
        let (mut w, d, found) = self.link_work(id, link, flow).await?;
        let mut patch = Map::new();
        patch.insert("removeLink".into(), remove_link_patch(&found));
        self.emit_updated(&mut w, &d.draft_ref(), execution, patch).await
    }

    /// Records an edit of a draft, then sends a decided review back to proposed (Go's `emitUpdated`, `editedAgain`).
    async fn emit_updated(
        &mut self,
        w: &mut Work,
        r: &NodeRef,
        execution: &str,
        patch: Map<String, Value>,
    ) -> Result<()> {
        self.emit(vec![ImpactEvent {
            change: w.c.id.clone(),
            impact: w.cn.id.clone(),
            op: ImpactOp::Updated,
            execution: execution.to_string(),
            post: Some(r.clone()),
            patch: Some(patch),
            ..ImpactEvent::default()
        }])
        .await?;
        self.edited_again(w, execution).await
    }

    /// Moves a node along its lifecycle in a change (Go's `ImpactNodeTransition`): the state of its draft, the node
    /// checked out first when the change holds no draft of it; a transitioned event with the patch of the state. A
    /// transition with a guard, guard or action algorithms, or a condition on the children of the node is the Go
    /// service's.
    pub async fn impact_node_transition(
        &mut self,
        id: &ChangeId,
        flow: &str,
        n: NodeCheckout,
        to: &str,
    ) -> Result<ChangeImpact> {
        on_main(flow)?;
        if to.is_empty() {
            return Err(GraphError::invalid("a transition names the state it goes to"));
        }
        let execution = n.execution.clone();
        // what the Go service serves is known before anything is written (the declaration of an impact moves the
        // sequence of the log)
        if let Some((typ, state, declares)) = self.transition_source(id, &n).await?
            && let Some(lc) = self.catalog().lifecycle(&typ)
        {
            let from = if state.is_empty() {
                lc.initial.as_str()
            } else {
                state.as_str()
            };
            let t = lc.transitions.iter().find(|t| t.from == from && t.to == to);
            if t.is_some_and(|t| {
                !t.guard.is_empty() || !t.guards.is_empty() || !t.actions.is_empty() || t.children.is_some()
            }) {
                return Err(unserved(
                    "a transition with a guard, algorithms or a condition on the children (ADR 0018)",
                ));
            }
            // the Go service resolves the move once to authorize it, in a transaction it rolls back: the impact it
            // declares there takes a position of the log and of the impacts
            if declares && t.is_some() && lc.state(to).is_some() {
                self.store.skip_declaration().await?;
            }
        }
        let mut w = self
            .resolve(
                id,
                Named {
                    impact: n.impact,
                    node: n.node,
                    key: n.key,
                    rationale: n.rationale,
                    execution: n.execution,
                    produced_by: n.produced_by,
                    ..Named::default()
                },
            )
            .await?;
        let own = self.views[id].fold.draft(&w.cn.id, "").cloned();
        let (key, typ, state) = match &own {
            Some(d) => (d.key.clone(), d.typ.clone(), d.state.clone()),
            None => match self.base(&w).await? {
                Some(b) => (b.key, b.typ, b.state),
                None => {
                    return Err(GraphError::invalid(format!(
                        "change impact {} has no version to move",
                        w.cn.id
                    )));
                }
            },
        };
        let quoted = serde_json::to_string(to).unwrap_or_default();
        let Some(lc) = self.catalog().lifecycle(&typ).cloned() else {
            return Err(GraphError::invalid(format!(
                "node type {typ} has no lifecycle: state {quoted}"
            )));
        };
        if lc.state(to).is_none() {
            return Err(GraphError::invalid(format!("{typ} has no state {quoted}")));
        }
        let from = if state.is_empty() { lc.initial.clone() } else { state };
        if from == to {
            return Err(GraphError::invalid(format!("{key} ({typ}) is already {to}")));
        }
        let Some(t) = lc.transitions.iter().find(|t| t.from == from && t.to == to).cloned() else {
            return Err(GraphError::invalid(format!(
                "{key} ({typ}) cannot go from {from} to {to}"
            )));
        };
        if !t.guard.is_empty() || !t.guards.is_empty() || !t.actions.is_empty() || t.children.is_some() {
            return Err(unserved(
                "a transition with a guard, algorithms or a condition on the children",
            ));
        }
        if own.is_none() {
            self.checkout(&w, &execution).await?;
            self.reread(&mut w).await?;
            let impact = w.cn.id.clone();
            self.select_impact(&mut w, &impact)?;
        }
        let d = self.working(&w)?;
        if let Some(req) = &t.requires {
            for k in &req.attributes {
                let v = d.props.as_ref().and_then(|p| p.get(k));
                if v.is_none_or(|v| v.is_null() || v.as_str() == Some("")) {
                    return Err(GraphError::invalid(format!(
                        "{} ({}) cannot take {}: attribute {} is required",
                        d.key,
                        d.typ,
                        t.name,
                        serde_json::to_string(k).unwrap_or_default()
                    )));
                }
            }
            for lt in &req.outgoing_links {
                if !d.links.iter().any(|l| l.typ == *lt) {
                    return Err(GraphError::invalid(format!(
                        "{} ({}) cannot take {}: an outgoing {} link is required",
                        d.key,
                        d.typ,
                        t.name,
                        serde_json::to_string(lt).unwrap_or_default()
                    )));
                }
            }
        }
        let mut state = Map::new();
        state.insert("from".into(), Value::String(d.state.clone()));
        state.insert("to".into(), Value::String(t.to.clone()));
        let mut patch = Map::new();
        patch.insert("state".into(), Value::Object(state));
        let r = d.draft_ref();
        self.emit(vec![ImpactEvent {
            change: id.clone(),
            impact: w.cn.id.clone(),
            op: ImpactOp::Transitioned,
            execution,
            post: Some(r.clone()),
            patch: Some(patch),
            ..ImpactEvent::default()
        }])
        .await?;
        Ok(w.seen_as(&r))
    }

    /// The type and state of the node a transition moves, as the change sees it, read without writing anything, and
    /// whether the change declares an impact for it; none when it is not found (the operation says why).
    async fn transition_source(&mut self, id: &ChangeId, n: &NodeCheckout) -> Result<Option<(String, String, bool)>> {
        let w = self.work_on(id, "").await?;
        let cn = if !n.impact.is_empty() {
            self.views[id].fold.impact(&n.impact).cloned()
        } else {
            w.seen
                .iter()
                .rev()
                .find(|cn| node_of(cn).as_ref() == Some(&n.node))
                .cloned()
        };
        if let Some(cn) = cn {
            if let Some(d) = self.views[id].fold.draft(&cn.id, "") {
                return Ok(Some((d.typ.clone(), d.state.clone(), false)));
            }
            let w = Work { cn, ..w };
            return Ok(self.base(&w).await?.map(|b| (b.typ, b.state, false)));
        }
        if n.node.is_empty() {
            return Ok(None);
        }
        let header = read::header_of(self.store, &w.c.baseline_id).await?;
        let at = read::select(self.store, &header, Some(std::slice::from_ref(&n.node))).await?;
        let Some(v) = at.get(&n.node) else { return Ok(None) };
        let b = read::stored_node(self.store, &NodeRef::new(n.node.clone(), *v)).await?;
        Ok(Some((b.typ, b.state, true)))
    }

    /// Sends a decided review of the impact w selects back to proposed: the draft it was reviewed on is gone (Go's
    /// `editedAgain`, ADR 0102).
    async fn edited_again(&mut self, w: &mut Work, execution: &str) -> Result<()> {
        if w.vcn.review == REVIEW_PROPOSED {
            return Ok(());
        }
        let at = self.env.now();
        self.emit(vec![ImpactEvent {
            change: w.c.id.clone(),
            impact: w.cn.id.clone(),
            op: ImpactOp::Reviewed,
            execution: execution.to_string(),
            by: self.env.by.clone(),
            review: Some(Review {
                status: REVIEW_PROPOSED.into(),
                by: self.env.by.clone(),
                comment: "edited again".into(),
                at,
                flow: String::new(),
                execution: execution.to_string(),
                superseded: false,
                review_id: String::new(),
            }),
            ..ImpactEvent::default()
        }])
        .await?;
        w.vcn.review = REVIEW_PROPOSED.into();
        Ok(())
    }

    /// Drops the draft of an impact (Go's `ImpactNodeCancel`): a creation cancelled is gone with its impact, a committed
    /// one falls back to the version the change wrote.
    pub async fn impact_node_cancel(
        &mut self,
        id: &ChangeId,
        impact: &str,
        flow: &str,
        execution: &str,
    ) -> Result<ChangeImpact> {
        let mut w = self.work_on(id, flow).await?;
        self.select_impact(&mut w, impact)?;
        if self.views[id].fold.draft(impact, "").is_none() {
            return Err(GraphError::conflict(format!(
                "change impact {impact} ({}) is not checked out",
                w.cn.key
            )));
        }
        let back = w.cn.committed.clone();
        self.emit(vec![ImpactEvent {
            change: id.clone(),
            impact: impact.to_string(),
            op: ImpactOp::Cancelled,
            execution: execution.to_string(),
            post: back.clone(),
            ..ImpactEvent::default()
        }])
        .await?;
        if w.cn.intent == INTENT_MODIFIED || w.cn.committed.is_some() {
            self.edited_again(&mut w, execution).await?;
        }
        let mut cn = w.vcn.clone();
        cn.post = back;
        Ok(cn)
    }

    /// Takes impacts out of the change, with the impacts via them, each once (Go's `withdrawTx`): their drafts are
    /// dropped, one committed already is reverted by the next commit.
    pub async fn withdraw_impacts(
        &mut self,
        id: &ChangeId,
        impacts: &[String],
        flow: &str,
        execution: &str,
    ) -> Result<()> {
        let mut w = self.work_on(id, flow).await?;
        let mut gone: Vec<String> = Vec::new();
        let mut events = Vec::new();
        let mut withdraw = |impact: &str, events: &mut Vec<ImpactEvent>| {
            if !gone.iter().any(|g| g == impact) {
                gone.push(impact.to_string());
                events.push(ImpactEvent {
                    change: id.clone(),
                    impact: impact.to_string(),
                    op: ImpactOp::Withdrawn,
                    execution: execution.to_string(),
                    ..ImpactEvent::default()
                });
            }
        };
        for impact in impacts {
            if events.iter().any(|e: &ImpactEvent| e.impact == *impact) {
                continue;
            }
            self.select_impact(&mut w, impact)?;
            if !w.cn.flow.is_empty() {
                return Err(GraphError::conflict(format!(
                    "change impact {impact} was declared on another flow"
                )));
            }
            let via: Vec<String> = self.views[id]
                .fold
                .impacts
                .iter()
                .filter(|o| o.via == *impact)
                .map(|o| o.id.clone())
                .collect();
            for o in via {
                withdraw(&o, &mut events);
            }
            withdraw(impact, &mut events);
        }
        self.emit(events).await
    }
}

/// The node an impact acts on: its post, else its pre.
pub fn node_of(cn: &ChangeImpact) -> Option<NodeId> {
    cn.post.as_ref().or(cn.pre.as_ref()).map(|r| r.id.clone())
}

fn base_node(n: &Node) -> BaseNode {
    BaseNode {
        node: n.node_ref(),
        key: n.key.clone(),
        typ: n.typ.clone(),
        state: n.state.clone(),
        owner: n.owner.clone(),
        props: n.props.clone(),
    }
}

/// A link added to a draft, for the updated event (Go's `linkPatch`).
fn add_link_patch(l: &DraftLink) -> Value {
    let mut m = Map::new();
    m.insert("id".into(), Value::String(l.id.0.clone()));
    m.insert("type".into(), Value::String(l.typ.clone()));
    m.insert("to".into(), Value::String(l.to.to_string()));
    m.insert("toId".into(), Value::String(l.to.id.0.clone()));
    m.insert("toVersion".into(), Value::from(l.to.version));
    m.insert("props".into(), l.props.clone().map_or(Value::Null, Value::Object));
    Value::Object(m)
}

/// A link removed from a draft, for the updated event (Go's `removeLinkPatch`).
fn remove_link_patch(l: &DraftLink) -> Value {
    let mut m = Map::new();
    m.insert("id".into(), Value::String(l.id.0.clone()));
    m.insert("type".into(), Value::String(l.typ.clone()));
    if l.is_external() {
        m.insert("to".into(), Value::String(l.target.to_string()));
        m.insert("target".into(), Value::String(l.target.to_string()));
    } else {
        m.insert("to".into(), Value::String(l.to.to_string()));
        m.insert("toId".into(), Value::String(l.to.id.0.clone()));
    }
    Value::Object(m)
}

/// The view of a change: the fold of its log, the checkouts hydrated (the simple world only: the main flow, impact
/// events and review records).
pub async fn fold_change<T: ReadTx>(store: &T, id: &ChangeId) -> Result<ChangeView> {
    let mut out = ChangeView::default();
    let mut events = Vec::new();
    for l in store.log(id, 0, &[]).await? {
        if l.stream() == LOG_IMPACT || l.stream() == LOG_FACT {
            out.seq = out.seq.max(l.seq); // the position of the view: its impact events and facts
        }
        if l.stream() == LOG_IMPACT {
            events.extend(impact::events_of(&l).map_err(GraphError::internal)?);
        } else if (l.stream() == LOG_FACT && VIEW_FACTS.contains(&l.typ.as_str()))
            || (l.stream() == "change" && !header_only(&l))
        {
            // the other streams (the journal, the prompts of the engine) are records of a use case: the view of a
            // change reads none
            return Err(unserved(&format!(
                "a change whose log records {} entries (options, decisions, executions)",
                l.typ
            )));
        }
    }
    hydrate(store, &mut events).await?;
    for e in &events {
        if e.op == ImpactOp::Adopted || !e.flow.is_empty() {
            return Err(unserved("a change working on options (ADR 0032)"));
        }
        out.fold.apply(e);
    }
    if out.fold.impacts.iter().any(|c| c.superseded || !c.flow.is_empty()) {
        return Err(unserved("a change working on options (ADR 0032)"));
    }
    out.events = events;
    Ok(out)
}

/// Whether a log entry edits the header of its change only, leaving its impacts and drafts as they are: a
/// `change.updated` entry but a rebase (`baselineId`) or an attachment (`parentId`), which move its fork.
fn header_only(l: &crate::impact::LogEntry) -> bool {
    l.typ == "change.updated"
        && !l.subject.is_empty()
        && l.subject.split(',').all(|f| f != "baselineId" && f != "parentId")
}

/// Rebuilds the drafts of the checkouts that copy a stored version (ADR 0142): the version and its links.
async fn hydrate<T: ReadTx>(store: &T, events: &mut [ImpactEvent]) -> Result<()> {
    let bases: Vec<NodeRef> = events
        .iter()
        .filter(|e| !e.hydrated())
        .filter_map(|e| e.draft.as_ref().and_then(|d| d.base.clone()))
        .collect::<HashSet<_>>()
        .into_iter()
        .collect();
    if bases.is_empty() {
        return Ok(());
    }
    let nodes: HashMap<NodeRef, Node> = store
        .nodes_at(&bases, "")
        .await?
        .into_iter()
        .map(|n| (n.node_ref(), n))
        .collect();
    let mut links: HashMap<NodeRef, Vec<BaseLink>> = HashMap::new();
    for l in store.links_from(&bases, None).await? {
        let (to, target) = ends(&l);
        links.entry(l.from.clone()).or_default().push(BaseLink {
            id: l.id,
            typ: l.typ,
            to,
            target,
            props: l.props,
        });
    }
    for e in events.iter_mut().filter(|e| !e.hydrated()) {
        let Some(base) = e.draft.as_ref().and_then(|d| d.base.clone()) else {
            continue;
        };
        let n = nodes
            .get(&base)
            .ok_or_else(|| GraphError::internal(format!("impact event {}: version {base} is gone", e.id)))?;
        let d = impact::copy_draft(
            e,
            &base_node(n),
            links.get(&base).map(Vec::as_slice).unwrap_or_default(),
        );
        e.draft = Some(d);
    }
    Ok(())
}
