//! What a flow (ADR 0017, 0025) sees of the change impacts, and what adopting or discarding
//! a flow does to them.

use std::collections::{HashMap, HashSet};

use goap_ctx::Ctx;
use goap_domain::{
    Change, ChangeImpact, ChangeImpactID, Draft, Flow, IMPACT_ADOPTED, IMPACT_CANCELLED,
    IMPACT_CHECKED_OUT, IMPACT_CREATED, IMPACT_DISCARDED, IMPACT_TRANSITIONED, IMPACT_UPDATED,
    ImpactEvent, LinkID, NodeID, REVIEW_ACCEPTED, REVIEW_REJECTED, Review, draft_seen_by,
    impacts_seen_by,
};
use serde_json::{Map, Value};

use crate::changelog::impact_events;
use crate::draft::own_draft;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// flowChain lists the flows from f up to the main flow (excluded), innermost first.
pub(crate) fn flow_chain(c: &Change, flow: &str) -> Vec<Flow> {
    let mut out = Vec::new();
    let mut flow = flow.to_string();
    let mut hops = 0;
    while !flow.is_empty() && hops < 64 {
        let Some(f) = c.flow(&flow) else {
            break;
        };
        flow = f.parent.clone();
        out.push(f);
        hops += 1;
    }
    out
}

/// staleOf are the action runs invalidated on the chain of a flow.
pub(crate) fn stale_of(chain: &[Flow]) -> HashMap<String, bool> {
    let mut out = HashMap::new();
    for f in chain {
        for e in &f.stale_runs {
            out.insert(e.clone(), true);
        }
    }
    out
}

/// nodeOf is the node a change impact is about.
pub(crate) fn node_of(cn: &ChangeImpact) -> NodeID {
    match (&cn.post, &cn.pre) {
        (Some(p), _) => p.id.clone(),
        (None, Some(p)) => p.id.clone(),
        _ => NodeID::default(),
    }
}

/// flowNodes is what the process running on a flow sees of the change impacts.
pub(crate) struct FlowNodes {
    pub(crate) c: Change,
    pub(crate) flow: String,
    pub(crate) chain: Vec<Flow>,
    pub(crate) in_flow: HashSet<String>,
    pub(crate) stale: HashMap<String, bool>,
}

impl FlowNodes {
    pub(crate) fn new(c: Change, flow: &str) -> FlowNodes {
        let chain = flow_chain(&c, flow);
        let in_flow = chain.iter().map(|f| f.id.clone()).collect();
        let stale = stale_of(&chain);
        FlowNodes {
            c,
            flow: flow.to_string(),
            chain,
            in_flow,
            stale,
        }
    }

    pub(crate) fn is_stale(&self, execution: &str) -> bool {
        !execution.is_empty() && self.stale.get(execution).copied().unwrap_or(false)
    }

    /// visible tells whether a stored change impact exists for the flow.
    pub(crate) fn visible(&self, cn: &ChangeImpact) -> bool {
        if cn.superseded {
            return false;
        }
        if !cn.flow.is_empty() && !self.in_flow.contains(&cn.flow) {
            return false;
        }
        !(!self.flow.is_empty() && self.is_stale(&cn.execution))
    }

    /// nodes returns the change impacts the flow sees, with the post version and the review it resolves to: a fold
    /// of the impact log (ADR 0029 §3).
    pub(crate) async fn nodes(&self, ctx: &Ctx, tx: &mut dyn Tx) -> Result<Vec<ChangeImpact>> {
        if self.flow.is_empty() {
            return Ok(impacts_seen_by(&self.c.nodes, &[], &[], &self.stale));
        }
        let events = impact_events(ctx, tx, &self.c.id).await?;
        let chain: Vec<String> = self.chain.iter().map(|f| f.id.clone()).collect();
        Ok(impacts_seen_by(&self.c.nodes, &events, &chain, &self.stale))
    }

