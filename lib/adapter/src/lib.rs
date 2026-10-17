//! Port of `lib/adapter` (see docs/rust-port.md).
//!
//! The model of the adapter, the one place where organisation, MCP and connector meet (ADR 0019, 0061).
//!
//! - [`Def`]: code that implements the tools an MCP expects with the operations a connector exposes: an AdapterDef
//!   node of the platform namespace (changed through a change), its parameters typed like those of `goap-algo`, plus
//!   the secret references only an adapter has.
//! - [`Instance`]: the instance of a Def by an organisational unit, with its parameter values and the restrictions it
//!   puts on the MCP (Adapter node of the organisation namespace).
//!
//! It depends on the definition of the MCP (`goap-mcp`) and the generic algorithm model (`goap-algo`); neither knows
//! it. The scripts run through `goap-dsl`.

mod adapter;
mod restrict;
mod template;

pub use adapter::*;
pub use restrict::*;
pub use template::*;
