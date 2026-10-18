//! A PostgreSQL [`Repo`] (schema migrated from [`MIGRATIONS`]). The statements are the ones
//! of [`SqlTx`]; this file holds the connection: its value encodings (uuid ids, jsonb, integer arrays, timestamptz)
//! and its error mapping.

use std::any::Any;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_platform::include_dir::{Dir, include_dir};
use serde_json::Value;
use sqlx::postgres::{PgArguments, PgDatabaseError, PgRow};
use sqlx::{AssertSqlSafe, PgPool, Postgres as PgDb, Row, Transaction};

use crate::repo::{Repo, Tx};
use crate::sqlbuild::{Arg, DIALECT_PG, Dialect};
use crate::sqltx::{Cell, Cells, Kind, SqlConn, SqlTx};
use crate::{Error, ErrorKind, Result};

/// Migrations holds the PostgreSQL schema of the graph service.
pub static MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations");

/// Postgres is a PostgreSQL Repo.
pub struct Postgres {
    pool: PgPool,
}

impl Postgres {
    /// NewPostgres returns a repository on pool (schema already migrated).
    pub fn new(pool: PgPool) -> Postgres {
        Postgres { pool }
    }

    /// The pool under the repository.
    pub fn pool(&self) -> &PgPool {
        &self.pool
    }
}

#[async_trait]
impl Repo for Postgres {
    async fn begin(&self, _ctx: &Ctx) -> Result<Box<dyn Tx>> {
        let tx = self.pool.begin().await.map_err(Error::other)?;
        Ok(Box::new(SqlTx { c: PgConn { tx } }))
    }

    fn as_any(&self) -> &dyn Any {
        self
    }
}

/// mapErr maps a driver error: a unique violation is a conflict (with its detail), a foreign key violation or a
/// malformed value an invalid write, a deadlock or a serialization failure a conflict (the caller tries again).
fn map_err(e: sqlx::Error, what: &str) -> Error {
    if let Some(db) = e.as_database_error() {
        let code = db.code().map(|c| c.to_string()).unwrap_or_default();
        let message = db.message().to_string();
        let detail = db
            .try_downcast_ref::<PgDatabaseError>()
            .and_then(|p| p.detail())
            .unwrap_or_default()
            .to_string();
        match code.as_str() {
            "23505" => return Error::wrap(ErrorKind::Conflict, format!("{what}: {detail}")),
            "23503" | "22P02" => {
                return Error::wrap(ErrorKind::Invalid, format!("{what}: {message}"));
            }
            "40P01" | "40001" => {
                return Error::wrap(ErrorKind::Conflict, format!("{what}: {message}"));
            }
            _ => {}
        }
    }
    Error::other(e)
}

pub(crate) struct PgConn {
    tx: Transaction<'static, PgDb>,
}

/// uuid parses an id; an empty one is NULL.
fn uuid(s: &str, what: &str) -> Result<Option<uuid::Uuid>> {
    if s.is_empty() {
        return Ok(None);
    }
    uuid::Uuid::parse_str(s).map(Some).map_err(|_| {
        Error::wrap(
            ErrorKind::Invalid,
            format!("{what}: invalid input syntax for type uuid: \"{s}\""),
        )
    })
}

type PgQuery = sqlx::query::Query<'static, PgDb, PgArguments>;

fn bind(q: PgQuery, a: &Arg, what: &str) -> Result<PgQuery> {
    Ok(match a {
        Arg::Text(s) => q.bind(s.clone()),
        Arg::Id(s) => q.bind(uuid(s, what)?),
        Arg::OptId(o) => q.bind(uuid(o.as_deref().unwrap_or_default(), what)?),
        Arg::Int(i) => q.bind(*i),
        Arg::OptInt(o) => q.bind(*o),
        Arg::Bool(b) => q.bind(*b),
        Arg::Json(v) => q.bind(v.clone()),
        Arg::Time(t) => q.bind(*t),
        Arg::Ints(v) => q.bind(v.iter().map(|x| *x as i32).collect::<Vec<i32>>()),
    })
}

/// An integer column: int8 (bigint, count) or int4 (integer).
fn int(r: &PgRow, i: usize) -> std::result::Result<Option<i64>, sqlx::Error> {
    match r.try_get::<Option<i64>, _>(i) {
        Ok(v) => Ok(v),
        Err(_) => Ok(r.try_get::<Option<i32>, _>(i)?.map(i64::from)),
    }
}

fn cell(r: &PgRow, i: usize, k: Kind) -> std::result::Result<Cell, sqlx::Error> {
    Ok(match k {
        Kind::Text => Cell::Text(r.try_get::<Option<String>, _>(i)?.unwrap_or_default()),
        Kind::OptText => Cell::OptText(r.try_get::<Option<String>, _>(i)?),
        Kind::Int => Cell::Int(int(r, i)?.unwrap_or_default()),
        Kind::OptInt => Cell::OptInt(int(r, i)?),
        Kind::Bool => Cell::Bool(r.try_get::<Option<bool>, _>(i)?.unwrap_or_default()),
        Kind::Json => Cell::Json(r.try_get::<Option<Value>, _>(i)?.unwrap_or(Value::Null)),
        Kind::Time => Cell::Time(
            r.try_get::<Option<DateTime<Utc>>, _>(i)?
                .unwrap_or_else(goap_domain::gotime::zero),
        ),
        Kind::Ints => Cell::Ints(
            r.try_get::<Option<Vec<i32>>, _>(i)?
                .unwrap_or_default()
                .into_iter()
                .map(i64::from)
                .collect(),
        ),
    })
}

fn query(q: &str, args: &[Arg], what: &str) -> Result<PgQuery> {
    let mut qy = sqlx::query(AssertSqlSafe(q.to_string()));
    for a in args {
        qy = bind(qy, a, what)?;
    }
    Ok(qy)
}

#[async_trait]
impl SqlConn for PgConn {
    const D: Dialect = DIALECT_PG;

    async fn rows(
        &mut self,
        q: &str,
        args: &[Arg],
        kinds: &[Kind],
        what: &str,
    ) -> Result<Vec<Cells>> {
        let rows = query(q, args, what)?
            .fetch_all(&mut *self.tx)
            .await
            .map_err(|e| map_err(e, what))?;
        let mut out = Vec::with_capacity(rows.len());
        for r in &rows {
            let mut cells = Vec::with_capacity(kinds.len());
            for (i, k) in kinds.iter().enumerate() {
                cells.push(cell(r, i, *k).map_err(|e| map_err(e, what))?);
            }
            out.push(Cells::new(cells));
        }
        Ok(out)
    }

    async fn exec(&mut self, q: &str, args: &[Arg], what: &str) -> Result<u64> {
        let res = query(q, args, what)?
            .execute(&mut *self.tx)
            .await
            .map_err(|e| map_err(e, what))?;
        Ok(res.rows_affected())
    }

    async fn commit(self: Box<Self>) -> Result<()> {
        self.tx.commit().await.map_err(|e| map_err(e, "commit"))
    }

    async fn rollback(self: Box<Self>) -> Result<()> {
        self.tx.rollback().await.map_err(|e| map_err(e, "rollback"))
    }
}
