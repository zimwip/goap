//! Change impact operations (Go's `nodeops.go`): what a script, a model or a person asks of the nodes of a change,
//! applied in order (ADR 0024, 0076, 0079).

use std::collections::{HashMap, HashSet};
use std::future::Future;

use goap_ctx::Ctx;
use goap_domain::{
    Blackboard, ChangeID, ChangeImpact, ChangeImpactID, ChangeItem, INTENT_CREATED,
    INTENT_MODIFIED, ItemID, LinkID, NodeIntent, NodeRef, REVIEW_ACCEPTED, REVIEW_REJECTED,
};
use goap_dsl::NodeOp;
use goap_graphapi::{
    LinkWrite, MergeInput, NodeCheckout, NodeCreate, NodeName, NodeTransition, NodeUpdate,
    SplitInput,
};
use goap_methodology::{Action, KIND_LLM, Verify};
use serde_json::{Map, Value, json};

use crate::engine::Engine;
use crate::error::{Error, Result};
use crate::process::Process;

/// IMPACT_WRITE_RETRIES bounds the retry of a single change-impact write on a conflict (ADR 0031, gap 6).
const IMPACT_WRITE_RETRIES: usize = 3;

async fn retry_on_conflict<T, F, Fut>(mut call: F) -> Result<T>
where
    F: FnMut() -> Fut,
    Fut: Future<Output = std::result::Result<T, goap_graphapi::Error>>,
{
    let mut last = None;
    for _ in 0..IMPACT_WRITE_RETRIES {
        match call().await {
            Ok(v) => return Ok(v),
            Err(e) if e.is_conflict() => last = Some(e),
            Err(e) => return Err(e.into()),
        }
    }
    Err(last
        .map(Error::from)
        .unwrap_or_else(|| Error::msg("conflict")))
}

/// change_impacts_from_blackboard builds the change impact snapshot given to scripts.
pub fn change_impacts_from_blackboard(bb: &Blackboard) -> Vec<goap_dsl::ChangeImpact> {
    let view = |r: Option<&NodeRef>| -> Option<goap_dsl::Node> {
        let r = r?;
        let mut n = goap_dsl::Node {
            id: r.id.to_string(),
            version: r.version,
            ..Default::default()
        };
        if let Some(v) = bb.nodes.get(r) {
            n.key = v.node.key.clone();
            n.r#type = v.node.r#type.clone();
            n.state = v.node.state.clone();
            n.props = v.node.properties.clone();
        }
        Some(n)
    };
    let end = |r: &NodeRef| -> goap_dsl::LinkEnd {
        let mut e = goap_dsl::LinkEnd {
            id: r.id.to_string(),
            version: r.version,
            ..Default::default()
        };
        if let Some(n) = bb.neighbors.get(r) {
            e.key = n.key.clone();
            e.r#type = n.r#type.clone();
        } else if let Some(v) = bb.nodes.get(r) {
            e.key = v.node.key.clone();
            e.r#type = v.node.r#type.clone();
        }
        e
    };
    let mut out = Vec::with_capacity(bb.change.nodes.len());
    for cn in &bb.change.nodes {
        let mut x = goap_dsl::ChangeImpact {
            id: cn.id.to_string(),
            key: cn.key.clone(),
            r#type: cn.r#type.clone(),
            intent: cn.intent.to_string(),
            rationale: cn.rationale.clone(),
            review: cn.review.to_string(),
            planned: cn.post.is_none(),
            pre: view(cn.pre.as_ref()),
            post: view(cn.post.as_ref()),
            landed: view(cn.landed.as_ref()),
            items: Vec::new(),
            links: Vec::new(),
            ..Default::default()
        };
        x.drafted = cn.drafted();
        if let Some(post) = &cn.post
            && let Some(v) = bb.nodes.get(post)
        {
            for l in &v.out {
                x.links.push(goap_dsl::Link {
                    id: l.id.to_string(),
                    r#type: l.r#type.clone(),
                    from: goap_dsl::LinkEnd {
                        id: post.id.to_string(),
                        version: post.version,
                        key: cn.key.clone(),
                        r#type: cn.r#type.clone(),
                    },
                    to: end(&l.to),
                });
            }
        }
        x.items = cn.items.iter().map(|i| i.to_string()).collect();
        out.push(x);
    }
    out
}

/// What the engine knows of the verification of the action it runs (ADR 0075).
#[derive(Debug, Clone)]
pub(crate) struct Verifying {
    action: String,
    model: String,
    verify: Verify,
}

