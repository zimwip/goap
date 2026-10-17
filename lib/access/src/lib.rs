//! Who may do what as graph data (organisation namespace): User nodes (profile, membership of a unit through
//! member_of), Policy nodes (the ABAC rules), Assignments (the roles a unit or a user holds on a project, or
//! platform-wide) and CriticalityPolicy nodes. A [`Directory`] reads them into a [`Snapshot`] of the head of the
//! organisation; an [`Authorizer`] built on it evaluates them with Casbin (`goap-authz`); the compiled-in floor keeps
//! administrators in whatever the stored policies say. The [`Session`] is what the web learns of its caller (ADR 0070).
//!
//! Port of `lib/access` (see docs/rust-port.md).

#[allow(clippy::module_inception)]
mod access;
mod criticality;
mod floor_validator;
mod personal;
mod project;
mod retired;
mod roles;
mod session;

pub use access::*;
pub use criticality::*;
pub use floor_validator::AdminFloorValidator;
pub use personal::*;
pub use project::{
    ASSIGNMENT_PREFIX, Assignment, DEFAULT_PROJECT, LINK_ASSIGNS_ORG, LINK_ASSIGNS_PROJECT,
    LINK_PROJECT_PART_OF, NODE_TYPE_ASSIGNMENT, NODE_TYPE_PROJECT_UNIT, PLATFORM_SCOPE,
    ProjectUnit, assignment_from_props, assignment_key, platform_assignment_key,
    project_from_props,
};
pub use retired::{STATE_RETIRED, in_force};
pub use roles::*;
pub use session::*;

/// The errors of the package.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// The properties of a node do not read (`user without subject`, `assignment: roles must be a list of strings`).
    #[error("{0}")]
    Invalid(String),
    /// `ErrNoSession` (joined with the error of the graph, as Go's `errors.Join`).
    #[error("{}", match .0 { Some(e) => format!("{NO_SESSION}\n{e}"), None => NO_SESSION.to_string() })]
    NoSession(Option<goap_graphapi::Error>),
    #[error(transparent)]
    Authz(#[from] goap_authz::Error),
}

impl Error {
    /// `errors.Is(err, ErrNoSession)`.
    pub fn is_no_session(&self) -> bool {
        matches!(self, Error::NoSession(_))
    }
}
