//! Package brief renders what a model needs to know of a change in few tokens (ADR 0036 §2): one line per fact, no
//! JSON, bounded, the most important first; and traces an information through the change (where it comes from, what
//! it led to).

mod brief;

pub use brief::*;
