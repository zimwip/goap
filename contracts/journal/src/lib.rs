//! The execution journal of a change (ADR 0011, 0030, 0059): the
//! records of what the agent processes do on it (every tick of the observe / plan / act loop, every action run with
//! its model and tool calls, every human decision) and the prompts of the LLM calls. It is a use case of the log of a
//! change: the graph holds a generic typed log (`domain::LogEntry`, `Graph::append_log` / `change_log`) and knows
//! nothing of what is in it; this crate turns a record into the entries of the log and back, caps the prompts and
//! says what a valid record is. The graph and the domain model import nothing of it (`platform/layering`).
//!
//! The history of a change (ADR 0109 §4, [`fold`]) is a pure fold of the log of the change.

mod history;
mod journal;

pub use history::*;
pub use journal::*;
