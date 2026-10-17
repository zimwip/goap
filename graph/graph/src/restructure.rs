//! Merge and split of nodes, seen from the parent side (ADR 0077, "Merge and split"). A node
//! never disappears and a merge or a split retires nothing: merging A and B into C is a modification of the parent P
//! of A and B, whose draft loses the links to A and B and gains one to C; splitting A into C and D loses the link to A
//! and gains the links to C and D. A and B stay in history, untouched; their change impacts carry Via, the impact of
//! the parent that realizes the move (ADR 0076 §3). The successors record their lineage (Node.origins, on their first
//! version).

use std::collections::{HashMap, HashSet};

use goap_ctx::Ctx;
use goap_domain::{
    Baseline, ChangeID, ChangeImpact, ChangeImpactID, Draft, IMPACT_PROPOSED, INTENT_CREATED,
    ImpactEvent, Link, LinkID, Node, NodeID, NodeRef, REVIEW_ACCEPTED, namespace_of,
};
use goap_operations as operations;
use serde_json::{Map, Value, json};

use crate::api::{
    LinkWrite, MergeInput, NodeCreate, NodeName, Restructured, SplitInput, SuspectLink,
};
use crate::checkout::{Target, Work};
use crate::draft::{draft_node, own_draft};
use crate::flownodes::node_of;
use crate::lifecycle::{TypeIndex, invalidf};
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// errOriginsPending: the refusal of an acceptance whose origins are not accepted yet (check_origins) ends with this
/// text, after the conflict sentinel; a batch of reviews settles its own origins first (review_batch).
pub(crate) const ORIGINS_PENDING: &str = "settle the origins first";

/// isOriginsPending tells the refusal of check_origins.
pub(crate) fn is_origins_pending(e: &Error) -> bool {
    e.is_conflict() && e.message().ends_with(ORIGINS_PENDING)
}

/// The gate a restructuring asks about each node type it touches.
pub(crate) type GateFn<'a> = &'a (dyn Fn(&str) -> Result<()> + Send + Sync);

/// restructured is a node whose links move: its current state (the draft the flow holds of it, else its stored
/// version) and the links it holds to the sources.
struct Holder {
    node: Node,
    /// holds a composition link to a source
    parent: bool,
    links: Vec<Link>,
    compose: HashSet<LinkID>,
}

impl Graph {
    /// ImpactNodeMerge merges nodes into a new one, in one transaction. Every source needs a parent: a node holding a
    /// link of a type flagged `compose` to it. The links of other types to the sources are moved to the new node too.
    /// Refused (invalid) for a source with no parent, nodes of another namespace or of a type incompatible with the new
    /// node, or created by the change; (conflict) for a source checked out.
    ///
    /// goap:operation impact.merge doer
    pub async fn impact_node_merge(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        input: MergeInput,
    ) -> Result<Restructured> {
        self.allow(ctx, id, operations::IMPACT_MERGE).await?;
        if input.sources.len() < 2 {
            return Err(invalidf("a merge needs at least two nodes"));
        }
        let mut into = input.into.clone();
        into.flow = input.flow.clone();
        into.execution = input.execution.clone();
        let into = vec![into];
        self.gated(
            ctx,
            id,
            &input.sources,
            &into,
            true,
            &input.rationale,
            &input.flow,
            &input.execution,
            input.gate.as_deref(),
        )
        .await
    }

    /// ImpactNodeSplit splits a node into new ones, in one transaction. The links of other types to the source are
    /// left and returned as suspect.
    ///
    /// goap:operation impact.split doer
    pub async fn impact_node_split(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        input: SplitInput,
    ) -> Result<Restructured> {
        self.allow(ctx, id, operations::IMPACT_SPLIT).await?;
        if input.into.len() < 2 {
            return Err(invalidf("a split needs at least two new nodes"));
        }
        let into: Vec<NodeCreate> = input
            .into
            .iter()
            .map(|c| NodeCreate {
                flow: input.flow.clone(),
                execution: input.execution.clone(),
                ..c.clone()
            })
            .collect();
        let sources = vec![input.source.clone()];
        self.gated(
            ctx,
            id,
            &sources,
            &into,
            false,
            &input.rationale,
            &input.flow,
            &input.execution,
            input.gate.as_deref(),
        )
        .await
    }

