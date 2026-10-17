//! The process store on SQL (Go's `sqlstore.go`): SQLite for the local mode, PostgreSQL for the services; also the
//! lock service of the engine replicas sharing it (ADR 0124).

use std::time::Duration;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::gotime;
use goap_engine::{
    Error, Locker, OrphanFinder, Process, ProcessLogEntry, Result, STATUS_RUNNING, Store,
};
use goap_intent::Turn;
use include_dir::{Dir, include_dir};
use serde_json::Value;
use sqlx::{AssertSqlSafe, PgPool, Row, SqlitePool};

/// MIGRATIONS holds the PostgreSQL process schema (its `*.sql` files at the root: `goap_platform::migrate(ctx, pool,
/// &MIGRATIONS, ".")`); SQLITE_MIGRATIONS the local mode one.
pub static MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations");
/// SQLITE_MIGRATIONS holds the process schema of the local development mode.
pub static SQLITE_MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations_sqlite");

/// The database of a [`SQLStore`] (Go's `DB` with its `Dollar` flag set for PostgreSQL).
#[derive(Debug, Clone)]
pub enum SqlDb {
    Sqlite(SqlitePool),
    Postgres(PgPool),
}

/// SQLStore is an engine Store keeping processes in SQLite (local development mode) or PostgreSQL, so that runs and
/// pending human tasks survive restarts. It is also the lock service of the engine replicas sharing it (Locker, ADR
/// 0124) and tells the processes none of them works on (OrphanFinder).
#[derive(Debug, Clone)]
pub struct SQLStore {
    pub db: SqlDb,
    /// Holder names this replica in the locks it takes (new_holder); its writes to a process whose lock another
    /// replica holds are refused (ErrFenced).
    pub holder: String,
}

/// NewHolder is a replica identity for SQLStore.holder: host, process and a random part.
pub fn new_holder() -> String {
    let host = hostname();
    let id = uuid::Uuid::new_v4().to_string();
    format!("{host}-{}-{}", std::process::id(), &id[..8])
}

fn hostname() -> String {
    std::fs::read_to_string("/proc/sys/kernel/hostname")
        .ok()
        .or_else(|| std::fs::read_to_string("/etc/hostname").ok())
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
        .or_else(|| std::env::var("HOSTNAME").ok())
        .or_else(|| std::env::var("COMPUTERNAME").ok())
        .unwrap_or_default()
}

/// q rewrites "?" placeholders as $1, $2… (PostgreSQL).
fn q(postgres: bool, query: &str) -> String {
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

fn millis(t: DateTime<Utc>) -> i64 {
    t.timestamp_millis()
}

fn sql_err(e: sqlx::Error) -> Error {
    Error::msg(e.to_string())
}

/// One body for both dialects: `$pool` is the pool of the variant, `$pg` whether it is PostgreSQL.
macro_rules! on_pool {
    ($store:expr, $pool:ident, $pg:ident => $body:expr) => {
        match &$store.db {
            SqlDb::Sqlite($pool) => {
                let $pg = false;
                $body
            }
            SqlDb::Postgres($pool) => {
                let $pg = true;
                $body
            }
        }
    };
}

const PUT: &str = "INSERT INTO process (id, status, created_at, updated_at, body)
		SELECT CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS BIGINT), CAST(? AS TEXT)
		WHERE NOT EXISTS (SELECT 1 FROM engine_lock WHERE key = ? AND holder <> ? AND lease_until > ?)
		ON CONFLICT (id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at, body = excluded.body";

const ACQUIRE: &str =
    "INSERT INTO engine_lock (key, holder, lease_until, interrupt) VALUES (?, ?, ?, '')
		ON CONFLICT (key) DO UPDATE SET holder = excluded.holder, lease_until = excluded.lease_until,
			interrupt = CASE WHEN engine_lock.holder = excluded.holder THEN engine_lock.interrupt ELSE '' END
		WHERE engine_lock.holder = excluded.holder OR engine_lock.lease_until <= ?
		RETURNING interrupt";

const ORPHANS: &str = "SELECT id FROM process p WHERE status = ? AND updated_at < ?
		AND NOT EXISTS (SELECT 1 FROM engine_lock l WHERE l.key = p.id AND l.lease_until > ?)";

/// Go's `fmt.Sprint` of a JSON value of a payload.
fn sprint(v: Option<&Value>) -> String {
    match v {
        None | Some(Value::Null) => "<nil>".into(),
        Some(Value::String(s)) => s.clone(),
        Some(v) => v.to_string(),
    }
}

impl SQLStore {
    /// A store on SQLite for the replica `holder`.
    pub fn sqlite(pool: SqlitePool, holder: impl Into<String>) -> SQLStore {
        SQLStore {
            db: SqlDb::Sqlite(pool),
            holder: holder.into(),
        }
    }

    /// A store on PostgreSQL for the replica `holder`.
    pub fn postgres(pool: PgPool, holder: impl Into<String>) -> SQLStore {
        SQLStore {
            db: SqlDb::Postgres(pool),
            holder: holder.into(),
        }
    }

    /// ReleaseAll drops every lock: for the local mode only, the one process working on its database file, at
    /// start, so that what the previous run left is resumed at once rather than once its leases end.
    pub async fn release_all(&self, _ctx: &Ctx) -> Result<()> {
        on_pool!(self, pool, _pg => {
            sqlx::query("DELETE FROM engine_lock").execute(pool).await.map_err(sql_err)?;
        });
        Ok(())
    }

    /// fill_intent_turns reconstructs p.intent.turns from process_log, since put no longer persists it in the
    /// process row's body.
    async fn fill_intent_turns(&self, ctx: &Ctx, p: &mut Process) -> Result<()> {
        let entries = self.list_process_log(ctx, &p.id).await?;
        for e in entries {
            if e.r#type != "intent.turn" {
                continue;
            }
            p.intent.turns.push(Turn::new(
                sprint(e.payload.get("role")),
                sprint(e.payload.get("text")),
            ));
        }
        Ok(())
    }
}

