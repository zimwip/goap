//! Port of `option.go` (and of the blackboard reads of `graph.go`): the options of a change (ADR 0009 §3, ADR 0032
//! §6), the facets of a blackboard (ADR 0066, 0109) and the blackboard itself.

use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};
use std::sync::Arc;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::{
    Baseline, Blackboard, CHANGE_ACTIVE, CHANGE_DRAFT, Change, ChangeID, DECISION_RULE_OP,
    DecisionEvent, FACET_ACTIVE_OPTION, FACET_DECISION_POINTS, FACET_DEPENDENCIES, FACET_OPTIONS,
    FLOW_ACTIVATE_OP, FLOW_ADOPT_OP, FLOW_DEACTIVATE_OP, FLOW_DISCARD_OP, FLOW_EVALUATE_OP,
    FLOW_OPEN, FLOW_OPEN_OP, FacetValue, Flow, FlowEvent, Link, LogEntry, LogFilter, MAIN_FLOW,
    Node, NodeID, NodeRef, OUTCOME_DECIDED, OptionSpec, Version, valid_option_intent,
};

use crate::api::{OpenOptionRequest, OptionComparison, OptionNode};
use crate::changeview::{VIEW_ACCEPTED, VIEW_WRITTEN};
use crate::dependency::dependencies_of;
use crate::draft::{DraftReader, view_in};
use crate::flow::flow_change;
use crate::flownodes::{FlowNodes, same_draft};
use crate::graph::baseline_graph_tx;
use crate::lifecycle::invalidf;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// BlackboardFacet computes a facet of the blackboard of a change (Blackboard::facets) from the change as stored,
/// replayed at now.
pub type BlackboardFacet = Arc<dyn Fn(&Change, DateTime<Utc>) -> FacetValue + Send + Sync>;

/// The fold of a [`LogFacet`]: the facet from the entries of the log of a change.
#[async_trait]
pub trait LogFacetFold: Send + Sync {
    async fn fold(&self, ctx: &Ctx, c: &Change, entries: &[LogEntry]) -> Result<FacetValue>;
}

/// LogFacet is a facet of the blackboard of a change folded from the entries of its log of the given types (a stream
/// "journal." or an exact type); the graph reads them, the use case that plugs it knows what they hold.
#[derive(Clone)]
pub struct LogFacet {
    pub types: Vec<String>,
    pub fold: Arc<dyn LogFacetFold>,
}

impl Graph {
    /// logEntries reads the entries of the log of a change each log facet folds.
    pub(crate) async fn log_entries(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
    ) -> Result<BTreeMap<String, Vec<LogEntry>>> {
        let mut out = BTreeMap::new();
        for (name, f) in &self.log_facets {
            let es = tx
                .log(
                    ctx,
                    &LogFilter {
                        change: id.clone(),
                        types: f.types.clone(),
                        ..Default::default()
                    },
                )
                .await?;
            out.insert(name.clone(), es);
        }
        Ok(out)
    }

    /// withLogFacets folds the log facets of the blackboard from the entries read with it (outside any transaction).
    pub(crate) async fn with_log_facets(
        &self,
        ctx: &Ctx,
        mut bb: Blackboard,
        entries: &BTreeMap<String, Vec<LogEntry>>,
    ) -> Result<Blackboard> {
        for (name, es) in entries {
            let Some(f) = self.log_facets.get(name) else {
                continue;
            };
            let v = f.fold.fold(ctx, &bb.change, es).await?;
            bb.facets.insert(name.clone(), v);
        }
        Ok(bb)
    }

    /// facets are the facets of the blackboard of a change: the built-in ones (the options and the decision points
    /// being the graph's own mechanisms, ADR 0009, 0032: what a view of a flow does not carry), then Graph::facets
    /// (which may replace them).
    pub(crate) fn facets_of(&self, c: &Change, now: DateTime<Utc>) -> HashMap<String, FacetValue> {
        let mut out: HashMap<String, FacetValue> = HashMap::new();
        out.insert(FACET_OPTIONS.into(), Arc::new(c.options()));
        out.insert(FACET_ACTIVE_OPTION.into(), Arc::new(c.active_option()));
        out.insert(
            FACET_DECISION_POINTS.into(),
            Arc::new(c.decision_points_at(now, self.decision_policy.as_deref())),
        );
        for (name, f) in &self.facets {
            out.insert(name.clone(), f(c, now));
        }
        out
    }
}

/// flowIDsOf lists the ids of flows.
pub(crate) fn flow_ids_of(fs: &[Flow]) -> Vec<String> {
    fs.iter().map(|f| f.id.clone()).collect()
}

