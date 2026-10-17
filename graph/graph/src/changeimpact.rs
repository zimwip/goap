//! Port of `changeimpact.go`: the change impacts of a change, their declaration (ProposeImpact) and their reviews
//! (ADR 0024, 0080, 0105); `Graph::change_tx` is in guard.rs.

use std::collections::{HashMap, HashSet};

use goap_ctx::Ctx;
use goap_domain::{
    Baseline, CHANGE_ABANDONED, CHANGE_ACTIVE, CHANGE_APPLIED, CHANGE_COMMITTED, CHANGE_DRAFT,
    Change, ChangeID, ChangeImpact, ChangeImpactID, ChangeItem, FLOW_OPEN, IMPACT_PROPOSED,
    IMPACT_REVIEWED, INTENT_CREATED, ITEM_PROPOSED, ImpactEvent, ImpactVerdict, ItemID, NodeID,
    NodeReview, REVIEW_ACCEPTED, REVIEW_PROPOSED, REVIEW_REJECTED, REVIEW_STATUS_SUBMITTED, Review,
    ReviewBatch, ReviewEntry, ReviewRecord, ReviewRequest, TYPE_REVIEW, namespace_of,
    review_outcome, review_record_of,
};
use goap_operations as operations;

use crate::changelog::{facts_filter, items_of, put_item};
use crate::flownodes::FlowNodes;
use crate::lifecycle::invalidf;
use crate::rebase::check_settled;
use crate::repo::Tx;
use crate::restructure::is_origins_pending;
use crate::{Error, ErrorKind, Graph, Result};

/// changeOpen loads a change and checks that it still accepts change impacts. It is the start of a write: it takes the
/// lock of the change's tree first (ADR 0127).
pub(crate) async fn change_open(ctx: &Ctx, tx: &mut dyn Tx, id: &ChangeID) -> Result<Change> {
    tx.lock_change(ctx, id).await?;
    let c = tx.change(ctx, id).await?;
    if c.status == CHANGE_APPLIED || c.status == CHANGE_ABANDONED || c.status == CHANGE_COMMITTED {
        return Err(Error::wrap(
            ErrorKind::Conflict,
            format!("change {id} is {}", c.status),
        ));
    }
    Ok(c)
}

/// findChangeImpact returns the position of a change impact in its change.
pub(crate) fn find_change_impact(c: &Change, id: &ChangeImpactID) -> Result<usize> {
    c.nodes.iter().position(|cn| &cn.id == id).ok_or_else(|| {
        Error::wrap(
            ErrorKind::NotFound,
            format!("change impact {id} of change {}", c.id),
        )
    })
}

impl Graph {
    /// ProposeImpact adds change impacts to a change (ADR 0024): each names an existing node (intent modified) and a
    /// rationale; its pre version must be in the reference baseline. The impact has no version until a
    /// ImpactNodeCheckout writes one (ADR 0076). A new node is created by ImpactNodeCreate, which adds its impact and
    /// its draft in one event (ADR 0077); it may be planned before (intent created, a key and a type, no draft).
    ///
    /// goap:operation impact.propose doer
    pub async fn propose_impact(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        nodes: Vec<ChangeImpact>,
    ) -> Result<Vec<ChangeImpact>> {
        self.allow(ctx, id, operations::IMPACT_PROPOSE).await?;
        for cn in &nodes {
            if cn.intent == INTENT_CREATED && (cn.key.is_empty() || cn.r#type.is_empty()) {
                return Err(invalidf(format!(
                    "change impact of {:?}: a planned creation needs a key and a type",
                    cn.key
                )));
            }
        }
        let mut tx = self.begin(ctx).await?;
        let out = self.declare_tx(ctx, &mut tx, id, nodes, true).await?;
        tx.commit(ctx).await?;
        Ok(out)
    }

