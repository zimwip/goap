//! Port of `contracts/authz` (see docs/rust-port.md): the identity of callers and the decision of access with
//! attribute-based rules (ABAC) evaluated by Casbin. Policies are Policy nodes of the graph (ADR 0020, read by
//! `lib/access`); this crate holds the model, the enforcer and the request types shared by every service.

mod authz;
mod casbin;

pub use self::authz::*;
pub use self::casbin::*;
