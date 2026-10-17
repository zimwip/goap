//! Port of `contracts/verify` (see docs/rust-port.md): the use case of independent verification (ADR 0075): a result
//! produced is not a result accepted. An action declares how its effect is verified (methodology Action.verify: the
//! kind of oracle, and that the verifier is not the producer); the engine records the states of what an action run
//! produced as facts of the change (fact.verification, ADR 0030), folded here; [`Policy`] is the rule the graph asks
//! before a review (domain::ReviewPolicy, as goap-decision is the policy of the decision points, ADR 0067): the
//! verifier is not the producer.
//!
//! The states, per effect (a change impact) of an action run: produced (the action ended), verified (its oracle
//! judged), then accepted, accepted_with_reserve (only with a derogation in force, goap-risk) or rejected (the review
//! settled). The graph and the domain model import nothing from here (`platform/layering`); the composition roots
//! plug the policy on the graph, explicitly (Graph.review_policy), and call [`register`] where such items are written
//! or read.
//!
//! Data of a verification item: state, action (the producing action), impact (the change impact, one per item except
//! on produced), impacts (the change impacts a produced item covers), oracle, independent (the verifier must differ
//! from the producer), model (the alias of a model producer, when known), by (the verifier).

mod policy;
mod verify;

pub use policy::*;
pub use verify::*;
