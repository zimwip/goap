//! The domain versions in the database of the domain registry, SQLite (local mode) and
//! PostgreSQL.

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_def::{Domain, Record, STATUS_DRAFT, STATUS_PUBLISHED, Status};
use goap_domain::gotime;
use include_dir::{Dir, include_dir};
use sqlx::{AssertSqlSafe, PgPool, Row, SqlitePool};

use crate::error::Error;
use crate::store::{Store, key, sort_domain_records};

/// Migrations holds the PostgreSQL schema of the domain registry (its `*.sql` files at the root:
/// `goap_platform::migrate(ctx, pool, &MIGRATIONS, ".")`); SQLITE_MIGRATIONS the local mode one.
pub static MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations");
pub static SQLITE_MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations_sqlite");

/// SqlStore keeps the domain versions in the database of the domain registry (ADR 0023, 0125): SQLite (local mode)
/// and PostgreSQL (Go's `SQLStore`, `Dollar` set for the latter). A domain is the model each graph holds in memory to
/// check its data; it is not graph data itself.
#[derive(Debug, Clone)]
pub enum SqlStore {
    Sqlite(SqlitePool),
    Postgres(PgPool),
}

/// The queries are written once with `?` placeholders and PostgreSQL casts (`?::jsonb`): numbered for PostgreSQL,
/// the casts dropped for SQLite.
fn dialect(postgres: bool, query: &str) -> String {
    if !postgres {
        return query.replace("::jsonb", "");
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

fn sql_time(t: Option<DateTime<Utc>>) -> String {
    match t {
        Some(t) if !gotime::is_zero(&t) => gotime::format(&t),
        _ => String::new(),
    }
}

fn parse_sql_time(s: &str) -> Result<Option<DateTime<Utc>>, Error> {
    if s.is_empty() {
        return Ok(None);
    }
    gotime::parse(s)
        .map(Some)
        .ok_or_else(|| Error::Other(format!("parsing time {s:?}: not RFC 3339")))
}

const DOMAIN_COLUMNS: &str =
    "name, version, status, definition, created_at, updated_at, published_at, updated_by";
const SELECT_COLUMNS: &str = "name, version, status, CAST(definition AS TEXT), created_at, updated_at, published_at, updated_by";

fn scan_domain<R: Row>(row: &R) -> Result<Record, Error>
where
    usize: sqlx::ColumnIndex<R>,
    for<'r> String: sqlx::Decode<'r, R::Database> + sqlx::Type<R::Database>,
{
    let name: String = row.try_get(0)?;
    let version: String = row.try_get(1)?;
    let status: String = row.try_get(2)?;
    let definition: String = row.try_get(3)?;
    let created: String = row.try_get(4)?;
    let updated: String = row.try_get(5)?;
    let published: String = row.try_get(6)?;
    let updated_by: String = row.try_get(7)?;
    let mut domain: Domain = serde_json::from_str(&definition)
        .map_err(|e| Error::Other(format!("domain {}: {e}", key(&name, &version))))?;
    domain.name = name;
    domain.version = version;
    Ok(Record {
        domain,
        status: Status::new(status),
        created_at: parse_sql_time(&created)?,
        updated_at: parse_sql_time(&updated)?,
        published_at: parse_sql_time(&published)?,
        updated_by,
        builtin: false,
    })
}

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

#[async_trait]
impl Store for SqlStore {
    async fn save_domain(&self, _ctx: &Ctx, r: Record) -> Result<(), Error> {
        let definition =
            serde_json::to_string(&r.domain).map_err(|e| Error::Other(e.to_string()))?;
        let k = key(&r.domain.name, &r.domain.version);
        on_pool!(self, pool, pg => {
            let mut tx = if pg {
                pool.begin().await?
            } else {
                pool.begin_with("BEGIN IMMEDIATE").await?
            };
            let res: Result<(), Error> = async {
                let row = sqlx::query(AssertSqlSafe(dialect(
                    pg,
                    "SELECT status, created_at FROM domain_version WHERE name = ? AND version = ?",
                )))
                .bind(&r.domain.name)
                .bind(&r.domain.version)
                .fetch_optional(&mut *tx)
                .await?;
                match row {
                    None => {
                        let mut created = sql_time(r.created_at);
                        if created.is_empty() {
                            created = sql_time(r.updated_at);
                        }
                        sqlx::query(AssertSqlSafe(dialect(
                            pg,
                            &format!(
                                "INSERT INTO domain_version ({DOMAIN_COLUMNS}) VALUES (?, ?, ?, ?::jsonb, ?, ?, ?, ?)"
                            ),
                        )))
                        .bind(&r.domain.name)
                        .bind(&r.domain.version)
                        .bind(r.status.as_str())
                        .bind(&definition)
                        .bind(created)
                        .bind(sql_time(r.updated_at))
                        .bind(sql_time(r.published_at))
                        .bind(&r.updated_by)
                        .execute(&mut *tx)
                        .await?;
                    }
                    Some(row) => {
                        let status: String = row.try_get(0)?;
                        if status != STATUS_DRAFT.as_str() {
                            return Err(Error::immutable(format!("domain {k}")));
                        }
                        sqlx::query(AssertSqlSafe(dialect(
                            pg,
                            "UPDATE domain_version SET status = ?, definition = ?::jsonb, updated_at = ?, published_at = ?, updated_by = ?
            WHERE name = ? AND version = ?",
                        )))
                        .bind(r.status.as_str())
                        .bind(&definition)
                        .bind(sql_time(r.updated_at))
                        .bind(sql_time(r.published_at))
                        .bind(&r.updated_by)
                        .bind(&r.domain.name)
                        .bind(&r.domain.version)
                        .execute(&mut *tx)
                        .await?;
                    }
                }
                Ok(())
            }
            .await;
            match res {
                Ok(()) => {
                    tx.commit().await?;
                    Ok(())
                }
                Err(e) => {
                    let _ = tx.rollback().await;
                    Err(e)
                }
            }
        })
    }

    async fn get_domain(&self, _ctx: &Ctx, name: &str, version: &str) -> Result<Record, Error> {
        if !version.is_empty() {
            let q = format!(
                "SELECT {SELECT_COLUMNS} FROM domain_version WHERE name = ? AND version = ?"
            );
            return on_pool!(self, pool, pg => {
                let row = sqlx::query(AssertSqlSafe(dialect(pg, &q)))
                    .bind(name)
                    .bind(version)
                    .fetch_optional(pool)
                    .await?;
                match row {
                    None => Err(Error::not_found(key(name, version))),
                    Some(row) => scan_domain(&row),
                }
            });
        }
        let q =
            format!("SELECT {SELECT_COLUMNS} FROM domain_version WHERE name = ? AND status = ?");
        let rs: Vec<Record> = on_pool!(self, pool, pg => {
            let rows = sqlx::query(AssertSqlSafe(dialect(pg, &q)))
                .bind(name)
                .bind(STATUS_PUBLISHED.as_str())
                .fetch_all(pool)
                .await?;
            rows.iter().map(scan_domain).collect::<Result<_, _>>()?
        });
        let mut best: Option<Record> = None;
        for r in rs {
            if best
                .as_ref()
                .is_none_or(|b| r.published_at > b.published_at)
            {
                best = Some(r);
            }
        }
        best.ok_or_else(|| Error::not_found(format!("{name} (published)")))
    }

    async fn list_domains(&self, _ctx: &Ctx) -> Result<Vec<Record>, Error> {
        let q = format!("SELECT {SELECT_COLUMNS} FROM domain_version");
        let mut out: Vec<Record> = on_pool!(self, pool, _pg => {
            let rows = sqlx::query(AssertSqlSafe(q.clone())).fetch_all(pool).await?;
            rows.iter().map(scan_domain).collect::<Result<_, _>>()?
        });
        sort_domain_records(&mut out);
        Ok(out)
    }

    async fn set_domain_status(
        &self,
        _ctx: &Ctx,
        name: &str,
        version: &str,
        st: Status,
        at: DateTime<Utc>,
    ) -> Result<(), Error> {
        let publish = st == STATUS_PUBLISHED;
        let query = if publish {
            "UPDATE domain_version SET status = ?, updated_at = ?, published_at = ? WHERE name = ? AND version = ?"
        } else {
            "UPDATE domain_version SET status = ?, updated_at = ? WHERE name = ? AND version = ?"
        };
        let at = sql_time(Some(at));
        let n = on_pool!(self, pool, pg => {
            let mut q = sqlx::query(AssertSqlSafe(dialect(pg, query)))
                .bind(st.as_str().to_string())
                .bind(at.clone());
            if publish {
                q = q.bind(at.clone());
            }
            q.bind(name).bind(version).execute(pool).await?.rows_affected()
        });
        if n == 0 {
            return Err(Error::not_found(key(name, version)));
        }
        Ok(())
    }

    async fn delete_domain(&self, ctx: &Ctx, name: &str, version: &str) -> Result<(), Error> {
        let r = self.get_domain(ctx, name, version).await?;
        if r.status != STATUS_DRAFT {
            return Err(Error::immutable(format!("domain {}", key(name, version))));
        }
        on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(
                pg,
                "DELETE FROM domain_version WHERE name = ? AND version = ? AND status = ?",
            )))
            .bind(name)
            .bind(version)
            .bind(STATUS_DRAFT.as_str())
            .execute(pool)
            .await?;
            Ok(())
        })
    }
}