// ---- options of a change (ADR 0009 §3, ADR 0032 §6) --------------------------------------------------------------
//
// An option is a flow branch opened as a hypothesis, forked from the main flow (intent derive/revise) or from another
// open option (intent refine). Several are explored at once; one of them may be active: the change works on it
// (every call that names no flow goes to it, "main" names the main flow). Selecting an option adopts its flow (its
// drafts become the main flow's, ADR 0079) and rejects the other open ones; rejecting one discards its flow.

impl Graph {
    /// OpenOption opens an option: a flow branch of its own, forked from the main flow (or, with parent set, from
    /// another open option - refine), that invalidates nothing.
    pub async fn open_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        input: OpenOptionRequest,
    ) -> Result<Flow> {
        let name = input.name.trim().to_string();
        if name.is_empty() {
            return Err(invalidf("an option needs a name"));
        }
        if !valid_option_intent(&input.intent) {
            return Err(invalidf(format!(
                "unknown option intent {:?}",
                input.intent.as_str()
            )));
        }
        let mut tx = self.begin(ctx).await?;
        let mut c = flow_change(ctx, &mut tx, id).await?;
        for o in c.options() {
            if o.status == FLOW_OPEN
                && o.option
                    .as_ref()
                    .is_some_and(|s| s.name.to_lowercase() == name.to_lowercase())
            {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("change {id} already has an open option {name:?}"),
                ));
            }
        }
        if !input.parent.is_empty() {
            open_option(&c, &input.parent).map_err(|e| e.context("parent option"))?;
        }
        let flow = self.new_id();
        self.flow_event(
            ctx,
            &mut tx,
            id,
            FlowEvent {
                op: FLOW_OPEN_OP.into(),
                flow: flow.clone(),
                parent: input.parent.clone(),
                by: input.by.clone(),
                option: Some(OptionSpec {
                    name,
                    hypothesis: input.hypothesis.clone(),
                    intent: input.intent.clone(),
                }),
                ..Default::default()
            },
        )
        .await?;
        if input.activate {
            self.flow_event(
                ctx,
                &mut tx,
                id,
                FlowEvent {
                    op: FLOW_ACTIVATE_OP.into(),
                    flow: flow.clone(),
                    by: input.by.clone(),
                    ..Default::default()
                },
            )
            .await?;
        }
        if c.status == CHANGE_DRAFT {
            c.status = CHANGE_ACTIVE;
            tx.put_change(ctx, c).await?;
        }
        let f = option_of(ctx, &mut tx, id, &flow).await?;
        tx.commit(ctx).await?;
        Ok(f)
    }

    /// ActivateOption makes an open option the one the change works on; "" or "main" goes back to the main flow.
    pub async fn activate_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        by: &str,
    ) -> Result<String> {
        let mut tx = self.begin(ctx).await?;
        let c = flow_change(ctx, &mut tx, id).await?;
        let cur = c.active_option();
        let mut active = String::new();
        if option.is_empty() || option == MAIN_FLOW {
            if !cur.is_empty() {
                self.flow_event(
                    ctx,
                    &mut tx,
                    id,
                    FlowEvent {
                        op: FLOW_DEACTIVATE_OP.into(),
                        flow: cur,
                        by: by.to_string(),
                        ..Default::default()
                    },
                )
                .await?;
            }
        } else {
            open_option(&c, option)?;
            active = option.to_string();
            if cur != option {
                self.flow_event(
                    ctx,
                    &mut tx,
                    id,
                    FlowEvent {
                        op: FLOW_ACTIVATE_OP.into(),
                        flow: option.to_string(),
                        by: by.to_string(),
                        ..Default::default()
                    },
                )
                .await?;
            }
        }
        tx.commit(ctx).await?;
        Ok(active)
    }

    /// EvaluateOption records the evaluation of an open option: it moves to evaluated.
    pub async fn evaluate_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        by: &str,
        comment: &str,
    ) -> Result<Flow> {
        if comment.trim().is_empty() {
            return Err(invalidf("an evaluation needs a comment"));
        }
        let mut tx = self.begin(ctx).await?;
        let c = flow_change(ctx, &mut tx, id).await?;
        open_option(&c, option)?;
        self.flow_event(
            ctx,
            &mut tx,
            id,
            FlowEvent {
                op: FLOW_EVALUATE_OP.into(),
                flow: option.to_string(),
                by: by.to_string(),
                comment: comment.to_string(),
                ..Default::default()
            },
        )
        .await?;
        let f = option_of(ctx, &mut tx, id, option).await?;
        tx.commit(ctx).await?;
        Ok(f)
    }

    /// SelectOption selects an open option: its flow is adopted (its drafts become the main flow's, ADR 0079) and the
    /// other open options are rejected.
    pub async fn select_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        by: &str,
    ) -> Result<Flow> {
        let mut tx = self.change_tx(ctx, id).await?;
        let c = flow_change(ctx, &mut tx, id).await?;
        self.select_option_tx(ctx, &mut tx, &c, option, by).await?;
        // a selection by hand settles the decision points that were choosing among the options (ADR 0009 §4)
        let c = tx.change(ctx, id).await?;
        for d in c.decision_points_at(self.now(), self.decision_policy.as_deref()) {
            if d.pending() && d.options.iter().any(|o| o == option) {
                self.decision_event(
                    ctx,
                    &mut tx,
                    id,
                    DecisionEvent {
                        op: DECISION_RULE_OP.into(),
                        point: d.id.clone(),
                        outcome: OUTCOME_DECIDED.into(),
                        option: option.to_string(),
                        confidence: 1.0,
                        justification: "option selected by hand".into(),
                        human: true,
                        by: by.to_string(),
                        ..Default::default()
                    },
                )
                .await?;
            }
        }
        let f = option_of(ctx, &mut tx, id, option).await?;
        tx.commit(ctx).await?;
        Ok(f)
    }

    /// selectOptionTx rejects the other open options of c, then adopts the flow of option.
    pub(crate) async fn select_option_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        option: &str,
        by: &str,
    ) -> Result<()> {
        open_option(c, option)?;
        let mut c = c.clone();
        for o in c.options() {
            if o.id == option || o.status != FLOW_OPEN {
                continue;
            }
            self.decide_flow_tx(ctx, tx, &c, &o.id, FLOW_DISCARD_OP, by)
                .await?;
            c = tx.change(ctx, &c.id).await?;
        }
        self.decide_flow_tx(ctx, tx, &c, option, FLOW_ADOPT_OP, by)
            .await
    }

    /// RejectOption rejects an open option: its flow is discarded, what it wrote stays on its branch for the audit.
    pub async fn reject_option(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        option: &str,
        by: &str,
    ) -> Result<Flow> {
        let mut tx = self.begin(ctx).await?;
        let c = flow_change(ctx, &mut tx, id).await?;
        open_option(&c, option)?;
        self.decide_flow_tx(ctx, &mut tx, &c, option, FLOW_DISCARD_OP, by)
            .await?;
        let f = option_of(ctx, &mut tx, id, option).await?;
        tx.commit(ctx).await?;
        Ok(f)
    }

    /// Options lists the options of a change, oldest first.
    pub async fn options(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<Flow>> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        tx.commit(ctx).await?;
        Ok(c.options())
    }

    /// CompareOptions compares the open options of a change (every option when all is set) at a level.
    pub async fn compare_options(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        level: &str,
        all: bool,
    ) -> Result<OptionComparison> {
        let level = if level.is_empty() {
            VIEW_WRITTEN
        } else {
            level
        };
        if level != VIEW_WRITTEN && level != VIEW_ACCEPTED {
            return Err(invalidf(format!(
                "options are compared at {VIEW_WRITTEN} or {VIEW_ACCEPTED}, not {level:?}"
            )));
        }
        let mut cmp = OptionComparison {
            level: level.to_string(),
            ..Default::default()
        };
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        let main = self.change_view_tx(ctx, &mut tx, &c, "", level).await?;
        let mut views: BTreeMap<String, Baseline> = BTreeMap::new();
        for o in c.options() {
            if !all && o.status != FLOW_OPEN {
                continue;
            }
            let v = self.change_view_tx(ctx, &mut tx, &c, &o.id, level).await?;
            views.insert(o.id.clone(), v);
            cmp.options.push(o);
        }
        // the drafts each side holds (ADR 0079): a node both sides hold a draft of differs when the drafts do
        let mut readers: HashMap<String, DraftReader> = HashMap::new();
        let mut sides = vec![MAIN_FLOW.to_string()];
        sides.extend(flow_ids_of(&cmp.options));
        for side in &sides {
            let flow = if side == MAIN_FLOW { "" } else { side.as_str() };
            let seen = FlowNodes::new(c.clone(), flow).nodes(ctx, &mut tx).await?;
            let r = self.new_draft_reader(ctx, &mut tx, &c, flow, &seen).await?;
            readers.insert(side.clone(), r);
        }
        let differs = |side: &str, id: &NodeID| -> bool {
            match (
                readers.get(side).and_then(|r| r.by_node.get(id)),
                readers.get(MAIN_FLOW).and_then(|r| r.by_node.get(id)),
            ) {
                (Some(a), Some(b)) => !same_draft(a, b),
                _ => false,
            }
        };
        let mut changed: BTreeSet<NodeID> = BTreeSet::new();
        for (oid, v) in &views {
            for (id, ver) in &v.nodes {
                if main.nodes.get(id) != Some(ver) || (*ver == 0 && differs(oid, id)) {
                    changed.insert(id.clone());
                }
            }
            for id in main.nodes.keys() {
                if !v.nodes.contains_key(id) {
                    changed.insert(id.clone());
                }
            }
        }
        let rf = |nodes: &BTreeMap<NodeID, Version>, id: &NodeID| -> Option<NodeRef> {
            nodes.get(id).map(|v| NodeRef {
                id: id.clone(),
                version: *v,
            })
        };
        for id in &changed {
            let mut on = OptionNode {
                node: id.clone(),
                main: rf(&main.nodes, id),
                ..Default::default()
            };
            let mut sides: Vec<(String, Option<NodeRef>)> =
                vec![(MAIN_FLOW.to_string(), on.main.clone())];
            for (oid, v) in &views {
                let r = rf(&v.nodes, id);
                on.options.insert(oid.clone(), r.clone());
                sides.push((oid.clone(), r));
            }
            for (side, r) in &sides {
                let Some(r) = r else {
                    continue;
                };
                let n = readers[side].node(ctx, &mut tx, r).await?;
                on.key = n.key.clone();
                on.r#type = n.r#type.clone();
                on.props.insert(side.clone(), n.properties.clone());
            }
            if on.key.is_empty() {
                // retired on every side it was on: name it from its history
                if let Ok(vs) = tx.versions(ctx, id).await
                    && let Some(first) = vs.first()
                {
                    on.key = first.key.clone();
                    on.r#type = first.r#type.clone();
                }
            }
            cmp.nodes.push(on);
        }
        cmp.nodes.sort_by(|a, b| a.key.cmp(&b.key));
        tx.commit(ctx).await?;
        Ok(cmp)
    }

    /// ChangeGraph is the graph a call on a change reads (the nodes and the links between them): on an option (the
    /// flow named, else the active option), what the option wrote over the head of the change branch (ChangeView at
    /// written); otherwise the reference baseline of the change.
    pub async fn change_graph(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<(Vec<Node>, Vec<Link>)> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        let flow = c.resolve_flow(flow);
        let is_option = c.flow(&flow).is_some_and(|f| f.option.is_some());
        if !is_option {
            let out = baseline_graph_tx(ctx, &mut tx, &c.baseline_id).await?;
            tx.commit(ctx).await?;
            return Ok(out);
        }
        let v = self
            .change_view_tx(ctx, &mut tx, &c, &flow, VIEW_WRITTEN)
            .await?;
        let seen = FlowNodes::new(c.clone(), &flow).nodes(ctx, &mut tx).await?;
        let reader = self
            .new_draft_reader(ctx, &mut tx, &c, &flow, &seen)
            .await?;
        let base = tx.nodes_in(ctx, &v.parent_id, "").await?;
        let contains = |r: &NodeRef| v.nodes.get(&r.id) == Some(&r.version);
        let mut nodes = Vec::new();
        let mut refs: HashSet<NodeRef> = HashSet::new();
        for n in base {
            if contains(&n.r#ref()) {
                refs.insert(n.r#ref());
                nodes.push(n);
            }
        }
        for (nid, ver) in &v.nodes {
            let r = NodeRef {
                id: nid.clone(),
                version: *ver,
            };
            if !refs.contains(&r) {
                nodes.push(reader.node(ctx, &mut tx, &r).await?);
            }
        }
        nodes.sort_by(|a, b| a.key.cmp(&b.key));
        let mut links = Vec::new();
        for n in &nodes {
            for l in reader.out_links(ctx, &mut tx, &n.r#ref()).await? {
                if l.to.is_draft() || contains(&l.to) {
                    links.push(l);
                }
            }
        }
        tx.commit(ctx).await?;
        Ok((nodes, links))
    }

    // ---- the blackboard of a change ------------------------------------------------------------------------------

    /// Blackboard returns the change and a hydrated view of every node it references, ready for condition
    /// evaluation.
    pub async fn blackboard(&self, ctx: &Ctx, id: &ChangeID) -> Result<Blackboard> {
        self.blackboard_in(ctx, id, "").await
    }

    /// BlackboardIn is the blackboard as seen by the process running on a flow branch ("" = the main flow, see
    /// Change::view).
    pub async fn blackboard_in(&self, ctx: &Ctx, id: &ChangeID, flow: &str) -> Result<Blackboard> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        let logs = self.log_entries(ctx, &mut tx, id).await?;
        let flow = c.resolve_flow(flow); // no flow: the active option (ADR 0032 §6)
        // the change impacts as the flow sees them (ADR 0025)
        let nodes = FlowNodes::new(c.clone(), &flow).nodes(ctx, &mut tx).await?;
        // what the view of a flow does not carry: the options and the decision points of the change (ADR 0009)
        let now = self.now();
        let mut facets = self.facets_of(&c, now);
        let deps = dependencies_of(ctx, &mut tx, &c).await?; // ADR 0108
        facets.insert(FACET_DEPENDENCIES.into(), Arc::new(deps));
        let full = c.clone(); // the drafts are read with the flows of the change, the view of a flow has none
        let mut v = c.view(&flow);
        v.nodes = nodes.clone();
        self.attach_validation(ctx, &mut tx, &full, &flow, &mut v.nodes, None)
            .await;
        let mut bb = Blackboard {
            change: v,
            facets,
            at: Some(now),
            ..Default::default()
        };
        let ix = self.types_at();
        let reader = self
            .new_draft_reader(ctx, &mut tx, &full, &flow, &nodes)
            .await?;
        // a draft reference is hydrated from the draft the flow sees (ADR 0079), any other from the stored version
        for r in bb.change.referenced_nodes() {
            let mut v = view_in(ctx, &mut tx, &reader, &r).await?;
            if let Some(lc) = ix.lifecycle_of(&v.r#type)
                && !v.state.is_empty()
            {
                v.not_landable = !lc.landable(&v.state);
            }
            for l in &v.out {
                neighbor(ctx, &mut tx, &reader, &mut bb.neighbors, &l.to).await?;
            }
            for l in &v.r#in {
                neighbor(ctx, &mut tx, &reader, &mut bb.neighbors, &l.from).await?;
            }
            bb.nodes.insert(r, v);
        }
        tx.commit(ctx).await?;
        // the facets folded from the log (the history, ADR 0109), outside the transaction
        self.with_log_facets(ctx, bb, &logs).await
    }

    /// ChangeChecked is Change with the validation of each impact carrying a draft (ImpactValidation): what the change
    /// impacts look like to a person who reviews them. Each impact is checked on the flow that declared it.
    pub async fn change_checked(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change> {
        let mut tx = self.begin(ctx).await?;
        let mut c = tx.change(ctx, id).await?;
        let flows: BTreeSet<String> = c.nodes.iter().map(|cn| cn.flow.clone()).collect();
        let mut nodes = c.nodes.clone();
        for flow in &flows {
            let f = flow.clone();
            let pick = move |cn: &goap_domain::ChangeImpact| cn.flow == f;
            self.attach_validation(ctx, &mut tx, &c, flow, &mut nodes, Some(&pick))
                .await;
        }
        c.nodes = nodes;
        tx.commit(ctx).await?;
        Ok(c)
    }
}

async fn neighbor(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    reader: &DraftReader,
    into: &mut HashMap<NodeRef, Node>,
    r: &NodeRef,
) -> Result<()> {
    if into.contains_key(r) {
        return Ok(());
    }
    let n = reader.node(ctx, tx, r).await?;
    into.insert(r.clone(), n);
    Ok(())
}

pub(crate) fn open_option(c: &Change, option: &str) -> Result<Flow> {
    let f = match c.flow(option) {
        Some(f) if f.option.is_some() => f,
        _ => {
            return Err(Error::wrap(
                ErrorKind::NotFound,
                format!("option {option} of change {}", c.id),
            ));
        }
    };
    if f.status != FLOW_OPEN {
        return Err(Error::wrap(
            ErrorKind::Conflict,
            format!(
                "option {} is {}",
                f.option
                    .as_ref()
                    .map(|o| o.name.as_str())
                    .unwrap_or_default(),
                f.option_status()
            ),
        ));
    }
    Ok(f)
}

pub(crate) async fn option_of(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    id: &ChangeID,
    option: &str,
) -> Result<Flow> {
    let c = tx.change(ctx, id).await?;
    Ok(c.flow(option).unwrap_or_default())
}
