//! The definition of an MCP, the generic usage of a tool by an LLM: a
//! name and tool signatures (document-repository: list, read, write), with no infrastructure dependency (ADR 0019).
//! It knows no connector, no adapter and no organisation: the adapter, where they meet, is `lib/adapter` (ADR 0061);
//! the schemas of the built-in MCPs are in `lib/mcpbuiltin`.

mod call;
mod mcp;

pub use call::*;
pub use mcp::*;
