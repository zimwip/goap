//! Package engine runs agent processes: intent loop, then observe / plan / act cycles over the blackboard of a change
//! until the goal holds. This module holds the process model and its store.

use std::collections::{BTreeMap, HashMap};

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::{BaselineID, BoardIssue, ChangeID, ChangeImpactID, ItemID, NodeRef, gotime};
use goap_goap::WorldState;
use goap_intent::{Candidate, Session};
use goap_journal::{ModelExchange, ModelMessage};
use parking_lot::RwLock;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::error::{Error, Result};
use crate::persistent::Turn;
use crate::serde_util::{is_false, is_zero_i64, nil_vec, null_default};
use crate::steps::StepContext;

/// Status is the lifecycle state of a process.
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Status(pub String);

impl Status {
    pub fn new(s: impl Into<String>) -> Status {
        Status(s.into())
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }

    /// Terminal reports whether the process has ended. A stuck process has not: a person can always unblock it.
    pub fn terminal(&self) -> bool {
        matches!(
            self.0.as_str(),
            STATUS_COMPLETED | STATUS_FAILED | STATUS_SUPERSEDED | STATUS_CANCELLED
        )
    }

    pub fn is(&self, s: &str) -> bool {
        self.0 == s
    }

    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }
}

impl std::fmt::Display for Status {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl From<&str> for Status {
    fn from(s: &str) -> Status {
        Status(s.to_string())
    }
}

impl PartialEq<&str> for Status {
    fn eq(&self, o: &&str) -> bool {
        self.0 == *o
    }
}

impl PartialEq<str> for Status {
    fn eq(&self, o: &str) -> bool {
        self.0 == o
    }
}

/// waiting for the user to answer an intent question
pub const STATUS_CLARIFYING: &str = "clarifying";
pub const STATUS_RUNNING: &str = "running";
/// waiting for a person, a sub-agent, or conditions established outside the process
pub const STATUS_WAITING: &str = "waiting";
pub const STATUS_COMPLETED: &str = "completed";
/// No plan reaches the goal, whatever is established outside the process; it waits for a person to unblock it
/// (TASK_UNBLOCK), and is tried again when the change moves.
pub const STATUS_STUCK: &str = "stuck";
pub const STATUS_FAILED: &str = "failed";
/// The run was replaced by a relaunched flow that was adopted, or it is the relaunched flow itself that was
/// discarded.
pub const STATUS_SUPERSEDED: &str = "superseded";
/// A person who answers for the run stopped it (Engine::cancel); its live sub-processes with it.
pub const STATUS_CANCELLED: &str = "cancelled";

/// Process is an agent process working on a change.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Process {
    pub id: String,
    pub methodology: String,
    #[serde(rename = "changeId")]
    pub change_id: ChangeID,
    /// Org is the organisation holding the change: its owner unit, or the default one (a cache: the change is the
    /// source). Its adapters decide which MCPs the actions can use.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub org: String,
    /// Project is the project the change belongs to (a cache: the change is the source, ADR 0039).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub project: String,
    /// Queued says why the process was last made runnable; the run that picks it up journals it as a schedule record
    /// (ADR 0011) and clears it.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub queued: Option<Queued>,
    /// Initiator is the principal who started the process; automatic actions run with its permissions.
    pub initiator: goap_authz::Principal,
    /// Agent of the methodology run by the process and its planner.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub agent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub planner: String,
    /// ParentID is the process that called this one as a sub-agent.
    #[serde(rename = "parentId", skip_serializing_if = "String::is_empty")]
    pub parent_id: String,
    /// Trigger ("<methodology>/<agent>/<trigger>") started the process.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub trigger: String,
    /// Cause is the process whose event fired the trigger.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub cause: String,
    /// Children maps sub-agent calls ("action#index:agent") to their process, so that a retried action finds the
    /// sub-agent it started.
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub children: BTreeMap<String, String>,
    #[serde(rename = "baselineId", skip_serializing_if = "BaselineID::is_empty")]
    pub baseline_id: BaselineID,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub namespace: String,
    #[serde(skip_serializing_if = "is_false")]
    pub own_branch: bool,
    /// Flow is the flow branch the process works on ("" = the main flow). RelaunchOf is the process it replaces if
    /// the branch is adopted, FromStep the step it restarts.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub flow: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub relaunch_of: String,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub from_step: i64,
    /// Dismissed lists the sets of blackboard issues a human chose to ignore (see issues_key).
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub dismissed: BTreeMap<String, bool>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub title: String,
    pub usage: Usage,
    #[serde(rename = "traceId", skip_serializing_if = "String::is_empty")]
    pub trace_id: String,
    /// TraceParent (W3C) of the process root span: later runs continue the trace.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub trace_parent: String,
    pub status: Status,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub goal: String,
    pub intent: Session,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub question: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub candidates: Vec<Candidate>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pending: Option<HumanTask>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub plan: Vec<String>,
    #[serde(skip_serializing_if = "world_empty", deserialize_with = "null_default")]
    pub world: WorldState,
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub unknown: BTreeMap<String, String>,
    /// Subjects are the values of the scoped conditions at the last observation, per scope and subject (ADR 0109
    /// §3): a scoped action runs for a subject pending there.
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub subjects: BTreeMap<String, Vec<goap_condition::Subject>>,
    #[serde(serialize_with = "nil_vec", deserialize_with = "null_default")]
    pub steps: Vec<Step>,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub vars: Map<String, Value>,
    /// Step is the step of a process of the parent that this process carries out (a sub-agent started by a call,
    /// ADR 0034): its guidance reaches the actions of this process.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub step: Option<StepContext>,
    /// Inbox holds the events a companion run received while it was at work, handled one after the other.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub inbox: Vec<Map<String, Value>>,
    /// Instance is the key of the persistent agent instance the process is ("<methodology>/<agent>@<scope>", ADR
    /// 0112). Closed: a run completed with the conditions that close it. Runs counts the executions after the first;
    /// Memory keeps the last exchanges of its llm actions with the model.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub instance: String,
    #[serde(skip_serializing_if = "is_false")]
    pub closed: bool,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub runs: i64,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub memory: Vec<Turn>,
    /// Disabled lists actions excluded from planning after repeatedly failing to deliver their effects.
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub disabled: BTreeMap<String, bool>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub created_at: DateTime<Utc>,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub updated_at: DateTime<Utc>,
    /// MethodologyVersion is the version of the methodology the process runs (ADR 0113), fixed when it starts.
    /// Empty: the latest.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology_version: String,
    /// JournalSeq numbers the execution journal records of the process.
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub journal_seq: i64,
    /// Started marks that the process.started journal record was written.
    #[serde(skip_serializing_if = "is_false")]
    pub started: bool,
}

