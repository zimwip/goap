//!
//! The schemas of the built-in MCPs (ADR 0028, 0061): the platform itself as tools. It builds MCP definitions
//! (`goap-mcp`) and the adapter side that gives them to every unit (`goap-adapter`); neither of them depends on it.

mod builtin;
mod claudeagent;
mod workspace;

pub use builtin::{ADMIN, CHANGE, GRAPH, NAMES, SCHEDULER, adapter, adapter_defs, defs, is};
pub use claudeagent::*;
pub use workspace::*;

#[cfg(test)]
mod builtin_test;
