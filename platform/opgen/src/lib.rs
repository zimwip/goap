//!
//! Generates the catalogue of operations (`contracts/operations`, ADR 0103) from the `goap:operation <code> <kind>`
//! annotations of the methods of the graph. It refuses a malformed annotation, an unknown kind, a code declared twice
//! (or two codes giving the same constant), an annotation on anything but an exported method of the graph, an
//! annotated method that does not ask for its operation (`allow(ctx, id, operations::<CONST>)`) and an `allow` naming
//! an operation no annotation declares.
//!
//! While `graph/graph` is still Go, the annotations are read from its Go sources (`//goap:operation`); once its Rust
//! sources carry them (`/// goap:operation` on a `pub fn` of `impl Graph`), those are read instead.

mod golang;
mod opgen;
mod rust;

pub use opgen::*;
