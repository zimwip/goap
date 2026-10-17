//! Port of `lib/identity` (see docs/rust-port.md): reads the caller identity propagated by the gateway.
//!
//! - [`from_headers`] / [`set_headers`]: the identity headers of a request and the principal they name.
//! - [`Forward`]: the client interceptor of a platform client acting for its caller (Go's `Forward()` client option).
//! - [`Extractor`]: puts the caller's principal in the context.
//! - [`who_am_i`]: the `GET /api/whoami` route (axum; Go's echo handler).
//! - [`CommandInterceptor`]: stamps the events published while a request is served (ADR 0053).

mod command;
mod identity;

pub use command::{CommandInterceptor, command_interceptor};
pub use identity::{
    BoxError, Extractor, Forward, HEADER_ON_BEHALF_OF, HEADER_ORG, HEADER_PROJECT, HEADER_ROLES,
    HEADER_SUBJECT, Session, SessionFuture, forward, from_headers, session_fn, set_headers,
    who_am_i,
};