    /// find returns the change impact a flow sees under an id.
    pub(crate) async fn find(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeImpactID,
    ) -> Result<ChangeImpact> {
        self.nodes(ctx, tx)
            .await?
            .into_iter()
            .find(|cn| &cn.id == id)
            .ok_or_else(|| {
                Error::wrap(
                    ErrorKind::NotFound,
                    format!(
                        "change impact {id} is not on the flow {:?} of change {}",
                        self.flow, self.c.id
                    ),
                )
            })
    }
}

// ---- adopting and discarding a flow (ADR 0025, 0079) -------------------------------------------------------------

impl Graph {
    /// adoptNodes installs the drafts of an adopted flow on the main flow (ADR 0079): a draft of the flow replaces the
    /// main flow's (refused when the main flow's moved on since the flow checked the node out, by a run that is not
    /// stale); else when the main flow's draft was written in part by stale runs, it is reset to what the other runs
    /// left, or dropped when they left nothing. The change impacts of the flow become the change's, the stale ones are
    /// superseded (their drafts go with them).
    pub(crate) async fn adopt_nodes(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        f: &Flow,
        by: &str,
    ) -> Result<()> {
        let stale: HashSet<String> = f.stale_runs.iter().cloned().collect();
        let is_stale = |e: &str| !e.is_empty() && stale.contains(e);
        let rows = self.drafts(ctx, tx, &c.id).await?;
        let events = impact_events(ctx, tx, &c.id).await?;
        let mut installs = Vec::new();
        for cn in &c.nodes {
            let fd = own_draft(&rows, &cn.id, &f.id);
            let md = own_draft(&rows, &cn.id, "");
            if let Some(fd) = fd {
                if !accepted_on_flow(c, &node_of(cn), &f.id) {
                    return Err(Error::wrap(
                        ErrorKind::Conflict,
                        format!(
                            "node {} is a draft on flow {} that the flow did not accept: accept it, or cancel the checkout, before the flow is adopted (ADR 0079)",
                            fd.key, f.id
                        ),
                    ));
                }
                // the flow was forked from the node as it was: it is a conflict when the main flow moved on since, by
                // a run that is not stale
                let mut seq = 0;
                for e in &events {
                    if e.impact != cn.id {
                        continue;
                    }
                    if e.flow == f.id && e.starts_draft() && seq == 0 {
                        seq = e.seq;
                    } else if seq > 0
                        && e.flow.is_empty()
                        && e.seq > seq
                        && changes_draft(e)
                        && !is_stale(&e.execution)
                    {
                        return Err(Error::wrap(
                            ErrorKind::Conflict,
                            format!(
                                "node {} was changed on the main flow since the flow forked (checked out again in the meantime)",
                                fd.key
                            ),
                        ));
                    }
                }
                let mut patch = Map::new();
                patch.insert("adopted".into(), Value::String(f.id.clone()));
                installs.push(ImpactEvent {
                    change: c.id.clone(),
                    impact: cn.id.clone(),
                    op: IMPACT_TRANSITIONED,
                    execution: fd.execution.clone(),
                    by: by.to_string(),
                    post: Some(fd.r#ref()),
                    draft: Some(fd),
                    patch,
                    ..Default::default()
                });
            } else if let Some(md) = md
                && !(cn.flow.is_empty() && !cn.superseded && is_stale(&cn.execution))
            {
                // the main flow's draft, without what the stale runs wrote
                match draft_seen_by(&events, &cn.id, &[], &is_stale) {
                    None => installs.push(ImpactEvent {
                        change: c.id.clone(),
                        impact: cn.id.clone(),
                        op: IMPACT_CANCELLED,
                        by: by.to_string(),
                        ..Default::default()
                    }),
                    Some(kept) if !same_draft(&kept, &md) => {
                        let mut patch = Map::new();
                        patch.insert("adopted".into(), Value::String(f.id.clone()));
                        installs.push(ImpactEvent {
                            change: c.id.clone(),
                            impact: cn.id.clone(),
                            op: IMPACT_TRANSITIONED,
                            execution: kept.execution.clone(),
                            by: by.to_string(),
                            post: Some(kept.r#ref()),
                            draft: Some(kept),
                            patch,
                            ..Default::default()
                        });
                    }
                    Some(_) => {}
                }
            }
        }
        self.adopt_change_impacts(ctx, tx, c, f, installs, by).await
    }

    /// adoptChangeImpacts moves the change impacts and reviews of an adopted flow to the main flow (ADR 0025 §5.3): one
    /// adopted event, folded over every change impact, then the drafts it installs on the main flow (ADR 0029, 0079).
    /// An install for an impact the adoption supersedes is left out.
    pub(crate) async fn adopt_change_impacts(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        f: &Flow,
        installs: Vec<ImpactEvent>,
        by: &str,
    ) -> Result<()> {
        self.emit(
            ctx,
            tx,
            vec![ImpactEvent {
                change: c.id.clone(),
                op: IMPACT_ADOPTED,
                flow: f.id.clone(),
                stale: f.stale_runs.clone(),
                by: by.to_string(),
                ..Default::default()
            }],
        )
        .await?;
        let impacts = tx.change_impacts(ctx, &c.id).await?;
        for e in installs {
            match impacts.iter().find(|cn| cn.id == e.impact) {
                Some(cn) if !cn.superseded => self.emit(ctx, tx, vec![e]).await?,
                _ => {}
            }
        }
        Ok(())
    }

    /// discardNodes rejects the change impacts a discarded flow and its descendants declared. What they drafted stays
    /// in the log, for the audit; nothing sees it any more.
    pub(crate) async fn discard_nodes(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        f: &Flow,
        by: &str,
    ) -> Result<()> {
        let mut gone: HashSet<String> = HashSet::from([f.id.clone()]);
        let mut changed = true;
        while changed {
            changed = false;
            for o in c.flows() {
                if !gone.contains(&o.id) && gone.contains(&o.parent) {
                    gone.insert(o.id.clone());
                    changed = true;
                }
            }
        }
        for cn in &c.nodes {
            if !gone.contains(&cn.flow) || cn.review == REVIEW_REJECTED {
                continue;
            }
            let r = Review {
                status: REVIEW_REJECTED,
                by: by.to_string(),
                comment: "flow discarded".into(),
                at: self.now(),
                flow: cn.flow.clone(),
                ..Default::default()
            };
            self.emit(
                ctx,
                tx,
                vec![ImpactEvent {
                    change: c.id.clone(),
                    impact: cn.id.clone(),
                    op: IMPACT_DISCARDED,
                    flow: cn.flow.clone(),
                    by: by.to_string(),
                    review: Some(r),
                    ..Default::default()
                }],
            )
            .await?;
        }
        Ok(())
    }
}

/// changesDraft reports an event that changes what a flow holds of a node (not a review).
pub(crate) fn changes_draft(e: &ImpactEvent) -> bool {
    e.op == IMPACT_CREATED
        || e.op == IMPACT_CHECKED_OUT
        || e.op == IMPACT_UPDATED
        || e.op == IMPACT_TRANSITIONED
        || e.op == IMPACT_CANCELLED
}

/// sameDraft reports drafts that hold the same: the flow, the run and the ids of the links are not what a draft holds.
pub(crate) fn same_draft(a: &Draft, b: &Draft) -> bool {
    let norm = |d: &Draft| {
        let mut d = d.clone();
        d.flow.clear();
        d.execution.clear();
        for l in &mut d.links {
            l.id = LinkID::default();
        }
        serde_json::to_string(&d).unwrap_or_default()
    };
    norm(a) == norm(b)
}

/// acceptedOnFlow reports whether the last review of a node of the change made on a flow accepts it (ADR 0079: a
/// draft is not written until the change lands, so adopting a flow asks its acceptance).
pub(crate) fn accepted_on_flow(c: &Change, node: &NodeID, flow: &str) -> bool {
    let mut accepted = false;
    for cn in &c.nodes {
        if &node_of(cn) != node {
            continue;
        }
        for r in &cn.reviews {
            if r.flow == flow && !r.superseded {
                accepted = r.status == REVIEW_ACCEPTED;
            }
        }
    }
    accepted
}
