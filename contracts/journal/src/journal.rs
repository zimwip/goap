//! The records of the journal and their entries in the log of a change.

use std::{collections::BTreeMap, sync::Arc};

use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::{
    ChangeID, ChangeImpactID, ItemID, LOG_MODEL, LogEntry, LogFilter, NodeRef, gotime,
};
use serde::{Deserialize, Deserializer, Serialize};
use serde_json::{Map, Value};

/// The text of the `ErrInvalid` sentinel, wrapped by the errors about a record or a filter that is not valid.
pub const INVALID: &str = "invalid journal";

/// An error of the journal.
#[derive(Debug, Clone, thiserror::Error)]
pub enum Error {
    /// A record or a filter that is not valid (`errors.Is(err, journal.ErrInvalid)`): the whole message, ending with
    /// [`INVALID`].
    #[error("{0}")]
    Invalid(String),
    /// An entry whose payload does not decode: the whole message and the JSON error.
    #[error("{message}")]
    Decode {
        message: String,
        #[source]
        source: Arc<serde_json::Error>,
    },
    /// A record that does not encode.
    #[error(transparent)]
    Json(Arc<serde_json::Error>),
    /// An error of the log written to or read from.
    #[error(transparent)]
    Log(#[from] goap_graphapi::Error),
}

impl Error {
    /// `errors.Is(err, journal.ErrInvalid)`.
    pub fn is_invalid(&self) -> bool {
        matches!(self, Error::Invalid(_))
    }

    fn invalid(context: &str) -> Self {
        Error::Invalid(format!("{context}: {INVALID}"))
    }
}

impl From<serde_json::Error> for Error {
    fn from(e: serde_json::Error) -> Self {
        Error::Json(Arc::new(e))
    }
}

/// Streams of the log of a change this crate writes (the first part of an entry type, LogEntry::stream):
/// journal.<record kind>: journal.schedule, journal.tick, journal.action…
pub const STREAM_JOURNAL: &str = "journal";
/// model.call: the request and the answer of one LLM call of an action run.
pub const STREAM_MODEL: &str = LOG_MODEL;

// Record kinds: every tick of the observe / plan / act loop, every action execution (with its model and tool calls)
// and every human decision is recorded on the change axis, next to the items it produced.
pub const KIND_PROCESS_STARTED: &str = "process.started";
/// observe + plan: world state, plan, chosen action.
pub const KIND_TICK: &str = "tick";
/// One action execution.
pub const KIND_ACTION: &str = "action";
/// A human approval decision.
pub const KIND_APPROVAL: &str = "approval";
/// KindSchedule records that a run picked up a process made runnable (started, resumed after a human input, an
/// approval or a sub-agent, relaunched on a flow, fired by a trigger): why, by whom and what caused it; started_at is
/// when it was queued, ended_at when the run picked it up.
pub const KIND_SCHEDULE: &str = "schedule";
pub const KIND_PROCESS_ENDED: &str = "process.ended";
/// KindAttach records a process being bound to a change (ADR 0031): the eager default at Start, or an agent's own
/// explicit bind of a deferred process.
pub const KIND_ATTACH: &str = "attach";
/// KindUnblock records a person's decision on a run waiting for conditions or stuck (ADR 0036 §3): retry or abandon.
pub const KIND_UNBLOCK: &str = "unblock";

fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

fn is_zero_i64(n: &i64) -> bool {
    *n == 0
}

fn is_false(b: &bool) -> bool {
    !*b
}

/// Record is one entry of the execution journal of a change.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Record {
    pub id: String,
    pub change_id: ChangeID,
    pub process_id: String,
    /// ParentProcessID is set for sub-agent processes.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub parent_process_id: String,
    /// Seq orders the records of a process.
    pub seq: i64,
    pub kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology: String,
    /// MethodologyVersion is the published version the process ran.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology_version: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub agent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub planner: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub goal: String,
    /// Status of the process after the record.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub status: String,
    pub step: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub action: String,
    /// ActivityRef is the Activity (methodology@Process/Step/Method/MethodStep node key) this record's action
    /// executes - the Activity Run this record is, resolved from the compiled step's Action.Step at run-start. Empty
    /// for records of an action with no step (not generated from a process/method), or before this is wired up by a
    /// caller.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub activity_ref: String,
    /// ActionKind is the kind of the executed action; Specialization is the specialized action actually run for an
    /// abstract one.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub action_kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub specialization: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub plan: Vec<String>,
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub before: BTreeMap<String, bool>,
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub after: BTreeMap<String, bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub effects_met: Option<bool>,
    /// Subject is the subject a scoped action ran for (ADR 0109 §3.5), SubjectRef what the log names it by (an impact
    /// id, "<kind>:<key>" of a keyed item): the only input of that run.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub subject: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub subject_ref: String,
    /// Items produced by the action (they carry this record id as provenance).
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub items: Vec<ItemID>,
    /// Nodes are the change impacts the action declared (they carry this record id as their execution).
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub nodes: Vec<ChangeImpactID>,
    /// A step reads the blackboard and extends it: BoardBefore / BoardAfter are the numbers of items on the change
    /// when the step starts / ends (the blackboard state), Reads the existing node versions the step started from.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub reads: Vec<NodeRef>,
    pub board_before: i64,
    pub board_after: i64,
    /// BoardLast is the last item of the flow before the step: where relaunching the step forks it.
    #[serde(skip_serializing_if = "ItemID::is_empty")]
    pub board_last: ItemID,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub input_tokens: i64,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub output_tokens: i64,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub model_calls: Vec<ModelCall>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub tool_calls: Vec<ToolUse>,
    /// Principal of a human decision.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub actor: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub output: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    /// TraceID / SpanID link the record to its OpenTelemetry span.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub trace_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub span_id: String,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub data: Map<String, Value>,
    /// Flow is the flow branch the process runs on ("" = the main flow, ADR 0017).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub flow: String,
    /// StartedAt and EndedAt (None: Go's zero time, written as such; Go's `omitempty` never omits a time).
    #[serde(with = "gotime::option")]
    pub started_at: Option<DateTime<Utc>>,
    #[serde(with = "gotime::option")]
    pub ended_at: Option<DateTime<Utc>>,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub duration_ms: i64,
}

