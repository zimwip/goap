//! The view levels of a change (ADR 0032), the same change impacts filtered one step of their
//! life earlier or later. A branch that is not validated yet (an option, a flow) is seen at written or accepted; only
//! landed is a baseline.

use goap_ctx::Ctx;
use goap_domain::{Baseline, CHANGE_APPLIED, Change, ChangeID, REVIEW_ACCEPTED, REVIEW_REJECTED};

use crate::changebranch::own_branch;
use crate::flownodes::FlowNodes;
use crate::lifecycle::invalidf;
use crate::merge::branch_head;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// ViewWritten: every version written and not rejected, proposed or accepted: what the change would look like.
pub const VIEW_WRITTEN: &str = "written";
/// ViewAccepted: the accepted versions only: what the change would land if it were applied now.
pub const VIEW_ACCEPTED: &str = "accepted";
/// ViewLanded: what the change landed: its result baseline once applied, its starting point before.
pub const VIEW_LANDED: &str = "landed";

impl Graph {
    /// ChangeView returns the graph a change sees at a level, on one of its flows ("" = the main flow): the head of
    /// the branch the change works on, with the posts of the change impacts the level counts laid over it: the
    /// version a landed change wrote, or version 0 for a node the change holds a draft of (ADR 0079; read it with
    /// ChangeNodeView). Nothing is stored: the result is a baseline without an id. At landed it is a stored baseline:
    /// the result of the change, or its start.
    pub async fn change_view(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        level: &str,
    ) -> Result<Baseline> {
        let level = match level {
            VIEW_WRITTEN | VIEW_ACCEPTED | VIEW_LANDED => level,
            "" => VIEW_WRITTEN,
            _ => return Err(invalidf(format!("unknown view level {level:?}"))),
        };
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        let flow = c.resolve_flow(flow); // no flow: the active option
        if !flow.is_empty() && c.flow(&flow).is_none() {
            return Err(Error::wrap(
                ErrorKind::NotFound,
                format!("flow {flow} of change {id}"),
            ));
        }
        let b = self.change_view_tx(ctx, &mut tx, &c, &flow, level).await?;
        tx.commit(ctx).await?;
        Ok(b)
    }

    pub(crate) async fn change_view_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        flow: &str,
        level: &str,
    ) -> Result<Baseline> {
        let mut from = c.baseline_id.clone();
        if level == VIEW_LANDED {
            if c.status == CHANGE_APPLIED && !c.result_baseline_id.is_empty() {
                from = c.result_baseline_id.clone();
            }
            return tx.baseline(ctx, &from).await;
        }
        if own_branch(ctx, tx, c).await?.is_some() {
            // the head of the change branch (what it and its merged sub-changes committed, ADR 0103)
            let head = branch_head(ctx, tx, &c.namespace, &c.branch).await?;
            from = head.id;
        }
        let base = tx.baseline(ctx, &from).await?;
        let mut nodes = base.nodes.clone();
        let impacts = FlowNodes::new(c.clone(), flow).nodes(ctx, tx).await?;
        for cn in &impacts {
            let Some(post) = &cn.post else {
                continue;
            };
            if cn.review == REVIEW_REJECTED
                || (level == VIEW_ACCEPTED && cn.review != REVIEW_ACCEPTED)
            {
                continue;
            }
            if post.is_draft() {
                nodes.insert(post.id.clone(), 0); // the draft the flow holds (ADR 0079): version 0
                continue;
            }
            let n = tx.node(ctx, post).await?;
            if n.deleted {
                nodes.remove(&n.id);
            } else {
                nodes.insert(n.id.clone(), n.version);
            }
        }
        Ok(Baseline {
            name: format!("{} ({level})", c.title),
            namespace: base.namespace.clone(),
            branch: c.branch.clone(),
            parent_id: base.id.clone(),
            change_id: c.id.clone(),
            nodes,
            created_at: base.created_at,
            ..Default::default()
        })
    }
}