#[async_trait]
impl Store for SQLStore {
    async fn get(&self, ctx: &Ctx, id: &str) -> Result<Process> {
        let body: Option<String> = on_pool!(self, pool, pg => {
            sqlx::query_scalar(AssertSqlSafe(q(pg, "SELECT body FROM process WHERE id = ?")))
                .bind(id)
                .fetch_optional(pool)
                .await
                .map_err(sql_err)?
        });
        let Some(body) = body else {
            return Err(Error::not_found(id));
        };
        let mut p: Process = serde_json::from_str(&body)?;
        self.fill_intent_turns(ctx, &mut p).await?;
        Ok(p)
    }

    /// Put stores a process. Intent.Turns is stripped from the stored body: it only ever grows, and re-serializing
    /// the whole (possibly long) dialogue on every turn would rewrite the row for no reason (ADR 0031) — each turn is
    /// instead appended to process_log by the engine (append_process_log) and folded back in by get/list. The write
    /// is refused (ErrFenced) while another replica holds the lock of the process (ADR 0124).
    async fn put(&self, _ctx: &Ctx, p: &Process) -> Result<()> {
        let mut stripped = p.clone();
        stripped.intent.turns = Vec::new();
        let body = serde_json::to_string(&stripped)?;
        let now = millis(Utc::now());
        let n = on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(q(pg, PUT)))
                .bind(&p.id)
                .bind(p.status.as_str())
                .bind(gotime::format(&p.created_at))
                .bind(now)
                .bind(&body)
                .bind(&p.id)
                .bind(&self.holder)
                .bind(now)
                .execute(pool)
                .await
                .map_err(sql_err)?
                .rows_affected()
        });
        if n == 0 {
            return Err(Error::Fenced(format!(
                "{}: {}",
                p.id,
                goap_engine::ERR_FENCED
            )));
        }
        Ok(())
    }

    /// List returns every process, newest first.
    async fn list(&self, ctx: &Ctx) -> Result<Vec<Process>> {
        let bodies: Vec<String> = on_pool!(self, pool, _pg => {
            sqlx::query_scalar("SELECT body FROM process")
                .fetch_all(pool)
                .await
                .map_err(sql_err)?
        });
        let mut out = Vec::with_capacity(bodies.len());
        for b in bodies {
            out.push(serde_json::from_str::<Process>(&b)?);
        }
        for p in &mut out {
            self.fill_intent_turns(ctx, p).await?;
        }
        out.sort_by_key(|p| std::cmp::Reverse(p.created_at));
        Ok(out)
    }

    async fn append_process_log(
        &self,
        _ctx: &Ctx,
        process_id: &str,
        entries: Vec<ProcessLogEntry>,
    ) -> Result<()> {
        for e in entries {
            // Go marshals the payload map: a nil map is "null"
            let payload = if e.payload.is_empty() {
                "null".to_string()
            } else {
                crate::handler::go_json(&Value::Object(e.payload))
            };
            let at = if gotime::is_zero(&e.at) {
                Utc::now()
            } else {
                e.at
            };
            on_pool!(self, pool, pg => {
                sqlx::query(AssertSqlSafe(q(
                    pg,
                    "INSERT INTO process_log (process_id, type, payload, at) VALUES (?, ?, ?, ?)",
                )))
                .bind(process_id)
                .bind(&e.r#type)
                .bind(&payload)
                .bind(gotime::format(&at))
                .execute(pool)
                .await
                .map_err(sql_err)?;
            });
        }
        Ok(())
    }

    /// ListProcessLog returns the log of a process, oldest first.
    async fn list_process_log(&self, _ctx: &Ctx, process_id: &str) -> Result<Vec<ProcessLogEntry>> {
        let rows: Vec<(i64, String, Option<String>, String)> = on_pool!(self, pool, pg => {
            let rows = sqlx::query(AssertSqlSafe(q(
                pg,
                "SELECT seq, type, payload, at FROM process_log WHERE process_id = ? ORDER BY seq",
            )))
            .bind(process_id)
            .fetch_all(pool)
            .await
            .map_err(sql_err)?;
            let mut out = Vec::with_capacity(rows.len());
            for r in rows {
                out.push((
                    r.try_get::<i64, _>(0).map_err(sql_err)?,
                    r.try_get::<String, _>(1).map_err(sql_err)?,
                    r.try_get::<Option<String>, _>(2).map_err(sql_err)?,
                    r.try_get::<String, _>(3).map_err(sql_err)?,
                ));
            }
            out
        });
        let mut out = Vec::with_capacity(rows.len());
        for (seq, typ, payload, at) in rows {
            let mut e = ProcessLogEntry {
                seq,
                process_id: process_id.to_string(),
                r#type: typ,
                ..Default::default()
            };
            if let Some(payload) = payload.filter(|p| !p.is_empty()) {
                // "null" decodes as an empty payload
                if let Some(m) =
                    serde_json::from_str::<Option<serde_json::Map<String, Value>>>(&payload)?
                {
                    e.payload = m;
                }
            }
            e.at = gotime::parse(&at)
                .ok_or_else(|| Error::msg(format!("parsing time {at:?}: invalid RFC 3339 time")))?;
            out.push(e);
        }
        Ok(out)
    }

    fn orphan_finder(&self) -> Option<&dyn OrphanFinder> {
        Some(self)
    }
}

