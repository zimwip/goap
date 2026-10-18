//! The unexported workings of an engine for the integration tests of the platform
//! (platform/e2e/engine), which run the engine on a real graph the engine's own tests do not import (ADR 0125 §5).
//! Production code never calls it (platform/layering).

use std::sync::Arc;
use std::sync::atomic::Ordering;

use goap_ctx::Ctx;
use goap_domain::{Blackboard, Change, ChangeID, ChangeImpactID};
use goap_dsl::NodeOp;
use goap_mcp::ToolInfo;
use goap_methodology::{Action, Compiled};

use crate::decisions::DecisionOp;
use crate::engine::{AttachRequest, Clock, Engine};
use crate::error::Result;
use crate::executors::{ActionContext, ExecResult, LLMExecutor};
use crate::host::Host;
use crate::ports::GraphPort;
use crate::process::{MergeTask, Process};
use crate::triggers::TriggerManager;

/// Internals reaches the unexported workings of an engine.
pub struct Internals {
    e: Arc<Engine>,
}

/// FOREACH_SUFFIX separates a step from the element of its foreach in the key of a child.
pub const FOREACH_SUFFIX: &str = crate::foreach::FOREACH_SUFFIX;

impl Engine {
    /// internals_of gives the internals of e.
    pub fn internals_of(e: &Arc<Engine>) -> Internals {
        Internals { e: e.clone() }
    }
}

impl Internals {
    /// schedule runs process id the way the engine schedules a run.
    pub fn schedule(&self, id: &str) {
        self.e.schedule_run(id);
    }

    /// set_clock replaces the engine's clock; None restores the real one.
    pub fn set_clock(&self, now: Option<Clock>) {
        self.e.set_clock(now);
    }

    /// set_inflight sets the number of runs the engine counts as held (max_runs).
    pub fn set_inflight(&self, n: i64) {
        self.e.inflight.store(n, Ordering::SeqCst);
    }

    /// observe reads the blackboard of the change of p as a run does.
    pub async fn observe(&self, ctx: &Ctx, p: &mut Process, m: &Compiled) -> Result<Blackboard> {
        self.e.observe(ctx, p, m).await
    }

    /// blackboard reads the blackboard of a change with its history.
    pub async fn blackboard(&self, ctx: &Ctx, id: &ChangeID, flow: &str) -> Result<Blackboard> {
        self.e.blackboard(ctx, id, flow).await
    }

    /// methodology_of gives the methodology version p runs.
    pub async fn methodology_of(&self, ctx: &Ctx, p: &Process) -> Result<Arc<Compiled>> {
        self.e.methodology_of(ctx, p).await
    }

    /// resolve_change gives the change an attach request names for p.
    pub async fn resolve_change(
        &self,
        ctx: &Ctx,
        p: &mut Process,
        m: &Compiled,
        req: &AttachRequest,
    ) -> Result<ChangeID> {
        self.e.resolve_change(ctx, p, m, req).await
    }

    /// apply_node_ops applies the node operations of an action of p.
    pub async fn apply_node_ops(
        &self,
        ctx: &Ctx,
        p: &Process,
        ops: Vec<NodeOp>,
        produced_by: &str,
        execution: &str,
    ) -> Result<Vec<ChangeImpactID>> {
        self.e
            .apply_node_ops(ctx, p, ops, produced_by, execution)
            .await
    }

    /// apply_decision_ops applies the decision operations of an action of p.
    pub async fn apply_decision_ops(
        &self,
        ctx: &Ctx,
        p: &Process,
        ops: Vec<DecisionOp>,
        human: bool,
        by: &str,
    ) -> Result<Vec<String>> {
        self.e.apply_decision_ops(ctx, p, ops, human, by).await
    }

    /// new_host gives the host an action of p runs in.
    pub fn new_host(&self, p: Arc<Process>, action: &Action, agent_mcps: &[String]) -> Host {
        self.e.new_host(p, action, agent_mcps)
    }

    /// with_verify marks ctx as running action a, for its verification.
    pub fn with_verify(ctx: &Ctx, a: &Action) -> Ctx {
        crate::nodeops::with_verify(ctx, a)
    }

    /// with_automated marks ctx as running the operations of action a, an automated one unless it is human (the
    /// oversight floor, person_reviews_model_output).
    pub fn with_automated(ctx: &Ctx, a: &Action) -> Ctx {
        crate::oversight::with_automated(ctx, a)
    }

    /// expire_all_derogations runs the builtin expiring the derogations.
    pub async fn expire_all_derogations(ctx: &Ctx, ac: &ActionContext) -> ExecResult {
        crate::derogation::expire_all_derogations(ctx, ac).await
    }

    /// merge_task_of gives the merge task of change c.
    pub async fn merge_task_of(ctx: &Ctx, g: &dyn GraphPort, c: &Change) -> Result<MergeTask> {
        crate::merge::merge_task_of(ctx, g, c).await
    }

    /// run_loop runs the tool loop of an llm action.
    pub async fn run_loop(
        x: &LLMExecutor,
        ctx: &Ctx,
        ac: &ActionContext,
        prompt: String,
        system: String,
        tools: Vec<ToolInfo>,
    ) -> ExecResult {
        x.run_loop(ctx, ac, prompt, system, tools).await
    }

    /// cron_fire fires the schedule trigger of key.
    pub async fn cron_fire(tm: &TriggerManager, key: &str) {
        tm.cron_fire(key).await;
    }
}
