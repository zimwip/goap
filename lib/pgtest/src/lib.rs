//! Throw-away PostgreSQL schemas for tests (enabled by `GOAP_TEST_PG_DSN`).
//!
//! ```ignore
//! static MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR"); // holds migrations/
//! #[tokio::test]
//! async fn store_on_postgres() {
//!     let Some(db) = goap_pgtest::pool(&MIGRATIONS).await else { return }; // skipped without GOAP_TEST_PG_DSN
//!     let store = SqlStore::new(db.pool().clone());
//!     // ...
//! } // the schema is dropped here
//! ```
use std::{
    ops::Deref,
    sync::atomic::{AtomicU64, Ordering},
    time::{SystemTime, UNIX_EPOCH},
};

use goap_ctx::Ctx;
use goap_platform::include_dir::Dir;
use sqlx::{AssertSqlSafe, PgPool, postgres::PgPoolOptions};

/// The variable naming the database of the PostgreSQL tests.
pub const DSN_VAR: &str = "GOAP_TEST_PG_DSN";

/// A pool on a fresh migrated schema of its own; the schema is dropped when it goes.
pub struct PgTest {
    pool: PgPool,
    schema: String,
    dsn: String,
}

impl PgTest {
    pub fn pool(&self) -> &PgPool {
        &self.pool
    }

    /// The name of the schema (`test_<n>`), the pool's `search_path`.
    pub fn schema(&self) -> &str {
        &self.schema
    }
}

impl Deref for PgTest {
    type Target = PgPool;

    fn deref(&self) -> &PgPool {
        &self.pool
    }
}

impl Drop for PgTest {
    fn drop(&mut self) {
        let (dsn, schema) = (self.dsn.clone(), self.schema.clone());
        // a thread and a runtime of its own: Drop cannot await, and the test's runtime is blocked meanwhile (the
        // pool, bound to that runtime, is left alone: its idle connections hold no lock)
        let _ = std::thread::spawn(move || {
            let Ok(rt) = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
            else {
                return;
            };
            rt.block_on(async move {
                if let Ok(admin) = goap_platform::open_postgres(&Ctx::background(), &dsn).await {
                    let _ = sqlx::raw_sql(AssertSqlSafe(format!("DROP SCHEMA {schema} CASCADE")))
                        .execute(&admin)
                        .await;
                    admin.close().await;
                }
            });
        })
        .join();
    }
}

static SEQ: AtomicU64 = AtomicU64::new(0);

/// A pool on a fresh schema migrated with the `migrations` directory of `migrations`, or None (the test is skipped)
/// when `GOAP_TEST_PG_DSN` is not set. A failure panics (fails the test).
pub async fn pool(migrations: &Dir<'_>) -> Option<PgTest> {
    let dsn = std::env::var(DSN_VAR).ok().filter(|d| !d.is_empty())?;
    Some(pool_with(&dsn, Some(migrations)).await)
}

/// A pool on a fresh schema of the database of `dsn`, migrated when `migrations` is given.
pub async fn pool_with(dsn: &str, migrations: Option<&Dir<'_>>) -> PgTest {
    let ctx = Ctx::background();
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or_default();
    let schema = format!("test_{nanos}_{}", SEQ.fetch_add(1, Ordering::Relaxed));
    let admin = goap_platform::open_postgres(&ctx, dsn)
        .await
        .unwrap_or_else(|e| panic!("{e}"));
    sqlx::raw_sql(AssertSqlSafe(format!("CREATE SCHEMA {schema}")))
        .execute(&admin)
        .await
        .unwrap_or_else(|e| panic!("{e}"));
    admin.close().await;
    let opts = goap_platform::pg_connect_options(dsn)
        .unwrap_or_else(|e| panic!("{e}"))
        .options([("search_path", schema.as_str())]);
    let pool = PgPoolOptions::new()
        .connect_with(opts)
        .await
        .unwrap_or_else(|e| panic!("{e}"));
    let t = PgTest {
        pool,
        schema,
        dsn: dsn.to_string(),
    };
    if let Some(m) = migrations {
        goap_platform::migrate(&ctx, &t.pool, m, "migrations")
            .await
            .unwrap_or_else(|e| panic!("{e}"));
    }
    t
}

#[cfg(test)]
mod tests {
    use super::*;

    static MIGRATIONS: Dir<'_> = include_dir::include_dir!("$CARGO_MANIFEST_DIR/testdata");

    #[tokio::test]
    async fn fresh_migrated_schema_dropped_after() {
        let Some(db) = pool(&MIGRATIONS).await else {
            return;
        };
        let schema = db.schema().to_string();
        let n: i64 = sqlx::query_scalar("SELECT count(*) FROM t")
            .fetch_one(db.pool())
            .await
            .unwrap();
        assert_eq!(n, 1);
        let dsn = std::env::var(DSN_VAR).unwrap();
        drop(db);
        let admin = goap_platform::open_postgres(&Ctx::background(), &dsn)
            .await
            .unwrap();
        let left: i64 = sqlx::query_scalar(
            "SELECT count(*) FROM information_schema.schemata WHERE schema_name = $1",
        )
        .bind(&schema)
        .fetch_one(&admin)
        .await
        .unwrap();
        assert_eq!(left, 0);
    }
}
