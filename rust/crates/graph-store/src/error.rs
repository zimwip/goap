use thiserror::Error;

#[derive(Debug, Error)]
pub enum StoreError {
    #[error("postgres: {0}")]
    Postgres(#[from] tokio_postgres::Error),
    #[error("postgres pool: {0}")]
    Pool(#[from] deadpool_postgres::PoolError),
    #[error("postgres pool: {0}")]
    Build(#[from] deadpool_postgres::BuildError),
    #[error("sqlite: {0}")]
    Sqlite(#[from] rusqlite::Error),
    #[error("migration {component}/{name}: {source}")]
    Migration {
        component: &'static str,
        name: &'static str,
        source: Box<StoreError>,
    },
    #[error("{0}")]
    Other(String),
    /// A statement sent ahead of another (a pipeline) failed: what it was about.
    #[error("{what}: {source}")]
    Queued { what: String, source: Box<StoreError> },
}

impl StoreError {
    /// A write on a read-only connection: the request runs again on the writer (PostgreSQL SQLSTATE 25006,
    /// SQLite SQLITE_READONLY).
    pub fn is_read_only(&self) -> bool {
        match self {
            StoreError::Postgres(e) => e.code() == Some(&tokio_postgres::error::SqlState::READ_ONLY_SQL_TRANSACTION),
            StoreError::Pool(deadpool_postgres::PoolError::Backend(e)) => {
                e.code() == Some(&tokio_postgres::error::SqlState::READ_ONLY_SQL_TRANSACTION)
            }
            StoreError::Sqlite(rusqlite::Error::SqliteFailure(e, _)) => e.code == rusqlite::ErrorCode::ReadOnly,
            _ => false,
        }
    }
}
