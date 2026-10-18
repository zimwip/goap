//! The definition of a domain (ADR 0060): its node and link types, attributes, enums, lifecycles, algorithms and
//! item types, the built-in domains, and the issues of a validation. `goap-methodology`, `goap-typecat`, the graph and
//! the services import it; a domain knows no methodology.
//!
//! A domain is read from YAML (or JSON) with [`parse_domain`], which
//! refuses an unknown field as yaml.v3's `KnownFields(true)` does.

mod algorithms;
mod builtin;
mod domain;
mod error;
mod itemtype;
mod model;
mod record;
mod schema;
mod serde_util;

pub use builtin::{builtin_domains, is_builtin_domain};
pub use domain::{Domain, DomainTypeSet, domain_types, load_domains, parse_domain};
pub use error::Error;
pub use itemtype::{ITEM_TYPE_NAME_RE, ItemType};
pub use model::*;
pub use record::{
    Record, STATUS_ARCHIVED, STATUS_DRAFT, STATUS_PUBLISHED, Status, in_force, latest,
};
pub use schema::Schema;
