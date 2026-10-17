//! Port of `sqlite.go`: a [`Repo`] on a SQLite database (schema migrated from [`SQLITE_MIGRATIONS`], local
//! development mode, ADR 0010). The statements are the ones of [`SqlTx`]; this file holds the connection: its
//! value encodings (text ids, JSON as text, fixed-width timestamps) and its error mapping.

use std::any::Any;

use async_trait::async_trait;
use chrono::{DateTime, NaiveDateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::gotime;
use goap_platform::include_dir::{Dir, include_dir};
use serde_json::Value;
use sqlx::sqlite::{SqliteArguments, SqliteRow};
use sqlx::{AssertSqlSafe, Row, Sqlite as SqliteDb, SqlitePool, Transaction};

use crate::repo::{Repo, Tx};
use crate::sqlbuild::{Arg, DIALECT_SQLITE, Dialect};
use crate::sqltx::{Cell, Cells, Kind, SqlConn, SqlTx};
use crate::{Error, ErrorKind, Result};

/// SQLiteMigrations holds the SQLite schema of the graph (local development mode).
pub static SQLITE_MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations_sqlite");

/// SQLite is a Repo on a SQLite database (schema migrated from [`SQLITE_MIGRATIONS`]): open the pool with
/// `goap_platform::open_sqlite` (one connection, foreign keys on). Its transactions begin immediate.
pub struct Sqlite {
    pool: SqlitePool,
}

impl Sqlite {
    /// NewSQLite returns a repository on pool.
    pub fn new(pool: SqlitePool) -> Sqlite {
        Sqlite { pool }
    }

    /// The pool under the repository.
    pub fn pool(&self) -> &SqlitePool {
        &self.pool
    }
}

#[async_trait]
impl Repo for Sqlite {
    async fn begin(&self, _ctx: &Ctx) -> Result<Box<dyn Tx>> {
        let tx = goap_platform::begin_immediate(&self.pool)
            .await
            .map_err(Error::other)?;
        Ok(Box::new(SqlTx {
            c: SqliteConn { tx },
        }))
    }

    fn as_any(&self) -> &dyn Any {
        self
    }
}

/// sqliteTime is a fixed-width UTC layout: text order is time order.
const SQLITE_TIME: &str = "%Y-%m-%dT%H:%M:%S%.9fZ";

/// tsText writes a time in the fixed-width layout of the SQLite store (`2006-01-02T15:04:05.000000000Z`).
pub fn ts_text(t: DateTime<Utc>) -> String {
    t.format(SQLITE_TIME).to_string()
}

/// tsParse reads a time written by [`ts_text`]; anything else is Go's zero time.
pub fn ts_parse(s: &str) -> DateTime<Utc> {
    NaiveDateTime::parse_from_str(s, "%Y-%m-%dT%H:%M:%S%.fZ")
        .map(|n| n.and_utc())
        .unwrap_or_else(|_| gotime::zero())
}

/// sqliteErr maps a driver error: a unique or primary key violation is a conflict, a foreign key one an invalid
/// write.
fn sqlite_err(e: sqlx::Error, what: &str) -> Error {
    if let Some(db) = e.as_database_error() {
        let msg = db.message().to_string();
        if msg.contains("UNIQUE constraint failed") || msg.contains("PRIMARY KEY constraint failed")
        {
            return Error::wrap(ErrorKind::Conflict, format!("{what}: {msg}"));
        }
        if msg.contains("FOREIGN KEY constraint failed") {
            return Error::wrap(ErrorKind::Invalid, format!("{what}: {msg}"));
        }
    }
    Error::other(e)
}

pub(crate) struct SqliteConn {
    tx: Transaction<'static, SqliteDb>,
}

fn bind(
    q: sqlx::query::Query<'static, SqliteDb, SqliteArguments>,
    a: &Arg,
) -> sqlx::query::Query<'static, SqliteDb, SqliteArguments> {
    match a {
        Arg::Text(s) | Arg::Id(s) => q.bind(s.clone()),
        Arg::OptId(o) => q.bind(o.clone().filter(|s| !s.is_empty())),
        Arg::Int(i) => q.bind(*i),
        Arg::OptInt(o) => q.bind(*o),
        Arg::Bool(b) => q.bind(*b),
        Arg::Json(v) => q.bind(v.to_string()),
        Arg::Time(t) => q.bind(ts_text(*t)),
        Arg::Ints(v) => q.bind(serde_json::to_string(v).unwrap_or_else(|_| "[]".to_string())),
    }
}

fn cell(r: &SqliteRow, i: usize, k: Kind) -> std::result::Result<Cell, sqlx::Error> {
    Ok(match k {
        Kind::Text => Cell::Text(r.try_get::<Option<String>, _>(i)?.unwrap_or_default()),
        Kind::OptText => Cell::OptText(r.try_get::<Option<String>, _>(i)?),
        Kind::Int => Cell::Int(r.try_get::<Option<i64>, _>(i)?.unwrap_or_default()),
        Kind::OptInt => Cell::OptInt(r.try_get::<Option<i64>, _>(i)?),
        Kind::Bool => Cell::Bool(r.try_get::<Option<bool>, _>(i)?.unwrap_or_default()),
        Kind::Json => {
            let s = r.try_get::<Option<String>, _>(i)?.unwrap_or_default();
            Cell::Json(serde_json::from_str(&s).unwrap_or(Value::Null))
        }
        Kind::Time => Cell::Time(ts_parse(
            &r.try_get::<Option<String>, _>(i)?.unwrap_or_default(),
        )),
        Kind::Ints => {
            let s = r.try_get::<Option<String>, _>(i)?.unwrap_or_default();
            Cell::Ints(serde_json::from_str(&s).unwrap_or_default())
        }
    })
}

#[async_trait]
impl SqlConn for SqliteConn {
    const D: Dialect = DIALECT_SQLITE;

    async fn rows(
        &mut self,
        q: &str,
        args: &[Arg],
        kinds: &[Kind],
        what: &str,
    ) -> Result<Vec<Cells>> {
        let mut qy = sqlx::query(AssertSqlSafe(q.to_string()));
        for a in args {
            qy = bind(qy, a);
        }
        let rows = qy
            .fetch_all(&mut *self.tx)
            .await
            .map_err(|e| sqlite_err(e, what))?;
        let mut out = Vec::with_capacity(rows.len());
        for r in &rows {
            let mut cells = Vec::with_capacity(kinds.len());
            for (i, k) in kinds.iter().enumerate() {
                cells.push(cell(r, i, *k).map_err(|e| sqlite_err(e, what))?);
            }
            out.push(Cells::new(cells));
        }
        Ok(out)
    }

    async fn exec(&mut self, q: &str, args: &[Arg], what: &str) -> Result<u64> {
        let mut qy = sqlx::query(AssertSqlSafe(q.to_string()));
        for a in args {
            qy = bind(qy, a);
        }
        let res = qy
            .execute(&mut *self.tx)
            .await
            .map_err(|e| sqlite_err(e, what))?;
        Ok(res.rows_affected())
    }

    async fn commit(self: Box<Self>) -> Result<()> {
        self.tx.commit().await.map_err(Error::other)
    }

    async fn rollback(self: Box<Self>) -> Result<()> {
        self.tx.rollback().await.map_err(Error::other)
    }
}
