//!
//! Names the events a trigger or a transverse methodology can react to (ADR 0036 §3), the one list the engine, the
//! graph service and the methodology compiler share. It imports nothing of the project.

mod events;

pub use events::*;
