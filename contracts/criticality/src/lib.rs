//! The use case of the criticality of a change (ADR 0075
//! §3): the weight of its effect, C1 (low), C2 (current) or C3 (critical), kept in Change.data
//! (domain::DATA_CRITICALITY; the graph never reads it) and the policy of the organisation that says what each level
//! requires: the kinds of oracle that may verify an effect, whether a review may be sampled, the role of whoever signs
//! a derogation and how long a derogation may last.
//!
//! The methodology names a default level (Methodology.criticality), the requester may raise it, only a permission
//! lowers it (ABAC change:lower-criticality, administrators by default). What a level requires is organisation data,
//! resolved along the unit chain with the nearest unit winning (goap-access, design rule 3); [`defaults`] is the table
//! that applies where the organisation gives none. The graph and the domain model import nothing from here
//! (`platform/layering`).

mod criticality;
mod goduration;

pub use criticality::*;
pub use goduration::go_duration_string;
