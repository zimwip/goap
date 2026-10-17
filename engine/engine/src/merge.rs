//! The merge a committed change waits for (ADR 0056).

use std::collections::BTreeMap;
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::{CHANGE_COMMITTED, Change, NodeID};
use goap_graphapi::Resolution;
use goap_journal::Record;
use serde_json::json;

use crate::engine::{Engine, obj};
use crate::error::{Error, Result};
use crate::ports::GraphPort;
use crate::process::{
    MergeConflict, MergeProperty, MergeTask, Process, STATUS_RUNNING, STATUS_WAITING, TASK_MERGE,
};

/// merge_task_of describes the merge a committed change waits for: the properties both sides changed apart, with
/// the value of each side and the one proposed (the change's). The three-way merge settles the rest by itself.
pub(crate) async fn merge_task_of(ctx: &Ctx, g: &dyn GraphPort, c: &Change) -> Result<MergeTask> {
    let br = g.branch(ctx, &c.namespace, &c.branch).await?;
    let plan = g
        .plan_merge(ctx, &c.namespace, &br.name, &br.parent)
        .await?;
    let mut t = MergeTask {
        namespace: goap_domain::namespace_of(&c.namespace).to_string(),
        from: plan.from.clone(),
        into: plan.into.clone(),
        conflicts: Vec::new(),
    };
    for cand in plan.conflicting() {
        let mut mc = MergeConflict {
            node: cand.node.to_string(),
            key: cand.key.clone(),
            deleted: cand.deleted,
            properties: Vec::new(),
        };
        for name in &cand.conflicts {
            let get = |m: &serde_json::Map<String, serde_json::Value>| {
                m.get(name).cloned().unwrap_or_default()
            };
            mc.properties.push(MergeProperty {
                name: name.clone(),
                base: get(&cand.base),
                ours: get(&cand.ours_props),
                theirs: get(&cand.theirs_props),
                proposed: get(&cand.theirs_props),
            });
        }
        t.conflicts.push(mc);
    }
    if t.conflicts.is_empty() {
        return Err(Error::msg(format!(
            "change {} is committed on {} but its merge into {} has no conflict to settle",
            c.id, plan.from, plan.into
        )));
    }
    Ok(t)
}

impl Engine {
    /// resolve_merge settles the conflicts of a TaskMerge (by node id: the properties to keep, or skip the node)
    /// and completes the merge of the change; the process goes on (the caller runs it). Conflicts that remain,
    /// because the target branch moved meanwhile, keep the process waiting with the new ones.
    pub async fn resolve_merge(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        resolutions: BTreeMap<NodeID, Resolution>,
        comment: &str,
    ) -> Result<Process> {
        let _u = self.lock(id).await;
        let mut p = self.store.get(ctx, id).await?;
        let merge = match &p.pending {
            Some(t) if p.status == STATUS_WAITING && t.kind == TASK_MERGE => {
                t.merge.clone().unwrap_or_default()
            }
            _ => {
                return Err(Error::invalid_state(format!(
                    "process {id} has no merge pending"
                )));
            }
        };
        for c in &merge.conflicts {
            if !resolutions.contains_key(&NodeID::new(c.node.clone())) {
                return Err(Error::invalid_state(format!("no resolution for {}", c.key)));
            }
        }
        let by = goap_authz::principal_from(ctx).subject;
        if let Err(err) = self
            .graph
            .integrate_change(ctx, &p.change_id, resolutions)
            .await
        {
            if let Ok(cur) = self.graph.change(ctx, &p.change_id).await
                && cur.status == CHANGE_COMMITTED
            {
                // the target moved: what is left to settle changed
                if let Ok(t) = merge_task_of(ctx, &*self.graph, &cur).await {
                    if let Some(pend) = p.pending.as_mut() {
                        pend.merge = Some(t);
                    }
                    let _ = self.save(ctx, &mut p, "step").await;
                }
            }
            return Err(err.into());
        }
        let pend = p.pending.take().unwrap_or_default();
        let i = pend.step as usize;
        if pend.step >= 0 && i < p.steps.len() {
            p.steps[i].output = format!("merged into {} by {by}", merge.into);
        }
        p.status = STATUS_RUNNING.into();
        self.queue(
            ctx,
            &mut p,
            "merged",
            Some(obj(json!({"into": merge.into}))),
        );
        let rec = Record {
            kind: goap_journal::KIND_APPROVAL.into(),
            step: pend.step,
            action: pend.action.clone(),
            actor: by.clone(),
            data: obj(
                json!({"decision": "merged", "comment": comment, "from": merge.from, "into": merge.into,
                "nodes": merge.conflicts.len()}),
            ),
            ..Default::default()
        };
        self.journal(ctx, &mut p, vec![rec]).await;
        self.save(ctx, &mut p, "step").await?;
        Ok(p)
    }
}
