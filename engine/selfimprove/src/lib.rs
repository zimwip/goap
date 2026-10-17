//! Port of `engine/selfimprove` (see docs/rust-port.md).
//!
//! Package selfimprove registers the builtins of the self-observation methodology (ADR 0011, ADR 0062): they analyze
//! a finished run, propose improvements and draft a new methodology version. They live outside the engine, which
//! knows no observer; the composition roots register them.

mod selfimprove;

pub use selfimprove::*;
