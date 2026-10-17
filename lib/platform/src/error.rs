use thiserror::Error;

/// The errors of the platform plumbing. The messages keep the Go wording.
#[derive(Debug, Error)]
pub enum PlatformError {
    #[error(transparent)]
    Sql(#[from] sqlx::Error),
    #[error(transparent)]
    Io(#[from] std::io::Error),
    #[error(transparent)]
    Json(#[from] serde_json::Error),
    #[error(transparent)]
    Http(#[from] reqwest::Error),
    /// A NATS / JetStream failure.
    #[error("{0}")]
    Nats(String),
    /// `connect postgres: <err>` after the retries.
    #[error("connect postgres: {0}")]
    ConnectPostgres(#[source] sqlx::Error),
    /// A migration that failed: `migration <name>: <err>` (PostgreSQL), `<component>/<name>: <err>` (SQLite).
    #[error("{name}: {source}")]
    Migration {
        name: String,
        #[source]
        source: sqlx::Error,
    },
    /// The context of the call ended.
    #[error(transparent)]
    Ctx(#[from] goap_ctx::CtxError),
    /// Any other failure, its message as Go writes it.
    #[error("{0}")]
    Other(String),
}

impl PlatformError {
    pub(crate) fn nats(e: impl std::fmt::Display) -> Self {
        PlatformError::Nats(e.to_string())
    }
}
