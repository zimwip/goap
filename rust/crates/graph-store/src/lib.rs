//! The storage of the Rust graph service (ADR 0163): one schema with the Go service, in PostgreSQL or SQLite.
//!
//! [`Store::open`] connects and brings the schema up to date with the migrations of the Go services the graph process
//! runs (the same files, recorded in the same `schema_migrations` table, so either service starts on a database the
//! other wrote). Each dialect keeps a writer and a pool of read-only connections: a request that only reads takes a
//! reader, and a write met there runs again on the writer ([`StoreError::is_read_only`]).
mod error;
pub mod migrate;
mod postgres;
pub mod read;
mod sqlite;
pub mod stats;

pub use error::StoreError;
pub use migrate::Part;
pub use postgres::Postgres;
pub use sqlite::Sqlite;

/// The migrations, embedded from the Go services' directories.
pub mod migrations {
    include!(concat!(env!("OUT_DIR"), "/migrations.rs"));
}

/// The SQL dialect of a store.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Dialect {
    Postgres,
    Sqlite,
}

/// Where the store is.
#[derive(Debug, Clone)]
pub enum StoreConfig {
    /// `GOAP_DB_DSN`: `postgres://…`.
    Postgres { dsn: String },
    /// `GOAP_SQLITE_PATH`: a SQLite file, `readers` read-only connections beside its writer.
    Sqlite { path: String, readers: usize },
    /// Neither: a SQLite database in memory, one connection (nothing else reaches it).
    Memory,
}

impl StoreConfig {
    /// The store of the environment: `GOAP_DB_DSN`, else `GOAP_SQLITE_PATH`, else in memory.
    pub fn from_env() -> StoreConfig {
        let var = |k: &str| std::env::var(k).ok().filter(|v| !v.trim().is_empty());
        if let Some(dsn) = var("GOAP_DB_DSN") {
            return StoreConfig::Postgres { dsn };
        }
        if let Some(path) = var("GOAP_SQLITE_PATH") {
            let readers = var("GOAP_SQLITE_READERS").and_then(|v| v.parse().ok()).unwrap_or(8);
            return StoreConfig::Sqlite { path, readers };
        }
        StoreConfig::Memory
    }
}

/// A store: one of the dialects.
pub enum Store {
    Postgres(Postgres),
    Sqlite(Sqlite),
}

impl Store {
    /// Connects and migrates the graph's tables.
    pub async fn open(cfg: &StoreConfig) -> Result<Store, StoreError> {
        Store::open_part(cfg, Part::Graph).await
    }

    /// Connects and migrates the domain registry's tables (ADR 0167).
    pub async fn open_domains(cfg: &StoreConfig) -> Result<Store, StoreError> {
        Store::open_part(cfg, Part::Domains).await
    }

    /// Connects and migrates the tables of a part.
    pub async fn open_part(cfg: &StoreConfig, part: Part) -> Result<Store, StoreError> {
        Ok(match cfg {
            StoreConfig::Postgres { dsn } => Store::Postgres(Postgres::open(dsn, part).await?),
            StoreConfig::Sqlite { path, readers } => Store::Sqlite(Sqlite::open(path, *readers, part).await?),
            StoreConfig::Memory => Store::Sqlite(Sqlite::memory(part).await?),
        })
    }

    pub fn dialect(&self) -> Dialect {
        match self {
            Store::Postgres(_) => Dialect::Postgres,
            Store::Sqlite(_) => Dialect::Sqlite,
        }
    }

    fn versions(&self) -> &read::Versions {
        match self {
            Store::Postgres(p) => &p.versions,
            Store::Sqlite(s) => &s.versions,
        }
    }

    /// A branch of a namespace moved in another process (a baseline event on the bus, Go's `Directory.Moved`): what
    /// the store keeps of that namespace's heads (the organisation's snapshot) is checked again at its next read.
    pub fn moved(&self, namespace: &str) {
        self.versions().moved(namespace);
    }

    /// Every namespace moved: an event the store could not read.
    pub fn moved_all(&self) {
        self.versions().moved_all();
    }

    /// The bus tells the store the moves of the other processes ([`Store::moved`], Go's `Directory.FollowEvents`):
    /// the organisation checked at its head is then kept for `access.FollowAge`, a lost event caught up then.
    pub fn follow_events(&self) {
        self.versions().follow_events();
    }

    /// A read-only session for the reads of a request.
    pub async fn reader(&self) -> Result<read::Session, StoreError> {
        Ok(match self {
            Store::Postgres(p) => read::Session::postgres(p.read.get().await?, p.versions.clone()),
            Store::Sqlite(s) => read::Session::sqlite(s.session().await?, s.versions.clone()),
        })
    }

    /// A write transaction on the writer: committed by [`read::Session::commit`], rolled back when dropped.
    pub async fn writer(&self) -> Result<read::Session, StoreError> {
        Ok(match self {
            Store::Postgres(p) => read::Session::postgres_tx(p.write.get().await?, p.versions.clone()).await?,
            Store::Sqlite(s) => read::Session::sqlite_tx(s.begin().await?, s.versions.clone()),
        })
    }

    /// Whether the database answers (readiness).
    pub async fn ping(&self) -> Result<(), StoreError> {
        match self {
            Store::Postgres(p) => p.ping().await,
            Store::Sqlite(s) => s.ping().await,
        }
    }
}
