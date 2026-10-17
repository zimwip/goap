//! Port of `rebase.go`: the conflicts a rebase left to settle (ADR 0101).

use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::{ChangeID, REVIEW_PROPOSED};
use goap_journal::Record;
use serde_json::json;

use crate::engine::{Engine, obj};
use crate::error::{Error, Result};
use crate::ports::GraphPort;
use crate::process::{
    Process, RebaseConflict, RebaseTask, STATUS_RUNNING, STATUS_WAITING, TASK_REBASE,
};

impl RebaseTask {
    /// target names what the change was rebased onto, for the description of the task.
    pub fn target(&self) -> String {
        match (self.onto.is_empty(), self.branch.is_empty()) {
            (false, false) => format!("{} ({})", self.branch, self.onto),
            (false, true) => self.onto.clone(),
            (true, false) => self.branch.clone(),
            (true, true) => "its target".into(),
        }
    }
}

/// rebase_task_of describes the conflicts the rebase of a change left to settle (ADR 0101); None when there are
/// none (the refusal of the apply is then something else: impacts awaiting their review, for instance).
pub(crate) async fn rebase_task_of(
    ctx: &Ctx,
    g: &dyn GraphPort,
    id: &ChangeID,
) -> Option<RebaseTask> {
    let st = g.rebase_state(ctx, id).await.ok()?;
    if st.conflicts.is_empty() {
        return None;
    }
    let mut t = RebaseTask {
        branch: st.branch.clone(),
        onto: st.onto.to_string(),
        impacts: Vec::new(),
    };
    if !st.parent.is_empty() {
        t.onto = st.parent.to_string();
        if t.branch.is_empty() {
            t.branch = "parent change".into();
        }
    }
    for c in &st.conflicts {
        t.impacts.push(RebaseConflict {
            impact: c.impact.to_string(),
            key: c.key.clone(),
            conflicts: c.conflicts.clone(),
        });
    }
    Some(t)
}

impl Engine {
    /// resolve_rebase continues a process waiting on a TaskRebase once the person settled the conflicts and
    /// reviewed the impacts again (in the change tab); the apply then runs again. It refuses while a conflict or an
    /// impact awaiting its review remains.
    pub async fn resolve_rebase(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        comment: &str,
    ) -> Result<Process> {
        let _u = self.lock(id).await;
        let mut p = self.store.get(ctx, id).await?;
        let impacts = match &p.pending {
            Some(t) if p.status == STATUS_WAITING && t.kind == TASK_REBASE => {
                t.rebase.as_ref().map(|r| r.impacts.len()).unwrap_or(0)
            }
            _ => {
                return Err(Error::invalid_state(format!(
                    "process {id} has no rebase pending"
                )));
            }
        };
        let st = self.graph.rebase_state(ctx, &p.change_id).await?;
        if !st.conflicts.is_empty() {
            let names: Vec<String> = st
                .conflicts
                .iter()
                .map(|c| format!("{} ({})", c.key, c.conflicts.join(", ")))
                .collect();
            return Err(Error::invalid_state(format!(
                "conflicts remain to settle: {}",
                names.join("; ")
            )));
        }
        let ch = self.graph.change(ctx, &p.change_id).await?;
        let open: Vec<String> = ch
            .nodes
            .iter()
            .filter(|cn| cn.flow.is_empty() && !cn.superseded && cn.review == REVIEW_PROPOSED)
            .map(|cn| cn.key.clone())
            .collect();
        if !open.is_empty() {
            return Err(Error::invalid_state(format!(
                "impacts still await their review: {}",
                open.join(", ")
            )));
        }
        let by = goap_authz::principal_from(ctx).subject;
        let pend = p.pending.take().unwrap_or_default();
        let i = pend.step as usize;
        if pend.step >= 0 && i < p.steps.len() {
            p.steps[i].output = format!("rebase settled by {by}");
        }
        p.status = STATUS_RUNNING.into();
        self.queue(
            ctx,
            &mut p,
            "rebased",
            Some(obj(json!({"impacts": impacts}))),
        );
        let rec = Record {
            kind: goap_journal::KIND_APPROVAL.into(),
            step: pend.step,
            action: pend.action.clone(),
            actor: by.clone(),
            data: obj(
                json!({"decision": "rebase-settled", "comment": comment, "impacts": impacts}),
            ),
            ..Default::default()
        };
        self.journal(ctx, &mut p, vec![rec]).await;
        self.save(ctx, &mut p, "step").await?;
        Ok(p)
    }
}