    /// declareTx declares change impacts in a transaction (ProposeImpact, and the impact an operation on an existing
    /// node declares). With propose false it only checks and returns them: the caller records them itself (a
    /// creation, whose created event carries the impact).
    pub(crate) async fn declare_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        nodes: Vec<ChangeImpact>,
        propose: bool,
    ) -> Result<Vec<ChangeImpact>> {
        let mut out = Vec::with_capacity(nodes.len());
        let mut c = change_open(ctx, tx, id).await?;
        let b = tx.baseline(ctx, &c.baseline_id).await?;
        let ix = self.types_at();
        let mut known: HashSet<ChangeImpactID> = c.nodes.iter().map(|cn| cn.id.clone()).collect();
        // a node appears once per flow: what the flow sees (the stale change impacts of a relaunched step do not count)
        let mut pres_by_flow: HashMap<String, HashSet<NodeID>> = HashMap::new();
        let mut batch = nodes;
        for cn in &mut batch {
            if cn.id.is_empty() {
                cn.id = ChangeImpactID::new(self.new_id());
            }
            cn.flow = c.resolve_flow(&cn.flow); // no flow: the active option (ADR 0032 §6)
            cn.validation = None; // a read model, never stored
            known.insert(cn.id.clone());
        }
        for mut cn in batch {
            if cn.post.is_some() || cn.landed.is_some() || !cn.reviews.is_empty() || cn.recheck {
                return Err(invalidf(format!(
                    "change impact {}: post, landed and reviews are set by ImpactNodeCreate / ImpactNodeCheckout, ImpactNodeReview and Apply",
                    cn.id
                )));
            }
            if cn.review.is_empty() {
                cn.review = REVIEW_PROPOSED;
            }
            if cn.review != REVIEW_PROPOSED {
                return Err(invalidf(format!(
                    "change impact {} starts proposed, it is accepted or rejected by ImpactNodeReview",
                    cn.id
                )));
            }
            if !cn.flow.is_empty() {
                if c.flow(&cn.flow).is_none() {
                    return Err(Error::wrap(
                        ErrorKind::NotFound,
                        format!("change impact {}: unknown flow {}", cn.id, cn.flow),
                    ));
                }
                if c.flow_status_of(&cn.flow) != FLOW_OPEN {
                    return Err(Error::wrap(
                        ErrorKind::Conflict,
                        format!("change impact {}: flow {} is not open", cn.id, cn.flow),
                    ));
                }
            }
            if !cn.via.is_empty() && !known.contains(&cn.via) {
                return Err(invalidf(format!(
                    "change impact {}: unknown via {}",
                    cn.id, cn.via
                )));
            }
            if let Some(pre) = cn.pre.clone() {
                if b.nodes.get(&pre.id) != Some(&pre.version) {
                    return Err(Error::wrap(
                        ErrorKind::Conflict,
                        format!(
                            "change impact {}: pre {pre} is not in baseline {} (the node moved on)",
                            cn.id, b.id
                        ),
                    ));
                }
                let pres = pres_by_flow.entry(cn.flow.clone()).or_insert_with(|| {
                    let fv = FlowNodes::new(c.clone(), &cn.flow);
                    c.nodes
                        .iter()
                        .filter(|x| x.pre.is_some() && fv.visible(x))
                        .filter_map(|x| x.pre.as_ref().map(|p| p.id.clone()))
                        .collect()
                });
                if !pres.insert(pre.id.clone()) {
                    return Err(Error::wrap(
                        ErrorKind::Conflict,
                        format!(
                            "change impact {}: node {} is already in the change",
                            cn.id, pre.id
                        ),
                    ));
                }
                let n = tx.node(ctx, &pre).await?;
                if (!cn.key.is_empty() && cn.key != n.key)
                    || (!cn.r#type.is_empty() && cn.r#type != n.r#type)
                {
                    return Err(invalidf(format!(
                        "change impact {}: key / type do not match node {}",
                        cn.id,
                        n.r#ref()
                    )));
                }
                cn.key = n.key.clone();
                cn.r#type = n.r#type.clone();
                let (got, want) = (namespace_of(&n.namespace), namespace_of(&c.namespace));
                if got != want {
                    return Err(invalidf(format!(
                        "change impact {}: {} belongs to namespace {got:?}, the change acts on {want:?}",
                        cn.id, n.key
                    )));
                }
            }
            if cn.pre.is_none() && !cn.r#type.is_empty() {
                ix.check_node(namespace_of(&c.namespace), &cn.r#type)
                    .map_err(|e| e.context(format!("change impact {}", cn.id)))?;
            }
            if propose && cn.intent == INTENT_CREATED {
                self.check_planned_key(ctx, tx, &c, &b, &cn).await?;
            }
            cn.created_at = self.now();
            if let Err(e) = cn.validate() {
                return Err(invalidf(format!("change impact {}: {e}", cn.id)));
            }
            if propose {
                self.emit(
                    ctx,
                    tx,
                    vec![ImpactEvent {
                        change: id.clone(),
                        impact: cn.id.clone(),
                        op: IMPACT_PROPOSED,
                        flow: cn.flow.clone(),
                        execution: cn.execution.clone(),
                        state: Some(cn.clone()),
                        ..Default::default()
                    }],
                )
                .await?;
            }
            out.push(cn);
        }
        if c.status == CHANGE_DRAFT {
            c.status = CHANGE_ACTIVE;
            // the projection moved with the events: the header is written as it is now
            let mut cur = tx.change(ctx, id).await?;
            cur.status = CHANGE_ACTIVE;
            tx.put_change(ctx, cur).await?;
        }
        Ok(out)
    }

    /// checkPlannedKey refuses a planned creation whose key a node already holds, or the change already acts on in
    /// the flow (createTx makes the same checks when the node is created).
    pub(crate) async fn check_planned_key(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        b: &Baseline,
        cn: &ChangeImpact,
    ) -> Result<()> {
        match tx
            .node_id_by_key(ctx, namespace_of(&c.namespace), &cn.key)
            .await
        {
            Ok(nid) => {
                if b.nodes.contains_key(&nid) {
                    return Err(Error::wrap(
                        ErrorKind::Conflict,
                        format!(
                            "cannot plan {} ({}): the reference baseline of change {} already holds it",
                            cn.key, cn.r#type, c.id
                        ),
                    ));
                }
            }
            Err(e) if e.is_not_found() => {}
            Err(e) => return Err(e),
        }
        let fv = FlowNodes::new(c.clone(), &cn.flow);
        for other in &c.nodes {
            if other.key == cn.key && other.id != cn.id && fv.visible(other) {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!(
                        "cannot plan {} ({}): the change already holds it as change impact {}",
                        cn.key, cn.r#type, other.id
                    ),
                ));
            }
        }
        Ok(())
    }

    /// ListChangeImpacts returns the change impacts of a change.
    pub async fn list_change_impacts(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<ChangeImpact>> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        tx.commit(ctx).await?;
        Ok(c.nodes)
    }

    /// ImpactsOf returns the change impacts one execution (an Activity Run) declared: scoped to exactly what that run
    /// touched rather than the whole change. Empty when the execution declared none.
    pub async fn impacts_of(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        execution: &str,
    ) -> Result<Vec<ChangeImpact>> {
        let all = self.list_change_impacts(ctx, id).await?;
        Ok(all
            .into_iter()
            .filter(|cn| cn.execution == execution)
            .collect())
    }

    /// ImpactNodeReview accepts or rejects a proposed change impact; accepting checks its draft, if any, as the
    /// version written from it will be (ADR 0079) and writes nothing: landing does. The comment is mandatory. The
    /// verdict is made by a review of one entry, submitted at once (ADR 0105).
    pub async fn impact_node_review(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        node: &ChangeImpactID,
        status: NodeReview,
        by: &str,
        comment: &str,
    ) -> Result<ChangeImpact> {
        self.impact_node_review_on(ctx, id, "", "", node, status, by, comment)
            .await
    }

    /// ImpactNodeReviewOn reviews a change impact as a flow sees it (ADR 0025): on a flow the review is a candidate
    /// until the flow is adopted, and execution is the action run that made it. As every verdict (ADR 0105), it is the
    /// submission of a review: one of a single entry, whose record the graph writes before the verdict.
    ///
    /// goap:operation impact.review reviewer
    #[allow(clippy::too_many_arguments)]
    pub async fn impact_node_review_on(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        execution: &str,
        node: &ChangeImpactID,
        status: NodeReview,
        by: &str,
        comment: &str,
    ) -> Result<ChangeImpact> {
        self.allow(ctx, id, operations::IMPACT_REVIEW).await?;
        let mut out = self
            .review_batch(
                ctx,
                id,
                ReviewBatch {
                    flow: flow.to_string(),
                    execution: execution.to_string(),
                    by: by.to_string(),
                    verdicts: vec![ImpactVerdict {
                        impact: node.clone(),
                        status,
                        comment: comment.to_string(),
                    }],
                    ..Default::default()
                },
            )
            .await?;
        Ok(out.remove(0))
    }

    /// reviewTx is one verdict inside a transaction: the policy, the acceptance gate, the reviewed event. Nothing is
    /// written when it refuses. reviewID is the key of the submitted review that makes the verdict, stamped on the
    /// review of the impact (ADR 0080), and causation the log entry of its record, the cause of the reviewed event
    /// (ADR 0105).
    #[allow(clippy::too_many_arguments)]
    pub(crate) async fn review_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        flow: &str,
        execution: &str,
        node: &ChangeImpactID,
        status: NodeReview,
        by: &str,
        comment: &str,
        review_id: &str,
        causation: &str,
    ) -> Result<ChangeImpact> {
        let c = change_open(ctx, tx, id).await?;
        let flow = c.resolve_flow(flow);
        if !flow.is_empty() {
            if c.flow(&flow).is_none() {
                return Err(Error::wrap(ErrorKind::NotFound, format!("flow {flow}")));
            }
            if c.flow_status_of(&flow) != FLOW_OPEN {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("flow {flow} is not open"),
                ));
            }
        }
        let i = find_change_impact(&c, node)?;
        let mut cn = c.nodes[i].clone();
        let seen = FlowNodes::new(c.clone(), &flow).find(ctx, tx, node).await?;
        if seen.review != REVIEW_PROPOSED {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change impact {node} is already {}", seen.review),
            ));
        }
        if let Some(p) = &self.review_policy {
            let entries = tx.log(ctx, &facts_filter(id)).await?;
            let facts = items_of(&entries)?;
            p.review(&ReviewRequest {
                impact: seen.clone(),
                reviewer: by.to_string(),
                status: status.clone(),
                items: facts,
            })
            .map_err(|e| invalidf(format!("review of {node} refused: {e}")))?;
        }
        // an accepted review is gated by what a version must satisfy (ADR 0079): validators, required links, link
        // attributes, the origins gate, checked on the draft; a refusal leaves the review proposed. Nothing is
        // written: the version is written when the change lands
        if status == REVIEW_ACCEPTED {
            check_settled(ctx, tx, &c, node).await?;
            self.check_modifies(ctx, tx, &c, &flow, &seen).await?;
        }
        if status == REVIEW_ACCEPTED && seen.post.is_some() {
            self.check_accepted(ctx, tx, id, &flow, node).await?;
        }
        let r = Review {
            status: status.clone(),
            by: by.to_string(),
            comment: comment.to_string(),
            at: self.now(),
            flow: flow.clone(),
            execution: execution.to_string(),
            review_id: review_id.to_string(),
            ..Default::default()
        };
        if flow.is_empty() {
            cn.review = status;
        }
        cn.reviews.push(r.clone());
        self.emit(
            ctx,
            tx,
            vec![ImpactEvent {
                change: id.clone(),
                impact: cn.id.clone(),
                op: IMPACT_REVIEWED,
                flow,
                execution: execution.to_string(),
                by: by.to_string(),
                review: Some(r),
                causation: causation.to_string(),
                ..Default::default()
            }],
        )
        .await?;
        Ok(cn)
    }

    /// ImpactNodeReviewBatch submits a review (ADR 0080, 0105): every verdict is made as ImpactNodeReviewOn would make
    /// it, and either all of them stand or none does. The record of the submitted review is written first, then the
    /// verdicts, each reviewed event naming it as its cause; every review carries its key. The batch's item, when set,
    /// is that record; else the graph writes one, keyed by id (allocated when empty). A verdict accepting a node whose
    /// origins are accepted by the same batch stands: the origins are settled first.
    pub async fn impact_node_review_batch(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        b: ReviewBatch,
    ) -> Result<Vec<ChangeImpact>> {
        self.allow(ctx, id, operations::IMPACT_REVIEW).await?;
        self.review_batch(ctx, id, b).await
    }

    /// reviewBatch is ImpactNodeReviewBatch once the operation is allowed.
    pub(crate) async fn review_batch(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        b: ReviewBatch,
    ) -> Result<Vec<ChangeImpact>> {
        if b.verdicts.is_empty() {
            return Err(invalidf("a review batch needs at least one verdict"));
        }
        let single = b.verdicts.len() == 1;
        let label = |n: usize, impact: &str| -> String {
            if single {
                String::new()
            } else {
                format!("verdict {} ({impact}): ", n + 1)
            }
        };
        let mut seen_impact: HashSet<ChangeImpactID> = HashSet::new();
        for (n, v) in b.verdicts.iter().enumerate() {
            check_verdict(&v.status, &v.comment)
                .map_err(|e| e.context_raw(&label(n, v.impact.as_str())))?;
            if !seen_impact.insert(v.impact.clone()) {
                return Err(invalidf(format!(
                    "verdict {}: change impact {} is reviewed twice in the batch",
                    n + 1,
                    v.impact
                )));
            }
        }
        let it = self.review_record(&b)?;
        self.authorize_items(ctx, id, std::slice::from_ref(&it))
            .await?;
        let key = review_record_of(&it.data).key;
        let mut tx = self.change_tx(ctx, id).await?;
        let c = tx.change(ctx, id).await?;
        let mut it = it;
        it.flow = c.resolve_flow(&b.flow); // the verdicts are made on this flow (ADR 0032 §6)
        put_item(ctx, &mut tx, id, &it).await?;
        let mut out: Vec<Option<ChangeImpact>> = vec![None; b.verdicts.len()];
        let mut pending: Vec<usize> = (0..b.verdicts.len()).collect();
        while !pending.is_empty() {
            let mut next = Vec::new();
            let mut last_err: Option<Error> = None;
            for &i in &pending {
                let v = &b.verdicts[i];
                match self
                    .review_tx(
                        ctx,
                        &mut tx,
                        id,
                        &b.flow,
                        &b.execution,
                        &v.impact,
                        v.status.clone(),
                        &b.by,
                        v.comment.trim(),
                        &key,
                        it.id.as_str(),
                    )
                    .await
                {
                    Ok(cn) => out[i] = Some(cn),
                    Err(e) => {
                        let name = match tx.change(ctx, id).await {
                            Ok(c) => match find_change_impact(&c, &v.impact) {
                                Ok(k) => label(i, &c.nodes[k].key),
                                Err(_) => label(i, v.impact.as_str()),
                            },
                            Err(_) => label(i, v.impact.as_str()),
                        };
                        if is_origins_pending(&e) {
                            next.push(i);
                            last_err = Some(e.context_raw(&name));
                        } else {
                            return Err(e.context_raw(&name));
                        }
                    }
                }
            }
            if next.len() == pending.len() {
                // no progress: the origins are not settled by this batch
                return Err(last_err.unwrap_or_else(|| invalidf("review batch")));
            }
            pending = next;
        }
        tx.commit(ctx).await?;
        Ok(out.into_iter().map(Option::unwrap_or_default).collect())
    }

    /// putReviewRecord writes the record of a review the graph submits on its own (a merge accepting what it lands,
    /// ADR 0105) on the main flow of a change: by the component, its entries decided. It returns the key of the review
    /// and the log entry of the record, the cause of the reviewed events that follow.
    pub(crate) async fn put_review_record(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        change: &ChangeID,
        by: &str,
        comment: &str,
        entries: Vec<ReviewEntry>,
    ) -> Result<(String, String)> {
        let rec = ReviewRecord {
            key: format!("REV-{}", self.new_id()),
            comment: comment.to_string(),
            status: REVIEW_STATUS_SUBMITTED.to_string(),
            entries,
            by: by.to_string(),
            submitted_at: Some(self.now()),
            ..Default::default()
        };
        let it = ChangeItem {
            id: ItemID::new(self.new_id()),
            r#type: TYPE_REVIEW,
            status: ITEM_PROPOSED,
            data: rec.data(),
            produced_by: by.to_string(),
            created_at: self.now(),
            ..Default::default()
        };
        self.check_item(&it)
            .map_err(|e| invalidf(format!("item {}: {e}", it.id)))?;
        put_item(ctx, tx, change, &it).await?;
        Ok((rec.key, it.id.to_string()))
    }

    /// reviewRecord is the item recording the submitted review of a batch: the batch's own, checked against its
    /// verdicts, or one the graph makes of them.
    pub(crate) fn review_record(&self, b: &ReviewBatch) -> Result<ChangeItem> {
        let mut it = match &b.item {
            Some(item) => {
                let it = item.clone();
                let rec = review_record_of(&it.data);
                if it.r#type != TYPE_REVIEW || rec.status != REVIEW_STATUS_SUBMITTED {
                    return Err(invalidf(format!(
                        "the item of a review batch is the submitted review, not a {} {} item",
                        rec.status, it.r#type
                    )));
                }
                if !b.id.is_empty() && rec.key != b.id {
                    return Err(invalidf(format!(
                        "review batch {} carries the record of review {}",
                        b.id, rec.key
                    )));
                }
                if rec.entries.len() != b.verdicts.len() {
                    return Err(invalidf(format!(
                        "review {} has {} entries for {} verdicts",
                        rec.key,
                        rec.entries.len(),
                        b.verdicts.len()
                    )));
                }
                for v in &b.verdicts {
                    let ok = rec.entries.iter().any(|e| {
                        e.impact == v.impact.as_str() && e.outcome == review_outcome(&v.status)
                    });
                    if !ok {
                        return Err(invalidf(format!(
                            "review {} does not record the verdict on {}",
                            rec.key, v.impact
                        )));
                    }
                }
                it
            }
            None => {
                let key = if b.id.is_empty() {
                    format!("REV-{}", self.new_id())
                } else {
                    b.id.clone()
                };
                let rec = ReviewRecord {
                    key,
                    flow: b.flow.clone(),
                    status: REVIEW_STATUS_SUBMITTED.to_string(),
                    by: b.by.clone(),
                    submitted_at: Some(self.now()),
                    entries: b
                        .verdicts
                        .iter()
                        .map(|v| ReviewEntry {
                            impact: v.impact.to_string(),
                            comment: v.comment.trim().to_string(),
                            outcome: review_outcome(&v.status).to_string(),
                        })
                        .collect(),
                    ..Default::default()
                };
                ChangeItem {
                    r#type: TYPE_REVIEW,
                    data: rec.data(),
                    produced_by: b.by.clone(),
                    execution: b.execution.clone(),
                    ..Default::default()
                }
            }
        };
        if it.id.is_empty() {
            it.id = ItemID::new(self.new_id());
        }
        if it.status.is_empty() {
            it.status = ITEM_PROPOSED;
        }
        it.created_at = self.now();
        self.check_item(&it)
            .map_err(|e| invalidf(format!("item {}: {e}", it.id)))?;
        Ok(it)
    }

    /// ReopenImpacts sends decided change impacts of the main flow back to proposed (a review event, as when a change
    /// goes back to a state, ADR 0058): what was accepted must be reviewed again, what was rejected is reworked. A use
    /// case calls it when the ground of an acceptance falls (ADR 0075: a derogation expired); the graph reads no clock
    /// and names no reason. A proposed impact is left as it is; the comment is mandatory.
    pub async fn reopen_impacts(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impacts: &[ChangeImpactID],
        comment: &str,
    ) -> Result<Vec<ChangeImpactID>> {
        let comment = comment.trim();
        if comment.is_empty() {
            return Err(invalidf("reopening a review needs a comment"));
        }
        let mut tx = self.begin(ctx).await?;
        let c = change_open(ctx, &mut tx, id).await?;
        let mut reopened = Vec::new();
        for imp in impacts {
            let i = find_change_impact(&c, imp)?;
            let cn = &c.nodes[i];
            if cn.review != REVIEW_ACCEPTED && cn.review != REVIEW_REJECTED {
                continue;
            }
            let r = Review {
                status: REVIEW_PROPOSED,
                by: self.caller_of(ctx),
                at: self.now(),
                comment: comment.to_string(),
                ..Default::default()
            };
            self.emit(
                ctx,
                &mut tx,
                vec![ImpactEvent {
                    change: id.clone(),
                    impact: cn.id.clone(),
                    op: IMPACT_REVIEWED,
                    by: r.by.clone(),
                    review: Some(r),
                    ..Default::default()
                }],
            )
            .await?;
            reopened.push(cn.id.clone());
        }
        tx.commit(ctx).await?;
        Ok(reopened)
    }

    /// checkModifies refuses to accept a change impact that modifies nothing (ADR 0102): a candidate never checked
    /// out, or a draft left as it was checked out. Only a modification is accepted, whoever accepts it.
    pub(crate) async fn check_modifies(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        flow: &str,
        cn: &ChangeImpact,
    ) -> Result<()> {
        let Some(d) = self.seen_draft(ctx, tx, c, flow, &cn.id).await? else {
            return Err(invalidf(format!(
                "change impact {} ({}) modifies nothing: check the node out and change it, or withdraw the impact",
                cn.key, cn.id
            )));
        };
        if !self.draft_modifies(ctx, tx, Some(&d)).await? {
            return Err(invalidf(format!(
                "change impact {} ({}) modifies nothing: its draft is the version it was checked out from",
                cn.key, cn.id
            )));
        }
        Ok(())
    }
}

/// checkVerdict checks the status and the comment of a verdict.
pub(crate) fn check_verdict(status: &NodeReview, comment: &str) -> Result<()> {
    if status != &REVIEW_ACCEPTED && status != &REVIEW_REJECTED {
        return Err(invalidf(format!(
            "a change impact is reviewed as accepted or rejected, not {:?}",
            status.as_str()
        )));
    }
    if comment.trim().is_empty() {
        return Err(invalidf("a review needs a comment"));
    }
    Ok(())
}

/// The prefix of a message without the ": " Error::context puts (Go's `fmt.Errorf("%s%w", label, err)`).
trait ContextRaw {
    fn context_raw(self, prefix: &str) -> Self;
}

impl ContextRaw for Error {
    fn context_raw(self, prefix: &str) -> Error {
        if prefix.is_empty() {
            return self;
        }
        Error::new(self.kind(), format!("{prefix}{}", self.message()))
    }
}
