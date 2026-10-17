//! The consistency of the blackboard as a process on a flow sees it.

use std::collections::HashMap;

use goap_ctx::Ctx;
use goap_domain::{BoardIssue, Change, ChangeID, ISSUE_ERROR, ISSUE_WARNING, ItemID, TYPE_FLOW};

use crate::flownodes::FlowNodes;
use crate::repo::Tx;
use crate::{Graph, Result};

impl Graph {
    /// ValidateBoard checks the consistency of the blackboard as a process on a flow sees it: every fact of the view
    /// is structurally valid, the facts it builds on exist and are in effect, and the change impacts it holds are
    /// coherent (their pre version is in the reference baseline, they are not waiting to be re-checked). The issues
    /// come in log order; each names the item (or change impact) to blame (the culprit). Only the outdated change
    /// impacts are warnings: they are fixed by re-checking the impact, not by relaunching a step.
    pub async fn validate_board(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<Vec<BoardIssue>> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        let flow = c.resolve_flow(flow);
        let out = self.validate_board_tx(ctx, &mut tx, c, &flow).await?;
        tx.commit(ctx).await?;
        Ok(out)
    }

    pub(crate) async fn validate_board_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: Change,
        flow: &str,
    ) -> Result<Vec<BoardIssue>> {
        let v = c.view(flow);
        let base = tx.baseline(ctx, &c.baseline_id).await?;
        let nodes = FlowNodes::new(c, flow).nodes(ctx, tx).await?;
        let by_id: HashMap<ItemID, ()> = v.items.iter().map(|it| (it.id.clone(), ())).collect();
        let mut out: Vec<BoardIssue> = Vec::new();
        let mut add = |item: &ItemID, culprit: Option<&ItemID>, code: &str, msg: String| {
            let culprit = culprit.unwrap_or(item).clone();
            // relaunching a step cannot fix an outdated impact (the reference baseline stays the same): the impact
            // is re-checked, so it is reported without blocking the run
            let severity = if code == "outdated" {
                ISSUE_WARNING
            } else {
                ISSUE_ERROR
            };
            out.push(BoardIssue {
                item: item.clone(),
                culprit,
                code: code.to_string(),
                message: msg,
                severity: severity.to_string(),
            });
        };
        for it in &v.items {
            if it.r#type == TYPE_FLOW || !v.in_effect(&it.id) {
                continue;
            }
            if let Err(e) = self.check_item(it) {
                add(&it.id, None, "structure", e);
            }
            // provenance and structure references
            let mut refs: Vec<ItemID> = it.derived_from.clone();
            if let Some(d) = &it.decision {
                // a decision may target an item that is out of effect (it is what rejected it): it only has to exist
                if !by_id.contains_key(&d.item) {
                    add(
                        &it.id,
                        None,
                        "dangling",
                        format!("decides on item {}, which is not on the blackboard", d.item),
                    );
                }
            }
            refs.dedup();
            for r in &refs {
                if !by_id.contains_key(r) {
                    add(
                        &it.id,
                        None,
                        "dangling",
                        format!("refers to item {r}, which is not on the blackboard"),
                    );
                } else if !v.in_effect(r) {
                    add(
                        &it.id,
                        Some(r),
                        "derived_from_invalid",
                        format!("builds on item {r}, which is {}", v.effective_status(r)),
                    );
                }
            }
        }
        // the change impacts: the id of a change impact stands where an item id does
        for cn in &nodes {
            let id = ItemID::new(cn.id.as_str());
            if let Err(e) = cn.validate() {
                add(&id, None, "structure", e.to_string());
            }
            if let Some(pre) = &cn.pre
                && !base.contains(pre)
                && !cn.recheck
            {
                add(
                    &id,
                    None,
                    "reference",
                    format!("refers to {pre} which is not in the reference baseline"),
                );
            }
            if cn.recheck {
                add(
                    &id,
                    None,
                    "outdated",
                    format!(
                        "the impact on {} was written against a version that is no longer the head",
                        cn.key
                    ),
                );
            }
        }
        // log order: the facts, then the change impacts
        let pos: HashMap<&ItemID, usize> = v
            .items
            .iter()
            .enumerate()
            .map(|(i, it)| (&it.id, i))
            .collect();
        out.sort_by_key(|x| pos.get(&x.item).copied().unwrap_or(0));
        Ok(out)
    }
}