impl Default for Process {
    fn default() -> Self {
        Process {
            id: String::new(),
            methodology: String::new(),
            change_id: ChangeID::default(),
            org: String::new(),
            project: String::new(),
            queued: None,
            initiator: Default::default(),
            agent: String::new(),
            planner: String::new(),
            parent_id: String::new(),
            trigger: String::new(),
            cause: String::new(),
            children: BTreeMap::new(),
            baseline_id: BaselineID::default(),
            namespace: String::new(),
            own_branch: false,
            flow: String::new(),
            relaunch_of: String::new(),
            from_step: 0,
            dismissed: BTreeMap::new(),
            title: String::new(),
            usage: Usage::default(),
            trace_id: String::new(),
            trace_parent: String::new(),
            status: Status::default(),
            goal: String::new(),
            intent: Session::default(),
            question: String::new(),
            candidates: Vec::new(),
            pending: None,
            plan: Vec::new(),
            world: WorldState::default(),
            unknown: BTreeMap::new(),
            subjects: BTreeMap::new(),
            steps: Vec::new(),
            vars: Map::new(),
            step: None,
            inbox: Vec::new(),
            instance: String::new(),
            closed: false,
            runs: 0,
            memory: Vec::new(),
            disabled: BTreeMap::new(),
            error: String::new(),
            created_at: gotime::zero(),
            updated_at: gotime::zero(),
            methodology_version: String::new(),
            journal_seq: 0,
            started: false,
        }
    }
}