/// with_verify marks the context of an action that declares a verification.
pub(crate) fn with_verify(ctx: &Ctx, a: &Action) -> Ctx {
    let Some(v) = &a.verify else {
        return ctx.clone();
    };
    ctx.with(Verifying {
        action: a.name.clone(),
        model: model_alias(a),
        verify: v.clone(),
    })
}

fn verify_of(ctx: &Ctx) -> Option<Verifying> {
    ctx.value::<Verifying>().cloned()
}

/// model_alias is the model alias of an llm action, the producer a model oracle must differ from (ADR 0021).
fn model_alias(a: &Action) -> String {
    if a.kind == KIND_LLM {
        a.model.clone()
    } else {
        String::new()
    }
}

/// A node a script declares as created, held until the first operation that needs it.
#[derive(Debug, Clone, PartialEq)]
struct Creation {
    r#ref: String,
    key: String,
    typ: String,
    rationale: String,
}

/// The state of one apply_node_ops call.
struct Ops<'a> {
    e: &'a Engine,
    ctx: &'a Ctx,
    p: &'a Process,
    change: ChangeID,
    produced_by: &'a str,
    execution: &'a str,
    baseline: HashMap<String, NodeRef>,
    by_key: HashMap<String, ChangeImpactID>,
    posts: HashMap<ChangeImpactID, NodeRef>,
    out: HashMap<ChangeImpactID, bool>,
    planned: HashSet<ChangeImpactID>,
    local: HashMap<String, ChangeImpactID>,
    declared: Vec<ChangeImpactID>,
    pending: HashMap<String, usize>,
    creations: Vec<Creation>,
    vf: Option<Verifying>,
    verified: HashSet<ChangeImpactID>,
}

