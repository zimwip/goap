//! Port of `form.go`: the submission of the form of a human action (ADR 0115).

use std::collections::{HashMap, HashSet};
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::{
    Blackboard, INTENT_CREATED, INTENT_MODIFIED, Node, OUTCOME_DECIDED, REVIEW_REJECTED,
};
use goap_dsl::{NodeOp, NodeOpLink};
use goap_form as form;
use goap_methodology::{Action, Compiled, KIND_SCRIPT};
use serde_json::{Map, Value};

use crate::decisions::DecisionOp;
use crate::engine::Engine;
use crate::error::{Error, Result};
use crate::executors::{ActionContext, ExecError};
use crate::items::ItemInput;
use crate::process::Process;

/// The error of an invalid submission: Go's `errors.Join(graphapi.ErrInvalid, err)`.
fn joined_invalid(text: String) -> Error {
    Error::Graph(goap_graphapi::Error::invalid(format!(
        "{}\n{text}",
        goap_graphapi::INVALID
    )))
}

impl Engine {
    /// form_items checks the values of the form of a human action and turns them into what the submission adds
    /// (ADR 0115): the items and change impact operations its code returns, else an artifact of the values, the nodes
    /// its new node fields create, the rulings of its decision fields and the links its link fields ask for.
    pub(crate) async fn form_items(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        m: &Arc<Compiled>,
        action: &Action,
        i: usize,
        values: Map<String, Value>,
    ) -> Result<(Vec<ItemInput>, Vec<NodeOp>)> {
        if p.change_id.is_empty() {
            return Err(Error::invalid_state(format!(
                "process {} has no change attached",
                p.id
            )));
        }
        let bb = self.graph.blackboard_in(ctx, &p.change_id, &p.flow).await?;
        let (nodes, _) = self
            .graph
            .baseline_graph(ctx, &bb.change.baseline_id)
            .await?;
        let (env, mut impacted) = form_env(&bb, &nodes);
        let f = action.form.clone().unwrap_or_default();
        let (clean, issues) = form::validate(&f, &values, &env);
        if !issues.is_empty() {
            return Err(joined_invalid(format!(
                "the form of {}: {issues}",
                action.name
            )));
        }
        if !action.code.is_empty() {
            return self.run_form_code(ctx, p, m, action, i, bb, clean).await;
        }
        let typ = if f.artifact.is_empty() {
            action.name.clone()
        } else {
            f.artifact.clone()
        };
        let mut items = vec![ItemInput {
            r#type: "artifact".into(),
            target: typ,
            data: clean.clone(),
            ..Default::default()
        }];
        for r in form::rulings(&f, &clean) {
            items.push(ItemInput {
                r#type: "decisionPoint".into(),
                decision_point: Some(DecisionOp {
                    op: "rule".into(),
                    point: r.point,
                    outcome: OUTCOME_DECIDED.into(),
                    option: r.option,
                    justification: r.justification,
                    ..Default::default()
                }),
                ..Default::default()
            });
        }
        let mut ops = create_ops(form::new_nodes(&f, &clean), &mut impacted);
        ops.extend(link_ops(form::links(&f, &clean), &impacted));
        Ok((items, ops))
    }

    /// run_form_code runs the code of a human action on the values of its form, as a script action runs.
    #[allow(clippy::too_many_arguments)]
    async fn run_form_code(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        m: &Arc<Compiled>,
        action: &Action,
        i: usize,
        bb: Blackboard,
        values: Map<String, Value>,
    ) -> Result<(Vec<ItemInput>, Vec<NodeOp>)> {
        let Some(exec) = self.executors.get(KIND_SCRIPT).cloned() else {
            return Err(Error::msg(format!(
                "no script executor to run the code of {}",
                action.name
            )));
        };
        let agent_mcps = m
            .agent(&p.agent)
            .map(|a| a.mcps.clone())
            .unwrap_or_default();
        let pending = p.pending.clone().unwrap_or_default();
        let snapshot = Arc::new(p.clone());
        let mut host = self.new_host(snapshot.clone(), action, &agent_mcps);
        host.step = pending.step;
        host.base = p
            .steps
            .get(i)
            .map(|s| s.llm_calls.len() as i64)
            .unwrap_or_default();
        let host = Arc::new(host);
        let ac = ActionContext {
            process: snapshot,
            action: action.clone(),
            blackboard: bb,
            graph: self.graph.clone(),
            host: Some(host.clone()),
            step: pending.context.clone(),
            form: Some(values),
        };
        let (res, err) = match exec.execute(ctx, ac).await {
            Ok(r) => (r, None),
            Err(ExecError { partial, error }) => (partial, Some(error)),
        };
        let step = &mut p.steps[i];
        host.record(step);
        for l in &res.logs {
            let mut l = l.clone();
            l.step = pending.step;
            step.logs.push(l);
        }
        if let Some(err) = err {
            return Err(joined_invalid(format!(
                "the code of {}: {err}",
                action.name
            )));
        }
        if res.suspended {
            return Err(Error::Graph(goap_graphapi::Error::invalid(format!(
                "the code of the human action {} cannot wait for a sub-agent: {}",
                action.name,
                goap_graphapi::INVALID
            ))));
        }
        if !res.output.is_empty() {
            step.output = res.output.clone();
        }
        Ok((res.items, res.nodes))
    }
}