/// Task kinds: a human action, submit items.
pub const TASK_INPUT: &str = "input";
/// An action needing a permission the initiator lacks.
pub const TASK_APPROVAL: &str = "approval";
/// A script action waiting for a sub-agent.
pub const TASK_AGENT: &str = "agent";
/// A relaunched flow is ready: a human adopts or discards it.
pub const TASK_FLOW: &str = "flow";
/// The blackboard is inconsistent; a human relaunches the step that produced the faulty content (Proposal) or
/// ignores the issues.
pub const TASK_BOARD: &str = "board";
/// The process waits for the decision of the flow relaunched to fix it.
pub const TASK_RELAUNCHED: &str = "relaunched";
/// No plan reaches the goal only because of conditions none of the agent's actions establish: the process waits for
/// them, and is tried again when the change moves.
pub const TASK_CONDITION: &str = "condition";
/// The process is stuck; whoever answers for it retries the actions it gave up on, or abandons it (Engine::unblock).
pub const TASK_UNBLOCK: &str = "unblock";
/// The change is committed on its own branch but its merge found conflicts: a person picks a side per property
/// (Engine::resolve_merge).
pub const TASK_MERGE: &str = "merge";
/// The apply of the change stopped because rebasing it left conflicts (Engine::resolve_rebase, ADR 0101).
pub const TASK_REBASE: &str = "rebase";

/// RebaseTask is what a person needs to settle after the rebase of a change found conflicts (ADR 0101).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct RebaseTask {
    /// Branch is the branch the change was rebased onto, Onto the head baseline or parent change it was rebased onto.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub branch: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub onto: String,
    #[serde(serialize_with = "nil_vec", deserialize_with = "null_default")]
    pub impacts: Vec<RebaseConflict>,
}

/// RebaseConflict is a change impact the rebase left conflicts on: their names are `props.<name>`, `owner`,
/// `state`, `link:<type>:<node>` and `node`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RebaseConflict {
    pub impact: String,
    pub key: String,
    #[serde(serialize_with = "nil_vec", deserialize_with = "null_default")]
    pub conflicts: Vec<String>,
}

/// MergeTask is what a person needs to settle the merge a change waits for (ADR 0056).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct MergeTask {
    pub namespace: String,
    /// From is the branch of the change, Into the branch it is merged into.
    pub from: String,
    pub into: String,
    #[serde(serialize_with = "nil_vec", deserialize_with = "null_default")]
    pub conflicts: Vec<MergeConflict>,
}

/// MergeConflict is a node both branches changed: only the properties changed on both sides, and differently, are
/// listed.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct MergeConflict {
    pub node: String,
    pub key: String,
    #[serde(skip_serializing_if = "is_false")]
    pub deleted: bool,
    #[serde(serialize_with = "nil_vec", deserialize_with = "null_default")]
    pub properties: Vec<MergeProperty>,
}

/// MergeProperty is one property of a conflict: the common ancestor's value, the target branch's (ours), the
/// change's (theirs) and the proposed one, the change's.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct MergeProperty {
    pub name: String,
    #[serde(skip_serializing_if = "Value::is_null")]
    pub base: Value,
    #[serde(skip_serializing_if = "Value::is_null")]
    pub ours: Value,
    #[serde(skip_serializing_if = "Value::is_null")]
    pub theirs: Value,
    #[serde(skip_serializing_if = "Value::is_null")]
    pub proposed: Value,
}

