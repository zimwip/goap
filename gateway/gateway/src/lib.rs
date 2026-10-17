//! Port of `gateway/gateway` (see docs/rust-port.md): the single entry point: authentication, CORS and routing of
//! Connect calls to the services (echo → axum).
//!
//! - [`Config`], [`mount`] (the whole gateway on an axum `Router`), [`mount_auth_endpoints`] (the sign-in endpoints
//!   alone), [`authenticator`] (the middleware turning a request's credentials into the identity headers), [`prepare`]
//!   (a configuration whose endpoints and authenticator share the state of the sessions).
//! - [`Mode`]: the authentication strategies (ADR 0068), `none`, `hs256` and `local`, found by name
//!   ([`lookup_mode`], [`register_mode`]).
//! - [`Credentials`]: what the local mode needs of the credentials service (ADR 0040, 0045).
//! - `GET /api/status` ([`PlatformStatus`]).
//!
//! Differences from the Go package: a mode's `Routes(e, cfg)` returns its routes ([`AuthRoute`]) for the caller to
//! add, and `Authenticate` takes the headers of the request (which it may change) rather than an echo context; the
//! callbacks of [`Config`] are async closures ([`on_sign_in_fn`], [`project_access_fn`], [`enrich_fn`]); the
//! credentials report errors as boxed errors, a [`goap_connect::ConnectError`] of code `already_exists` making a
//! register a 409 as in Go; the reverse proxy is a small one over `reqwest` (no `X-Forwarded-For`, no OpenTelemetry
//! transport).

mod gateway;
mod mode;
mod status;

#[cfg(test)]
mod tests;

pub use gateway::{
    Authenticator, BoxError, Claims, Config, Credentials, DEFAULT_AUTH_MODE, DEFAULT_MAX_SESSION,
    DEFAULT_SESSION_CHECK_TTL, DEFAULT_TOKEN_TTL, Enrich, Error, HEADER_ORG, HEADER_PROJECT,
    HEADER_ROLES, HEADER_SUBJECT, HttpError, OnSignIn, ProjectAccess, Route, SessionCache,
    Sessions, authenticator, enrich_fn, mount, mount_auth_endpoints, on_sign_in_fn, prepare,
    project_access_fn,
};
pub use mode::{AuthRoute, Mode, lookup_mode, register_mode};
pub use status::{PlatformStatus, ServiceStatus};
