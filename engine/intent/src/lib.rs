//! Package intent implements the intent loop run before planning: a user
//! request is ranked against the goals of a methodology until one goal is
//! selected with enough confidence, asking clarification questions otherwise.
//!

mod intent;
mod rankers;

pub use intent::*;
pub use rankers::*;
