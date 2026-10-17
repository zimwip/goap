//! Port of `graph/graphsvc` (see docs/rust-port.md): the graph exposed over Connect (`graph.v1.GraphService`), the
//! platform bootstrap and its seeds (ADR 0071), the users seen by the platform (ADR 0039, 0040, 0042), the
//! authorizers and gates the compositions plug on the graph, the staged settings (ADR 0122) and the history facet
//! (ADR 0109).
//!
//! - [`Handler`]: implements `goap_gen::graph::v1::GraphService`; its interceptors [`Handler::identify`],
//!   [`Handler::ensure_caller`] and [`Handler::personal_scope`] go first in the chain of the router.
//! - [`boot`] and [`Options`] / [`ModelsConfig`] / [`Report`]; the seeds ([`seed_access`], [`seed_builtins`],
//!   [`seed_models`], [`seed_protected_aliases`], [`seed_behaviors`], [`seed_change`], [`seed_node`], ...).
//! - [`ensure_user`], [`new_user_unit`], [`SYSTEM_PRINCIPAL`] / [`system`].
//! - [`transition_authorizer`], [`change_transition_authorizer`], [`item_authorizer`], [`operation_authorizer`],
//!   [`project_move_gate`]: the seams of the graph.
//! - [`log_facets`]: the history facet of the blackboards.

mod boot;
mod demo;
mod handler;
mod history;
mod lifecycle;
mod move_gate;
mod okf_export;
mod personal;
mod seed_defaults;
mod settings;

pub use boot::{BoxError, BoxFuture, DevFn, ModelsConfig, Options, Report, boot, dev_fn};
pub use demo::{DemoFn, demo_fn};
pub use handler::{Handler, OkfTypes, Publisher, caller};
pub use history::{HistoryFold, log_facets};
pub use lifecycle::{
    OperationRoles, change_transition_authorizer, item_authorizer, operation_authorizer,
    transition_authorizer,
};
pub use move_gate::project_move_gate;
pub use personal::{
    EnsureCaller, Identify, OWNER_ME, PersonalScope, SYSTEM_PRINCIPAL, ensure_user, new_user_unit,
    system,
};
pub use seed_defaults::{
    seed_access, seed_adapter, seed_adapter_def, seed_behaviors, seed_builtins, seed_change,
    seed_criticality_policy, seed_models, seed_node, seed_policy, seed_protected_aliases,
    seed_unit, seed_user,
};
