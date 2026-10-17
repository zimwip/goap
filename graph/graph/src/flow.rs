//! The flow branches of a change (ADR 0017, 0025). A flow is opened when a step restarts from a
//! point of the change; the items it invalidated are stale until it is adopted or discarded.

use std::collections::HashSet;

use goap_ctx::Ctx;
use goap_domain::{
    CHANGE_ACTIVE, CHANGE_DRAFT, Change, ChangeID, ChangeItem, FLOW_ADOPT_OP, FLOW_ADOPTED,
    FLOW_DISCARD_OP, FLOW_DISCARDED, FLOW_OPEN, FLOW_OPEN_OP, Flow, FlowEvent, ITEM_ACCEPTED,
    ItemID, TYPE_DECISION_POINT, TYPE_FLOW, TYPE_TRANSITION, stale_closure,
};

use crate::api::OpenFlowRequest;
use crate::changelog::put_item;
use crate::lifecycle::invalidf;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// flowEvent appends a flow event to the log of a change.
    pub(crate) async fn flow_event(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        e: FlowEvent,
    ) -> Result<()> {
        put_item(
            ctx,
            tx,
            id,
            &ChangeItem {
                id: ItemID::new(self.new_id()),
                r#type: TYPE_FLOW,
                target: format!("flow.{}", e.op),
                status: ITEM_ACCEPTED,
                produced_by: "graph.flow".into(),
                flow_event: Some(e),
                created_at: self.now(),
                ..Default::default()
            },
        )
        .await
    }

    /// OpenFlow opens a flow branch: the step restarts from fork_after, the items it invalidated (the closure of seeds
    /// over the provenance of the parent's view) become stale until the branch is adopted. A change may have several
    /// open flows: parallel alternatives, decided one by one.
    pub async fn open_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        input: OpenFlowRequest,
    ) -> Result<Flow> {
        let mut tx = self.change_tx(ctx, id).await?;
        let mut c = flow_change(ctx, &mut tx, id).await?;
        if !input.parent.is_empty() && c.flow_status_of(&input.parent) == FLOW_DISCARDED {
            return Err(invalidf(format!("flow {} is discarded", input.parent)));
        }
        let base = c.view(&input.parent);
        let in_view: HashSet<ItemID> = base.items.iter().map(|it| it.id.clone()).collect();
        if !input.fork_after.is_empty() && !in_view.contains(&input.fork_after) {
            return Err(invalidf(format!(
                "fork point {} is not on the flow {:?}",
                input.fork_after, input.parent
            )));
        }
        for s in &input.seeds {
            if !in_view.contains(s) {
                return Err(invalidf(format!(
                    "item {s} is not on the flow {:?}",
                    input.parent
                )));
            }
        }
        let flow = self.new_id();
        let stale = stale_closure(&base.items, &input.seeds);
        self.flow_event(
            ctx,
            &mut tx,
            id,
            FlowEvent {
                op: FLOW_OPEN_OP.into(),
                flow: flow.clone(),
                parent: input.parent.clone(),
                fork_after: input.fork_after.clone(),
                stale,
                stale_runs: input.stale_runs.clone(),
                origin: input.origin.clone(),
                ..Default::default()
            },
        )
        .await?;
        for mut it in input.items {
            if it.r#type == TYPE_FLOW
                || it.r#type == TYPE_TRANSITION
                || it.r#type == TYPE_DECISION_POINT
            {
                return Err(invalidf(format!(
                    "a {} item is recorded by its own operation, not at the opening of a flow",
                    it.r#type
                )));
            }
            it.id = ItemID::new(self.new_id());
            it.flow = flow.clone();
            it.created_at = self.now();
            if it.status.is_empty() {
                it.status = ITEM_ACCEPTED;
            }
            self.check_item(&it)
                .map_err(|e| invalidf(format!("item {}: {e}", it.id)))?;
            put_item(ctx, &mut tx, id, &it).await?;
        }
        if c.status == CHANGE_DRAFT {
            c.status = CHANGE_ACTIVE;
            tx.put_change(ctx, c).await?;
        }
        let full = tx.change(ctx, id).await?;
        let f = full.flow(&flow).unwrap_or_default();
        tx.commit(ctx).await?;
        Ok(f)
    }

    async fn decide_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        op: &str,
        by: &str,
    ) -> Result<Flow> {
        let mut tx = self.begin(ctx).await?;
        let c = flow_change(ctx, &mut tx, id).await?;
        self.decide_flow_tx(ctx, &mut tx, &c, flow, op, by).await?;
        let full = tx.change(ctx, id).await?;
        let f = full.flow(flow).unwrap_or_default();
        tx.commit(ctx).await?;
        Ok(f)
    }

    /// decideFlowTx adopts or discards an open flow of c.
    pub(crate) async fn decide_flow_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        flow: &str,
        op: &str,
        by: &str,
    ) -> Result<()> {
        let Some(cur) = c.flow(flow) else {
            return Err(Error::wrap(ErrorKind::NotFound, format!("flow {flow}")));
        };
        if cur.status != FLOW_OPEN {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("flow {flow} is {}", cur.status),
            ));
        }
        if op == FLOW_ADOPT_OP {
            if !cur.parent.is_empty() && c.flow_status_of(&cur.parent) != FLOW_ADOPTED {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("flow {flow}: its parent {} is not adopted", cur.parent),
                ));
            }
            if let Some(other) = cur.competes_with.first() {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!(
                        "flow {flow} competes with the adopted flow {other} (it replaces the same items): relaunch or discard it"
                    ),
                ));
            }
            self.adopt_nodes(ctx, tx, c, &cur, by).await?;
        } else if op == FLOW_DISCARD_OP {
            self.discard_nodes(ctx, tx, c, &cur, by).await?;
        }
        self.flow_event(
            ctx,
            tx,
            &c.id,
            FlowEvent {
                op: op.to_string(),
                flow: flow.to_string(),
                by: by.to_string(),
                ..Default::default()
            },
        )
        .await
    }

    /// AdoptFlow adopts an open flow branch: its items count, the items it invalidated become superseded.
    pub async fn adopt_flow(&self, ctx: &Ctx, id: &ChangeID, flow: &str, by: &str) -> Result<Flow> {
        self.decide_flow(ctx, id, flow, FLOW_ADOPT_OP, by).await
    }

    /// DiscardFlow discards an open flow branch: its items are rejected, the items it marked stale count again.
    pub async fn discard_flow(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        by: &str,
    ) -> Result<Flow> {
        self.decide_flow(ctx, id, flow, FLOW_DISCARD_OP, by).await
    }

    /// Flows lists the flow branches of a change.
    pub async fn flows(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<Flow>> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        tx.commit(ctx).await?;
        Ok(c.flows())
    }
}

/// flowChange reads a change a flow operation works on: draft or active.
pub(crate) async fn flow_change(ctx: &Ctx, tx: &mut dyn Tx, id: &ChangeID) -> Result<Change> {
    let c = tx.change(ctx, id).await?;
    if c.status != CHANGE_DRAFT && c.status != CHANGE_ACTIVE {
        return Err(Error::wrap(
            ErrorKind::Conflict,
            format!("change {id} is {}", c.status),
        ));
    }
    Ok(c)
}

/// openFlows are the flow branches still waiting for a decision.
pub(crate) fn open_flows(c: &Change) -> Vec<Flow> {
    c.flows()
        .into_iter()
        .filter(|f| f.status == FLOW_OPEN)
        .collect()
}

/// firstNonEmpty is the first of the strings that is not empty.
pub(crate) fn first_non_empty(ss: &[&str]) -> String {
    ss.iter()
        .find(|s| !s.is_empty())
        .map(|s| s.to_string())
        .unwrap_or_default()
}
