//! Port of `mcp/connectors/claudeagent` (see docs/rust-port.md).
//!
//! Package claudeagent is the connector of the claude-coding-agent MCP (ADR 0123): a Claude Code session, run by the
//! Claude Agent SDK in the workspace of the change, that a methodology hands a coding task to and resumes.
//!
//! The SDK exists in TypeScript, not in Rust: each call starts the runner (`runner/runner.mjs`, embedded and written
//! into the connector's directory next to its `package.json`, the SDK installed there by `npm install`), gives it one
//! JSON request on its standard input and reads JSON lines from its output, the last one the outcome of the turn. The
//! session works in the directory of the workspace of the change (the workspace connector's, under the same root),
//! with a cleaned environment, the unit's API key and the repository's Claude Code settings; it runs with the rights
//! of the connector's host, like the local workspace commands.

mod claudeagent;

pub use claudeagent::*;

#[cfg(test)]
mod claudeagent_test;
