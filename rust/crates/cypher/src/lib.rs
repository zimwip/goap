//! The read-only subset of openCypher the graph answers (ADR 0160), as Go's `lib/cypher` reads it: a query is parsed
//! into the small tree of [`ast`], refusing what the subset leaves out; expressions are evaluated by [`eval::eval`]
//! over values and the entities a caller binds; [`project::project`] computes RETURN. Matching a pattern is the
//! caller's (the graph's query, over a view), EXISTS included: [`eval::Scope::exists`].
pub mod ast;
pub mod eval;
pub mod lex;
pub mod parse;
pub mod project;

pub use parse::parse;