    /// One pass of a restructuring in its own transaction: committed when the gate collected nothing new, else rolled
    /// back (Go's errCollected).
    #[allow(clippy::too_many_arguments)]
    async fn restructure_pass(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        sources: &[NodeName],
        into: &[NodeCreate],
        merge: bool,
        rationale: &str,
        flow: &str,
        execution: &str,
        gate: &Collector,
    ) -> Result<Restructured> {
        let mut tx = self.begin(ctx).await?;
        let f = |typ: &str| gate.ask(typ);
        let out = self
            .restructure(
                ctx, &mut tx, id, sources, into, merge, rationale, flow, execution, &f,
            )
            .await;
        match out {
            Ok(out) if !gate.collected() => {
                tx.commit(ctx).await?;
                Ok(out)
            }
            Ok(out) => {
                tx.rollback().await?;
                Ok(out)
            }
            Err(e) => {
                tx.rollback().await?;
                Err(e)
            }
        }
    }

    /// gated runs a restructuring whose gate may read the graph (the access snapshot does), which its own transaction
    /// would block: the stores are not reentrant. Like the guardian of a merge it asks outside any transaction: a pass
    /// runs the transaction with a gate that only collects the node types it is asked about, rolled back while some
    /// were not answered yet. The real gate answers them here and the pass runs again; the pass that commits is one
    /// that was asked nothing new.
    #[allow(clippy::too_many_arguments)]
    async fn gated(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        sources: &[NodeName],
        into: &[NodeCreate],
        merge: bool,
        rationale: &str,
        flow: &str,
        execution: &str,
        gate: Option<GateFn<'_>>,
    ) -> Result<Restructured> {
        let Some(gate) = gate else {
            let c = Collector::open();
            return self
                .restructure_pass(
                    ctx, id, sources, into, merge, rationale, flow, execution, &c,
                )
                .await;
        };
        let mut allowed: HashSet<String> = HashSet::new();
        loop {
            let c = Collector::new(allowed.clone());
            let res = self
                .restructure_pass(
                    ctx, id, sources, into, merge, rationale, flow, execution, &c,
                )
                .await;
            let missing = c.missing();
            if missing.is_empty() {
                return res;
            }
            // the gate answers first: its refusal wins over whatever the pass found
            for typ in missing {
                gate(&typ)?;
                allowed.insert(typ);
            }
            res?;
        }
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) async fn restructure(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        sources: &[NodeName],
        into: &[NodeCreate],
        merge: bool,
        rationale: &str,
        flow: &str,
        execution: &str,
        gate: GateFn<'_>,
    ) -> Result<Restructured> {
        let mut out = Restructured::default();
        for c in into {
            if c.key.is_empty() || c.r#type.is_empty() {
                return Err(invalidf("a node needs a key and a type"));
            }
            gate(&c.r#type)?;
        }
        let rationale = if rationale.is_empty() {
            into[0].rationale.clone()
        } else {
            rationale.to_string()
        };
        // 1. the sources, as impacts of the change
        let mut srcs: Vec<Node> = Vec::new();
        let mut src_impacts: Vec<ChangeImpactID> = Vec::new();
        let mut src_set: HashSet<NodeID> = HashSet::new();
        let mut w = Work::default();
        for name in sources {
            w = self
                .resolve(
                    ctx,
                    tx,
                    id,
                    Target {
                        impact: name.impact.clone(),
                        node: name.node.clone(),
                        key: name.key.clone(),
                        rationale: rationale.clone(),
                        flow: flow.to_string(),
                        execution: execution.to_string(),
                        ..Default::default()
                    },
                )
                .await?;
            if w.cn.intent == INTENT_CREATED {
                return Err(invalidf(format!(
                    "{} is created by the change: create the node you want instead of merging or splitting it",
                    w.cn.key
                )));
            }
            let Some(n) = w.base(ctx, tx).await? else {
                return Err(invalidf(format!(
                    "change impact {} has no version to merge or split",
                    w.cn.id
                )));
            };
            if self
                .seen_draft(ctx, tx, &w.c, &w.flow, &w.cn.id)
                .await?
                .is_some()
            {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!(
                        "{} is checked out by change impact {}: cancel the checkout first",
                        n.key, w.cn.id
                    ),
                ));
            }
            if namespace_of(&n.namespace) != w.ns {
                return Err(invalidf(format!(
                    "node {} is in namespace {}, the change acts on {}",
                    n.key,
                    namespace_of(&n.namespace),
                    w.ns
                )));
            }
            gate(&n.r#type)?;
            if !src_set.insert(n.id.clone()) {
                return Err(invalidf(format!("{} is named twice", n.key)));
            }
            for c in into {
                if !w.ix.compatible(&n.r#type, &c.r#type) {
                    return Err(invalidf(format!(
                        "{} ({}) cannot be replaced by {} ({}): the types are not compatible",
                        n.key, n.r#type, c.key, c.r#type
                    )));
                }
            }
            srcs.push(n);
            src_impacts.push(w.cn.id.clone());
        }

