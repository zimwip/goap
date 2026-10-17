//! SQLite for the local development mode: one file, one writer.
use std::{path::Path, str::FromStr, time::Duration};

use goap_ctx::Ctx;
use include_dir::Dir;
use sqlx::{
    AssertSqlSafe, Sqlite, SqlitePool, Transaction,
    sqlite::{SqliteConnectOptions, SqliteJournalMode, SqlitePoolOptions, SqliteSynchronous},
};

use crate::{PlatformError, Result, postgres::sql_files};

/// Opens (and creates) a SQLite database file for the local development mode. Writes are serialized on a single
/// connection: SQLite has one writer, and this avoids SQLITE_BUSY on concurrent transactions; another process on the
/// file waits for the lock (`busy_timeout`, 30 s) rather than failing. Foreign keys on, WAL journal, synchronous
/// NORMAL. The bundled SQLite has FTS5. A transaction that writes should begin with [`begin_immediate`] (Go's
/// `_txlock=immediate`), so that it waits for the write lock when it begins instead of failing when a read turns into
/// a write.
pub async fn open_sqlite(_ctx: &Ctx, path: &str) -> Result<SqlitePool> {
    if let Some(dir) = Path::new(path).parent()
        && !dir.as_os_str().is_empty()
    {
        std::fs::create_dir_all(dir)?;
    }
    let opts = if path == ":memory:" {
        SqliteConnectOptions::from_str("sqlite::memory:")?
    } else {
        SqliteConnectOptions::new().filename(path)
    };
    let opts = opts
        .create_if_missing(true)
        .foreign_keys(true)
        .journal_mode(SqliteJournalMode::Wal)
        .busy_timeout(Duration::from_secs(30))
        .synchronous(SqliteSynchronous::Normal);
    SqlitePoolOptions::new()
        .max_connections(1)
        .connect_with(opts)
        .await
        .map_err(|e| PlatformError::Other(format!("sqlite {path}: {e}")))
}

/// Begins a transaction that takes the write lock at once (`BEGIN IMMEDIATE`).
pub async fn begin_immediate(pool: &SqlitePool) -> Result<Transaction<'static, Sqlite>> {
    Ok(pool.begin_with("BEGIN IMMEDIATE").await?)
}

/// Applies the `*.sql` files of `dir` in name order, once. Several components share one file: applied migrations
/// are recorded per component.
pub async fn migrate_sqlite(
    _ctx: &Ctx,
    pool: &SqlitePool,
    component: &str,
    migrations: &Dir<'_>,
    dir: &str,
) -> Result<()> {
    sqlx::query(
        "CREATE TABLE IF NOT EXISTS schema_migrations (
		component TEXT NOT NULL, name TEXT NOT NULL, applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		PRIMARY KEY (component, name))",
    )
    .execute(pool)
    .await?;
    for (name, script) in sql_files(migrations, dir)? {
        let n: i64 = sqlx::query_scalar(
            "SELECT count(*) FROM schema_migrations WHERE component = ? AND name = ?",
        )
        .bind(component)
        .bind(&name)
        .fetch_one(pool)
        .await?;
        if n > 0 {
            continue;
        }
        let mut tx = pool.begin().await?;
        if let Err(source) = sqlx::raw_sql(AssertSqlSafe(script)).execute(&mut *tx).await {
            let _ = tx.rollback().await;
            return Err(PlatformError::Migration {
                name: format!("{component}/{name}"),
                source,
            });
        }
        sqlx::query("INSERT INTO schema_migrations (component, name) VALUES (?, ?)")
            .bind(component)
            .bind(&name)
            .execute(&mut *tx)
            .await?;
        tx.commit().await?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    static MIGRATIONS: Dir<'_> = include_dir::include_dir!("$CARGO_MANIFEST_DIR/testdata");

    #[tokio::test]
    async fn open_migrate_once_per_component() {
        let dir = std::env::temp_dir().join(format!("goap-platform-sqlite-{}", std::process::id()));
        let path = dir.join("sub").join("goap.db");
        let ctx = Ctx::background();
        let pool = open_sqlite(&ctx, path.to_str().unwrap()).await.unwrap();
        migrate_sqlite(&ctx, &pool, "one", &MIGRATIONS, "migrations")
            .await
            .unwrap();
        migrate_sqlite(&ctx, &pool, "one", &MIGRATIONS, "migrations")
            .await
            .unwrap();
        let n: i64 = sqlx::query_scalar("SELECT count(*) FROM b")
            .fetch_one(&pool)
            .await
            .unwrap();
        assert_eq!(n, 1);
        let applied: i64 =
            sqlx::query_scalar("SELECT count(*) FROM schema_migrations WHERE component = 'one'")
                .fetch_one(&pool)
                .await
                .unwrap();
        assert_eq!(applied, 2);
        // another component applies its own: here the same files again, which fail on the existing tables
        let err = migrate_sqlite(&ctx, &pool, "two", &MIGRATIONS, "migrations")
            .await
            .unwrap_err();
        assert!(err.to_string().starts_with("two/0001_a.sql: "), "{err}");
        // foreign keys are enforced, the journal is WAL
        assert!(
            sqlx::query("INSERT INTO b (id, a_id) VALUES ('z', 'nope')")
                .execute(&pool)
                .await
                .is_err()
        );
        let mode: String = sqlx::query_scalar("PRAGMA journal_mode")
            .fetch_one(&pool)
            .await
            .unwrap();
        assert_eq!(mode, "wal");
        let mut tx = begin_immediate(&pool).await.unwrap();
        sqlx::query("INSERT INTO a (id) VALUES ('w')")
            .execute(&mut *tx)
            .await
            .unwrap();
        tx.commit().await.unwrap();
        pool.close().await;
        let _ = std::fs::remove_dir_all(dir);
    }

    #[tokio::test]
    async fn bundled_sqlite_has_fts5() {
        let pool = open_sqlite(&Ctx::background(), ":memory:").await.unwrap();
        sqlx::query("CREATE VIRTUAL TABLE t USING fts5(body)")
            .execute(&pool)
            .await
            .unwrap();
        sqlx::query("INSERT INTO t (body) VALUES ('the quick brown fox')")
            .execute(&pool)
            .await
            .unwrap();
        let n: i64 = sqlx::query_scalar("SELECT count(*) FROM t WHERE t MATCH 'quick'")
            .fetch_one(&pool)
            .await
            .unwrap();
        assert_eq!(n, 1);
    }
}