/// ModelCall is one LLM call of an action.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ModelCall {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub provider: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub model: String,
    pub input_tokens: i64,
    pub output_tokens: i64,
    pub duration_ms: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    /// Exchange is what was sent and answered. It is not part of the journal record: entries writes it as an entry of
    /// the log (STREAM_MODEL) next to the record and strips it, so reading a record never carries the prompts.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub exchange: Option<ModelExchange>,
}

/// MaxExchangeText caps each text of a ModelExchange (bytes); a longer one is cut and the exchange flagged truncated.
pub const MAX_EXCHANGE_TEXT: usize = 256 << 10;

/// ModelExchange is the request and the answer of one LLM call: the payload of a STREAM_MODEL entry.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ModelExchange {
    /// Step is the step of the process the action run is, Call the position of the call in the model_calls of its
    /// record.
    pub step: i64,
    pub call: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub system: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub messages: Vec<ModelMessage>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub response: String,
    /// Behaviors names the global behaviours the gateway added to system on its way to the model (ADR 0093; "!name":
    /// dropped by the cap), BehaviorTokens the tokens they added (an estimate). System is what the engine built: the
    /// gateway's addition is not repeated in the log, the ledger row of the call holds the same names.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub behaviors: Vec<String>,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub behavior_tokens: i64,
    /// BehaviorsEstimated: behavior_tokens is an estimate, not a measure (ADR 0093).
    #[serde(skip_serializing_if = "is_false")]
    pub behaviors_estimated: bool,
    /// Truncated says a text was cut to MAX_EXCHANGE_TEXT.
    #[serde(skip_serializing_if = "is_false")]
    pub truncated: bool,
}

/// ModelMessage is a turn of the conversation sent to the model.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ModelMessage {
    pub role: String,
    pub content: String,
}

/// Cuts a text to MAX_EXCHANGE_TEXT bytes, dropping a character cut in its middle (Go's `strings.ToValidUTF8`).
fn cut(s: &mut String, truncated: &mut bool) {
    if s.len() <= MAX_EXCHANGE_TEXT {
        return;
    }
    *truncated = true;
    let mut end = MAX_EXCHANGE_TEXT;
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    s.truncate(end);
}

impl ModelExchange {
    /// Cap cuts the texts of the exchange to MAX_EXCHANGE_TEXT.
    pub fn cap(&mut self) {
        let mut truncated = self.truncated;
        cut(&mut self.system, &mut truncated);
        cut(&mut self.response, &mut truncated);
        for m in &mut self.messages {
            cut(&mut m.content, &mut truncated);
        }
        self.truncated = truncated;
    }
}

/// ToolUse is one tool call of an action.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ToolUse {
    pub name: String,
    pub duration_ms: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
}

