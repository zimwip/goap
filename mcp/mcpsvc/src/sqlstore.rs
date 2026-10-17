//! The registry of connectors on SQL (`sqlstore.go`).

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_gen::connector::v1::ConnectorInfo;
use include_dir::{Dir, include_dir};
use sqlx::{AssertSqlSafe, PgPool, Row, SqlitePool};

use crate::{ConnectorReg, Error, Store};

/// Migrations holds the PostgreSQL schema (its `*.sql` files at the root: `goap_platform::migrate(ctx, pool,
/// &MIGRATIONS, ".")`); SQLITE_MIGRATIONS the local mode one.
pub static MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations");
pub static SQLITE_MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations_sqlite");

/// SQLStore is the Store on SQL, for SQLite (local mode) and PostgreSQL (Go's `SQLStore{DB, Dollar}`: one variant per
/// dialect, the `?` placeholders numbered for PostgreSQL).
#[derive(Debug, Clone)]
pub enum SqlStore {
    Sqlite(SqlitePool),
    Postgres(PgPool),
}

/// `q`: the `?` placeholders as $1, $2… for PostgreSQL.
fn dialect(postgres: bool, query: &str) -> String {
    if !postgres {
        return query.to_string();
    }
    let mut b = String::with_capacity(query.len() + 8);
    let mut n = 0;
    for c in query.chars() {
        if c == '?' {
            n += 1;
            b.push('$');
            b.push_str(&n.to_string());
        } else {
            b.push(c);
        }
    }
    b
}

const UPSERT: &str = "INSERT INTO connector (id, endpoint, info, last_seen_ms) VALUES (?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET endpoint = excluded.endpoint, info = excluded.info, last_seen_ms = excluded.last_seen_ms";
const SELECT_ONE: &str = "SELECT endpoint, info, last_seen_ms FROM connector WHERE id = ?";
const SELECT_ALL: &str = "SELECT endpoint, info, last_seen_ms FROM connector ORDER BY id";

/// One body for both dialects: `$pool` is the pool of the variant, `$pg` whether it is PostgreSQL.
macro_rules! on_pool {
    ($store:expr, $pool:ident, $pg:ident => $body:expr) => {
        match $store {
            SqlStore::Sqlite($pool) => {
                let $pg = false;
                $body
            }
            SqlStore::Postgres($pool) => {
                let $pg = true;
                $body
            }
        }
    };
}

fn scan(endpoint: String, info: String, ms: i64) -> Result<ConnectorReg, Error> {
    Ok(ConnectorReg {
        info: serde_json::from_str::<ConnectorInfo>(&info)?,
        endpoint,
        last_seen: DateTime::<Utc>::from_timestamp_millis(ms).unwrap_or_default(),
    })
}

#[async_trait]
impl Store for SqlStore {
    async fn save_connector(&self, _ctx: &Ctx, r: ConnectorReg) -> Result<(), Error> {
        let info = serde_json::to_string(&r.info)?;
        on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, UPSERT)))
                .bind(&r.info.id)
                .bind(&r.endpoint)
                .bind(&info)
                .bind(r.last_seen.timestamp_millis())
                .execute(pool)
                .await?;
        });
        Ok(())
    }

    async fn connector(&self, _ctx: &Ctx, id: &str) -> Result<ConnectorReg, Error> {
        let row: Option<(String, String, i64)> = on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, SELECT_ONE)))
                .bind(id)
                .fetch_optional(pool)
                .await?
                .map(|r| Ok::<_, sqlx::Error>((r.try_get(0)?, r.try_get(1)?, r.try_get(2)?)))
                .transpose()?
        });
        let Some((endpoint, info, ms)) = row else {
            return Err(Error::not_found(format!("connector {id}")));
        };
        scan(endpoint, info, ms)
    }

    async fn connectors(&self, _ctx: &Ctx) -> Result<Vec<ConnectorReg>, Error> {
        let rows: Vec<(String, String, i64)> = on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, SELECT_ALL)))
                .fetch_all(pool)
                .await?
                .into_iter()
                .map(|r| Ok::<_, sqlx::Error>((r.try_get(0)?, r.try_get(1)?, r.try_get(2)?)))
                .collect::<Result<Vec<_>, _>>()?
        });
        rows.into_iter()
            .map(|(endpoint, info, ms)| scan(endpoint, info, ms))
            .collect()
    }
}
