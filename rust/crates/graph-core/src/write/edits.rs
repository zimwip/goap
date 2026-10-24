//! The producer's way of making a change of node edits (Go's `Graph.Commit`, `commit.go`; ADR 0024, 0076, 0102): the
//! change is opened on a branch of its own, a created node is an `ImpactNodeCreate`, a modified one a proposed impact
//! checked out and edited, each impact that modifies its node is accepted in one review and one that modifies nothing
//! withdrawn, then the change is committed and merged, all in the transaction of the call.
use std::collections::{HashMap, HashSet};

use serde_json::{Map, Value};

use super::{
    change::NewChange,
    commit::NOTHING_TO_COMMIT,
    review::{ReviewBatch, Verdict, draft_changed},
    tx::Tx,
    work::{Authorize, LinkWrite, NodeCheckout, NodeCreate},
};
use crate::{
    GraphError, Result, gojson,
    impact::{ChangeImpact, INTENT_MODIFIED, ImpactEvent, ImpactOp},
    model::{Baseline, BaselineId, ChangeId, LinkId, MAIN_BRANCH, NodeId, NodeRef, Props, Target},
    operations::{
        CHANGE_COMMIT, CHANGE_MERGE, IMPACT_CHECKOUT, IMPACT_CREATE, IMPACT_LINK_CREATE, IMPACT_LINK_DELETE,
        IMPACT_PROPOSE, IMPACT_REVIEW, IMPACT_TRANSITION, IMPACT_UPDATE, IMPACT_WITHDRAW,
    },
    read,
    store::WriteTx,
};

/// The check of an operation a commit performs on its change (Go's `Graph.allow`).
pub type Allow<'a> = dyn Fn(&crate::change::Change, &str) -> Result<()> + Sync + 'a;

/// A link an edit gives its node: to a stored version, or to a node of the graph or of the commit by key.
#[derive(Debug, Clone, Default)]
pub struct LinkEdit {
    pub typ: String,
    pub to: Option<NodeRef>,
    pub to_key: String,
    pub props: Props,
}

/// A node a commit creates (no `pre`) or modifies (from its `pre` version).
#[derive(Debug, Clone, Default)]
pub struct NodeEdit {
    pub key: String,
    pub typ: String,
    pub pre: Option<NodeRef>,
    pub props: Props,
    pub rationale: String,
    pub links: Vec<LinkEdit>,
    pub remove_links: Vec<LinkId>,
    pub owner: String,
    pub state: String,
}

/// The edits of one commit and the change they make.
#[derive(Debug, Clone, Default)]
pub struct Commit {
    pub namespace: String,
    pub title: String,
    pub intent: String,
    pub methodology: String,
    pub data: Option<Map<String, Value>>,
    pub baseline: BaselineId,
    pub by: String,
    pub baseline_name: String,
    pub edits: Vec<NodeEdit>,
    pub owner_org: String,
    pub project_id: String,
}

/// The change a commit made and the header of the baseline it left.
#[derive(Debug, Clone, Default)]
pub struct CommitResult {
    pub change: ChangeId,
    pub baseline: Baseline,
}

fn node_name(e: &NodeEdit) -> String {
    match &e.pre {
        Some(p) => p.to_string(),
        None => e.key.clone(),
    }
}

/// The attributes of two links alike, no attribute and an empty set alike (Go's `sameLinkProps`).
fn same_link_props(a: &Props, b: &Props) -> bool {
    let len = |p: &Props| p.as_ref().map_or(0, Map::len);
    if len(a) == 0 || len(b) == 0 {
        return len(a) == len(b);
    }
    let text = |p: &Props| gojson::to_string(&Value::Object(p.clone().unwrap_or_default()));
    text(a) == text(b)
}

/// The node a stored link reaches, if a node.
fn link_to(t: &Target) -> Option<&NodeId> {
    match t {
        Target::Node(r) => Some(&r.id),
        Target::External(_) => None,
    }
}

