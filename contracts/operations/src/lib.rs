//! Port of `contracts/operations` (see docs/rust-port.md).
//!
//! The catalogue of the operations a person performs on a change (ADR 0103): checking a node out, editing its draft,
//! withdrawing an impact, reviewing it, committing and merging the change. The list comes from the code and is fixed:
//! each operation is a method of the graph annotated with `goap:operation <code> <kind>`, and goap-opgen
//! (`platform/opgen`) generates `operations_gen.rs` from those annotations, refusing a duplicate code. A methodology's
//! roles name the operations they allow (methodology's `Role.operations`), by code or by kind; the graph asks its
//! operation authorizer before each one. A leaf crate: no project import.

mod operations;
mod operations_gen;

pub use operations::*;
pub use operations_gen::*;
