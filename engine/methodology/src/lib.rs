//! The declarative definition of a methodology, its compile
//! pipeline and the compiled form the engine runs.

mod assessment;
mod compile;
mod composition;
mod cron;
mod domain;
mod event;
mod hints;
mod integrate;
mod issues;
mod levels;
mod loops;
mod method;
mod methodology;
mod persist;
mod process;
mod processgraph;
mod reaction;
mod refs;
mod review;
mod specialize;
mod util;
mod view;

#[cfg(test)]
mod golden_test;

pub use assessment::*;
pub use compile::CompileOptions;
pub use composition::{Composition, LifecycleOf};
pub use domain::{BuiltinSet, load_file};
pub use event::*;
pub use hints::missing;
pub use integrate::*;
pub use levels::*;
pub use loops::*;
pub use method::*;
pub use methodology::*;
pub use persist::*;
pub use process::*;
pub use processgraph::*;
pub use reaction::*;
pub use refs::*;
pub use review::*;
pub use specialize::*;
pub use util::Conds;
pub use view::*;

#[cfg(test)]
mod tests;
