use goap_gen::graph::v1 as pb;
use goap_journal as journal;

use crate::{opt_time, ref_to_pb, to_struct};

/// ExecutionToPB converts a journal record (the prompts of its model calls are entries of the log of their own, never
/// part of it).
pub fn execution_to_pb(r: &journal::Record) -> pb::ExecutionRecord {
    pb::ExecutionRecord {
        id: r.id.clone(),
        change_id: r.change_id.to_string(),
        process_id: r.process_id.clone(),
        parent_process_id: r.parent_process_id.clone(),
        seq: r.seq as i32,
        kind: r.kind.clone(),
        methodology: r.methodology.clone(),
        methodology_version: r.methodology_version.clone(),
        agent: r.agent.clone(),
        planner: r.planner.clone(),
        goal: r.goal.clone(),
        status: r.status.clone(),
        step: r.step as i32,
        action: r.action.clone(),
        action_kind: r.action_kind.clone(),
        specialization: r.specialization.clone(),
        plan: r.plan.clone(),
        before: r.before.iter().map(|(k, v)| (k.clone(), *v)).collect(),
        after: r.after.iter().map(|(k, v)| (k.clone(), *v)).collect(),
        effects_met: r.effects_met,
        input_tokens: r.input_tokens,
        output_tokens: r.output_tokens,
        actor: r.actor.clone(),
        output: r.output.clone(),
        error: r.error.clone(),
        trace_id: r.trace_id.clone(),
        span_id: r.span_id.clone(),
        data: to_struct(&r.data),
        started_at: opt_time(&r.started_at),
        ended_at: opt_time(&r.ended_at),
        duration_ms: r.duration_ms,
        items: crate::graph::ids_to_pb(&r.items),
        board_before: r.board_before as i32,
        board_after: r.board_after as i32,
        board_last: r.board_last.to_string(),
        flow: r.flow.clone(),
        reads: r.reads.iter().map(ref_to_pb).collect(),
        model_calls: r
            .model_calls
            .iter()
            .map(|c| pb::ModelCall {
                provider: c.provider.clone(),
                model: c.model.clone(),
                input_tokens: c.input_tokens,
                output_tokens: c.output_tokens,
                duration_ms: c.duration_ms,
                error: c.error.clone(),
            })
            .collect(),
        tool_calls: r
            .tool_calls
            .iter()
            .map(|c| pb::ToolUse {
                name: c.name.clone(),
                duration_ms: c.duration_ms,
                error: c.error.clone(),
            })
            .collect(),
    }
}

pub fn executions_to_pb(rs: &[journal::Record]) -> Vec<pb::ExecutionRecord> {
    rs.iter().map(execution_to_pb).collect()
}