/// Filter selects journal records (empty fields match everything; at least one of change_id / process_ids is
/// required).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Filter {
    #[serde(skip_serializing_if = "ChangeID::is_empty")]
    pub change_id: ChangeID,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub process_ids: Vec<String>,
}

impl Filter {
    /// LogFilter is the filter of the log of a change selecting the journal records.
    pub fn log_filter(&self) -> Result<LogFilter, Error> {
        if self.change_id.is_empty() && self.process_ids.is_empty() {
            return Err(Error::invalid("journal filter needs a change or processes"));
        }
        Ok(LogFilter {
            change: self.change_id.clone(),
            types: vec![format!("{STREAM_JOURNAL}.")],
            processes: self.process_ids.clone(),
            ..Default::default()
        })
    }
}

/// Log is the log of the changes the journal is written to and read from (the graph and the graph service client).
/// Every [`goap_graphapi::Port`] is one.
#[async_trait::async_trait]
pub trait Log: Send + Sync {
    /// AppendLog appends the entries as one write.
    async fn append_log(
        &self,
        ctx: &Ctx,
        entries: Vec<LogEntry>,
    ) -> Result<(), goap_graphapi::Error>;
    /// ChangeLog returns the entries matching the filter.
    async fn change_log(
        &self,
        ctx: &Ctx,
        f: LogFilter,
    ) -> Result<(Vec<LogEntry>, BTreeMap<String, i64>), goap_graphapi::Error>;
}

#[async_trait::async_trait]
impl<T: goap_graphapi::Port + ?Sized> Log for T {
    async fn append_log(
        &self,
        ctx: &Ctx,
        entries: Vec<LogEntry>,
    ) -> Result<(), goap_graphapi::Error> {
        goap_graphapi::Port::append_log(self, ctx, entries).await
    }

    async fn change_log(
        &self,
        ctx: &Ctx,
        f: LogFilter,
    ) -> Result<(Vec<LogEntry>, BTreeMap<String, i64>), goap_graphapi::Error> {
        goap_graphapi::Port::change_log(self, ctx, f).await
    }
}

/// Append writes the records to the log, as one write (ids are assigned and the start time is set when empty).
pub async fn append(ctx: &Ctx, l: &(impl Log + ?Sized), recs: &[Record]) -> Result<(), Error> {
    let mut all = Vec::new();
    for r in recs {
        all.extend(entries(r)?);
    }
    if all.is_empty() {
        return Ok(());
    }
    Ok(l.append_log(ctx, all).await?)
}

/// Read returns the records matching the filter, in the order of the log.
pub async fn read(ctx: &Ctx, l: &(impl Log + ?Sized), f: &Filter) -> Result<Vec<Record>, Error> {
    let lf = f.log_filter()?;
    let (entries, _) = l.change_log(ctx, lf).await?;
    decode(&entries)
}

/// Entries are the log entries of a record: its own, then the exchange of each model call that carries one (the
/// record keeps the summary of its calls, the prompts are entries of their own, capped to MAX_EXCHANGE_TEXT). The
/// record is not changed.
pub fn entries(r: &Record) -> Result<Vec<LogEntry>, Error> {
    if r.change_id.is_empty() || r.process_id.is_empty() || r.kind.is_empty() {
        return Err(Error::invalid(
            "execution record needs a change, a process and a kind",
        ));
    }
    let mut r = r.clone();
    if r.id.is_empty() {
        r.id = uuid::Uuid::new_v4().to_string();
    }
    if r.started_at.is_none_or(|t| gotime::is_zero(&t)) {
        r.started_at = Some(Utc::now());
    }
    let calls = std::mem::take(&mut r.model_calls);
    r.model_calls = calls
        .iter()
        .map(|c| ModelCall {
            exchange: None,
            ..c.clone()
        })
        .collect();
    let mut out = vec![entry(&r)?];
    for (i, c) in calls.into_iter().enumerate() {
        let Some(mut ex) = c.exchange else {
            continue;
        };
        ex.step = r.step;
        ex.call = i as i64;
        ex.cap();
        out.push(model_entry(&r, &ex)?);
    }
    Ok(out)
}

/// entry is the log entry of a record: its own action run.
pub(crate) fn entry(r: &Record) -> Result<LogEntry, Error> {
    let raw = serde_json::to_value(r)?;
    let by = if r.actor.is_empty() {
        r.agent.clone()
    } else {
        r.actor.clone()
    };
    Ok(LogEntry {
        id: r.id.clone(),
        change: r.change_id.clone(),
        r#type: format!("{STREAM_JOURNAL}.{}", r.kind),
        flow: r.flow.clone(),
        process: r.process_id.clone(),
        execution: r.id.clone(),
        subject: r.action.clone(),
        by,
        at: r.started_at,
        payload: raw,
        ..Default::default()
    })
}

