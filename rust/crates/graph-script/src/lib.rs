//! The scripts and expressions a domain runs (ADR 0018), as the Go service runs them (`lib/dsl`, `lib/guard`): the
//! JavaScript algorithms of its validators and lifecycle transitions on QuickJS ([`js`]), the CEL guards of its
//! transitions ([`guard`]), the conditions of a methodology ([`conditions`]).
pub mod abac;
pub mod conditions;
pub mod guard;
pub mod js;
