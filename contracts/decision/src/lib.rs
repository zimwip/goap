//! Port of `contracts/decision` (see docs/rust-port.md): the policy of the decision points of a change (ADR 0067):
//! who decides, how sure an agent must be to settle a point alone, how many rulings may fail before a person takes
//! over, and by when. The graph core keeps the mechanism (open, rule, answer, ratify, the status machine); it stores
//! the opaque policy values the opener gives and asks a domain::DecisionPolicy. The composition roots plug [`Policy`]
//! on the graph (Graph.decision_policy), explicitly.
//!
//! The values of a point, by key (the opener gives some, open resolves them):
//!
//! ```text
//! decider      "agent" (default) or "human": a point of a human decider is ruled by a person only
//! threshold    confidence under which an agent ruling waits for a person's ratification (default 0.7)
//! maxRounds    rulings that may fail to settle the point (undecidable, or a ratification refused) before it is
//!              escalated (default 3; negative: no limit)
//! maxDuration  the opener's duration ("24h", a Go duration string, or an integer of nanoseconds: Go's
//!              time.Duration): becomes the deadline, past which the point is escalated (none: no deadline)
//! ```
//!
//! and, kept by the policy as it folds the events, rounds (the rulings that failed to settle the point) and deadline
//! (RFC 3339).

mod decision;
mod duration;

pub use decision::*;
pub use duration::*;

/// An error of the crate.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    /// A value that does not hold: the message (Go's text).
    #[error("{0}")]
    Invalid(String),
}