/// modelEntry is the log entry of the exchange of an LLM call. It belongs to the action run (journal record) that made
/// the call, whose process and flow it takes; the subject is the position of the call in the record's model_calls.
fn model_entry(r: &Record, ex: &ModelExchange) -> Result<LogEntry, Error> {
    let raw = serde_json::to_value(ex)?;
    Ok(LogEntry {
        id: format!("{}/{}", r.id, ex.call),
        change: r.change_id.clone(),
        r#type: format!("{STREAM_MODEL}.call"),
        flow: r.flow.clone(),
        process: r.process_id.clone(),
        execution: r.id.clone(),
        subject: ex.call.to_string(),
        by: r.agent.clone(),
        at: r.started_at,
        payload: raw,
        ..Default::default()
    })
}

fn decode_error(e: &LogEntry, err: serde_json::Error) -> Error {
    Error::Decode {
        message: format!("log entry {} ({}): {err}", e.seq, e.r#type),
        source: Arc::new(err),
    }
}

/// Decode returns the records held by the journal entries of a log (the other entries are skipped).
pub fn decode(entries: &[LogEntry]) -> Result<Vec<Record>, Error> {
    let mut out = Vec::new();
    for e in entries {
        if e.stream() != STREAM_JOURNAL {
            continue;
        }
        let r = serde_json::from_value::<Record>(e.payload.clone())
            .map_err(|err| decode_error(e, err))?;
        out.push(r);
    }
    Ok(out)
}

/// DecodeExchange is the exchange of a model entry.
pub fn decode_exchange(e: &LogEntry) -> Result<ModelExchange, Error> {
    serde_json::from_value::<ModelExchange>(e.payload.clone()).map_err(|err| decode_error(e, err))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn entries_strip_and_cap_the_exchanges() {
        let r = Record {
            change_id: "c1".into(),
            process_id: "p".into(),
            kind: KIND_ACTION.into(),
            agent: "a".into(),
            step: 3,
            model_calls: vec![
                ModelCall {
                    model: "m".into(),
                    ..Default::default()
                },
                ModelCall {
                    exchange: Some(ModelExchange {
                        system: "é".repeat(MAX_EXCHANGE_TEXT),
                        response: "ok".into(),
                        ..Default::default()
                    }),
                    ..Default::default()
                },
            ],
            ..Default::default()
        };
        let es = entries(&r).unwrap();
        assert_eq!(es.len(), 2);
        assert_eq!(es[0].r#type, "journal.action");
        assert!(es[0].at.is_some());
        let back = decode(&es).unwrap();
        assert_eq!(back.len(), 1);
        assert!(back[0].model_calls.iter().all(|c| c.exchange.is_none()));
        assert_eq!(es[1].r#type, "model.call");
        assert_eq!(es[1].id, format!("{}/1", es[0].id));
        assert_eq!(es[1].subject, "1");
        let ex = decode_exchange(&es[1]).unwrap();
        assert!(
            ex.truncated && ex.system.len() <= MAX_EXCHANGE_TEXT && ex.step == 3 && ex.call == 1
        );
        assert_eq!(ex.response, "ok");
        // the record given is not changed
        assert!(r.model_calls[1].exchange.is_some() && r.id.is_empty());
    }

    #[test]
    fn invalid_records_and_filters() {
        let e = entries(&Record::default()).unwrap_err();
        assert!(e.is_invalid());
        assert_eq!(
            e.to_string(),
            "execution record needs a change, a process and a kind: invalid journal"
        );
        assert!(Filter::default().log_filter().unwrap_err().is_invalid());
        let f = Filter {
            change_id: "c".into(),
            ..Default::default()
        }
        .log_filter()
        .unwrap();
        assert_eq!(f.types, vec!["journal.".to_string()]);
    }

    #[test]
    fn record_json_keeps_the_go_shape() {
        let j = serde_json::to_value(Record::default()).unwrap();
        assert_eq!(
            j,
            serde_json::json!({"id": "", "changeId": "", "processId": "", "seq": 0, "kind": "", "step": 0,
                "boardBefore": 0, "boardAfter": 0, "startedAt": "0001-01-01T00:00:00Z", "endedAt": "0001-01-01T00:00:00Z"})
        );
    }
}
