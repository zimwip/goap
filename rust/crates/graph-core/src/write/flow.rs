//! The flows and options of a change (Go's `graph/graph/flow.go`, `option.go`, `flownodes.go`; ADR 0009 §3, 0017,
//! 0025, 0032 §6): every move is a `flow` item of the log, the flows its replay ([`crate::flow::Flows`]). Adopting a
//! flow installs its drafts on the main flow, discarding it rejects the impacts it declared.
use std::collections::HashSet;

use serde::Serialize;
use serde_json::{Map, Value};

use super::{
    items::ItemWrite,
    tx::Tx,
    work::{fold_any, node_of},
};
use crate::{
    GraphError, Result,
    change::Change,
    decision::{DecisionEvent, OP_RULE, OUTCOME_DECIDED},
    flow::{
        FLOW_ADOPTED, FLOW_DISCARDED, Flow, FlowEvent, Flows, Item, MAIN_FLOW, OP_ACTIVATE, OP_ADOPT, OP_DEACTIVATE,
        OP_DISCARD, OP_EVALUATE, OP_OPEN, OptionSpec, TYPE_FLOW, stale_closure,
    },
    impact::{
        self, ChangeImpact, Draft, ImpactEvent, ImpactOp, LOG_FACT, LogEntry, REVIEW_ACCEPTED, REVIEW_REJECTED, Review,
    },
    jsontext,
    model::{ChangeId, NodeId},
    store::{ReadTx, WriteTx},
};

/// A flow item as Go marshals a `domain.ChangeItem` carrying a flow event.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct FlowItemJson<'a> {
    id: &'a str,
    #[serde(rename = "type")]
    typ: &'a str,
    target: &'a str,
    status: &'a str,
    flow_event: &'a FlowEvent,
    produced_by: &'a str,
    #[serde(with = "jsontext::time")]
    created_at: chrono::DateTime<chrono::Utc>,
}

/// The items of a change, in the order of its log.
pub async fn items_of<T: ReadTx>(tx: &T, id: &ChangeId) -> Result<Vec<Item>> {
    tx.log(id, 0, &[&format!("{LOG_FACT}.")])
        .await?
        .iter()
        .map(|l| Item::of(&l.payload).map_err(GraphError::internal))
        .collect()
}

/// The flows of a change, replayed from its items.
pub async fn flows_of<T: ReadTx>(tx: &T, id: &ChangeId) -> Result<Flows> {
    Ok(Flows::replay(&items_of(tx, id).await?))
}

/// An option of a change by its id, or by its name (the open options first); the name given when none answers.
pub fn option_id(fx: &Flows, s: &str) -> String {
    if fx.options().any(|o| o.id == s) {
        return s.to_string();
    }
    for open in [true, false] {
        if let Some(o) = fx.options().find(|o| {
            o.is_open() == open
                && o.option
                    .as_ref()
                    .is_some_and(|spec| spec.name.to_lowercase() == s.to_lowercase())
        }) {
            return o.id.clone();
        }
    }
    s.to_string()
}

/// An open option of a change (Go's `openOption`).
pub fn open_option(fx: &Flows, change: &ChangeId, option: &str) -> Result<Flow> {
    let Some(f) = fx.get(option).filter(|f| f.option.is_some()) else {
        return Err(GraphError::not_found(format!("option {option} of change {change}")));
    };
    if !f.is_open() {
        let name = f.option.as_ref().map(|o| o.name.as_str()).unwrap_or_default();
        return Err(GraphError::conflict(format!("option {name} is {}", f.option_status())));
    }
    Ok(f.clone())
}

/// What opens a flow branch (Go's `OpenFlowRequest`).
#[derive(Debug, Clone, Default)]
pub struct OpenFlow {
    pub parent: String,
    pub fork_after: String,
    pub seeds: Vec<String>,
    pub stale_runs: Vec<String>,
    pub origin: Map<String, Value>,
    pub items: Vec<ItemWrite>,
}

