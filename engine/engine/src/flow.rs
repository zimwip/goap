//! Relaunching a step on a flow branch (Go's `flow.go`): what step K and the steps after it produced is marked stale,
//! a new process replans from the state before step K on the branch, and a person adopts or discards it.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::{Arc, LazyLock};

use goap_ctx::Ctx;
use goap_domain::{ChangeItem, Flow, ItemID};
use goap_journal::Record;
use regex::Regex;
use serde_json::json;

use crate::engine::{Engine, obj};
use crate::error::{Error, Result};
use crate::executors::GUIDANCE_TYPE;
use crate::process::{
    Process, STATUS_CLARIFYING, STATUS_COMPLETED, STATUS_FAILED, STATUS_RUNNING, STATUS_STUCK,
    STATUS_SUPERSEDED, STATUS_WAITING, TASK_FLOW,
};

static RELAUNCH_SUFFIX: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"\s*\(relaunch from step \d+\)$").expect("relaunch suffix"));

/// base_title drops the "(relaunch from step N)" suffixes a relaunched run carries.
pub(crate) fn base_title(t: &str) -> String {
    let mut t = t.to_string();
    while RELAUNCH_SUFFIX.is_match(&t) {
        t = RELAUNCH_SUFFIX.replace_all(&t, "").to_string();
    }
    t
}

fn event_of(p: &Process) -> String {
    if p.status == STATUS_RUNNING {
        "step".into()
    } else {
        p.status.to_string()
    }
}

impl Engine {
    /// relaunch restarts a run from step `step`: it opens a flow branch on the change and returns the new process
    /// (status running, not scheduled yet: the caller runs it). Guidance is a comment for the agent, recorded on the
    /// branch.
    pub async fn relaunch(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        step: usize,
        reason: &str,
        guidance: &str,
    ) -> Result<Process> {
        let _u = self.lock(id).await;
        self.relaunch_locked(ctx, id, step, reason, guidance).await
    }

    /// relaunch_locked is relaunch with the lock of the process already held.
    pub(crate) async fn relaunch_locked(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        step: usize,
        reason: &str,
        guidance: &str,
    ) -> Result<Process> {
        let old = self.store.get(ctx, id).await?;
        if old.change_id.is_empty() {
            return Err(Error::invalid_state(format!("process {id} has no change")));
        }
        if !old.parent_id.is_empty() {
            return Err(Error::invalid_state(format!(
                "process {id} is a sub-agent run: relaunch the step of its parent"
            )));
        }
        if old.status == STATUS_RUNNING
            || old.status == STATUS_CLARIFYING
            || old.status == STATUS_SUPERSEDED
        {
            return Err(Error::invalid_state(format!(
                "process {id} is {}",
                old.status
            )));
        }
        if step >= old.steps.len() {
            return Err(Error::invalid_state(format!(
                "process {id} has no step {step}"
            )));
        }
        let (seeds, execs) = self.relaunched_items(ctx, &old, step).await?;
        let mut who = goap_authz::principal_from(ctx);
        if who.anonymous() {
            who = old.initiator.clone();
        }
        let mut req = goap_graphapi::OpenFlowRequest {
            parent: old.flow.clone(),
            fork_after: old.steps[step].last_item.clone(),
            seeds,
            stale_runs: execs,
            origin: obj(
                json!({"step": step, "execution": old.steps[step].execution, "process": old.id, "reason": reason}),
            ),
            ..Default::default()
        };
        if !guidance.is_empty() {
            let by = if who.subject.is_empty() {
                "human".to_string()
            } else {
                who.subject.clone()
            };
            req.items = vec![ChangeItem {
                r#type: goap_domain::TYPE_ARTIFACT,
                target: GUIDANCE_TYPE.into(),
                produced_by: by,
                data: obj(json!({"text": guidance, "step": step})),
                ..Default::default()
            }];
        }
        let flow = self.graph.open_flow(ctx, &old.change_id, req).await?;
        let now = self.clock();
        let mut p = Process {
            id: uuid::Uuid::new_v4().to_string(),
            methodology: old.methodology.clone(),
            methodology_version: old.methodology_version.clone(),
            agent: old.agent.clone(),
            planner: old.planner.clone(),
            goal: old.goal.clone(),
            change_id: old.change_id.clone(),
            baseline_id: old.baseline_id.clone(),
            namespace: old.namespace.clone(),
            title: format!(
                "{} (relaunch from step {})",
                base_title(&old.title),
                step + 1
            ),
            trigger: old.trigger.clone(),
            initiator: who.clone(),
            vars: old.vars.clone(),
            disabled: BTreeMap::new(),
            status: STATUS_RUNNING.into(),
            flow: flow.id.clone(),
            relaunch_of: old.id.clone(),
            from_step: step as i64,
            created_at: now,
            updated_at: now,
            intent: old.intent.clone(),
            ..Default::default()
        };
        let cause =
            obj(json!({"process": old.id, "fromStep": step, "flow": flow.id, "why": reason}));
        self.queue(
            &goap_authz::with_principal(ctx, who),
            &mut p,
            "relaunched",
            Some(cause),
        );
        self.store.put(ctx, &p).await?;
        tracing::info!(process = %old.id, step, flow = %flow.id, stale = flow.stale.len(), new = %p.id, "step relaunched");
        Ok(p)
    }

