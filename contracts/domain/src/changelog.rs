//! The log of a change (ADR 0030): one append-only stream of everything that happens to it, in one order. The facts
//! of its blackboard (ADR 0017), the records of its execution journal (ADR 0011, scheduling included) and the events
//! of its change impacts (ADR 0029) are entries of the same log; what an entry is, the flow it belongs to, the process
//! and the action run behind it are columns, so the log is filtered on them directly.

use std::collections::BTreeMap;

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::{ChangeID, ChangeItem, Error, ImpactEvent, gotime, serde_util::null_default};

/// Streams of the log the graph itself writes: the first part of an entry type. Any other stream is written by a use
/// case through Graph.AppendLog (the execution journal and the prompts of the model calls, pkg/journal).
/// fact.<item kind>: fact.artifact, fact.decision, fact.flow…
pub const LOG_FACT: &str = "fact";
/// impact.<op>: impact.proposed, impact.transitioned…
pub const LOG_IMPACT: &str = "impact";
/// change.updated: an edit of the header of the change (title, intent, goal, status, data)
pub const LOG_CHANGE: &str = "change";
/// LogModel is the stream of the exchanges with a model (model.call, ADR 0059), written by the execution journal
/// (journal.StreamModel); the graph only reads that an execution called a model (the provenance of an export).
pub const LOG_MODEL: &str = "model";

/// HeaderEdit is the payload of a change.updated entry: for each field of the header the edit set, the value it left
/// and the one it wrote (Data: per key).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct HeaderEdit {
    #[serde(deserialize_with = "null_default")]
    pub fields: BTreeMap<String, HeaderValue>,
}

/// HeaderValue is one field of a HeaderEdit. A JSON null is Go's nil.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct HeaderValue {
    #[serde(skip_serializing_if = "Value::is_null")]
    pub from: Value,
    pub to: Value,
}

/// HeaderEntry is the log entry of an edit of the header of a change; Subject lists the fields it changed.
pub fn header_entry(
    id: &str,
    change: &ChangeID,
    by: &str,
    at: DateTime<Utc>,
    edit: &HeaderEdit,
) -> Result<LogEntry, Error> {
    let raw = serde_json::to_value(edit)?;
    let fields: Vec<&str> = edit.fields.keys().map(String::as_str).collect();
    Ok(LogEntry {
        id: id.to_string(),
        change: change.clone(),
        r#type: format!("{LOG_CHANGE}.updated"),
        subject: fields.join(","),
        by: by.to_string(),
        at: Some(at),
        payload: raw,
        ..Default::default()
    })
}

/// LogEntry is one entry of a change's log. Payload is the whole fact, journal record or impact event (JSON).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct LogEntry {
    /// Seq orders the log (one sequence for every change: the order of the entries of a change is total).
    pub seq: i64,
    pub id: String,
    pub change: ChangeID,
    /// Type is "<stream>.<kind>".
    pub r#type: String,
    /// Flow is the flow branch the entry belongs to ("" = the main flow).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub flow: String,
    /// Process is the process of a journal record; Execution the action run behind the entry (a journal record is
    /// its own); Subject what it is about (the action, the fact type, the change impact); By the principal or
    /// component.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub process: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub execution: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub subject: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    /// At is when the entry was written (None: Go's zero time, set by the graph on append).
    #[serde(with = "gotime::option")]
    pub at: Option<DateTime<Utc>>,
    /// Causation is the ID of the entry this one follows from (ADR 0105): a decided review of a change impact names
    /// the submitted review (fact.review) that decided it. Empty when the entry has no cause in the log.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub causation: String,
    /// The entry as JSON (Go's json.RawMessage; null when unset).
    pub payload: Value,
}

impl LogEntry {
    /// Stream is the stream of the entry: fact, impact, or one a use case writes.
    pub fn stream(&self) -> &str {
        self.r#type
            .split_once('.')
            .map_or(self.r#type.as_str(), |(s, _)| s)
    }
}

/// LogFilter selects entries of the log. Empty fields select everything.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct LogFilter {
    pub change: ChangeID,
    /// Types are exact types ("journal.action") or streams ("journal.": a type prefix ending with a dot).
    pub types: Vec<String>,
    /// Flows are flow branches; "" is the main flow. None selects every flow (Go's nil), an empty list none.
    pub flows: Option<Vec<String>>,
    pub processes: Vec<String>,
    pub execution: String,
    /// AfterSeq keeps the entries after a position (to follow the log); Limit caps their number.
    pub after_seq: i64,
    pub limit: i64,
}

impl LogFilter {
    /// MatchType tells whether a type is selected by the filter's types.
    pub fn match_type(&self, t: &str) -> bool {
        self.types.is_empty()
            || self
                .types
                .iter()
                .any(|x| x == t || (x.ends_with('.') && t.starts_with(x.as_str())))
    }

    /// Match tells whether an entry is selected (the limit aside).
    pub fn matches(&self, e: &LogEntry) -> bool {
        (self.change.is_empty() || e.change == self.change)
            && self.match_type(&e.r#type)
            && self.flows.as_ref().is_none_or(|f| f.contains(&e.flow))
            && (self.processes.is_empty() || self.processes.contains(&e.process))
            && (self.execution.is_empty() || e.execution == self.execution)
            && e.seq > self.after_seq
    }
}

/// FactEntry is the log entry of a fact. A flow event belongs to the flow it opens, adopts or discards.
pub fn fact_entry(change: &ChangeID, it: &ChangeItem) -> Result<LogEntry, Error> {
    let raw = serde_json::to_value(it)?;
    let mut e = LogEntry {
        id: it.id.to_string(),
        change: change.clone(),
        r#type: format!("{LOG_FACT}.{}", it.r#type),
        flow: it.flow.clone(),
        execution: it.execution.clone(),
        subject: it.target.clone(),
        by: it.produced_by.clone(),
        at: Some(it.created_at).filter(|t| !gotime::is_zero(t)),
        payload: raw,
        ..Default::default()
    };
    if let Some(f) = &it.flow_event {
        e.flow = f.flow.clone();
        e.subject = f.op.clone();
        if !f.by.is_empty() {
            e.by = f.by.clone();
        }
    }
    if let Some(d) = &it.decision_event {
        e.subject = format!("decision.{}", d.op);
        if !d.by.is_empty() {
            e.by = d.by.clone();
        }
    }
    Ok(e)
}

/// ImpactEntry is the log entry of an impact event.
pub fn impact_entry(e: &ImpactEvent) -> Result<LogEntry, Error> {
    let raw = serde_json::to_value(e)?;
    Ok(LogEntry {
        id: e.id.clone(),
        change: e.change.clone(),
        r#type: format!("{LOG_IMPACT}.{}", e.op),
        flow: e.flow.clone(),
        execution: e.execution.clone(),
        subject: e.impact.to_string(),
        by: e.by.clone(),
        at: e.at,
        causation: e.causation.clone(),
        payload: raw,
        ..Default::default()
    })
}
