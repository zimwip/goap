//! Port of `contracts/assess` (see docs/rust-port.md): the assessment of what a person wrote on a change (ADR 0110):
//! an action rates a subject (the description of the change) with an item of type assessment, and the person
//! discusses the rating with items of type comment, the thread of the subject. It is a use case like goap-risk: the
//! graph and the domain model know nothing of it, the conditions derive from it (the CEL variable assessments, the
//! library assessments of goap-condition).

mod assess;
mod canonical;

pub use assess::*;