/// HumanTask is a pending human action or approval.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct HumanTask {
    pub kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub permission: String,
    /// Roles allowed to approve an action waiting for one of them (Permission PERMISSION_RUN_ACTION, ADR 0043).
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub roles: Vec<String>,
    pub action: String,
    pub description: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub instructions: String,
    /// NodeTypes restricts which qualified node types a TASK_INPUT may create or pick to edit.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub node_types: Vec<String>,
    /// Form is what the person fills in for a TASK_INPUT, when the action declares one (ADR 0115).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub form: Option<goap_form::Form>,
    pub step: i64,
    /// ChildProcessID is the sub-agent a TASK_AGENT waits for.
    #[serde(rename = "childProcessId", skip_serializing_if = "String::is_empty")]
    pub child_process_id: String,
    /// WakeOn lists signal names (TASK_AGENT) that, if emitted by the child and addressed to this process before it
    /// terminates, wake this process early.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub wake_on: Vec<String>,
    /// TASK_BOARD: what is wrong, and the earliest step to restart from (None when none can be).
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub issues: Vec<BoardIssue>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub proposal: Option<RelaunchProposal>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub merge: Option<MergeTask>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub rebase: Option<RebaseTask>,
    /// FlowID is the flow a TASK_RELAUNCHED waits for.
    #[serde(rename = "flowId", skip_serializing_if = "String::is_empty")]
    pub flow_id: String,
    /// Context is the step of a process the task belongs to (ADR 0034, ADR 0035 §2): what to do and how.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub context: Option<StepContext>,
    /// Conditions a TASK_CONDITION waits for, or that would unblock a TASK_UNBLOCK ("name" when expected true,
    /// "!name" when expected false).
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub conditions: Vec<String>,
}

impl Process {
    /// WaitsForConditions reports whether the process waits for conditions established outside it (TASK_CONDITION).
    pub fn waits_for_conditions(&self) -> bool {
        self.status == STATUS_WAITING
            && self
                .pending
                .as_ref()
                .is_some_and(|t| t.kind == TASK_CONDITION)
    }
}

/// Usage accounts LLM tokens and calls.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Usage {
    pub input_tokens: i64,
    pub output_tokens: i64,
    #[serde(rename = "llmCalls")]
    pub llm_calls: i64,
    pub tool_calls: i64,
}

impl Usage {
    /// Add accumulates u2.
    pub fn add(&mut self, u2: Usage) {
        self.input_tokens += u2.input_tokens;
        self.output_tokens += u2.output_tokens;
        self.llm_calls += u2.llm_calls;
        self.tool_calls += u2.tool_calls;
    }
}

/// LLMCall records one model call.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct LLMCall {
    pub provider: String,
    pub model: String,
    pub input_tokens: i64,
    pub output_tokens: i64,
    pub duration_ms: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    /// Exchange is what was sent and answered. It travels from the call to the journal and is not part of the
    /// process state.
    #[serde(skip)]
    pub exchange: Option<ModelExchange>,
}

/// exchange_of is the request and the answer of a call.
pub(crate) fn exchange_of(req: &goap_llm::Request, resp: &goap_llm::Response) -> ModelExchange {
    ModelExchange {
        system: req.system.clone(),
        response: resp.transcript(),
        behaviors: resp.behaviors.clone(),
        behavior_tokens: resp.behavior_tokens,
        behaviors_estimated: resp.behaviors_estimated,
        // tool calls and results as text (ADR 0098)
        messages: req
            .messages
            .iter()
            .map(|m| ModelMessage {
                role: m.role.clone(),
                content: m.transcript(),
            })
            .collect(),
        ..Default::default()
    }
}

/// ToolCall records one tool call.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ToolCall {
    pub name: String,
    pub duration_ms: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
}

/// LogLine is a process log line.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct LogLine {
    #[serde(with = "gotime", default = "gotime::zero")]
    pub time: DateTime<Utc>,
    pub level: String,
    pub message: String,
    #[serde(rename = "processId", skip_serializing_if = "String::is_empty")]
    pub process_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub action: String,
    pub step: i64,
}

impl Default for LogLine {
    fn default() -> Self {
        LogLine {
            time: gotime::zero(),
            level: String::new(),
            message: String::new(),
            process_id: String::new(),
            action: String::new(),
            step: 0,
        }
    }
}

