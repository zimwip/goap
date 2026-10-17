//! The conversational assistant (ADR 0087).
//!
//! It answers the messages a person writes in a conversation (ADR 0085) with the model of the `assistant` alias, and
//! may do a few things for them through its server tools, no more (ADR 0090 added the agents), and asks the interface
//! to act on the screen they are on (ADR 0092: its tools are handed back, never run here). Each turn follows its frame
//! (frame.rs): the active project, the change the person is looking at and its methodology, computed from the context
//! and the history; the tools offered and the prompt are those of the frame, and a change is created only once the
//! open changes were searched (search_changes). It is a use case over the graph, the model gateway and the
//! conversation service: the graph and the engine do not depend on it.

mod agents;
mod compact;
mod context;
mod engine;
mod frame;
mod handler;
mod methodquery;
mod methodtool;
mod prompt;
mod relate;
mod searchtool;
mod service;
mod toolspecs;
mod trace;
mod turn;
mod uitools;
mod util;

#[cfg(test)]
mod tests;

pub use agents::{
    ACTION_START_AGENT, ConfirmInput, DECISION_ACCEPT, DECISION_REJECT, STATUS_FAILED,
    STATUS_PROPOSED, STATUS_REJECTED, STATUS_STARTED, STATUS_STARTING, TOOL_LIST_AGENTS,
    TOOL_START_AGENT,
};
pub use compact::COMPACT_BATCH;
pub use context::{
    App, Context, Dialog, Element, Entity, Focus, MAX_ENTITIES, MAX_ERRORS, Screen, Tab,
};
pub use engine::{
    AgentChecks, Engine, EngineApi, EngineClient, Point, Points, ProcessInfo, StartProcess, ViewApi,
};
pub use frame::{Frame, Level};
pub use handler::{Handler, rpc_err};
pub use methodquery::{
    DEFAULT_QUERY_LIMIT, DETAIL_FULL, DETAIL_NAMES, DETAIL_SUMMARY, ELEMENT_KINDS, KIND_ACTION,
    KIND_AGENT, KIND_CONDITION, KIND_GOAL, KIND_LIBRARY, KIND_METHOD, KIND_PROCESS, KIND_ROLE,
    KIND_STEP, KIND_TRIGGER, MAX_FULL_ITEMS, MAX_QUERY_BYTES, MAX_QUERY_LIMIT, NextPage, Query,
    QueryResult, run_query,
};
pub use methodtool::TOOL_METHODOLOGY_QUERY;
pub use relate::{
    ACTION_ATTACH_CHANGE, ACTION_DETACH_CHANGE, TOOL_ATTACH_CHANGE, TOOL_DETACH_CHANGE,
};
pub use searchtool::{ACTION_SEARCH_CHANGES, TOOL_SEARCH_CHANGES};
pub use service::{
    ALIAS, BoxError, Clock, Directory, Error, Graph, MAX_CONTEXT_BYTES, MAX_HISTORY_BYTES,
    MAX_HISTORY_MESSAGES, MAX_ROUNDS, MAX_SELECTION_BYTES, MAX_TEXT_BYTES, MAX_TOOL_CALLS,
    Methodologies, Model, PRINCIPAL, PortGraph, Projects, Runner, STALE_AFTER, SendInput, Service,
    TURN_TIMEOUT, TurnFuture,
};
pub use turn::{
    TOOL_CREATE_CHANGE, TOOL_LIST_METHODOLOGIES, TOOL_OPEN_CHANGE, TOOL_SELECT_PROJECT, tools,
};
pub use uitools::{
    ACTION_UI_TOOL, LEVEL_EFFECT, LEVEL_WRITE, MAX_EFFECTS_PER_ANSWER, MAX_PROPOSALS_PER_ANSWER,
    MAX_UI_TOOLS, PREFIX_UI, ReportInput, STATUS_ACCEPTED, STATUS_DONE, STATUS_REQUESTED, UIArgs,
    UIParam, UITool,
};
