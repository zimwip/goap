//! The execution engine. A process runs an agent of a methodology
//! on a change: intent loop, then observe / plan / act cycles over the blackboard of the change until the goal
//! holds. The graph is reached only through `goap_graphapi::Port` (`GraphPort`); who and where through `Scope`.

pub mod asks;
pub mod assessmentstates;
pub mod awaiting;
pub mod board;
pub mod broker;
pub mod cancel;
pub mod changestate;
pub mod companion;
pub mod condense;
pub mod cron;
pub mod cycle;
pub mod decisions;
pub mod derogation;
pub mod engine;
pub mod error;
pub mod eventing;
pub mod executors;
pub mod flow;
pub mod foreach;
pub mod form;
pub mod gotemplate;
pub mod history;
pub mod host;
pub mod internals;
pub mod items;
pub mod journal;
pub mod locks;
pub mod merge;
pub mod nodeops;
pub mod oversight;
pub mod persistent;
pub mod planners;
pub mod ports;
pub mod preview;
pub mod process;
pub mod progress;
pub mod rebase;
pub mod resume;
pub mod scope;
pub mod scoped;
pub(crate) mod serde_util;
pub mod startingpoints;
pub mod steps;
pub mod tracer;
pub mod triggers;
pub mod unblock;

pub use asks::*;
pub use broker::*;
pub use changestate::{ChangeState, Fact, Need, NeedState, ProcessState, StepState};
pub use companion::{COMPANION_PREFIX, StepEvent};
pub use engine::*;
pub use error::*;
pub use eventing::*;
pub use executors::*;
pub use host::*;
pub use internals::*;
pub use items::*;
pub use locks::*;
pub use persistent::Turn;
pub use ports::*;
pub use preview::*;
pub use process::*;
pub use progress::{
    Lane, ProcessProgress, STEP_ACTIVE, STEP_BLOCKED, STEP_DONE, STEP_READY, STEP_SKIPPED,
    STEP_TODO, STEP_WAITING, StepProgress,
};
pub use scope::*;
pub use startingpoints::*;
pub use steps::*;
pub use tracer::*;
pub use triggers::*;
pub use unblock::*;

#[cfg(test)]
mod tests;
