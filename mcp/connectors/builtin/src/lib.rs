//! Port of `mcp/connectors/builtin` (see docs/rust-port.md).
//!
//! Package builtin holds the connectors of the platform itself (ADR 0028): the drivers of the built-in MCPs
//! goap-graph, goap-change, goap-scheduler and goap-admin. Like a harness that ships its own tools next to the ones
//! users plug in, the hub serves them in-process (endpoint inproc://<id>) and they register like any connector; the
//! default organisation holds an adapter instance of each, so that every unit can use them until it restricts them.
//!
//! They call the platform's own APIs for the caller: the principal of the context (the initiator of the process) is
//! checked by the hub (tool:call), then by the APIs they reach (the engine for processes, the access gate for User
//! and Policy nodes, read authorization per node type). An anonymous call is refused. The call context
//! (`goap_mcp::call_from`) gives the unit and the change the call runs for: goap-change works on the change of the
//! calling process by default.

mod admin;
mod builtin;
mod change;
mod change_review;
mod graph;
mod pathmatch;
mod scheduler;

pub use admin::{Admin, Domains, Hub};
pub use builtin::{
    DEFAULT_LIMIT, EngineAPI, Error, Methodologies, Ports, VERSION, connectors, forward_identity,
};
pub use change::{Change, INTENT_ITEM_TYPE, latest_intent_item};
pub use change_review::ReviewPort;
pub use graph::Graph;
pub use scheduler::Scheduler;

#[cfg(test)]
mod tests;