        // 2. the nodes holding links to the sources, as the flow sees them: a node the flow holds a draft of through
        // its draft, any other through the links of its stored version
        let b = tx.baseline(ctx, &w.c.baseline_id).await?;
        let reader = self
            .new_draft_reader(ctx, tx, &w.c, &w.flow, &w.seen)
            .await?;
        let mut holders: HashMap<NodeID, Holder> = HashMap::new();
        let mut has_parent: HashSet<NodeID> = HashSet::new();
        let ix = w.ix.clone();
        let mut hold = |n: Node, l: Link, src: &NodeID| {
            let h = holders.entry(n.id.clone()).or_insert_with(|| Holder {
                node: n,
                parent: false,
                links: Vec::new(),
                compose: HashSet::new(),
            });
            if !h.links.iter().any(|x| x.id == l.id) {
                h.links.push(l.clone());
            }
            if ix.composes(&l.r#type) {
                h.parent = true;
                h.compose.insert(l.id.clone());
                has_parent.insert(src.clone());
            }
        };
        for s in &srcs {
            let vs = tx.versions(ctx, &s.id).await?;
            for v in vs {
                let ins = tx.in_links(ctx, &v.r#ref()).await?;
                for l in ins {
                    if reader.by_node.contains_key(&l.from.id) {
                        continue; // read through its draft below
                    }
                    let Some(cur) = self.current_of(ctx, tx, &w, &b, &l.from.id).await? else {
                        continue;
                    };
                    if cur.r#ref() != l.from || cur.deleted {
                        continue; // a link of a version the flow no longer sees
                    }
                    let composes = w.ix.composes(&l.r#type);
                    if src_set.contains(&cur.id) {
                        if composes {
                            return Err(invalidf(format!(
                                "{} is the parent of {}: merge or split nodes that are not parts of each other",
                                cur.key, s.key
                            )));
                        }
                        continue;
                    }
                    hold(cur, l, &s.id);
                }
            }
            for d in reader.sorted_drafts() {
                for dl in &d.links {
                    if dl.to.id != s.id {
                        continue;
                    }
                    let l = Link {
                        id: dl.id.clone(),
                        r#type: dl.r#type.clone(),
                        from: d.r#ref(),
                        to: reader.normalize(&dl.to),
                        properties: dl.properties.clone(),
                        change_id: d.change.clone(),
                    };
                    if src_set.contains(&d.node) {
                        if w.ix.composes(&l.r#type) {
                            return Err(invalidf(format!(
                                "{} is the parent of {}: merge or split nodes that are not parts of each other",
                                d.key, s.key
                            )));
                        }
                        continue;
                    }
                    hold(draft_node(&w.c, &d), l, &s.id);
                }
            }
        }
        for s in &srcs {
            if !has_parent.contains(&s.id) {
                return Err(invalidf(format!(
                    "{} has no parent: a merge or a split is a modification of the parent of the nodes, the node holding a link of a type flagged compose to it",
                    s.key
                )));
            }
        }
        let mut order: Vec<Holder> = holders.into_values().collect();
        order.sort_by(|a, b| a.node.key.cmp(&b.node.key));

        // 3. the successors
        let origins: Vec<NodeRef> = srcs.iter().map(|s| s.r#ref()).collect();
        let mut succ: Vec<NodeRef> = Vec::new();
        for c in into {
            let cn = self.create_tx(ctx, tx, id, c, &origins).await?;
            succ.push(cn.post.clone().unwrap_or_default());
        }

        // 4. the links move in the drafts of the holders
        let mut via: HashMap<NodeID, ChangeImpactID> = HashMap::new();
        let mut holder_impacts: Vec<ChangeImpactID> = Vec::new();
        for h in &order {
            if !merge && !h.parent {
                for l in &h.links {
                    let to_key = match reader.node(ctx, tx, &l.to).await {
                        Ok(n) => n.key,
                        Err(_) => String::new(),
                    };
                    out.suspect.push(SuspectLink {
                        from: l.from.clone(),
                        from_key: h.node.key.clone(),
                        r#type: l.r#type.clone(),
                        to: l.to.clone(),
                        to_key,
                    });
                }
                continue;
            }
            gate(&h.node.r#type)?;
            let mut hw = self
                .resolve(
                    ctx,
                    tx,
                    id,
                    Target {
                        node: h.node.id.clone(),
                        rationale: rationale.clone(),
                        flow: flow.to_string(),
                        execution: execution.to_string(),
                        ..Default::default()
                    },
                )
                .await?;
            let rows = self.drafts(ctx, tx, id).await?;
            if own_draft(&rows, &hw.cn.id, &hw.flow).is_none() {
                self.checkout_tx(ctx, tx, &hw, execution).await?;
                hw = self
                    .resolve(
                        ctx,
                        tx,
                        id,
                        Target {
                            node: h.node.id.clone(),
                            flow: flow.to_string(),
                            execution: execution.to_string(),
                            ..Default::default()
                        },
                    )
                    .await?;
            }
            let d = self.working(ctx, tx, &hw).await?;
            let mut moves: Vec<(String, Map<String, Value>)> = Vec::new();
            for l in &d.links {
                if !src_set.contains(&l.to.id) || (!merge && !w.ix.composes(&l.r#type)) {
                    continue;
                }
                self.emit_remove_link(ctx, tx, &mut hw, &d, l, execution)
                    .await?;
                if w.ix.composes(&l.r#type) {
                    via.entry(l.to.id.clone())
                        .or_insert_with(|| hw.cn.id.clone());
                }
                if !moves.iter().any(|(t, _)| t == &l.r#type) {
                    moves.push((l.r#type.clone(), l.properties.clone()));
                }
            }
            if moves.is_empty() {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("{} no longer holds a link to the nodes", h.node.key),
                ));
            }
            for (typ, props) in &moves {
                for to in &succ {
                    let dl = self
                        .new_draft_link(
                            ctx,
                            tx,
                            &hw,
                            &d,
                            &LinkWrite {
                                r#type: typ.clone(),
                                to: to.clone(),
                                properties: props.clone(),
                            },
                        )
                        .await?;
                    self.emit_add_link(ctx, tx, &mut hw, &d, &dl, execution)
                        .await?;
                }
            }
            holder_impacts.push(hw.cn.id.clone());
        }

        // 5. the impacts of the sources carry the impact of their parent
        for (i, s) in srcs.iter().enumerate() {
            let Some(v) = via.get(&s.id).cloned() else {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("{}: no parent link was moved", s.key),
                ));
            };
            let sw = self
                .resolve(
                    ctx,
                    tx,
                    id,
                    Target {
                        impact: src_impacts[i].clone(),
                        flow: flow.to_string(),
                        execution: execution.to_string(),
                        ..Default::default()
                    },
                )
                .await?;
            if sw.cn.via == v {
                continue;
            }
            if !sw.cn.via.is_empty() {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!(
                        "change impact {} of {} is already realized through {}",
                        sw.cn.id, s.key, sw.cn.via
                    ),
                ));
            }
            let mut cn = sw.cn.clone();
            cn.via = v;
            self.emit(
                ctx,
                tx,
                vec![ImpactEvent {
                    change: id.clone(),
                    impact: cn.id.clone(),
                    op: IMPACT_PROPOSED,
                    flow: cn.flow.clone(),
                    execution: cn.execution.clone(),
                    state: Some(cn),
                    ..Default::default()
                }],
            )
            .await?;
        }

