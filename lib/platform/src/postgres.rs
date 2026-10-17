//! PostgreSQL: the pool of a service and its migrations.
use std::{str::FromStr, time::Duration};

use goap_ctx::Ctx;
use include_dir::Dir;
use sqlx::{
    AssertSqlSafe, Connection, PgPool,
    postgres::{PgConnectOptions, PgPoolOptions},
};

use crate::{PlatformError, Result};

/// The connection options of a DSN (`postgres://user:pass@host/db?sslmode=disable`). A `search_path` query parameter,
/// which pgx passes to the server as a run-time parameter, is passed so (the service schema).
pub fn pg_connect_options(dsn: &str) -> Result<PgConnectOptions> {
    let (base, search_path) = split_param(dsn, "search_path");
    let mut opts = PgConnectOptions::from_str(&base)?;
    if let Some(sp) = search_path {
        opts = opts.options([("search_path", sp)]);
    }
    Ok(opts)
}

/// Removes the query parameter `name` of a URL, returning it.
fn split_param(dsn: &str, name: &str) -> (String, Option<String>) {
    let Some((base, query)) = dsn.split_once('?') else {
        return (dsn.to_string(), None);
    };
    let mut found = None;
    let kept: Vec<&str> = query
        .split('&')
        .filter(|kv| match kv.split_once('=') {
            Some((k, v)) if k == name => {
                found = Some(v.replace("%2C", ",").replace("%2c", ","));
                false
            }
            _ => true,
        })
        .collect();
    let base = if kept.is_empty() {
        base.to_string()
    } else {
        format!("{base}?{}", kept.join("&"))
    };
    (base, found)
}

/// Connects to PostgreSQL, retrying for 20 s while the database comes up. The DSN should set `search_path` to the
/// service schema (dev: one database, one schema per service).
pub async fn open_postgres(ctx: &Ctx, dsn: &str) -> Result<PgPool> {
    let opts = pg_connect_options(dsn)?;
    let mut attempt = 1;
    loop {
        match PgPoolOptions::new().connect_with(opts.clone()).await {
            Ok(pool) => return Ok(pool),
            Err(e) if attempt >= 20 => return Err(PlatformError::ConnectPostgres(e)),
            Err(_) => {}
        }
        attempt += 1;
        tokio::select! {
            e = ctx.done() => return Err(e.into()),
            _ = tokio::time::sleep(Duration::from_secs(1)) => {}
        }
    }
}

/// The `*.sql` files of `dir` (a path within `migrations`, `""` or `"."` for its root), in name order.
pub(crate) fn sql_files(migrations: &Dir<'_>, dir: &str) -> Result<Vec<(String, String)>> {
    let d = match dir.trim_matches('/') {
        "" | "." => migrations,
        sub => migrations
            .get_dir(sub)
            .ok_or_else(|| PlatformError::Other(format!("open {dir}: file does not exist")))?,
    };
    let mut out: Vec<(String, String)> = d
        .files()
        .filter_map(|f| {
            let name = f.path().file_name()?.to_str()?.to_string();
            if !name.ends_with(".sql") {
                return None;
            }
            Some((name, f.contents_utf8().unwrap_or_default().to_string()))
        })
        .collect();
    out.sort_by(|a, b| a.0.cmp(&b.0));
    Ok(out)
}

/// Applies the `*.sql` files of `dir` (lexicographic order) that were not applied yet, under an advisory lock so that
/// replicas can start together; each file runs in a transaction with its record in `schema_migrations`.
pub async fn migrate(ctx: &Ctx, pool: &PgPool, migrations: &Dir<'_>, dir: &str) -> Result<()> {
    let files = sql_files(migrations, dir)?;
    let mut conn = pool.acquire().await?;
    sqlx::query("SELECT pg_advisory_lock(hashtext(current_schema()))")
        .execute(&mut *conn)
        .await?;
    let res = async {
        sqlx::query(
            "CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())",
        )
        .execute(&mut *conn)
        .await?;
        for (name, script) in files {
            if let Some(e) = ctx.err() {
                return Err(e.into());
            }
            let exists: bool = sqlx::query_scalar("SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)")
                .bind(&name)
                .fetch_one(&mut *conn)
                .await?;
            if exists {
                continue;
            }
            let applied = async {
                let mut tx = conn.begin().await?;
                sqlx::raw_sql(AssertSqlSafe(script)).execute(&mut *tx).await?;
                sqlx::query("INSERT INTO schema_migrations(name) VALUES ($1)").bind(&name).execute(&mut *tx).await?;
                tx.commit().await
            }
            .await;
            applied.map_err(|source| PlatformError::Migration { name: format!("migration {name}"), source })?;
            tracing::info!(name = %name, "migration applied");
        }
        Ok(())
    }
    .await;
    let _ = sqlx::query("SELECT pg_advisory_unlock(hashtext(current_schema()))")
        .execute(&mut *conn)
        .await;
    res
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn search_path_is_a_run_time_parameter() {
        let (base, sp) = split_param(
            "postgres://u:p@h/db?sslmode=disable&search_path=graph",
            "search_path",
        );
        assert_eq!(base, "postgres://u:p@h/db?sslmode=disable");
        assert_eq!(sp.as_deref(), Some("graph"));
        let (base, sp) = split_param("postgres://u:p@h/db?search_path=a%2Cb", "search_path");
        assert_eq!(base, "postgres://u:p@h/db");
        assert_eq!(sp.as_deref(), Some("a,b"));
        assert!(pg_connect_options("postgres://u:p@h/db?sslmode=disable&search_path=x").is_ok());
    }

    static MIGRATIONS: Dir<'_> = include_dir::include_dir!("$CARGO_MANIFEST_DIR/testdata");

    #[test]
    fn files_in_name_order() {
        let f = sql_files(&MIGRATIONS, "migrations").unwrap();
        let names: Vec<_> = f.iter().map(|x| x.0.as_str()).collect();
        assert_eq!(names, ["0001_a.sql", "0002_b.sql"]);
        assert!(sql_files(&MIGRATIONS, "nope").is_err());
    }

    /// The migrations on a real database (GOAP_TEST_PG_DSN), applied once.
    #[tokio::test]
    async fn migrate_postgres() {
        let Ok(dsn) = std::env::var("GOAP_TEST_PG_DSN") else {
            return;
        };
        let ctx = Ctx::background();
        let admin = open_postgres(&ctx, &dsn).await.unwrap();
        let schema = format!("test_platform_{}", std::process::id());
        sqlx::raw_sql(AssertSqlSafe(format!("CREATE SCHEMA {schema}")))
            .execute(&admin)
            .await
            .unwrap();
        let pool = PgPoolOptions::new()
            .connect_with(
                pg_connect_options(&dsn)
                    .unwrap()
                    .options([("search_path", &schema)]),
            )
            .await
            .unwrap();
        migrate(&ctx, &pool, &MIGRATIONS, "migrations")
            .await
            .unwrap();
        migrate(&ctx, &pool, &MIGRATIONS, "migrations")
            .await
            .unwrap();
        let n: i64 = sqlx::query_scalar("SELECT count(*) FROM b")
            .fetch_one(&pool)
            .await
            .unwrap();
        assert_eq!(n, 1);
        pool.close().await;
        sqlx::raw_sql(AssertSqlSafe(format!("DROP SCHEMA {schema} CASCADE")))
            .execute(&admin)
            .await
            .unwrap();
    }
}
