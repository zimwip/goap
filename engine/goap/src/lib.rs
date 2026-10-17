//! Package goap implements a Goal Oriented Action Planner (A* over boolean
//! world states), in the spirit of Embabel's planner.
//!
//! Port of `engine/goap` (see docs/rust-port.md).

mod planner;

pub use planner::*;
