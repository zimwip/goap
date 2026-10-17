//!
//! Package observe analyzes the execution journal of agent processes (and their OpenTelemetry spans) to find what
//! makes a methodology costly, and turns the findings into improvement proposals on the methodology model (ADR 0011):
//! specializations, agent definitions, new actions, MCP tools.

mod analyze;
mod improve;
mod util;

pub use analyze::*;
pub use improve::*;