    /// relaunched_items are the items the step and what followed it produced on the flow of the process.
    async fn relaunched_items(
        &self,
        ctx: &Ctx,
        old: &Process,
        step: usize,
    ) -> Result<(Vec<ItemID>, Vec<String>)> {
        let mut execs: BTreeSet<String> = old.steps[step..]
            .iter()
            .filter(|s| !s.execution.is_empty())
            .map(|s| s.execution.clone())
            .collect();
        let recs = goap_journal::read(
            ctx,
            &*self.graph,
            &goap_journal::Filter {
                change_id: old.change_id.clone(),
                ..Default::default()
            },
        )
        .await?;
        let since = old.steps[step].started_at;
        let mut desc: BTreeSet<String> = BTreeSet::from([old.id.clone()]);
        let mut changed = true;
        while changed {
            changed = false;
            for r in &recs {
                let after = r.started_at.is_some_and(|t| t >= since);
                if !r.parent_process_id.is_empty()
                    && desc.contains(&r.parent_process_id)
                    && !desc.contains(&r.process_id)
                    && after
                {
                    desc.insert(r.process_id.clone());
                    changed = true;
                }
            }
        }
        for r in &recs {
            if r.process_id != old.id
                && desc.contains(&r.process_id)
                && r.kind == goap_journal::KIND_ACTION
            {
                execs.insert(r.id.clone());
            }
        }
        let bb = self
            .graph
            .blackboard_in(ctx, &old.change_id, &old.flow)
            .await?;
        let seeds = bb
            .change
            .items
            .iter()
            .filter(|it| !it.execution.is_empty() && execs.contains(&it.execution))
            .map(|it| it.id.clone())
            .collect();
        Ok((seeds, execs.into_iter().collect()))
    }

    /// decide_flow adopts or discards the flow branch of a relaunched process.
    pub async fn decide_flow(
        self: &Arc<Self>,
        ctx: &Ctx,
        id: &str,
        adopt: bool,
        comment: &str,
    ) -> Result<Process> {
        let _u = self.lock(id).await;
        let mut p = self.store.get(ctx, id).await?;
        if p.flow.is_empty() {
            return Err(Error::invalid_state(format!(
                "process {id} does not run on a flow branch"
            )));
        }
        let by = goap_authz::principal_from(ctx).subject;
        let mut decision = "discarded";
        if adopt {
            let goal_reached = p.status == STATUS_WAITING
                && p.pending.as_ref().is_some_and(|t| t.kind == TASK_FLOW);
            if !goal_reached && p.status != STATUS_FAILED && p.status != STATUS_STUCK {
                return Err(Error::invalid_state(format!(
                    "process {id} is {}: not ready to adopt on flow {}",
                    p.status, p.flow
                )));
            }
            self.graph
                .adopt_flow(ctx, &p.change_id, &p.flow, &by)
                .await?;
            decision = "adopted";
            if !p.relaunch_of.is_empty() {
                let of = p.relaunch_of.clone();
                self.supersede(ctx, &of).await?;
            }
            if goal_reached {
                p.status = STATUS_COMPLETED.into();
            }
        } else {
            self.graph
                .discard_flow(ctx, &p.change_id, &p.flow, &by)
                .await?;
            p.status = STATUS_SUPERSEDED.into();
        }
        p.pending = None;
        self.resume_waiting(ctx, &p, !adopt).await;
        let rec = Record {
            kind: goap_journal::KIND_APPROVAL.into(),
            step: p.steps.len() as i64,
            action: "flow".into(),
            actor: by,
            data: obj(
                json!({"flow": p.flow, "decision": decision, "comment": comment, "relaunchOf": p.relaunch_of, "fromStep": p.from_step}),
            ),
            ..Default::default()
        };
        self.journal(ctx, &mut p, vec![rec]).await;
        let ev = event_of(&p);
        self.save(ctx, &mut p, &ev).await?;
        Ok(p)
    }

    /// supersede marks the run replaced by an adopted flow.
    async fn supersede(self: &Arc<Self>, ctx: &Ctx, id: &str) -> Result<()> {
        let _u = self.lock(id).await;
        let mut old = match self.store.get(ctx, id).await {
            Err(e) if e.is_not_found() => return Ok(()),
            Err(e) => return Err(e),
            Ok(o) => o,
        };
        if old.status == STATUS_SUPERSEDED {
            return Ok(());
        }
        old.pending = None;
        old.status = STATUS_SUPERSEDED.into();
        let ev = event_of(&old);
        self.save(ctx, &mut old, &ev).await
    }

    /// flows_of returns the flow branches of the change of a process (for display).
    pub async fn flows_of(&self, ctx: &Ctx, id: &str) -> Result<Vec<Flow>> {
        let p = self.store.get(ctx, id).await?;
        let bb = self.graph.blackboard(ctx, &p.change_id).await?;
        Ok(bb.change.flows())
    }
}