impl<S: WriteTx> Tx<'_, S> {
    /// The change a flow operation works on (Go's `flowChange`): open.
    pub(crate) async fn flow_change(&mut self, id: &ChangeId) -> Result<Change> {
        self.lock_change(id).await?;
        let c = self.change_head(id).await?;
        if c.status != "draft" && c.status != "active" {
            return Err(GraphError::conflict(format!("change {id} is {}", c.status)));
        }
        Ok(c)
    }

    pub(crate) async fn flows(&self, id: &ChangeId) -> Result<Flows> {
        flows_of(self.store, id).await
    }

    async fn flow_of(&self, id: &ChangeId, flow: &str) -> Result<Flow> {
        Ok(self.flows(id).await?.get(flow).cloned().unwrap_or_default())
    }

    /// Appends a flow event to the log of a change (Go's `flowEvent`).
    async fn flow_event(&mut self, id: &ChangeId, e: FlowEvent) -> Result<()> {
        let item = self.env.new_id();
        let at = self.env.now();
        let target = format!("flow.{}", e.op);
        let payload = jsontext::to_string(&FlowItemJson {
            id: &item,
            typ: TYPE_FLOW,
            target: &target,
            status: "accepted",
            flow_event: &e,
            produced_by: "graph.flow",
            created_at: at,
        });
        self.append_log(&LogEntry {
            seq: 0,
            id: item,
            change: id.clone(),
            typ: format!("{LOG_FACT}.{TYPE_FLOW}"),
            flow: e.flow.clone(),
            process: String::new(),
            execution: String::new(),
            subject: e.op.clone(),
            by: if e.by.is_empty() {
                "graph.flow".into()
            } else {
                e.by.clone()
            },
            at,
            causation: String::new(),
            payload,
        })
        .await?;
        Ok(())
    }

    async fn activate(&mut self, c: &mut Change) -> Result<()> {
        if c.status == "draft" {
            c.status = "active".into();
            self.put_change(c).await?;
        }
        Ok(())
    }

    /// Opens an option: a flow branch of its own, forked from the main flow, that invalidates nothing.
    pub async fn open_option(
        &mut self,
        id: &ChangeId,
        name: &str,
        hypothesis: &str,
        activate: bool,
        by: &str,
    ) -> Result<Flow> {
        let name = name.trim();
        if name.is_empty() {
            return Err(GraphError::invalid("an option needs a name"));
        }
        let mut c = self.flow_change(id).await?;
        let fx = self.flows(id).await?;
        if fx.options().any(|o| {
            o.is_open()
                && o.option
                    .as_ref()
                    .is_some_and(|s| s.name.to_lowercase() == name.to_lowercase())
        }) {
            return Err(GraphError::conflict(format!(
                "change {id} already has an open option {name:?}"
            )));
        }
        let flow = self.env.new_id();
        self.flow_event(
            id,
            FlowEvent {
                op: OP_OPEN.into(),
                flow: flow.clone(),
                by: by.into(),
                option: Some(OptionSpec {
                    name: name.into(),
                    hypothesis: hypothesis.into(),
                    intent: String::new(),
                }),
                ..FlowEvent::default()
            },
        )
        .await?;
        if activate {
            self.flow_event(
                id,
                FlowEvent {
                    op: OP_ACTIVATE.into(),
                    flow: flow.clone(),
                    by: by.into(),
                    ..FlowEvent::default()
                },
            )
            .await?;
        }
        self.activate(&mut c).await?;
        self.flow_of(id, &flow).await
    }

    /// Makes an open option the one the change works on; empty or "main" goes back to the main flow. Answers the
    /// option now active.
    pub async fn activate_option(&mut self, id: &ChangeId, option: &str, by: &str) -> Result<String> {
        self.flow_change(id).await?;
        let fx = self.flows(id).await?;
        let cur = fx.active.clone();
        if option.is_empty() || option == MAIN_FLOW {
            if !cur.is_empty() {
                self.flow_event(
                    id,
                    FlowEvent {
                        op: OP_DEACTIVATE.into(),
                        flow: cur,
                        by: by.into(),
                        ..FlowEvent::default()
                    },
                )
                .await?;
            }
            return Ok(String::new());
        }
        open_option(&fx, id, option)?;
        if cur != option {
            self.flow_event(
                id,
                FlowEvent {
                    op: OP_ACTIVATE.into(),
                    flow: option.into(),
                    by: by.into(),
                    ..FlowEvent::default()
                },
            )
            .await?;
        }
        Ok(option.to_string())
    }

    /// Records the evaluation of an open option.
    pub async fn evaluate_option(&mut self, id: &ChangeId, option: &str, by: &str, comment: &str) -> Result<Flow> {
        if comment.trim().is_empty() {
            return Err(GraphError::invalid("an evaluation needs a comment"));
        }
        self.flow_change(id).await?;
        open_option(&self.flows(id).await?, id, option)?;
        self.flow_event(
            id,
            FlowEvent {
                op: OP_EVALUATE.into(),
                flow: option.into(),
                by: by.into(),
                comment: comment.into(),
                ..FlowEvent::default()
            },
        )
        .await?;
        self.flow_of(id, option).await
    }

    /// Rejects an open option: its flow is discarded.
    pub async fn reject_option(&mut self, id: &ChangeId, option: &str, by: &str) -> Result<Flow> {
        self.flow_change(id).await?;
        open_option(&self.flows(id).await?, id, option)?;
        self.decide_flow(id, option, OP_DISCARD, by).await?;
        self.flow_of(id, option).await
    }

    /// Selects an open option: the other open ones are rejected, its flow adopted, and the decision points choosing
    /// among the options are ruled for it.
    pub async fn select_option(&mut self, id: &ChangeId, option: &str, by: &str) -> Result<Flow> {
        self.flow_change(id).await?;
        self.select_option_tx(id, option, by).await?;
        // a selection by hand settles the decision points that were choosing among the options (ADR 0009 §4)
        let now = self.env.now();
        for d in super::decision::points_of(self.store, id, now).await? {
            if d.pending() && d.options.iter().any(|o| o == option) {
                self.put_decision_event(
                    id,
                    &DecisionEvent {
                        op: OP_RULE.into(),
                        point: d.id.clone(),
                        outcome: OUTCOME_DECIDED.into(),
                        option: option.into(),
                        confidence: 1.0,
                        justification: "option selected by hand".into(),
                        human: true,
                        by: by.into(),
                        ..DecisionEvent::default()
                    },
                )
                .await?;
            }
        }
        self.flow_of(id, option).await
    }

    /// Rejects the other open options of a change, then adopts the flow of an option (Go's `selectOptionTx`).
    pub(crate) async fn select_option_tx(&mut self, id: &ChangeId, option: &str, by: &str) -> Result<()> {
        let fx = self.flows(id).await?;
        open_option(&fx, id, option)?;
        let others: Vec<String> = fx
            .options()
            .filter(|o| o.id != option && o.is_open())
            .map(|o| o.id.clone())
            .collect();
        for o in others {
            self.decide_flow(id, &o, OP_DISCARD, by).await?;
        }
        self.decide_flow(id, option, OP_ADOPT, by).await
    }

    /// Adopts or discards an open flow branch, answering it as it is then.
    pub async fn decide_flow_op(&mut self, id: &ChangeId, flow: &str, op: &str, by: &str) -> Result<Flow> {
        self.flow_change(id).await?;
        self.decide_flow(id, flow, op, by).await?;
        self.flow_of(id, flow).await
    }

    /// Adopts or discards an open flow of a change (Go's `decideFlowTx`).
    async fn decide_flow(&mut self, id: &ChangeId, flow: &str, op: &str, by: &str) -> Result<()> {
        let fx = self.flows(id).await?;
        let Some(cur) = fx.get(flow).cloned() else {
            return Err(GraphError::not_found(format!("flow {flow}")));
        };
        if !cur.is_open() {
            return Err(GraphError::conflict(format!("flow {flow} is {}", cur.status)));
        }
        if op == OP_ADOPT {
            if !cur.parent.is_empty() && fx.effective(&cur.parent) != FLOW_ADOPTED {
                return Err(GraphError::conflict(format!(
                    "flow {flow}: its parent {} is not adopted",
                    cur.parent
                )));
            }
            if let Some(a) = cur.competes_with.first() {
                return Err(GraphError::conflict(format!(
                    "flow {flow} competes with the adopted flow {a} (it replaces the same items): relaunch or discard it"
                )));
            }
            self.adopt_nodes(id, &cur, by).await?;
        } else {
            self.discard_nodes(id, &fx, &cur, by).await?;
        }
        self.flow_event(
            id,
            FlowEvent {
                op: op.into(),
                flow: flow.into(),
                by: by.into(),
                ..FlowEvent::default()
            },
        )
        .await
    }

    /// Loads the view of a change whatever its flows, for the operations on them.
    async fn flow_view(&mut self, id: &ChangeId) -> Result<()> {
        if !self.views.contains_key(id) {
            let (v, _) = fold_any(self.store, id).await?;
            self.views.insert(id.clone(), v);
        }
        Ok(())
    }

    /// Installs on the main flow what an adopted flow saw of the nodes (Go's `adoptNodes`, ADR 0025 §5, 0079): a draft
    /// the flow holds becomes the main flow's (a conflict when the main flow's moved on since the flow checked the node
    /// out, by a run that is not stale); a main draft written in part by stale runs is reset to what the others left,
    /// or dropped. Then the adoption, folded over every impact, then the installs (Go's `adoptChangeImpacts`).
    async fn adopt_nodes(&mut self, id: &ChangeId, f: &Flow, by: &str) -> Result<()> {
        self.flow_view(id).await?;
        let v = &self.views[id];
        let stale: HashSet<&str> = f.stale_runs.iter().map(String::as_str).collect();
        let is_stale = |e: &str| !e.is_empty() && stale.contains(e);
        let mut installs = Vec::new();
        for cn in &v.fold.impacts {
            let fd = v.fold.draft(&cn.id, &f.id);
            let md = v.fold.draft(&cn.id, "");
            if let Some(fd) = fd {
                if !accepted_on_flow(&v.fold.impacts, node_of(cn).as_ref(), &f.id) {
                    return Err(GraphError::conflict(format!(
                        "node {} is a draft on flow {} that the flow did not accept: accept it, or cancel the checkout, before the flow is adopted (ADR 0079)",
                        fd.key, f.id
                    )));
                }
                let mut seq = 0;
                for e in v.events.iter().filter(|e| e.impact == cn.id) {
                    if e.flow == f.id && e.starts_draft() && seq == 0 {
                        seq = e.seq;
                    } else if seq > 0
                        && e.flow.is_empty()
                        && e.seq > seq
                        && changes_draft(e.op)
                        && !is_stale(&e.execution)
                    {
                        return Err(GraphError::conflict(format!(
                            "node {} was changed on the main flow since the flow forked (checked out again in the meantime)",
                            fd.key
                        )));
                    }
                }
                installs.push(install(id, &cn.id, fd.clone(), by, &f.id));
            } else if let Some(md) = md {
                if cn.flow.is_empty() && !cn.superseded && is_stale(&cn.execution) {
                    continue;
                }
                match impact::draft_seen_by(&v.events, &cn.id, &[], &is_stale) {
                    None => installs.push(ImpactEvent {
                        change: id.clone(),
                        impact: cn.id.clone(),
                        op: ImpactOp::Cancelled,
                        by: by.into(),
                        ..ImpactEvent::default()
                    }),
                    Some(kept) if !same_draft(&kept, md) => installs.push(install(id, &cn.id, kept, by, &f.id)),
                    Some(_) => {}
                }
            }
        }
        self.emit(vec![ImpactEvent {
            change: id.clone(),
            op: ImpactOp::Adopted,
            flow: f.id.clone(),
            stale: f.stale_runs.clone(),
            by: by.into(),
            ..ImpactEvent::default()
        }])
        .await?;
        for e in installs {
            let live = self.views[id].fold.impact(&e.impact).is_some_and(|cn| !cn.superseded);
            if live {
                self.emit(vec![e]).await?;
            }
        }
        Ok(())
    }

    /// Rejects the impacts a discarded flow and its descendants declared (Go's `discardNodes`).
    async fn discard_nodes(&mut self, id: &ChangeId, fx: &Flows, f: &Flow, by: &str) -> Result<()> {
        self.flow_view(id).await?;
        let mut gone = vec![f.id.clone()];
        let mut changed = true;
        while changed {
            changed = false;
            for o in &fx.list {
                if !gone.contains(&o.id) && gone.contains(&o.parent) {
                    gone.push(o.id.clone());
                    changed = true;
                }
            }
        }
        let rejected: Vec<_> = self.views[id]
            .fold
            .impacts
            .iter()
            .filter(|cn| gone.contains(&cn.flow) && cn.review != REVIEW_REJECTED)
            .map(|cn| (cn.id.clone(), cn.flow.clone()))
            .collect();
        let now = self.env.now();
        for (impact, flow) in rejected {
            self.emit(vec![ImpactEvent {
                change: id.clone(),
                impact,
                op: ImpactOp::Discarded,
                flow: flow.clone(),
                by: by.into(),
                review: Some(Review {
                    status: REVIEW_REJECTED.into(),
                    by: by.into(),
                    comment: "flow discarded".into(),
                    at: now,
                    flow,
                    ..Review::default()
                }),
                ..ImpactEvent::default()
            }])
            .await?;
        }
        Ok(())
    }

    /// Opens a flow branch (Go's `OpenFlow`): the items it invalidates (the closure of the seeds over the provenance
    /// of its parent's view) are stale until it is adopted; the items given are written on it.
    pub async fn open_flow(&mut self, id: &ChangeId, r#in: OpenFlow) -> Result<Flow> {
        let mut c = self.flow_change(id).await?;
        let items = items_of(self.store, id).await?;
        let fx = Flows::replay(&items);
        if !r#in.parent.is_empty() && fx.effective(&r#in.parent) == FLOW_DISCARDED {
            return Err(GraphError::invalid(format!("flow {} is discarded", r#in.parent)));
        }
        let base: Vec<&Item> = fx.view(&items, &r#in.parent).into_iter().map(|i| &items[i]).collect();
        let in_view = |s: &str| base.iter().any(|it| it.id == s);
        if !r#in.fork_after.is_empty() && !in_view(&r#in.fork_after) {
            return Err(GraphError::invalid(format!(
                "fork point {} is not on the flow {:?}",
                r#in.fork_after, r#in.parent
            )));
        }
        if let Some(s) = r#in.seeds.iter().find(|s| !in_view(s)) {
            return Err(GraphError::invalid(format!(
                "item {s} is not on the flow {:?}",
                r#in.parent
            )));
        }
        let flow = self.env.new_id();
        let stale = stale_closure(&base, &r#in.seeds);
        self.flow_event(
            id,
            FlowEvent {
                op: OP_OPEN.into(),
                flow: flow.clone(),
                parent: r#in.parent.clone(),
                fork_after: r#in.fork_after.clone(),
                stale,
                stale_runs: r#in.stale_runs.clone(),
                origin: r#in.origin.clone(),
                ..FlowEvent::default()
            },
        )
        .await?;
        for mut it in r#in.items {
            if matches!(it.typ.as_str(), "flow" | "transition" | "decision_point") {
                return Err(GraphError::invalid(format!(
                    "a {} item is recorded by its own operation, not at the opening of a flow",
                    it.typ
                )));
            }
            it.id = self.env.new_id();
            it.flow = flow.clone();
            self.put_item(id, &it, "accepted").await?;
        }
        self.activate(&mut c).await?;
        self.flow_of(id, &flow).await
    }
}