        // 6. the result, as the flow sees it
        let w = self.work_on(ctx, tx, id, flow).await?;
        let find = |f: &dyn Fn(&ChangeImpact) -> bool| -> ChangeImpact {
            w.seen.iter().find(|x| f(x)).cloned().unwrap_or_default()
        };
        for c in into {
            out.successors
                .push(find(&|i| i.key == c.key && i.intent == INTENT_CREATED));
        }
        for i in &src_impacts {
            out.sources.push(find(&|x| &x.id == i));
        }
        for i in &holder_impacts {
            out.parents.push(find(&|x| &x.id == i));
        }
        Ok(out)
    }

    /// currentOf is the stored version of a node the flow starts from (None: none): its pre version when the change
    /// holds an impact on it, else the version of the reference baseline. The draft the flow holds of it is read
    /// through the draft reader.
    pub(crate) async fn current_of(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        w: &Work,
        b: &Baseline,
        nid: &NodeID,
    ) -> Result<Option<Node>> {
        for i in &w.seen {
            if &node_of(i) != nid {
                continue;
            }
            let mut w2 = w.clone();
            w2.select_impact(&i.id)?;
            return w2.base(ctx, tx).await;
        }
        let Some(v) = b.nodes.get(nid) else {
            return Ok(None);
        };
        Ok(Some(
            tx.node(
                ctx,
                &NodeRef {
                    id: nid.clone(),
                    version: *v,
                },
            )
            .await?,
        ))
    }

    /// checkOrigins is the review gate of a successor (ADR 0077): a node created by the change with origins is
    /// accepted and landed only when the change impact of every origin is accepted, the way what a node derives from
    /// is settled before it. impacts are the ones the flow sees, d the draft of the successor.
    pub(crate) fn check_origins(
        &self,
        impacts: &[ChangeImpact],
        cn: &ChangeImpact,
        d: Option<&Draft>,
    ) -> Result<()> {
        let Some(d) = d else {
            return Ok(());
        };
        if cn.intent != INTENT_CREATED || d.origins.is_empty() {
            return Ok(());
        }
        let mut pending = Vec::new();
        for o in &d.origins {
            let mut i = impacts.iter().position(|x| node_of(x) == o.id);
            // an origin is changed by the impact of its parent (via): that one decides it (ADR 0077, 0102)
            if let Some(j) = i
                && !impacts[j].via.is_empty()
            {
                let via = impacts[j].via.clone();
                i = impacts.iter().position(|x| x.id == via);
            }
            match i {
                None => pending.push(format!("{} (not in the change)", o.id)),
                Some(j) if impacts[j].review != REVIEW_ACCEPTED => {
                    pending.push(format!("{} ({})", impacts[j].key, impacts[j].review))
                }
                _ => {}
            }
        }
        if !pending.is_empty() {
            return Err(Error::new(
                ErrorKind::Conflict,
                format!(
                    "{} derives from nodes whose change impacts are not accepted: {}: {}: {ORIGINS_PENDING}",
                    cn.key,
                    pending.join(", "),
                    ErrorKind::Conflict.sentinel()
                ),
            ));
        }
        Ok(())
    }

    /// DerivedNodes returns the first versions of the nodes that derive from a node (ADR 0077): the successors of the
    /// merges and splits that replaced it. Version 0 matches any version of the node. Node.origins answers the other
    /// way.
    pub async fn derived_nodes(&self, ctx: &Ctx, r: &NodeRef) -> Result<Vec<Node>> {
        let mut tx = self.begin(ctx).await?;
        let out = tx.derived_nodes(ctx, r).await?;
        tx.commit(ctx).await?;
        Ok(out)
    }
}

