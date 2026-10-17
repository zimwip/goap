//! Unblock decisions (ADR 0036 §3, Go's `unblock.go`): a run waiting for conditions established outside it, or stuck,
//! is never left without a way out. Nobody declares a condition established: a person establishes it by working on
//! the change (ADR 0109 §6).

use std::collections::BTreeMap;
use std::sync::Arc;

use goap_authz::Principal;
use goap_ctx::Ctx;
use goap_goap::{Action as Op, Goal, WorldState};
use goap_journal::Record;
use serde_json::json;

use crate::engine::{Engine, obj};
use crate::error::{Error, Result};
use crate::process::{Process, STATUS_FAILED, STATUS_RUNNING, STATUS_STUCK};

/// UNBLOCK_RETRY tries again the actions the run gave up on.
pub const UNBLOCK_RETRY: &str = "retry";
/// UNBLOCK_ABANDON ends the run: it fails, with the reason.
pub const UNBLOCK_ABANDON: &str = "abandon";
/// PERMISSION_UNBLOCK lets a principal unblock runs beyond those who answer for a run.
pub const PERMISSION_UNBLOCK: &str = "process:unblock";

/// UnblockRequest is a person's decision on a blocked run.
#[derive(Debug, Clone, Default)]
pub struct UnblockRequest {
    pub decision: String,
    pub reason: String,
}

impl Engine {
    /// unblock decides on a run that waits for conditions established outside it, or is stuck. Call run (or
    /// schedule) afterwards to continue.
    pub async fn unblock(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        req: UnblockRequest,
    ) -> Result<Process> {
        let _u = self.lock(id).await;
        let mut p = self.store.get(ctx, id).await?;
        if p.status != STATUS_STUCK && !p.waits_for_conditions() {
            return Err(Error::invalid_state(format!(
                "process {id} is neither stuck nor waiting for conditions"
            )));
        }
        let who = goap_authz::principal_from(ctx);
        if !self.may_unblock(ctx, &p, &who).await? {
            return Err(Error::forbidden(format!(
                "{:?} does not answer for process {id}",
                who.subject
            )));
        }
        let reason = req.reason.trim().to_string();
        let mut data = obj(
            json!({"decision": req.decision, "reason": reason, "status": p.status.to_string()}),
        );
        match req.decision.as_str() {
            UNBLOCK_RETRY => {
                let disabled: Vec<String> = p.disabled.keys().cloned().collect();
                data.insert("disabled".into(), json!(disabled));
                p.disabled = BTreeMap::new();
                let rec = Record {
                    kind: goap_journal::KIND_UNBLOCK.into(),
                    step: p.steps.len() as i64,
                    actor: who.subject.clone(),
                    data,
                    ..Default::default()
                };
                self.journal(ctx, &mut p, vec![rec]).await;
            }
            UNBLOCK_ABANDON => {
                if reason.is_empty() {
                    return Err(Error::invalid_state("abandoning a run gives its reason"));
                }
                let rec = Record {
                    kind: goap_journal::KIND_UNBLOCK.into(),
                    step: p.steps.len() as i64,
                    actor: who.subject.clone(),
                    data,
                    ..Default::default()
                };
                self.journal(ctx, &mut p, vec![rec]).await;
                p.pending = None;
                p.plan = Vec::new();
                p.status = STATUS_FAILED.into();
                p.error = format!("abandoned by {}: {reason}", who.subject);
                self.save(ctx, &mut p, STATUS_FAILED).await?;
                return Ok(p);
            }
            other => {
                return Err(Error::invalid_state(format!(
                    "unknown decision {other:?} (retry or abandon)"
                )));
            }
        }
        p.status = STATUS_RUNNING.into();
        p.error = String::new();
        p.plan = Vec::new();
        p.pending = None;
        let cause = obj(json!({"decision": req.decision, "by": who.subject}));
        self.queue(ctx, &mut p, "unblocked", Some(cause));
        self.save(ctx, &mut p, "step").await?;
        Ok(p)
    }

    /// may_unblock reports whether who answers for the run p.
    pub(crate) async fn may_unblock(
        &self,
        ctx: &Ctx,
        p: &Process,
        who: &Principal,
    ) -> Result<bool> {
        // the initiator of the run, or of a run above it (the owner of the work)
        let mut q = p.clone();
        for _ in 0..16 {
            if !who.subject.is_empty() && q.initiator.subject == who.subject {
                return Ok(true);
            }
            if q.parent_id.is_empty() {
                break;
            }
            match self.store.get(ctx, &q.parent_id).await {
                Ok(parent) => q = parent,
                Err(_) => break,
            }
        }
        // the accountable role of the step the run carries out
        if let Some(sc) = &p.step
            && sc.roles.as_ref().is_some_and(|r| !r.accountable.is_empty())
        {
            let ok = self.step_allowed(ctx, p, who, Some(sc), "approve").await?;
            if ok {
                return Ok(true);
            }
        }
        self.allowed(ctx, p, who, PERMISSION_UNBLOCK).await
    }

    /// unblocking are the conditions that would unblock a stuck process once the change establishes them.
    pub(crate) fn unblocking(
        &self,
        world: &WorldState,
        admissible: &[Op],
        goal: &Goal,
    ) -> Vec<String> {
        let out = self.awaited(world, admissible, admissible, goal);
        if !out.is_empty() {
            return out;
        }
        let mut out: Vec<String> = goal
            .pre
            .iter()
            .filter(|(k, v)| world.0.get(*k) != Some(*v))
            .map(|(k, v)| if *v { k.clone() } else { format!("!{k}") })
            .collect();
        out.sort();
        out
    }
}