/// create_ops are the change impact operations creating the nodes of a form: each declared created, then written
/// with its properties; the nodes join the ones the change has an impact on.
fn create_ops(nodes: Vec<form::NewNode>, impacted: &mut HashSet<String>) -> Vec<NodeOp> {
    let mut ops = Vec::new();
    for n in nodes {
        ops.push(NodeOp {
            op: "declare".into(),
            intent: INTENT_CREATED.to_string(),
            key: n.key.clone(),
            r#type: n.r#type.clone(),
            rationale: "created by a form".into(),
            ..Default::default()
        });
        ops.push(NodeOp {
            op: "write".into(),
            node: n.key.clone(),
            props: n.props,
            ..Default::default()
        });
        impacted.insert(n.key);
    }
    ops
}

/// form_env resolves the node keys of a form: the nodes of the reference baseline, then the nodes the change
/// writes or creates (in the change); impacted holds the keys of the nodes the change has an impact on.
pub(crate) fn form_env(bb: &Blackboard, baseline: &[Node]) -> (form::Env, HashSet<String>) {
    let mut known: HashMap<String, form::Node> = HashMap::new();
    for n in baseline {
        if !n.deleted {
            known.insert(
                n.key.clone(),
                form::Node {
                    key: n.key.clone(),
                    r#type: n.r#type.clone(),
                    state: n.state.clone(),
                    namespace: n.namespace.clone(),
                    in_change: false,
                },
            );
        }
    }
    let mut impacted = HashSet::new();
    for cn in &bb.change.nodes {
        if cn.superseded || cn.review == REVIEW_REJECTED {
            continue;
        }
        impacted.insert(cn.key.clone());
        let mut n = known.get(&cn.key).cloned().unwrap_or_default();
        n.key = cn.key.clone();
        n.in_change = true;
        if n.r#type.is_empty() {
            n.r#type = cn.r#type.clone();
        }
        if let Some(post) = &cn.post
            && let Some(v) = bb.nodes.get(post)
        {
            n.r#type = v.node.r#type.clone();
            n.state = v.node.state.clone();
            if !v.node.namespace.is_empty() {
                n.namespace = v.node.namespace.clone();
            }
        }
        if n.namespace.is_empty() {
            n.namespace = bb.change.namespace.clone();
        }
        known.insert(cn.key.clone(), n);
    }
    let mut points: HashMap<String, form::Decision> = HashMap::new();
    for d in goap_domain::decision_points_of(bb) {
        points.insert(
            d.id.clone(),
            form::Decision {
                id: d.id.clone(),
                question: d.question.clone(),
                options: d.options.clone(),
                decided: !d.pending(),
            },
        );
    }
    let supertypes = bb.supertypes.clone();
    let env = form::Env {
        node: Some(Arc::new(move |k: &str| known.get(k).cloned())),
        is_a: Some(Arc::new(move |typ: &str, sup: &str| {
            typ == sup
                || supertypes
                    .get(typ)
                    .is_some_and(|s| s.iter().any(|x| x == sup))
        })),
        decision: Some(Arc::new(move |id: &str| points.get(id).cloned())),
    };
    (env, impacted)
}

/// link_ops are the change impact operations adding the links of a form: a source the change does not hold yet is
/// declared modified, then each source is written once with its links.
fn link_ops(links: Vec<form::Link>, impacted: &HashSet<String>) -> Vec<NodeOp> {
    let mut ops = Vec::new();
    let mut order: Vec<String> = Vec::new();
    let mut by_source: HashMap<String, Vec<NodeOpLink>> = HashMap::new();
    for l in links {
        if !by_source.contains_key(&l.from) {
            order.push(l.from.clone());
        }
        by_source
            .entry(l.from.clone())
            .or_default()
            .push(NodeOpLink {
                r#type: l.r#type,
                to: l.to,
                ..Default::default()
            });
    }
    for from in order {
        if !impacted.contains(&from) {
            ops.push(NodeOp {
                op: "declare".into(),
                intent: INTENT_MODIFIED.to_string(),
                key: from.clone(),
                rationale: "links asked by a form".into(),
                ..Default::default()
            });
        }
        let links = by_source.remove(&from).unwrap_or_default();
        ops.push(NodeOp {
            op: "write".into(),
            node: from,
            links,
            ..Default::default()
        });
    }
    ops
}
