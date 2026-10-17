//! Part of `flownodes.go` (G1): what a flow (ADR 0017, 0025) sees of the change impacts. Adopting a flow
//! (`adoptNodes`) and the drafts a flow sees belong to a later slice (see PORTING.md).

use std::collections::{HashMap, HashSet};

use goap_ctx::Ctx;
use goap_domain::{Change, ChangeImpact, ChangeImpactID, Flow, NodeID, impacts_seen_by};

use crate::changelog::impact_events;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Result};

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