/// Step records one executed action.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Step {
    pub index: i64,
    pub action: String,
    #[serde(serialize_with = "nil_vec", deserialize_with = "null_default")]
    pub plan: Vec<String>,
    #[serde(deserialize_with = "null_default", serialize_with = "nil_world")]
    pub before: WorldState,
    #[serde(skip_serializing_if = "world_empty", deserialize_with = "null_default")]
    pub after: WorldState,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub items: Vec<ItemID>,
    /// Nodes are the change impacts the step declared (ADR 0024).
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub nodes: Vec<ChangeImpactID>,
    /// Decisions are the decision points the step opened, ruled or answered a question of (ADR 0009 §4).
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub decisions: Vec<String>,
    /// Reads are the node versions referenced on the blackboard when the step started; BoardBefore / BoardAfter the
    /// item count of the change around it.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub reads: Vec<NodeRef>,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub board_before: i64,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub board_after: i64,
    /// LastItem is the last item of the flow when the step started (the fork point of a relaunch).
    #[serde(skip_serializing_if = "ItemID::is_empty")]
    pub last_item: ItemID,
    pub effects_met: bool,
    /// Subject is the subject a scoped action ran for (ADR 0109 §3.5), SubjectRef what the log names it by.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub subject: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub subject_ref: String,
    /// Progress: an incremental action produced items without reaching its effects yet.
    #[serde(skip_serializing_if = "is_false")]
    pub progress: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub approved_by: String,
    pub usage: Usage,
    #[serde(
        rename = "llmCalls",
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub llm_calls: Vec<LLMCall>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub tool_calls: Vec<ToolCall>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub logs: Vec<LogLine>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub children: Vec<String>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub sandbox: String,
    /// Specialization is the action actually run for an abstract action.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub specialization: String,
    /// Execution is the journal record of the step; SpanID its OpenTelemetry span.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub execution: String,
    #[serde(rename = "spanId", skip_serializing_if = "String::is_empty")]
    pub span_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub output: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub started_at: DateTime<Utc>,
    /// None while the step has not ended (Go's zero time).
    #[serde(with = "gotime::option")]
    pub ended_at: Option<DateTime<Utc>>,
}

pub(crate) fn world_empty(w: &WorldState) -> bool {
    w.0.is_empty()
}

fn nil_world<S: serde::Serializer>(w: &WorldState, s: S) -> std::result::Result<S::Ok, S::Error> {
    if w.is_empty() {
        s.serialize_none()
    } else {
        w.serialize(s)
    }
}

impl Default for Step {
    fn default() -> Self {
        Step {
            index: 0,
            action: String::new(),
            plan: Vec::new(),
            before: WorldState::default(),
            after: WorldState::default(),
            items: Vec::new(),
            nodes: Vec::new(),
            decisions: Vec::new(),
            reads: Vec::new(),
            board_before: 0,
            board_after: 0,
            last_item: ItemID::default(),
            effects_met: false,
            subject: String::new(),
            subject_ref: String::new(),
            progress: false,
            approved_by: String::new(),
            usage: Usage::default(),
            llm_calls: Vec::new(),
            tool_calls: Vec::new(),
            logs: Vec::new(),
            children: Vec::new(),
            sandbox: String::new(),
            specialization: String::new(),
            execution: String::new(),
            span_id: String::new(),
            output: String::new(),
            error: String::new(),
            started_at: gotime::zero(),
            ended_at: None,
        }
    }
}

impl Step {
    /// Pending reports whether the step has not ended yet (waiting for a human).
    pub fn pending(&self) -> bool {
        self.ended_at.is_none()
    }

    /// drop_exchanges forgets the prompts of the calls of the step once the journal holds them: the process keeps
    /// the counts, not the text.
    pub(crate) fn drop_exchanges(&mut self) {
        for c in &mut self.llm_calls {
            c.exchange = None;
        }
    }
}