impl<S: WriteTx> Tx<'_, S> {
    /// Runs the edits as one change (Go's `Graph.Commit` with no event carried).
    /// The moves of the state edits are authorized by `authorize` (Go's `Graph.Authorizer`).
    pub async fn commit_edits(&mut self, inp: Commit, authorize: &Authorize<'_>) -> Result<CommitResult> {
        self.commit_edits_as(inp, authorize, &|_, _| Ok(())).await
    }

    /// A commit whose operations are each asked of `allow` where Go's `Commit` calls the public operation asking it
    /// (`Graph.allow`, ADR 0103): the roles the methodology of the change allows a person.
    pub async fn commit_edits_as(
        &mut self,
        inp: Commit,
        authorize: &Authorize<'_>,
        allow: &Allow<'_>,
    ) -> Result<CommitResult> {
        if inp.edits.is_empty() {
            return Err(GraphError::invalid("a commit needs at least one edit"));
        }
        for e in &inp.edits {
            self.check_required_parent(e)?;
        }
        let c = self
            .create_change(NewChange {
                title: inp.title.clone(),
                intent: inp.intent.clone(),
                methodology: inp.methodology.clone(),
                methodology_version: String::new(),
                namespace: inp.namespace.clone(),
                baseline_id: inp.baseline.clone(),
                branch: String::new(),
                own_branch: true,
                owner_org: inp.owner_org.clone(),
                project_id: inp.project_id.clone(),
                data: inp.data.clone(),
                ..NewChange::default()
            })
            .await?;
        let id = c.id.clone();
        let order = self.commit_order(&inp.edits).await?;
        let n = inp.edits.len();
        let mut impacts = vec![String::new(); n];
        let mut posts: Vec<Option<NodeRef>> = vec![None; n];
        let mut written: HashMap<String, usize> = HashMap::new();
        let mut later: Vec<(usize, LinkEdit)> = Vec::new();
        let created: HashSet<&str> = inp
            .edits
            .iter()
            .filter(|e| e.pre.is_none())
            .map(|e| e.key.as_str())
            .collect();
        let why = |e: &NodeEdit| {
            if e.rationale.is_empty() {
                inp.title.clone()
            } else {
                e.rationale.clone()
            }
        };
        for &i in &order {
            let e = &inp.edits[i];
            let mut links = Vec::new();
            for l in &e.links {
                let mut to = l.to.clone();
                if !l.to_key.is_empty() {
                    if let Some(&j) = written.get(&l.to_key) {
                        to = posts[j].clone();
                    } else if created.contains(l.to_key.as_str()) {
                        later.push((i, l.clone()));
                        continue;
                    } else {
                        // not a node of this commit: a node the graph already holds, whose version stays
                        let found = match self.store.node_id_by_key(&inp.namespace, &l.to_key).await? {
                            Some(nid) => self.store.latest_on(&nid, MAIN_BRANCH).await?,
                            None => None,
                        };
                        let Some(found) = found else {
                            return Err(GraphError::invalid(format!(
                                "link of {} to {:?}: no such node, neither stored nor created by this commit",
                                node_name(e),
                                l.to_key
                            )));
                        };
                        to = Some(found.node_ref());
                    }
                }
                let Some(to) = to else {
                    return Err(GraphError::invalid(format!("link of {} needs a target", node_name(e))));
                };
                if let Some(pre) = &e.pre
                    && self.links_to(pre, &l.typ, &to.id, &l.props, &e.remove_links).await?
                {
                    // a link is part of its source version: the one it already carries is not added again
                    continue;
                }
                links.push(LinkWrite {
                    typ: l.typ.clone(),
                    to,
                    target: Default::default(),
                    props: l.props.clone(),
                });
            }
            let Some(pre) = &e.pre else {
                allow(&c, IMPACT_CREATE).map_err(|err| err.context(node_name(e)))?;
                let cn = self
                    .impact_node_create(
                        &id,
                        NodeCreate {
                            key: e.key.clone(),
                            typ: e.typ.clone(),
                            props: e.props.clone(),
                            owner: e.owner.clone(),
                            rationale: why(e),
                            links,
                            execution: String::new(),
                            produced_by: inp.by.clone(),
                        },
                        "",
                    )
                    .await
                    .map_err(|err| err.context(node_name(e)))?;
                impacts[i] = cn.id.clone();
                posts[i] = cn.post.clone();
                written.insert(e.key.clone(), i);
                continue;
            };
            // the impact names the version the edit starts from: a stale one conflicts
            let decl = ChangeImpact {
                intent: INTENT_MODIFIED.into(),
                pre: Some(pre.clone()),
                rationale: why(e),
                produced_by: inp.by.clone(),
                ..ChangeImpact::default()
            };
            allow(&c, IMPACT_PROPOSE).map_err(|err| err.context(node_name(e)))?;
            self.view(&id).await?;
            let added = self
                .declare(&id, decl, true)
                .await
                .map_err(|err| err.context(node_name(e)))?;
            impacts[i] = added.id.clone();
            let props = e.props.clone().unwrap_or_default();
            if props.is_empty() && e.owner.is_empty() && links.is_empty() && e.remove_links.is_empty() {
                continue;
            }
            allow(&c, IMPACT_CHECKOUT).map_err(|err| err.context(node_name(e)))?;
            let mut out = self
                .impact_node_checkout(
                    &id,
                    "",
                    vec![NodeCheckout {
                        impact: impacts[i].clone(),
                        ..NodeCheckout::default()
                    }],
                )
                .await
                .map_err(|err| err.context(node_name(e)))?;
            let cn = out.pop().unwrap_or_default();
            posts[i] = cn.post.clone();
            if !props.is_empty() || !e.owner.is_empty() {
                allow(&c, IMPACT_UPDATE).map_err(|err| err.context(node_name(e)))?;
                self.impact_node_update(&id, &cn.id, props, &e.owner, "", "")
                    .await
                    .map_err(|err| err.context(node_name(e)))?;
            }
            for link in &e.remove_links {
                allow(&c, IMPACT_LINK_DELETE).map_err(|err| err.context(node_name(e)))?;
                self.impact_link_delete(&id, link, "", "")
                    .await
                    .map_err(|err| err.context(node_name(e)))?;
            }
            for l in links {
                allow(&c, IMPACT_LINK_CREATE).map_err(|err| err.context(node_name(e)))?;
                self.impact_link_create(&id, &cn.id, l, "", "")
                    .await
                    .map_err(|err| err.context(node_name(e)))?;
            }
        }
        for (from, l) in later {
            let to = posts[written[&l.to_key]].clone().unwrap_or_default();
            let w = LinkWrite {
                typ: l.typ.clone(),
                to,
                target: Default::default(),
                props: l.props.clone(),
            };
            let created = match allow(&c, IMPACT_LINK_CREATE) {
                Ok(()) => self
                    .impact_link_create(&id, &impacts[from], w, "", "")
                    .await
                    .map(|_| ()),
                Err(err) => Err(err),
            };
            created.map_err(|err| {
                err.context(format!(
                    "link {} from {} to {}",
                    l.typ,
                    node_name(&inp.edits[from]),
                    l.to_key
                ))
            })?;
        }
        // a state edit is a move of the node's draft (a node with none is checked out by the move)
        for (i, e) in inp.edits.iter().enumerate() {
            if e.state.is_empty() {
                continue;
            }
            self.view(&id).await?;
            let (node, mut cur) = match &posts[i] {
                Some(_) => match self.views[&id].fold.draft(&impacts[i], "") {
                    Some(d) => (d.node.clone(), d.state.clone()),
                    None => return Err(GraphError::not_found(format!("the draft of {}", node_name(e)))),
                },
                None => {
                    let pre = e.pre.clone().unwrap_or_default();
                    (pre.id.clone(), crate::read::stored_node(self.store, &pre).await?.state)
                }
            };
            if cur.is_empty()
                && e.pre.is_none()
                && let Some(lc) = self.catalog().lifecycle(&e.typ)
            {
                cur = lc.initial.clone();
            }
            if cur == e.state {
                continue;
            }
            allow(&c, IMPACT_TRANSITION).map_err(|err| err.context(node_name(e)))?;
            self.impact_node_transition(
                &id,
                "",
                NodeCheckout {
                    impact: impacts[i].clone(),
                    node,
                    rationale: why(e),
                    produced_by: inp.by.clone(),
                    ..NodeCheckout::default()
                },
                &e.state,
                authorize,
            )
            .await
            .map_err(|err| err.context(node_name(e)))?;
        }
        // each impact that modifies its node is accepted by the producer, one that modifies nothing withdrawn
        let mut verdicts = Vec::new();
        for (i, e) in inp.edits.iter().enumerate() {
            if impacts[i].is_empty() {
                continue;
            }
            if !self.impact_modifies(&id, &impacts[i]).await? {
                allow(&c, IMPACT_WITHDRAW).map_err(|err| err.context(node_name(e)))?;
                self.withdraw_impact(&id, &impacts[i])
                    .await
                    .map_err(|err| err.context(node_name(e)))?;
                continue;
            }
            verdicts.push(Verdict {
                impact: impacts[i].clone(),
                accept: true,
                comment: why(e),
            });
        }
        if verdicts.is_empty() {
            return Err(GraphError::invalid(format!(
                "commit {:?}: every edit restates what the graph holds: {NOTHING_TO_COMMIT}",
                inp.title
            )));
        }
        allow(&c, IMPACT_REVIEW).map_err(|err| err.context(format!("commit {:?}", inp.title)))?;
        self.impact_node_review_batch(
            &id,
            ReviewBatch {
                verdicts,
                by: inp.by.clone(),
                ..ReviewBatch::default()
            },
        )
        .await
        .map_err(|err| err.context(format!("commit {:?}", inp.title)))?;
        // the apply: commit what is pending, then merge (Go's `apply`)
        allow(&c, CHANGE_COMMIT)?;
        if let Err(err) = self.commit_change(&id, &inp.baseline_name).await
            && !err.message.ends_with(&format!("{NOTHING_TO_COMMIT}: invalid"))
        {
            return Err(err);
        }
        // a rebase the merge keeps is refused with the whole commit: one transaction
        allow(&c, CHANGE_MERGE)?;
        let done = match self.merge_change(&id).await? {
            super::merge::Merge::Applied(done) => done,
            super::merge::Merge::Rebased(err) => return Err(err),
        };
        let mut res = CommitResult {
            change: id,
            baseline: Baseline::default(),
        };
        if !done.change.result_baseline_id.is_empty() {
            res.baseline = read::header_of(self.store, &done.change.result_baseline_id).await?;
        }
        Ok(res)
    }

    /// A created node of a structure, or of a type requiring links, names them (Go's `checkRequiredParent`).
    fn check_required_parent(&self, e: &NodeEdit) -> Result<()> {
        if e.pre.is_some() {
            return Ok(());
        }
        for (link, want, of) in super::check::requirements(self.catalog(), &e.key, &e.typ) {
            let got = e.links.iter().filter(|l| l.typ == link).count() as i64;
            if got != want {
                return Err(GraphError::invalid(format!("{} needs {of}, got {got}", e.key)));
            }
        }
        Ok(())
    }

    /// Whether a version already has an outgoing link of the type to the node with these attributes, other than the
    /// ones the edit removes (Go's `linksTo`).
    async fn links_to(
        &self,
        from: &NodeRef,
        typ: &str,
        to: &NodeId,
        props: &Props,
        removed: &[LinkId],
    ) -> Result<bool> {
        let out = self.store.links_from(std::slice::from_ref(from), None).await?;
        Ok(out.iter().any(|l| {
            l.typ == typ && link_to(&l.to) == Some(to) && !removed.contains(&l.id) && same_link_props(&l.props, props)
        }))
    }

    /// Orders the edits so that what a link points to is written before the node linking to it (Go's `commitOrder`).
    async fn commit_order(&self, edits: &[NodeEdit]) -> Result<Vec<usize>> {
        let mut by_key: HashMap<&str, usize> = HashMap::new();
        let mut by_node: HashMap<&NodeId, usize> = HashMap::new();
        for (i, e) in edits.iter().enumerate() {
            match &e.pre {
                None => {
                    if by_key.insert(e.key.as_str(), i).is_some() {
                        return Err(GraphError::invalid(format!("node {:?} is created twice", e.key)));
                    }
                }
                Some(p) => {
                    by_node.insert(&p.id, i);
                }
            }
        }
        let mut deps: Vec<Vec<usize>> = vec![Vec::new(); edits.len()];
        for (i, e) in edits.iter().enumerate() {
            for l in &e.links {
                if !l.to_key.is_empty() {
                    if let Some(&j) = by_key.get(l.to_key.as_str()) {
                        deps[i].push(j);
                    }
                } else if let Some(to) = &l.to
                    && let Some(&j) = by_node.get(&to.id)
                    && j != i
                {
                    deps[i].push(j);
                }
            }
            let Some(pre) = &e.pre else { continue };
            for l in self.store.links_from(std::slice::from_ref(pre), None).await? {
                if let Some(to) = link_to(&l.to)
                    && let Some(&j) = by_node.get(to)
                    && j != i
                {
                    deps[i].push(j);
                }
            }
        }
        fn visit(i: usize, deps: &[Vec<usize>], state: &mut [u8], order: &mut Vec<usize>) {
            if state[i] != 0 {
                return; // done, or a cycle: the given order is kept
            }
            state[i] = 1;
            for &d in &deps[i] {
                visit(d, deps, state, order);
            }
            state[i] = 2;
            order.push(i);
        }
        let mut order = Vec::with_capacity(edits.len());
        let mut state = vec![0u8; edits.len()];
        for i in 0..edits.len() {
            visit(i, &deps, &mut state, &mut order);
        }
        Ok(order)
    }

    /// Whether the draft of an impact modifies its node (Go's `ImpactModifies`): a creation does, a checkout when it
    /// differs from the version it starts from, no draft never.
    async fn impact_modifies(&mut self, id: &ChangeId, impact: &str) -> Result<bool> {
        self.view(id).await?;
        let Some(d) = self.views[id].fold.draft(impact, "").cloned() else {
            return Ok(false);
        };
        let Some(base) = &d.base else { return Ok(true) };
        Ok(draft_changed(&self.stored_draft(base).await?, &d))
    }

    /// Takes an impact out of the change with the impacts via it (Go's `WithdrawImpact` on the main flow).
    async fn withdraw_impact(&mut self, id: &ChangeId, impact: &str) -> Result<()> {
        let mut w = self.work_on(id, crate::flow::MAIN_FLOW).await?;
        self.select_impact(&mut w, impact)?;
        if !w.cn.flow.is_empty() {
            return Err(GraphError::conflict(format!(
                "change impact {impact} was declared on another flow"
            )));
        }
        let mut gone: Vec<String> = self.views[id]
            .fold
            .impacts
            .iter()
            .filter(|o| o.via == impact)
            .map(|o| o.id.clone())
            .collect();
        gone.push(impact.to_string());
        let mut events = Vec::new();
        let mut seen = HashSet::new();
        for i in gone {
            if seen.insert(i.clone()) {
                events.push(ImpactEvent {
                    change: id.clone(),
                    impact: i,
                    op: ImpactOp::Withdrawn,
                    ..ImpactEvent::default()
                });
            }
        }
        self.emit(events).await
    }
}
