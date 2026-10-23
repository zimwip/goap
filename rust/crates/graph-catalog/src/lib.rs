//! The type catalogue of the Rust graph service (ADR 0162): the definition of a domain (lib/def) and the types of the
//! domains in force resolved (lib/typecat), read-only: the domain registry writes the domains, the graph judges its
//! nodes by them.
pub mod catalog;
pub mod def;
pub mod record;

pub use catalog::{Catalog, Structure, Type};
pub use def::Domain;