/// The gate of one pass of a restructuring: it lets through the types answered already and collects the others
/// (Go's closure over `missing`).
pub(crate) struct Collector {
    open: bool,
    allowed: HashSet<String>,
    missing: parking_lot::Mutex<Vec<String>>,
}

impl Collector {
    fn open() -> Collector {
        Collector {
            open: true,
            allowed: HashSet::new(),
            missing: parking_lot::Mutex::new(Vec::new()),
        }
    }

    fn new(allowed: HashSet<String>) -> Collector {
        Collector {
            open: false,
            allowed,
            missing: parking_lot::Mutex::new(Vec::new()),
        }
    }

    fn ask(&self, typ: &str) -> Result<()> {
        if self.open || self.allowed.contains(typ) {
            return Ok(());
        }
        let mut m = self.missing.lock();
        if !m.iter().any(|t| t == typ) {
            m.push(typ.to_string());
        }
        Ok(())
    }

    fn collected(&self) -> bool {
        !self.missing.lock().is_empty()
    }

    fn missing(&self) -> Vec<String> {
        self.missing.lock().clone()
    }
}

impl TypeIndex {
    /// composes reports a composition link type.
    pub(crate) fn composes(&self, link_type: &str) -> bool {
        self.cat.as_ref().is_some_and(|c| c.composes(link_type))
    }

    /// compatible reports that a node of type a may be replaced by one of type b: the same type, or one a subtype of
    /// the other.
    pub(crate) fn compatible(&self, a: &str, b: &str) -> bool {
        a == b
            || self
                .cat
                .as_ref()
                .is_some_and(|c| c.is_a(a, b) || c.is_a(b, a))
    }
}

/// originsPatch describes origins for the created event of a successor: id, version and key of each.
pub(crate) async fn origins_patch(ctx: &Ctx, tx: &mut dyn Tx, origins: &[NodeRef]) -> Vec<Value> {
    let mut out = Vec::with_capacity(origins.len());
    for o in origins {
        let mut m = json!({"id": o.id.to_string(), "version": o.version});
        if let Ok(n) = tx.node(ctx, o).await {
            m["key"] = Value::String(n.key);
        }
        out.push(m);
    }
    out
}
