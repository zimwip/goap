//! The scripts and expressions a domain runs (ADR 0018), as the Go service runs them (`lib/dsl`, `lib/guard`): the
//! JavaScript algorithms of its validators and lifecycle transitions on QuickJS ([`js`]), the CEL guards of its
//! transitions ([`guard`]).
pub mod abac;
mod casbin;
pub mod celcheck;
mod goregexp;
mod gotime;
mod govaluate;
pub mod guard;
pub mod js;

pub use govaluate::go_float;
