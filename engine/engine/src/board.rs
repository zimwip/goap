//! Blackboard consistency (Go's `board.go`): a process asks for no action while the blackboard it reads has errors;
//! a person relaunches the step at fault or ignores the issues.

use std::collections::{BTreeSet, HashMap};
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::{Blackboard, BoardIssue, Change, ItemID};
use goap_journal::Record;
use serde_json::json;
use sha2::{Digest, Sha256};

use crate::engine::{Engine, obj};
use crate::error::{Error, Result};
use crate::process::{
    HumanTask, Process, RelaunchProposal, STATUS_CLARIFYING, STATUS_RUNNING, STATUS_SUPERSEDED,
    STATUS_WAITING, TASK_BOARD, TASK_RELAUNCHED,
};

/// issues_key identifies a set of blackboard issues: a human who chose to ignore them is not asked again until the
/// set changes.
pub(crate) fn issues_key(is: &[BoardIssue]) -> String {
    let mut keys: Vec<String> = is
        .iter()
        .map(|x| format!("{}|{}|{}", x.item, x.culprit, x.code))
        .collect();
    keys.sort();
    let sum = Sha256::digest(keys.join("\n").as_bytes());
    sum[..8].iter().map(|b| format!("{b:02x}")).collect()
}

impl Engine {
    /// check_board validates the blackboard the process reads before it asks for an action. On errors the process
    /// waits (TASK_BOARD) with the issues and the earliest step to relaunch. It reports whether the process is
    /// blocked. A validation failure never blocks the process.
    pub(crate) async fn check_board(
        &self,
        ctx: &Ctx,
        p: &mut Process,
        bb: &Blackboard,
    ) -> Result<bool> {
        if p.change_id.is_empty() {
            return Ok(false);
        }
        let all = match self.graph.validate_board(ctx, &p.change_id, &p.flow).await {
            Ok(a) => a,
            Err(err) => {
                tracing::warn!(process = %p.id, err = %err, "blackboard validation failed");
                return Ok(false);
            }
        };
        let errs: Vec<BoardIssue> = all
            .into_iter()
            .filter(|i| i.severity == goap_domain::ISSUE_ERROR)
            .collect();
        if errs.is_empty()
            || p.dismissed
                .get(&issues_key(&errs))
                .copied()
                .unwrap_or(false)
        {
            return Ok(false);
        }
        let prop = self.propose_relaunch(ctx, p, &bb.change, &errs).await;
        let mut desc = format!(
            "The blackboard is inconsistent ({} issue(s)): {}",
            errs.len(),
            errs[0].message
        );
        if let Some(prop) = &prop {
            desc += &format!(
                ". Restart from step {} ({}) of process {}.",
                prop.step + 1,
                prop.action,
                prop.process
            );
        }
        p.status = STATUS_WAITING.into();
        p.plan = Vec::new();
        p.pending = Some(HumanTask {
            kind: TASK_BOARD.into(),
            action: "validate_board".into(),
            step: p.steps.len() as i64,
            description: desc,
            issues: errs.clone(),
            proposal: prop.clone(),
            ..Default::default()
        });
        let rec = Record {
            kind: goap_journal::KIND_TICK.into(),
            step: p.steps.len() as i64,
            data: obj(json!({"boardInvalid": errs.len(), "issues": errs, "proposal": prop})),
            ..Default::default()
        };
        self.journal(ctx, p, vec![rec]).await;
        Ok(true)
    }

    /// propose_relaunch finds the earliest step, over every run of the change, that produced content at fault.
    async fn propose_relaunch(
        &self,
        ctx: &Ctx,
        p: &Process,
        view: &Change,
        issues: &[BoardIssue],
    ) -> Option<RelaunchProposal> {
        let exec_of: HashMap<&ItemID, &str> = view
            .items
            .iter()
            .map(|it| (&it.id, it.execution.as_str()))
            .collect();
        let procs = self.store.list(ctx).await.ok()?;
        let mut by_id: HashMap<&str, &Process> = HashMap::new();
        let mut by_exec: HashMap<&str, (&Process, usize)> = HashMap::new();
        for q in &procs {
            if q.change_id != p.change_id {
                continue;
            }
            by_id.insert(&q.id, q);
            for (i, s) in q.steps.iter().enumerate() {
                if !s.execution.is_empty() {
                    by_exec.insert(&s.execution, (q, i));
                }
            }
        }
        let mut best: Option<RelaunchProposal> = None;
        let mut best_at = 0i64;
        let mut culprits: BTreeSet<ItemID> = BTreeSet::new();
        for is in issues {
            let exec = exec_of.get(&is.culprit).copied().unwrap_or_default();
            let Some(&(q, i)) = by_exec.get(exec) else {
                continue;
            };
            let (mut top, mut idx) = (Some(q), i);
            while let Some(t) = top {
                if t.parent_id.is_empty() {
                    break;
                }
                // a sub-agent step: restart the step of the parent that started it
                let Some(parent) = by_id.get(t.parent_id.as_str()) else {
                    top = None;
                    break;
                };
                match parent
                    .steps
                    .iter()
                    .position(|st| st.children.contains(&t.id))
                {
                    Some(j) => {
                        top = Some(parent);
                        idx = j;
                    }
                    None => {
                        top = None;
                        break;
                    }
                }
            }
            let Some(top) = top else {
                continue;
            };
            if top.status == STATUS_SUPERSEDED
                || top.status == STATUS_CLARIFYING
                || (top.status == STATUS_RUNNING && top.id != p.id)
            {
                continue;
            }
            let at = top.steps[idx]
                .started_at
                .timestamp_nanos_opt()
                .unwrap_or_default();
            if best.is_none() || at < best_at {
                best = Some(RelaunchProposal {
                    process: top.id.clone(),
                    step: idx as i64,
                    action: top.steps[idx].action.clone(),
                    ..Default::default()
                });
                best_at = at;
                culprits.clear();
            }
            if let Some(b) = &best
                && b.process == top.id
                && b.step == idx as i64
            {
                culprits.insert(is.culprit.clone());
            }
        }
        let mut best = best?;
        best.culprits = culprits.into_iter().collect();
        best.reason = format!("blackboard inconsistent: {}", issues[0].message);
        Some(best)
    }