/// Store persists processes.
#[async_trait]
pub trait Store: Send + Sync {
    async fn get(&self, ctx: &Ctx, id: &str) -> Result<Process>;
    async fn put(&self, ctx: &Ctx, p: &Process) -> Result<()>;
    async fn list(&self, ctx: &Ctx) -> Result<Vec<Process>>;
    /// AppendProcessLog records entries of a process's own log (ADR 0031): turn-by-turn state that would otherwise
    /// force a full rewrite of the process row, independent of any change.
    async fn append_process_log(
        &self,
        ctx: &Ctx,
        process_id: &str,
        entries: Vec<ProcessLogEntry>,
    ) -> Result<()>;
    async fn list_process_log(&self, ctx: &Ctx, process_id: &str) -> Result<Vec<ProcessLogEntry>>;
    /// orphan_finder is the store as an OrphanFinder (ADR 0124), when it tells the processes nobody holds: Go's
    /// `Store.(OrphanFinder)` assertion.
    fn orphan_finder(&self) -> Option<&dyn crate::locks::OrphanFinder> {
        None
    }
}

/// ProcessLogEntry is one append-only entry of a process's own log.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ProcessLogEntry {
    pub seq: i64,
    #[serde(rename = "processId")]
    pub process_id: String,
    pub r#type: String,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub payload: Map<String, Value>,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub at: DateTime<Utc>,
}

impl Default for ProcessLogEntry {
    fn default() -> Self {
        ProcessLogEntry {
            seq: 0,
            process_id: String::new(),
            r#type: String::new(),
            payload: Map::new(),
            at: gotime::zero(),
        }
    }
}

/// MemoryStore is an in-memory Store.
#[derive(Default)]
pub struct MemoryStore {
    #[allow(clippy::type_complexity)]
    m: RwLock<(
        HashMap<String, Process>,
        HashMap<String, Vec<ProcessLogEntry>>,
    )>,
}

impl MemoryStore {
    /// NewMemoryStore returns an empty store.
    pub fn new() -> MemoryStore {
        MemoryStore::default()
    }
}

#[async_trait]
impl Store for MemoryStore {
    async fn append_process_log(
        &self,
        _: &Ctx,
        process_id: &str,
        entries: Vec<ProcessLogEntry>,
    ) -> Result<()> {
        let mut g = self.m.write();
        let log = g.1.entry(process_id.to_string()).or_default();
        for mut e in entries {
            e.process_id = process_id.to_string();
            e.seq = log.len() as i64 + 1;
            log.push(e);
        }
        Ok(())
    }

    async fn list_process_log(&self, _: &Ctx, process_id: &str) -> Result<Vec<ProcessLogEntry>> {
        Ok(self.m.read().1.get(process_id).cloned().unwrap_or_default())
    }

    async fn get(&self, _: &Ctx, id: &str) -> Result<Process> {
        self.m
            .read()
            .0
            .get(id)
            .cloned()
            .ok_or_else(|| Error::not_found(id))
    }

    async fn put(&self, _: &Ctx, p: &Process) -> Result<()> {
        self.m.write().0.insert(p.id.clone(), p.clone());
        Ok(())
    }

    async fn list(&self, _: &Ctx) -> Result<Vec<Process>> {
        let mut out: Vec<Process> = self.m.read().0.values().cloned().collect();
        out.sort_by_key(|p| std::cmp::Reverse(p.created_at));
        Ok(out)
    }
}

/// RelaunchProposal is the step to restart from to fix an inconsistent blackboard: the earliest step, over every run
/// of the change, that produced faulty content.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RelaunchProposal {
    pub process: String,
    pub step: i64,
    pub action: String,
    pub reason: String,
    /// Culprits are the faulty items the step produced.
    #[serde(serialize_with = "nil_vec", deserialize_with = "null_default")]
    pub culprits: Vec<ItemID>,
}

/// Queued is why and by whom a process was made runnable, and what caused it.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Queued {
    /// Reason: started, sub-agent, trigger, answered, input, approved, rejected, relaunched, sub-agent-ended,
    /// board-ignored, flow-adopted, flow-discarded.
    pub reason: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub at: DateTime<Utc>,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub cause: Map<String, Value>,
}

impl Default for Queued {
    fn default() -> Self {
        Queued {
            reason: String::new(),
            by: String::new(),
            at: gotime::zero(),
            cause: Map::new(),
        }
    }
}