#[async_trait]
impl Locker for SQLStore {
    async fn acquire(&self, _ctx: &Ctx, key: &str, ttl: Duration) -> Result<(bool, String)> {
        let now = Utc::now();
        let until = now + chrono::TimeDelta::from_std(ttl).unwrap_or(chrono::TimeDelta::MAX);
        let interrupt: Option<String> = on_pool!(self, pool, pg => {
            sqlx::query_scalar(AssertSqlSafe(q(pg, ACQUIRE)))
                .bind(key)
                .bind(&self.holder)
                .bind(millis(until))
                .bind(millis(now))
                .fetch_optional(pool)
                .await
                .map_err(sql_err)?
        });
        match interrupt {
            // another replica's lease runs
            None => Ok((false, String::new())),
            Some(i) => Ok((true, i)),
        }
    }

    async fn release(&self, _ctx: &Ctx, key: &str) -> Result<()> {
        on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(q(pg, "DELETE FROM engine_lock WHERE key = ? AND holder = ?")))
                .bind(key)
                .bind(&self.holder)
                .execute(pool)
                .await
                .map_err(sql_err)?;
        });
        Ok(())
    }

    async fn interrupt(&self, _ctx: &Ctx, key: &str, by: &str) -> Result<bool> {
        let n = on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(q(
                pg,
                "UPDATE engine_lock SET interrupt = ? WHERE key = ? AND holder <> ? AND lease_until > ?",
            )))
            .bind(by)
            .bind(key)
            .bind(&self.holder)
            .bind(millis(Utc::now()))
            .execute(pool)
            .await
            .map_err(sql_err)?
            .rows_affected()
        });
        Ok(n > 0)
    }
}

#[async_trait]
impl OrphanFinder for SQLStore {
    async fn orphans(&self, _ctx: &Ctx, before: DateTime<Utc>) -> Result<Vec<String>> {
        on_pool!(self, pool, pg => {
            sqlx::query_scalar(AssertSqlSafe(q(pg, ORPHANS)))
                .bind(STATUS_RUNNING)
                .bind(millis(before))
                .bind(millis(Utc::now()))
                .fetch_all(pool)
                .await
                .map_err(sql_err)
        })
    }
}
