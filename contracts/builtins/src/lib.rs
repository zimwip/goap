//!
//! Names the builtin actions of the platform (action kind `builtin`, ADR 0062): the names a methodology may use,
//! known to the compiler and the registry without an engine, and implemented by the engine (generic ones) or by the
//! packages that register their own (self-improvement). A leaf crate: no project import.

mod builtins;

pub use builtins::*;
