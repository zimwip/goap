//! The plumbing shared by every service (Go's `lib/platform`): configuration and logging ([`env`], [`logger`]), the
//! HTTP server ([`Server`], axum, HTTP/1.1 and HTTP/2 cleartext) and its client ([`h2c_client`]), PostgreSQL and
//! SQLite (sqlx) with their migrations, NATS JetStream events and leases (async-nats), Vault secrets ([`Secrets`]),
//! the optional dependencies of a service ([`optional_postgres`], [`optional_events`]) and the [`Stamp`] of a
//! request.
//!
//! Differences from the Go package: the functions that took a `*slog.Logger` log through `tracing`; a nil `*Events`
//! / `*Lease` that the Go methods tolerated is an `Option` the caller holds; the migrations are an
//! `include_dir::Dir` in place of an `fs.FS` (the crate is re-exported as [`include_dir`] for the type; the
//! `include_dir!` macro needs `include_dir` as a dependency of the crate that embeds); the OpenTelemetry middleware and the
//! trace propagation through NATS headers belong to `goap-telemetry`.

mod bootstrap;
mod client;
mod config;
mod error;
mod lease;
mod nats;
mod postgres;
mod secrets;
mod server;
mod sqlite;
mod stamp;

pub use include_dir;

pub use bootstrap::{optional_events, optional_postgres};
pub use client::h2c_client;
pub use config::{
    env, env_bool, env_duration, env_int, fatal, logger, parse_duration, parse_go_bool,
    service_name,
};
pub use error::PlatformError;
pub use lease::Lease;
pub use nats::{Events, STREAM, connect_events};
pub use postgres::{migrate, open_postgres, pg_connect_options};
pub use secrets::Secrets;
pub use server::{ReadyCheck, Server};
pub use sqlite::{begin_immediate, migrate_sqlite, open_sqlite};
pub use stamp::{HEADER_ACTOR, HEADER_COMMAND, Stamp, stamp_of, with_stamp};

/// The result of the platform functions.
pub type Result<T, E = PlatformError> = std::result::Result<T, E>;