    /// resolve_board answers a blackboard inconsistency: relaunch the proposed step (returns the new process, to run)
    /// or ignore the issues and go on (the process is running again, to run).
    pub async fn resolve_board(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        relaunch: bool,
        comment: &str,
    ) -> Result<(Process, Option<Process>)> {
        let _u = self.lock(id).await;
        let mut p = self.store.get(ctx, id).await?;
        if p.status != STATUS_WAITING || p.pending.as_ref().is_none_or(|t| t.kind != TASK_BOARD) {
            return Err(Error::invalid_state(format!(
                "process {id} has no blackboard issue pending"
            )));
        }
        let pend = p.pending.clone().unwrap_or_default();
        let by = goap_authz::principal_from(ctx).subject;
        if !relaunch {
            p.dismissed.insert(issues_key(&pend.issues), true);
            p.pending = None;
            p.status = STATUS_RUNNING.into();
            let cause = obj(json!({"issues": pend.issues.len()}));
            self.queue(ctx, &mut p, "board-ignored", Some(cause));
            let rec = Record {
                kind: goap_journal::KIND_APPROVAL.into(),
                step: pend.step,
                action: "validate_board".into(),
                actor: by,
                data: obj(
                    json!({"decision": "ignored", "comment": comment, "issues": pend.issues.len()}),
                ),
                ..Default::default()
            };
            self.journal(ctx, &mut p, vec![rec]).await;
            self.save(ctx, &mut p, "step").await?;
            return Ok((p, None));
        }
        let Some(prop) = pend.proposal.clone() else {
            return Err(Error::invalid_state(format!(
                "no step can be relaunched to fix the blackboard of process {id}"
            )));
        };
        let np = if prop.process == p.id {
            self.relaunch_locked(ctx, &p.id, prop.step as usize, &prop.reason, comment)
                .await?
        } else {
            self.relaunch(
                ctx,
                &prop.process,
                prop.step as usize,
                &prop.reason,
                comment,
            )
            .await?
        };
        p.pending = Some(HumanTask {
            kind: TASK_RELAUNCHED.into(),
            action: "validate_board".into(),
            step: pend.step,
            issues: pend.issues.clone(),
            flow_id: np.flow.clone(),
            description: format!(
                "Relaunched from step {} ({}) as process {}: waiting for the decision on flow {}.",
                prop.step, prop.action, np.id, np.flow
            ),
            ..Default::default()
        });
        let rec = Record {
            kind: goap_journal::KIND_APPROVAL.into(),
            step: pend.step,
            action: "validate_board".into(),
            actor: by,
            data: obj(
                json!({"decision": "relaunched", "comment": comment, "process": prop.process, "fromStep": prop.step, "flow": np.flow}),
            ),
            ..Default::default()
        };
        self.journal(ctx, &mut p, vec![rec]).await;
        self.save(ctx, &mut p, "step").await?;
        Ok((p, Some(np)))
    }

    /// resume_waiting resumes the processes that waited for the decision on p's flow to fix their blackboard.
    pub(crate) async fn resume_waiting(self: &Arc<Self>, ctx: &Ctx, p: &Process, discarded: bool) {
        let Ok(procs) = self.store.list(ctx).await else {
            return;
        };
        for q in procs {
            let waits = q
                .pending
                .as_ref()
                .is_some_and(|t| t.kind == TASK_RELAUNCHED && t.flow_id == p.flow);
            if q.id == p.id || q.change_id != p.change_id || q.status != STATUS_WAITING || !waits {
                continue;
            }
            let _u = self.lock(&q.id).await;
            let Ok(mut cur) = self.store.get(ctx, &q.id).await else {
                continue;
            };
            if cur.status != STATUS_WAITING
                || cur
                    .pending
                    .as_ref()
                    .is_none_or(|t| t.kind != TASK_RELAUNCHED)
            {
                continue;
            }
            if discarded {
                let k = issues_key(
                    &cur.pending
                        .as_ref()
                        .map(|t| t.issues.clone())
                        .unwrap_or_default(),
                );
                cur.dismissed.insert(k, true);
            }
            cur.pending = None;
            cur.status = STATUS_RUNNING.into();
            let cause = obj(json!({"flow": p.flow}));
            self.queue(
                ctx,
                &mut cur,
                if discarded {
                    "flow-discarded"
                } else {
                    "flow-adopted"
                },
                Some(cause),
            );
            if let Err(err) = self.save(ctx, &mut cur, "step").await {
                tracing::warn!(process = %cur.id, err = %err, "resume after flow decision");
                continue;
            }
            self.schedule_run(&cur.id);
        }
    }
}
