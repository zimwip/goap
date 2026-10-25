//! The type catalogue of the Rust graph service (ADR 0163): the definition of a domain (lib/def) and the types of the
//! domains in force resolved (lib/typecat), and a domain as `ImportDomain` reads it (`imported`): the domain registry
//! writes the domains, the graph judges its nodes by them.
pub mod algo;
pub mod catalog;
pub mod decode;
pub mod def;
pub mod form;
pub mod imported;
pub mod record;
pub mod shape;
pub mod validate;
pub mod yamlv3;

pub use catalog::{Catalog, Structure, Type};
pub use def::Domain;
