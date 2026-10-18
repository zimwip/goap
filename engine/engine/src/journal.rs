//! The execution journal of a change (ADR 0011): the records of the runs of the engine.

use goap_ctx::Ctx;
use goap_journal::{KIND_ACTION, KIND_PROCESS_ENDED, ModelCall, Record, ToolUse};
use goap_methodology::Compiled;
use serde_json::{Map, Value, json};

use crate::engine::{Engine, truncate};
use crate::process::{LLMCall, Process};

impl Engine {
    pub(crate) fn span_id(&self, ctx: &Ctx) -> String {
        self.tracer().span_id(ctx)
    }

    /// journal appends records to the execution journal of the change of p (ADR 0011). It is best effort: a journal
    /// failure is logged, never fails the process.
    pub(crate) async fn journal(&self, ctx: &Ctx, p: &mut Process, mut recs: Vec<Record>) {
        if p.change_id.is_empty() || recs.is_empty() {
            return;
        }
        for r in &mut recs {
            if r.id.is_empty() {
                r.id = uuid::Uuid::new_v4().to_string();
            }
            p.journal_seq += 1;
            r.seq = p.journal_seq;
            r.change_id = p.change_id.clone();
            r.process_id = p.id.clone();
            r.parent_process_id = p.parent_id.clone();
            r.flow = p.flow.clone();
            r.methodology = p.methodology.clone();
            r.methodology_version = p.methodology_version.clone();
            r.agent = p.agent.clone();
            r.planner = p.planner.clone();
            r.goal = p.goal.clone();
            if r.status.is_empty() {
                r.status = p.status.to_string();
            }
            if r.trace_id.is_empty() {
                r.trace_id = self.tracer().trace_id(ctx);
            }
            if r.started_at.is_none() {
                r.started_at = Some(self.clock());
            }
            if let (Some(end), Some(start)) = (r.ended_at, r.started_at) {
                r.duration_ms = (end - start).num_milliseconds();
            }
        }
        if let Err(e) = goap_journal::append(ctx, &*self.graph, &recs).await {
            tracing::warn!(process = %p.id, err = %e, "journal write failed");
        }
    }
}

/// tick_record_with_calls attaches planning-time LLM calls (llm/llm-scoring planners) to a tick record, so they are
/// visible in the journal (ADR 0011) even on the paths (blocked board, goal reached, stuck) that never reach a Step.
pub(crate) fn tick_record_with_calls(mut r: Record, calls: &[LLMCall]) -> Record {
    for c in calls {
        r.model_calls.push(ModelCall {
            provider: c.provider.clone(),
            model: c.model.clone(),
            input_tokens: c.input_tokens,
            output_tokens: c.output_tokens,
            duration_ms: c.duration_ms,
            error: c.error.clone(),
            exchange: None,
        });
        r.input_tokens += c.input_tokens;
        r.output_tokens += c.output_tokens;
    }
    r
}

/// action_step is the Activity (process/step/method/method-step) path of a declared action, "" when it was not
/// generated from one.
pub(crate) fn action_step(m: &Compiled, name: &str) -> String {
    m.action(name).map(|a| a.step.clone()).unwrap_or_default()
}

/// action_record describes the execution of step i: the Activity Run of activity_ref.
pub(crate) fn action_record(
    p: &Process,
    i: usize,
    kind: &str,
    activity_ref: &str,
    id: &str,
) -> Record {
    let s = &p.steps[i];
    let mut r = Record {
        id: id.to_string(),
        kind: KIND_ACTION.to_string(),
        step: i as i64,
        action: s.action.clone(),
        action_kind: kind.to_string(),
        activity_ref: activity_ref.to_string(),
        specialization: s.specialization.clone(),
        plan: s.plan.clone(),
        before: s.before.0.clone(),
        after: s.after.0.clone(),
        items: s.items.clone(),
        nodes: s.nodes.clone(),
        reads: s.reads.clone(),
        board_before: s.board_before,
        board_after: s.board_after.max(s.board_before),
        board_last: s.last_item.clone(),
        input_tokens: s.usage.input_tokens,
        output_tokens: s.usage.output_tokens,
        actor: s.approved_by.clone(),
        output: truncate(&s.output, 2000),
        error: s.error.clone(),
        span_id: s.span_id.clone(),
        started_at: Some(s.started_at).filter(|t| !goap_domain::gotime::is_zero(t)),
        ended_at: s.ended_at,
        subject: s.subject.clone(),
        subject_ref: s.subject_ref.clone(),
        ..Default::default()
    };
    if s.ended_at.is_some() && s.error.is_empty() && p.pending.is_none() {
        r.effects_met = Some(s.effects_met);
    }
    for c in &s.llm_calls {
        r.model_calls.push(ModelCall {
            provider: c.provider.clone(),
            model: c.model.clone(),
            input_tokens: c.input_tokens,
            output_tokens: c.output_tokens,
            duration_ms: c.duration_ms,
            error: c.error.clone(),
            exchange: c.exchange.clone(),
        });
    }
    for c in &s.tool_calls {
        r.tool_calls.push(ToolUse {
            name: c.name.clone(),
            duration_ms: c.duration_ms,
            error: c.error.clone(),
        });
    }
    if let Some(t) = p.pending.as_ref().filter(|t| t.step == i as i64) {
        r.data = Map::new();
        r.data.insert("waiting".into(), json!(t.kind));
        if !t.child_process_id.is_empty() {
            r.data.insert("child".into(), json!(t.child_process_id));
        }
    }
    if s.progress {
        r.data.insert("progress".into(), Value::Bool(true));
    }
    if !s.children.is_empty() {
        r.data.insert("children".into(), json!(s.children));
    }
    r
}

/// replanned reports whether the new plan departs from the continuation of the previous one (the previous first
/// action was executed).
pub(crate) fn replanned(prev: &[String], next: &[String]) -> bool {
    if prev.is_empty() {
        return false;
    }
    prev[1..] != *next
}

/// end_record summarizes a finished process.
pub(crate) fn end_record(p: &Process) -> Record {
    let disabled: Vec<&String> = p.disabled.keys().collect();
    let disabled = if disabled.is_empty() {
        Value::Null
    } else {
        json!(disabled)
    };
    let mut data = Map::new();
    data.insert("status".into(), json!(p.status.as_str()));
    data.insert("steps".into(), json!(p.steps.len()));
    data.insert("llmCalls".into(), json!(p.usage.llm_calls));
    data.insert("toolCalls".into(), json!(p.usage.tool_calls));
    data.insert("disabled".into(), disabled);
    data.insert("trigger".into(), json!(p.trigger));
    Record {
        kind: KIND_PROCESS_ENDED.to_string(),
        error: p.error.clone(),
        step: p.steps.len() as i64,
        input_tokens: p.usage.input_tokens,
        output_tokens: p.usage.output_tokens,
        data,
        started_at: Some(p.created_at).filter(|t| !goap_domain::gotime::is_zero(t)),
        ..Default::default()
    }
}
