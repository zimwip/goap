//! One observe / plan / act iteration of a process (Go's `cycle.go`).

use std::sync::Arc;

use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::{Blackboard, Change, ItemID};
use goap_goap::Action as Op;
use goap_journal::Record;
use goap_methodology::{Action, Agent, Compiled, Goal, KIND_HUMAN};
use serde_json::json;

use crate::engine::{Engine, PERMISSION_RUN_ACTION, conversation_of, obj};
use crate::error::{Error, Result};
use crate::journal::{replanned, tick_record_with_calls};
use crate::planners::{operator, operators, planning_goal};
use crate::process::{
    HumanTask, LLMCall, Process, STATUS_COMPLETED, STATUS_RUNNING, STATUS_STUCK, STATUS_WAITING,
    Step, TASK_APPROVAL, TASK_CONDITION, TASK_FLOW, TASK_UNBLOCK,
};
use crate::steps::step_context;

/// planned is the outcome of a planning phase that found a plan: the action to run first and the model calls the
/// planner made.
pub(crate) struct Planned {
    pub(crate) next: String,
    pub(crate) calls: Vec<LLMCall>,
}

/// last_item is the last item of the change that is not a flow.
pub(crate) fn last_item(c: &Change) -> ItemID {
    c.items
        .iter()
        .rev()
        .find(|it| it.r#type != goap_domain::TYPE_FLOW)
        .map(|it| it.id.clone())
        .unwrap_or_default()
}

/// carried leaves out the actions of the automatic review steps (ADR 0130) the run does not carry out.
pub(crate) fn carried(m: &Compiled, p: &Process, all: Vec<Action>) -> Vec<Action> {
    all.into_iter()
        .filter(|a| match m.automatic_review(&a.name) {
            Some(step) => goap_methodology::carries(&p.goal, step),
            None => true,
        })
        .collect()
}

/// new_step is the step an action about to run opens, with the model calls of the planning that chose it.
pub(crate) fn new_step(
    p: &Process,
    bb: &Blackboard,
    action: &Action,
    calls: &[LLMCall],
    now: DateTime<Utc>,
) -> Step {
    let mut step = Step {
        index: p.steps.len() as i64,
        action: action.name.clone(),
        plan: p.plan.clone(),
        before: p.world.clone(),
        started_at: now,
        reads: bb.change.referenced_nodes(),
        board_before: bb.change.items.len() as i64,
        last_item: last_item(&bb.change),
        ..Default::default()
    };
    for c in calls {
        step.llm_calls.push(c.clone());
        step.usage.llm_calls += 1;
        step.usage.input_tokens += c.input_tokens;
        step.usage.output_tokens += c.output_tokens;
    }
    step
}

impl Engine {
    /// cycle runs one observe / plan / act iteration.
    pub(crate) async fn cycle(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        m: &Arc<Compiled>,
    ) -> Result<()> {
        let (mut bb, goal, done) = self.observe_goal(ctx, p, m).await?;
        if done {
            return Ok(());
        }
        // plan with the agent's admissible actions and planner
        let (ag, actions) = self.admissible(ctx, p, m).await?;
        let Some(pl) = self.plan_step(ctx, p, m, &ag, &goal, &bb, &actions).await? else {
            return Ok(());
        };
        // act
        let action = m.action(&pl.next).cloned().unwrap_or_default();
        let mut step = new_step(p, &bb, &action, &pl.calls, self.clock());
        if action.is_scoped() {
            // a scoped action runs per pending subject, given to it as vars.item (ADR 0109 §3.5)
            let ids = self.pending_subjects(p, &action, self.parallelism(&action));
            match ids.len() {
                0 => {
                    self.disable(p, &action.name, "no pending subject left to run it for");
                    return Ok(());
                }
                1 => bb = crate::scoped::bind_subject(bb, &action, &ids[0], &mut step),
                _ => {
                    return self
                        .run_scoped(ctx, p, m, bb, &action, &pl.calls, ids)
                        .await;
                }
            }
        }
        // the permission is the one of the implementation that will run
        let mut permission = action.permission.clone();
        if let Ok((imp, spec)) = self.specialize(ctx, p, m, &action, &bb).await
            && !spec.is_empty()
        {
            permission = imp.permission.clone();
            step.specialization = spec;
        }
        if self
            .gate_action(ctx, p, m, &action, &step, &permission)
            .await?
        {
            return Ok(());
        }
        p.steps.push(step);
        let i = p.steps.len() - 1;
        self.execute(ctx, p, m, &bb, &action, i).await
    }

    /// observe_goal observes the change and checks the goal: done is true when the cycle ends there.
    pub(crate) async fn observe_goal(
        self: &Arc<Self>,
        ctx: &Ctx,
        p: &mut Process,
        m: &Arc<Compiled>,
    ) -> Result<(Blackboard, Goal, bool)> {
        let bb = self.observe(ctx, p, m).await?;
        let Some(goal) = m.goal(&p.goal) else {
            return Err(Error::msg(format!("unknown goal {:?}", p.goal)));
        };
        // the blackboard must be consistent before any action is asked, and before the goal is declared reached
        if self.check_board(ctx, p, &bb).await? {
            return Ok((bb, goal, true));
        }
        if !p.world.satisfies(&goal.pre) {
            return Ok((bb, goal, false));
        }
        p.plan = Vec::new();
        if !p.flow.is_empty() && p.status == STATUS_RUNNING {
            // the relaunched flow reached the goal: a human confirms before it replaces the previous run
            p.status = STATUS_WAITING.into();
            let desc = "The relaunched flow reached the goal. Adopt it to replace the outputs of the previous run, or discard it.";
            p.pending = Some(HumanTask {
                kind: TASK_FLOW.into(),
                action: "adopt_flow".into(),
                step: p.steps.len() as i64,
                description: desc.into(),
                ..Default::default()
            });
            let rec = Record {
                kind: goap_journal::KIND_TICK.into(),
                step: p.steps.len() as i64,
                before: p.world.0.clone(),
                data: obj(json!({"goalSatisfied": true, "flow": p.flow, "awaitingDecision": true})),
                ..Default::default()
            };
            self.journal(ctx, p, vec![rec]).await;
            return Ok((bb, goal, true));
        }
        p.status = STATUS_COMPLETED.into();
        crate::persistent::close_instance(p, m);
        let rec = Record {
            kind: goap_journal::KIND_TICK.into(),
            step: p.steps.len() as i64,
            before: p.world.0.clone(),
            data: obj(json!({"goalSatisfied": true, "closed": p.closed})),
            ..Default::default()
        };
        self.journal(ctx, p, vec![rec]).await;
        Ok((bb, goal, true))
    }

    /// admissible returns the agent of the process and the actions it may plan with.
    pub(crate) async fn admissible(
        &self,
        ctx: &Ctx,
        p: &Process,
        m: &Compiled,
    ) -> Result<(Agent, Vec<Op>)> {
        let Some(ag) = m.agent(&p.agent).cloned() else {
            return Err(Error::msg(format!("unknown agent {:?}", p.agent)));
        };
        let bound = if m.uses_mcps() {
            Some(self.bound_mcps(ctx, p).await?)
        } else {
            None
        };
        let actions = carried(m, p, m.agent_actions(&ag))
            .iter()
            .filter(|a| {
                !p.disabled.get(&a.name).copied().unwrap_or(false)
                    && self.schedulable(m, &a.name, bound.as_ref())
            })
            .map(operator)
            .collect();
        Ok((ag, actions))
    }

    /// plan_step plans from the world of the process and journals the tick. When no plan reaches the goal it settles
    /// the process instead and returns None.
    #[allow(clippy::too_many_arguments)]
    pub(crate) async fn plan_step(
        &self,
        ctx: &Ctx,
        p: &mut Process,
        m: &Compiled,
        ag: &Agent,
        goal: &Goal,
        bb: &Blackboard,
        actions: &[Op],
    ) -> Result<Option<Planned>> {
        let tick_start = self.clock();
        // the planning calls belong to the step this plan opens (ledger of the gateway, ADR 0089)
        let ctx = goap_llm::with_meta(
            ctx,
            goap_llm::CallMeta {
                source: goap_llm::SOURCE_ENGINE.into(),
                conversation_id: conversation_of(p),
                process_id: p.id.clone(),
                change_id: p.change_id.to_string(),
                step: p.steps.len() as i64,
                agent: p.agent.clone(),
                ..Default::default()
            },
        );
        let (res, calls) = self
            .plan(
                &ctx,
                m,
                ag,
                &p.world,
                actions,
                &planning_goal(goal),
                &m.utilities(bb),
            )
            .await;
        let plan = match res {
            Err(e) if e.is_no_plan() => {
                self.settle_no_plan(&ctx, p, m, ag, goal, actions, tick_start, &calls)
                    .await;
                return Ok(None);
            }
            Err(e) => return Err(e),
            Ok(plan) => plan,
        };
        let prev = std::mem::take(&mut p.plan);
        p.plan = plan.actions.iter().map(|a| a.name.clone()).collect();
        let first = p.plan.first().cloned().unwrap_or_default();
        let mut tick = tick_record_with_calls(
            Record {
                kind: goap_journal::KIND_TICK.into(),
                step: p.steps.len() as i64,
                before: p.world.0.clone(),
                plan: p.plan.clone(),
                board_before: bb.change.items.len() as i64,
                board_after: bb.change.items.len() as i64,
                action: first.clone(),
                started_at: Some(tick_start),
                ended_at: Some(self.clock()),
                data: obj(
                    json!({"replanned": replanned(&prev, &p.plan), "candidates": actions.len()}),
                ),
                ..Default::default()
            },
            &calls,
        );
        if !p.unknown.is_empty() {
            tick.data.insert("unknown".into(), json!(p.unknown));
        }
        self.journal(&ctx, p, vec![tick]).await;
        Ok(Some(Planned { next: first, calls }))
    }

    /// settle_no_plan handles a goal no plan reaches: the process waits when what is missing is established outside
    /// it; else it is stuck, and a person can always unblock it.
    #[allow(clippy::too_many_arguments)]
    async fn settle_no_plan(
        &self,
        ctx: &Ctx,
        p: &mut Process,
        m: &Compiled,
        ag: &Agent,
        goal: &Goal,
        actions: &[Op],
        tick_start: DateTime<Utc>,
        calls: &[LLMCall],
    ) {
        p.plan = Vec::new();
        let all = operators(&carried(m, p, m.agent_actions(ag)));
        let awaited = self.awaited(&p.world, &all, actions, &planning_goal(goal));
        if !awaited.is_empty() {
            p.status = STATUS_WAITING.into();
            p.pending = Some(HumanTask {
                kind: TASK_CONDITION.into(),
                step: p.steps.len() as i64,
                conditions: awaited.clone(),
                description: format!(
                    "Waiting for {}: conditions this agent's actions do not establish",
                    awaited.join(", ")
                ),
                ..Default::default()
            });
            let rec = tick_record_with_calls(
                Record {
                    kind: goap_journal::KIND_TICK.into(),
                    step: p.steps.len() as i64,
                    before: p.world.0.clone(),
                    started_at: Some(tick_start),
                    ended_at: Some(self.clock()),
                    data: obj(json!({"awaiting": awaited})),
                    ..Default::default()
                },
                calls,
            );
            self.journal(ctx, p, vec![rec]).await;
            return;
        }
        p.status = STATUS_STUCK.into();
        p.error = format!("no plan reaches goal {} from the current state", goal.name);
        p.pending = Some(HumanTask {
            kind: TASK_UNBLOCK.into(),
            step: p.steps.len() as i64,
            conditions: self.unblocking(&p.world, actions, &planning_goal(goal)),
            description: format!(
                "Stuck: {}. Declare conditions established, retry the actions it gave up on, or abandon it.",
                p.error
            ),
            ..Default::default()
        });
        let rec = tick_record_with_calls(
            Record {
                kind: goap_journal::KIND_TICK.into(),
                step: p.steps.len() as i64,
                before: p.world.0.clone(),
                error: p.error.clone(),
                started_at: Some(tick_start),
                ended_at: Some(self.clock()),
                ..Default::default()
            },
            calls,
        );
        self.journal(ctx, p, vec![rec]).await;
    }

    /// gate_action decides whether the initiator may run the action now: true when a person has to approve it first
    /// (the step is recorded and the process waits).
    pub(crate) async fn gate_action(
        &self,
        ctx: &Ctx,
        p: &mut Process,
        m: &Compiled,
        action: &Action,
        step: &Step,
        permission: &str,
    ) -> Result<bool> {
        let roles = self.run_roles(m, p, action);
        if !roles.is_empty() && action.kind != KIND_HUMAN {
            let ini = p.initiator.clone();
            if !self
                .may_run(ctx, p, &ini, "action", &action.name, &roles)
                .await?
            {
                self.await_approval(p, m, action, step.clone(), PERMISSION_RUN_ACTION, roles);
                return Ok(true);
            }
        }
        if !permission.is_empty() {
            let ini = p.initiator.clone();
            if !self.allowed(ctx, p, &ini, permission).await? {
                // the initiator may not run this action: wait for an authorized approver
                self.await_approval(p, m, action, step.clone(), permission, Vec::new());
                return Ok(true);
            }
        }
        Ok(false)
    }

    /// await_approval records the step and makes the process wait for an approval of the permission.
    fn await_approval(
        &self,
        p: &mut Process,
        m: &Compiled,
        action: &Action,
        step: Step,
        permission: &str,
        roles: Vec<String>,
    ) {
        let index = step.index;
        p.steps.push(step);
        p.status = STATUS_WAITING.into();
        let context = step_context(m, p, action);
        p.pending = Some(HumanTask {
            kind: TASK_APPROVAL.into(),
            permission: permission.to_string(),
            roles,
            action: action.name.clone(),
            description: action.description.clone(),
            instructions: action.instructions.clone(),
            step: index,
            context,
            ..Default::default()
        });
    }
}