/// The install of a draft on the main flow by an adoption.
fn install(change: &ChangeId, impact: &str, d: Draft, by: &str, flow: &str) -> ImpactEvent {
    let mut patch = Map::new();
    patch.insert("adopted".into(), Value::String(flow.into()));
    ImpactEvent {
        change: change.clone(),
        impact: impact.into(),
        op: ImpactOp::Transitioned,
        execution: d.execution.clone(),
        by: by.into(),
        post: Some(d.draft_ref()),
        draft: Some(d),
        patch: Some(patch),
        ..ImpactEvent::default()
    }
}

/// The events that change what a draft holds.
fn changes_draft(op: ImpactOp) -> bool {
    matches!(
        op,
        ImpactOp::Created | ImpactOp::CheckedOut | ImpactOp::Updated | ImpactOp::Transitioned | ImpactOp::Cancelled
    )
}

/// Drafts that hold the same (Go's `sameDraft`): the flow, the run and the ids of the links are not what a draft
/// holds.
fn same_draft(a: &Draft, b: &Draft) -> bool {
    let norm = |d: &Draft| {
        let mut d = d.clone();
        d.flow.clear();
        d.execution.clear();
        for l in d.links.iter_mut() {
            l.id = Default::default();
        }
        jsontext::to_string(&d)
    };
    norm(a) == norm(b)
}

/// Whether the last review of a node of the change made on a flow accepts it (Go's `acceptedOnFlow`).
fn accepted_on_flow(impacts: &[ChangeImpact], node: Option<&NodeId>, flow: &str) -> bool {
    let mut accepted = false;
    for cn in impacts.iter().filter(|cn| node_of(cn).as_ref() == node) {
        for r in cn.reviews.iter().filter(|r| r.flow == flow && !r.superseded) {
            accepted = r.status == REVIEW_ACCEPTED;
        }
    }
    accepted
}
