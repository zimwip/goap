use goap_domain::{self as domain, ChangeID};
use goap_gen::graph::v1 as pb;
use serde_json::Value;

use crate::{from_opt_time, opt_time};

/// LogEntryToPB converts an entry of the log of a change (ADR 0030); the payload travels as its JSON text.
pub fn log_entry_to_pb(e: &domain::LogEntry) -> pb::LogEntry {
    pb::LogEntry {
        seq: e.seq,
        id: e.id.clone(),
        change_id: e.change.to_string(),
        r#type: e.r#type.clone(),
        flow: e.flow.clone(),
        process_id: e.process.clone(),
        execution: e.execution.clone(),
        subject: e.subject.clone(),
        by: e.by.clone(),
        at: opt_time(&e.at),
        payload: if e.payload.is_null() {
            String::new()
        } else {
            e.payload.to_string()
        },
        causation: e.causation.clone(),
    }
}

/// LogEntryFromPB converts a protobuf entry of the log of a change (an empty or unreadable payload is null).
pub fn log_entry_from_pb(e: &pb::LogEntry) -> domain::LogEntry {
    domain::LogEntry {
        seq: e.seq,
        id: e.id.clone(),
        change: ChangeID::new(e.change_id.clone()),
        r#type: e.r#type.clone(),
        flow: e.flow.clone(),
        process: e.process_id.clone(),
        execution: e.execution.clone(),
        subject: e.subject.clone(),
        by: e.by.clone(),
        at: from_opt_time(e.at.as_ref()),
        payload: if e.payload.is_empty() {
            Value::Null
        } else {
            serde_json::from_str(&e.payload).unwrap_or(Value::Null)
        },
        causation: e.causation.clone(),
    }
}

/// LogEntriesFromPB converts protobuf entries.
pub fn log_entries_from_pb(es: &[pb::LogEntry]) -> Vec<domain::LogEntry> {
    es.iter().map(log_entry_from_pb).collect()
}
