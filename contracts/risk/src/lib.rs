//! Port of `contracts/risk` (see docs/rust-port.md): the risk register of a change (ADR 0036 §1, ADR 0065): risks,
//! actions and derogations are items of the change whose types this crate registers ([`register`]), and the free
//! functions [`risks`], [`actions`] and [`derogations`] fold them. It is a use case: the graph and the domain model
//! know nothing of it.

mod derogation;
mod register;
mod risk;

pub use derogation::*;
pub use register::*;
pub use risk::*;

/// An error of the crate.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    /// A value that does not hold (an expiry that does not parse): the message.
    #[error("{0}")]
    Invalid(String),
}
