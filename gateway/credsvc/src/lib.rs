//! The local sign-in password of subjects that connect without an
//! external identity provider (ADR 0040): one credential per subject, outside the graph, so a failed login never
//! touches the audited graph/change log and no secret material ever becomes graph data; and their sign-in sessions
//! (ADR 0045).
//!
//! - [`Service`]: register, verify, set a password (bcrypt), open, check and end sessions.
//! - [`Store`]: [`MemoryStore`] and [`SqlStore`] (SQLite for the local mode, PostgreSQL), with their migrations
//!   [`MIGRATIONS`] / [`SQLITE_MIGRATIONS`]. Store is a small interface so the concrete backend can move to
//!   `platform::Secrets`/Vault later, with no call-site change.
//! - [`Handler`]: the Connect `CredentialsService`, refusing every request without the service credential
//!   ([`TOKEN_HEADER`]); [`client_token`] the client interceptor presenting it.
//!
//! Differences from the Go package: Go's `SQLStore{DB, Dollar}` is the enum [`SqlStore`]; bcrypt runs on the
//! blocking pool, with Go's cost (10) and hash version (`$2a$`); a password longer than bcrypt's 72 bytes is refused
//! by the hash, as by Go's x/crypto.

mod handler;
mod service;
mod store;

pub use handler::{ClientToken, Handler, TOKEN_HEADER, client_token};
pub use service::{Error, MIN_PASSWORD_LEN, Service};
pub use store::{MIGRATIONS, MemoryStore, SQLITE_MIGRATIONS, Session, SqlStore, Store};
