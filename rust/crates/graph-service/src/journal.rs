//! The execution journal the engine writes on the logs of the changes (ADR 0011, Go's `contracts/journal`): records
//! decoded from their entries, as the engine wrote them.
use std::collections::HashMap;

use chrono::{DateTime, Datelike, Utc};
use goap_proto::graph::v1 as pb;
use graph_core::impact::LogEntry;
use serde::Deserialize;

use crate::convert;

/// The stream of the journal entries (`journal.StreamJournal`).
pub const STREAM: &str = "journal";

#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase", default)]
struct ModelCall {
    provider: String,
    model: String,
    input_tokens: i64,
    output_tokens: i64,
    duration_ms: i64,
    error: String,
}

#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase", default)]
struct ToolUse {
    name: String,
    duration_ms: i64,
    error: String,
}

#[derive(Debug, Default, Deserialize)]
#[serde(default)]
struct Ref {
    id: String,
    version: i32,
}

/// A record of the journal (`journal.Record`): the fields the protocol carries.
#[derive(Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase", default)]
struct Record {
    id: String,
    change_id: String,
    process_id: String,
    parent_process_id: String,
    seq: i64,
    kind: String,
    methodology: String,
    methodology_version: String,
    agent: String,
    planner: String,
    goal: String,
    status: String,
    step: i64,
    action: String,
    action_kind: String,
    specialization: String,
    plan: Option<Vec<String>>,
    before: Option<HashMap<String, bool>>,
    after: Option<HashMap<String, bool>>,
    effects_met: Option<bool>,
    items: Option<Vec<String>>,
    reads: Option<Vec<Ref>>,
    board_before: i64,
    board_after: i64,
    board_last: String,
    input_tokens: i64,
    output_tokens: i64,
    model_calls: Option<Vec<ModelCall>>,
    tool_calls: Option<Vec<ToolUse>>,
    actor: String,
    output: String,
    error: String,
    trace_id: String,
    span_id: String,
    data: Option<serde_json::Map<String, serde_json::Value>>,
    flow: String,
    started_at: Option<DateTime<Utc>>,
    ended_at: Option<DateTime<Utc>>,
    duration_ms: i64,
}

/// A time as the protocol sends it: Go's zero time is none.
fn time(t: Option<DateTime<Utc>>) -> Option<goap_proto::wkt::Timestamp> {
    convert::time(&t.filter(|t| t.year() > 1))
}

/// The records of journal entries, in their order (`journal.Decode`).
pub fn records(entries: &[LogEntry]) -> Result<Vec<pb::ExecutionRecord>, String> {
    let mut out = Vec::new();
    for e in entries.iter().filter(|e| e.stream() == STREAM) {
        let r: Record =
            serde_json::from_str(&e.payload).map_err(|err| format!("log entry {} ({}): {err}", e.seq, e.typ))?;
        out.push(pb::ExecutionRecord {
            id: r.id,
            change_id: r.change_id,
            process_id: r.process_id,
            parent_process_id: r.parent_process_id,
            seq: r.seq as i32,
            kind: r.kind,
            methodology: r.methodology,
            methodology_version: r.methodology_version,
            agent: r.agent,
            planner: r.planner,
            goal: r.goal,
            status: r.status,
            step: r.step as i32,
            action: r.action,
            action_kind: r.action_kind,
            specialization: r.specialization,
            plan: r.plan.unwrap_or_default(),
            before: r.before.unwrap_or_default(),
            after: r.after.unwrap_or_default(),
            effects_met: r.effects_met,
            items: r.items.unwrap_or_default(),
            input_tokens: r.input_tokens,
            output_tokens: r.output_tokens,
            model_calls: r
                .model_calls
                .unwrap_or_default()
                .into_iter()
                .map(|c| pb::ModelCall {
                    provider: c.provider,
                    model: c.model,
                    input_tokens: c.input_tokens,
                    output_tokens: c.output_tokens,
                    duration_ms: c.duration_ms,
                    error: c.error,
                })
                .collect(),
            tool_calls: r
                .tool_calls
                .unwrap_or_default()
                .into_iter()
                .map(|c| pb::ToolUse {
                    name: c.name,
                    duration_ms: c.duration_ms,
                    error: c.error,
                })
                .collect(),
            actor: r.actor,
            output: r.output,
            error: r.error,
            trace_id: r.trace_id,
            span_id: r.span_id,
            data: r.data.as_ref().map(convert::object),
            started_at: time(r.started_at),
            ended_at: time(r.ended_at),
            duration_ms: r.duration_ms,
            reads: r
                .reads
                .unwrap_or_default()
                .into_iter()
                .map(|n| pb::NodeRef {
                    id: n.id,
                    version: n.version,
                })
                .collect(),
            board_before: r.board_before as i32,
            board_after: r.board_after as i32,
            board_last: r.board_last,
            flow: r.flow,
        });
    }
    Ok(out)
}