impl Ops<'_> {
    fn checkout(&self, id: &ChangeImpactID) -> NodeCheckout {
        NodeCheckout {
            impact: id.clone(),
            flow: self.p.flow.clone(),
            execution: self.execution.to_string(),
            ..Default::default()
        }
    }

    async fn create(&mut self, ci: usize, props: Map<String, Value>) -> Result<ChangeImpact> {
        let c = self.creations[ci].clone();
        self.pending.remove(&c.r#ref);
        self.pending.remove(&c.key);
        let cn = retry_on_conflict(|| {
            self.e.graph.impact_node_create(
                self.ctx,
                &self.change,
                NodeCreate {
                    key: c.key.clone(),
                    r#type: c.typ.clone(),
                    properties: props.clone(),
                    rationale: c.rationale.clone(),
                    flow: self.p.flow.clone(),
                    execution: self.execution.to_string(),
                    produced_by: self.produced_by.to_string(),
                    ..Default::default()
                },
            )
        })
        .await?;
        self.local.insert(c.r#ref.clone(), cn.id.clone());
        self.by_key.insert(cn.key.clone(), cn.id.clone());
        self.declared.push(cn.id.clone());
        if let Some(post) = &cn.post {
            self.posts.insert(cn.id.clone(), post.clone());
            self.out.insert(cn.id.clone(), true);
        }
        self.produce(&cn.id).await?;
        Ok(cn)
    }

    async fn resolve(&mut self, s: &str) -> Result<ChangeImpactID> {
        if let Some(&ci) = self.pending.get(s) {
            return Ok(self.create(ci, Map::new()).await?.id);
        }
        if s.starts_with('#') {
            return self
                .local
                .get(s)
                .cloned()
                .ok_or_else(|| Error::msg(format!("unknown change impact reference {s:?}")));
        }
        self.by_key.get(s).cloned().ok_or_else(|| {
            Error::msg(format!(
                "no change impact for {s:?}: declare it with impactNode or impactNodeCreate"
            ))
        })
    }

    /// target is a link target: a node written by the change, else the reference baseline.
    async fn target(&mut self, s: &str) -> Result<NodeRef> {
        if s.starts_with('#') {
            let id = self.resolve(s).await?;
            return self.posts.get(&id).cloned().ok_or_else(|| {
                Error::msg(format!(
                    "{s} is not written yet: write it before linking to it"
                ))
            });
        }
        if self.pending.contains_key(s) {
            self.resolve(s).await?;
        }
        if let Some(id) = self.by_key.get(s).cloned() {
            if let Some(r) = self.posts.get(&id) {
                return Ok(r.clone());
            }
            if self.planned.contains(&id) {
                // a planned creation a link needs: its draft, bare
                let co = self.checkout(&id);
                let cn = retry_on_conflict(|| {
                    self.e
                        .graph
                        .impact_node_checkout(self.ctx, &self.change, co.clone())
                })
                .await?;
                self.planned.remove(&id);
                let post = cn.post.clone().unwrap_or_default();
                self.posts.insert(id.clone(), post.clone());
                self.out.insert(id, true);
                return Ok(post);
            }
        }
        self.baseline
            .get(s)
            .cloned()
            .ok_or_else(|| Error::msg(format!("unknown node {s:?}")))
    }

    async fn record(
        &self,
        state: &str,
        impact: &ChangeImpactID,
        extra: Map<String, Value>,
    ) -> Result<()> {
        let mut data = Map::new();
        data.insert(goap_verify::KEY_STATE.into(), json!(state));
        data.insert(goap_verify::KEY_ACTION.into(), json!(self.produced_by));
        data.insert(goap_verify::KEY_IMPACT.into(), json!(impact.to_string()));
        data.extend(extra);
        self.e
            .record_verification(self.ctx, self.p, self.execution, self.produced_by, data)
            .await
    }

    async fn produce(&mut self, id: &ChangeImpactID) -> Result<()> {
        let Some(vf) = self.vf.clone() else {
            return Ok(());
        };
        if !self.verified.insert(id.clone()) {
            return Ok(());
        }
        let mut d = Map::new();
        d.insert(goap_verify::KEY_STATE.into(), json!(goap_verify::PRODUCED));
        d.insert(goap_verify::KEY_ACTION.into(), json!(vf.action));
        d.insert(goap_verify::KEY_IMPACTS.into(), json!([id.to_string()]));
        d.insert(goap_verify::KEY_ORACLE.into(), json!(vf.verify.oracle));
        d.insert(
            goap_verify::KEY_INDEPENDENT.into(),
            json!(Verify::is_independent(Some(&vf.verify))),
        );
        if !vf.model.is_empty() {
            d.insert(goap_verify::KEY_MODEL.into(), json!(vf.model));
        }
        self.e
            .record_verification(self.ctx, self.p, self.execution, self.produced_by, d)
            .await
    }

    fn propose(&self, intent: NodeIntent, op: &NodeOp, typ: String) -> ChangeImpact {
        ChangeImpact {
            intent,
            key: op.key.clone(),
            r#type: typ,
            rationale: op.rationale.clone(),
            produced_by: self.produced_by.to_string(),
            execution: self.execution.to_string(),
            flow: self.p.flow.clone(),
            ..Default::default()
        }
    }

    async fn apply(
        &mut self,
        op: &NodeOp,
        bb: &Blackboard,
        qualify: &(dyn Fn(&str) -> String + Sync),
    ) -> Result<()> {
        match op.op.as_str() {
            "declare" => {
                if op.intent == INTENT_CREATED.as_str() {
                    let c = Creation {
                        r#ref: op.r#ref.clone(),
                        key: op.key.clone(),
                        typ: qualify(&op.r#type),
                        rationale: op.rationale.clone(),
                    };
                    let i = self.creations.len();
                    self.pending.insert(c.key.clone(), i);
                    if !c.r#ref.is_empty() {
                        self.pending.insert(c.r#ref.clone(), i);
                    }
                    self.creations.push(c);
                    return Ok(());
                }
                let mut cn = self.propose(
                    NodeIntent::from(op.intent.as_str()),
                    op,
                    qualify(&op.r#type),
                );
                if cn.intent == INTENT_MODIFIED {
                    let Some(r) = self.baseline.get(&op.key) else {
                        return Err(Error::msg(format!(
                            "unknown node {:?} in the reference baseline",
                            op.key
                        )));
                    };
                    cn.pre = Some(r.clone());
                }
                let added = retry_on_conflict(|| {
                    self.e
                        .graph
                        .propose_impact(self.ctx, &self.change, vec![cn.clone()])
                })
                .await?;
                let a = added.first().cloned().unwrap_or_default();
                self.local.insert(op.r#ref.clone(), a.id.clone());
                self.by_key.insert(a.key.clone(), a.id.clone());
                self.declared.push(a.id.clone());
                self.produce(&a.id).await?;
            }
            "plan" => {
                // a creation planned, not written: no draft until a write of its key
                let cn = self.propose(INTENT_CREATED, op, qualify(&op.r#type));
                let added = retry_on_conflict(|| {
                    self.e
                        .graph
                        .propose_impact(self.ctx, &self.change, vec![cn.clone()])
                })
                .await?;
                let a = added.first().cloned().unwrap_or_default();
                self.local.insert(op.r#ref.clone(), a.id.clone());
                self.by_key.insert(a.key.clone(), a.id.clone());
                self.planned.insert(a.id.clone());
                self.declared.push(a.id.clone());
            }
            "merge" | "split" => {
                let names = if op.op == "split" {
                    vec![op.node.clone()]
                } else {
                    op.sources.clone()
                };
                let mut srcs = Vec::new();
                for s in &names {
                    match self.resolve(s).await {
                        Ok(id) => srcs.push(NodeName {
                            impact: id,
                            ..Default::default()
                        }),
                        Err(e) if s.starts_with('#') => return Err(e),
                        Err(_) => srcs.push(NodeName {
                            key: s.clone(),
                            ..Default::default()
                        }),
                    }
                }
                let into: Vec<NodeCreate> = op
                    .into
                    .iter()
                    .map(|x| NodeCreate {
                        key: x.key.clone(),
                        r#type: qualify(&x.r#type),
                        properties: x.props.clone(),
                        rationale: x.rationale.clone(),
                        flow: self.p.flow.clone(),
                        execution: self.execution.to_string(),
                        produced_by: self.produced_by.to_string(),
                        ..Default::default()
                    })
                    .collect();
                let res = if op.op == "merge" && into.len() == 1 {
                    self.e
                        .graph
                        .impact_node_merge(
                            self.ctx,
                            &self.change,
                            MergeInput {
                                sources: srcs,
                                into: into[0].clone(),
                                rationale: op.rationale.clone(),
                                flow: self.p.flow.clone(),
                                execution: self.execution.to_string(),
                                ..Default::default()
                            },
                        )
                        .await?
                } else if op.op == "split" && srcs.len() == 1 {
                    self.e
                        .graph
                        .impact_node_split(
                            self.ctx,
                            &self.change,
                            SplitInput {
                                source: srcs[0].clone(),
                                into,
                                rationale: op.rationale.clone(),
                                flow: self.p.flow.clone(),
                                execution: self.execution.to_string(),
                                ..Default::default()
                            },
                        )
                        .await?
                } else {
                    return Err(Error::msg(format!("malformed {}", op.op)));
                };
                for (i, cn) in res.successors.iter().enumerate() {
                    if let Some(x) = op.into.get(i) {
                        self.local.insert(x.r#ref.clone(), cn.id.clone());
                    }
                }
                for cn in res
                    .successors
                    .iter()
                    .chain(&res.sources)
                    .chain(&res.parents)
                {
                    self.by_key.insert(cn.key.clone(), cn.id.clone());
                    self.declared.push(cn.id.clone());
                    if let Some(post) = &cn.post {
                        self.posts.insert(cn.id.clone(), post.clone());
                    }
                }
                for cn in res.successors.iter().chain(&res.parents) {
                    self.out.insert(cn.id.clone(), true);
                }
                for cn in &res.successors {
                    self.produce(&cn.id).await?;
                }
            }
            "write" => {
                let mut props_in = false; // the creation carried the properties
                let id = if let Some(&ci) = self.pending.get(&op.node) {
                    let cn = self.create(ci, op.props.clone()).await?;
                    props_in = true;
                    cn.id
                } else {
                    self.resolve(&op.node).await?
                };
                if !op.state.is_empty() {
                    return Err(Error::msg(
                        "a lifecycle state is a transition of its own: use impactNodeTransition (ADR 0076)",
                    ));
                }
                // the first write checks the node out: its draft, edited by the next ones (ADR 0076, 0079)
                if !self.out.get(&id).copied().unwrap_or(false) {
                    let co = self.checkout(&id);
                    let cn = retry_on_conflict(|| {
                        self.e
                            .graph
                            .impact_node_checkout(self.ctx, &self.change, co.clone())
                    })
                    .await?;
                    self.posts
                        .insert(id.clone(), cn.post.clone().unwrap_or_default());
                    self.out.insert(id.clone(), true);
                    self.planned.remove(&id);
                }
                if !op.props.is_empty() && !props_in {
                    self.e
                        .graph
                        .impact_node_update(
                            self.ctx,
                            &self.change,
                            &id,
                            NodeUpdate {
                                properties: op.props.clone(),
                                flow: self.p.flow.clone(),
                                execution: self.execution.to_string(),
                                ..Default::default()
                            },
                        )
                        .await?;
                }
                for l in &op.links {
                    let to = self.target(&l.to).await?;
                    self.e
                        .graph
                        .impact_link_create(
                            self.ctx,
                            &self.change,
                            &id,
                            LinkWrite {
                                r#type: qualify(&l.r#type),
                                to,
                                properties: l.props.clone(),
                            },
                            &self.p.flow,
                            self.execution,
                        )
                        .await?;
                }
                for l in &op.remove_links {
                    self.e
                        .graph
                        .impact_link_delete(
                            self.ctx,
                            &self.change,
                            &LinkID::from(l.as_str()),
                            &self.p.flow,
                            self.execution,
                        )
                        .await?;
                }
                self.produce(&id).await?;
            }
            "transition" => {
                let id = self.resolve(&op.node).await?;
                let tr = NodeTransition {
                    checkout: self.checkout(&id),
                    to: op.state.clone(),
                };
                let cn = retry_on_conflict(|| {
                    self.e
                        .graph
                        .impact_node_transition(self.ctx, &self.change, tr.clone())
                })
                .await?;
                // a transition checks the node out when it has no draft (ADR 0079)
                self.posts
                    .insert(id.clone(), cn.post.clone().unwrap_or_default());
                self.out.insert(id, true);
            }
            "cancel" => {
                let id = self.resolve(&op.node).await?;
                let cn = self
                    .e
                    .graph
                    .impact_node_cancel(self.ctx, &self.change, &id, &self.p.flow, self.execution)
                    .await?;
                self.out.insert(id.clone(), false);
                match cn.post {
                    Some(post) => {
                        self.posts.insert(id, post);
                    }
                    None => {
                        self.posts.remove(&id);
                    }
                }
            }
            "remove" => {
                let id = self.resolve(&op.node).await?;
                self.e
                    .graph
                    .withdraw_impact(self.ctx, &self.change, &id, &self.p.flow, self.execution)
                    .await?;
                self.out.remove(&id);
                self.posts.remove(&id);
                self.by_key.retain(|_, v| *v != id);
            }
            "review" => {
                let id = self.resolve(&op.node).await?;
                let mut status = REVIEW_REJECTED;
                if op.accept {
                    self.e.check_oversight(self.ctx, &bb.change, &id).await?;
                    status = REVIEW_ACCEPTED;
                }
                // an acceptance with a reserve stands on a derogation in force that covers the effect (ADR 0075 §2)
                let mut derogation = String::new();
                if !op.reserve.is_empty() {
                    if !op.accept {
                        return Err(Error::msg("only an acceptance may carry a reserve"));
                    }
                    let cur = self
                        .e
                        .graph
                        .blackboard_in(self.ctx, &self.change, &self.p.flow)
                        .await?;
                    let ids = id.to_string();
                    let d = goap_risk::covering(
                        &cur.change,
                        self.e.clock(),
                        &[&ids, &op.node, self.produced_by],
                    );
                    match d {
                        Some(d) if d.key == op.reserve => derogation = d.key,
                        _ => {
                            return Err(Error::msg(format!(
                                "no derogation {} in force covers {}: it is open, unexpired and targets the effect or its action",
                                op.reserve, op.node
                            )));
                        }
                    }
                }
                retry_on_conflict(|| {
                    self.e.graph.impact_node_review_on(
                        self.ctx,
                        &self.change,
                        &self.p.flow,
                        self.execution,
                        &id,
                        status.clone(),
                        self.produced_by,
                        &op.comment,
                    )
                })
                .await?;
                if self.verified.contains(&id) {
                    let mut final_state = goap_verify::ACCEPTED;
                    let mut extra = Map::new();
                    extra.insert(goap_verify::KEY_BY.into(), json!(self.produced_by));
                    if status == REVIEW_REJECTED {
                        final_state = goap_verify::REJECTED;
                    }
                    if !derogation.is_empty() {
                        final_state = goap_verify::ACCEPTED_WITH_RESERVE;
                        extra.insert(goap_verify::KEY_DEROGATION.into(), json!(derogation));
                    }
                    let mut by = Map::new();
                    by.insert(goap_verify::KEY_BY.into(), json!(self.produced_by));
                    self.record(goap_verify::VERIFIED, &id, by).await?;
                    self.record(final_state, &id, extra).await?;
                }
            }
            _ => return Err(Error::msg("unknown operation")),
        }
        Ok(())
    }
}

impl Engine {
    /// apply_node_ops applies the change impact operations a script buffered, in order: a declaration adds a change
    /// impact, a write edits its draft (the node is checked out on the first write), a review accepts or rejects it, a
    /// transition moves it along its lifecycle, a cancel drops the draft. References ("#nN") name the change impacts
    /// declared earlier by the same script; a key names a change impact the process sees.
    pub(crate) async fn apply_node_ops(
        &self,
        ctx: &Ctx,
        p: &Process,
        ops: Vec<NodeOp>,
        produced_by: &str,
        execution: &str,
    ) -> Result<Vec<ChangeImpactID>> {
        if ops.is_empty() {
            return Ok(Vec::new());
        }
        if p.change_id.is_empty() {
            return Err(Error::msg(format!(
                "process {} has no change attached: call goap-scheduler/attach first (ADR 0031)",
                p.id
            )));
        }
        let bb = self.graph.blackboard_in(ctx, &p.change_id, &p.flow).await?;
        // the pre version of a change impact is the released one: the reference baseline, whatever option is active
        let (nodes, _) = self
            .graph
            .baseline_graph(ctx, &bb.change.baseline_id)
            .await?;
        let mut o = Ops {
            e: self,
            ctx,
            p,
            change: p.change_id.clone(),
            produced_by,
            execution,
            baseline: nodes.iter().map(|n| (n.key.clone(), n.r#ref())).collect(),
            by_key: HashMap::new(),
            posts: HashMap::new(),
            out: HashMap::new(),
            planned: HashSet::new(),
            local: HashMap::new(),
            declared: Vec::new(),
            pending: HashMap::new(),
            creations: Vec::new(),
            vf: verify_of(ctx),
            verified: HashSet::new(),
        };
        for cn in &bb.change.nodes {
            if !cn.items.is_empty() {
                continue; // derived from items: decided through them
            }
            o.by_key.insert(cn.key.clone(), cn.id.clone());
            if cn.intent == INTENT_CREATED && cn.post.is_none() && cn.landed.is_none() {
                o.planned.insert(cn.id.clone());
            }
            if let Some(post) = &cn.post {
                o.posts.insert(cn.id.clone(), post.clone());
                let working = bb
                    .nodes
                    .get(post)
                    .is_some_and(|v| goap_graphapi::is_working(&bb.change, &p.flow, &v.node));
                o.out.insert(cn.id.clone(), working);
            }
        }
        for sub in goap_verify::subjects(&bb.change.items) {
            o.verified.insert(ChangeImpactID::from(sub.impact));
        }
        let ns = bb.change.namespace.clone();
        // a bare type or link type written by an action is one of the target namespace of the change (ADR 0012)
        let qualify = move |t: &str| -> String {
            if t.is_empty() || t.contains(goap_domain::TYPE_SEP) || ns.is_empty() {
                return t.to_string();
            }
            format!("{ns}{}{t}", goap_domain::TYPE_SEP)
        };
        for (i, op) in ops.iter().enumerate() {
            if let Err(err) = o.apply(op, &bb, &qualify).await {
                return Err(err.context(format!("change impact operation {i} ({})", op.op)));
            }
        }
        for ci in 0..o.creations.len() {
            let key = o.creations[ci].key.clone();
            if o.pending.get(&key) == Some(&ci)
                && let Err(err) = o.create(ci, Map::new()).await
            {
                return Err(err.context(format!("change impact of {key}")));
            }
        }
        Ok(o.declared)
    }

    /// record_verification writes a verification entry of the change log (fact.verification, ADR 0030).
    pub(crate) async fn record_verification(
        &self,
        ctx: &Ctx,
        p: &Process,
        execution: &str,
        by: &str,
        data: Map<String, Value>,
    ) -> Result<()> {
        let target = data
            .get(goap_verify::KEY_STATE)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string();
        let it = ChangeItem {
            id: ItemID::from(uuid::Uuid::new_v4().to_string()),
            r#type: goap_verify::TYPE_VERIFICATION,
            target,
            data,
            produced_by: by.to_string(),
            execution: execution.to_string(),
            flow: p.flow.clone(),
            ..Default::default()
        };
        it.validate()?;
        self.graph.add_items(ctx, &p.change_id, vec![it]).await?;
        Ok(())
    }
}
