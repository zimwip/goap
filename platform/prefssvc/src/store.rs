use std::collections::HashMap;

use async_trait::async_trait;
use chrono::{SecondsFormat, Utc};
use goap_ctx::Ctx;
use include_dir::{Dir, include_dir};
use parking_lot::Mutex;
use serde_json::{Map, Value};
use sqlx::{AssertSqlSafe, PgPool, Row, SqlitePool};

use crate::service::Error;

/// Migrations holds the PostgreSQL schema (its `*.sql` files at the root: `goap_platform::migrate(ctx, pool,
/// &MIGRATIONS, ".")`); SQLITE_MIGRATIONS the local mode one.
pub static MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations");
pub static SQLITE_MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations_sqlite");

/// Prefs is the preference document of a user.
pub type Prefs = Map<String, Value>;

/// The change [`Store::update`] applies: it gets the stored document and returns the one to store.
pub type UpdateFn<'a> = Box<dyn FnOnce(Prefs) -> Result<Prefs, Error> + Send + 'a>;

/// Store keeps the documents.
#[async_trait]
pub trait Store: Send + Sync {
    /// Get returns the document of a subject (empty until one is set).
    async fn get(&self, ctx: &Ctx, subject: &str) -> Result<Prefs, Error>;
    /// Update reads the document of a subject, lets f change it and stores the result, atomically.
    async fn update(&self, ctx: &Ctx, subject: &str, f: UpdateFn<'_>) -> Result<Prefs, Error>;
    /// Delete forgets the document of a subject.
    async fn delete(&self, ctx: &Ctx, subject: &str) -> Result<(), Error>;
}

/// `json.Marshal` of a document: `<`, `>`, `&`, U+2028 and U+2029 escaped the way Go escapes them.
pub(crate) fn go_json(v: &Prefs) -> String {
    let s = serde_json::to_string(v).unwrap_or_default();
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c => out.push(c),
        }
    }
    out
}

/// MemoryStore is an in-memory Store (tests, GOAP_STORE=memory).
#[derive(Default)]
pub struct MemoryStore {
    docs: Mutex<HashMap<String, Prefs>>,
}

impl MemoryStore {
    /// An empty in-memory store.
    pub fn new() -> Self {
        Self::default()
    }
}

#[async_trait]
impl Store for MemoryStore {
    async fn get(&self, _ctx: &Ctx, subject: &str) -> Result<Prefs, Error> {
        Ok(self.docs.lock().get(subject).cloned().unwrap_or_default())
    }

    async fn update(&self, _ctx: &Ctx, subject: &str, f: UpdateFn<'_>) -> Result<Prefs, Error> {
        let mut docs = self.docs.lock();
        let out = f(docs.get(subject).cloned().unwrap_or_default())?;
        docs.insert(subject.to_string(), out.clone());
        Ok(out)
    }

    async fn delete(&self, _ctx: &Ctx, subject: &str) -> Result<(), Error> {
        self.docs.lock().remove(subject);
        Ok(())
    }
}

/// SqlStore is the Store on SQL, for SQLite (local mode) and PostgreSQL (Go's `SQLStore`, `Dollar` set for the
/// latter).
#[derive(Debug, Clone)]
pub enum SqlStore {
    Sqlite(SqlitePool),
    Postgres(PgPool),
}

/// The queries are written once with `?` placeholders and PostgreSQL casts (`?::jsonb`): numbered for PostgreSQL,
/// the casts dropped for SQLite.
fn dialect(postgres: bool, query: &str) -> String {
    if !postgres {
        return query.replace("::jsonb", "").replace("::timestamptz", "");
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

fn decode(raw: Option<String>) -> Result<Prefs, Error> {
    match raw {
        None => Ok(Prefs::new()),
        Some(r) if r.is_empty() => Ok(Prefs::new()),
        Some(r) => serde_json::from_str(&r).map_err(Error::Stored),
    }
}

const SELECT: &str = "SELECT CAST(prefs AS TEXT) FROM user_preference WHERE subject = ?";
const UPSERT: &str =
    "INSERT INTO user_preference (subject, prefs, updated_at) VALUES (?, ?::jsonb, ?::timestamptz)
		ON CONFLICT (subject) DO UPDATE SET prefs = excluded.prefs, updated_at = excluded.updated_at";
const DELETE: &str = "DELETE FROM user_preference WHERE subject = ?";

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
    async fn get(&self, _ctx: &Ctx, subject: &str) -> Result<Prefs, Error> {
        on_pool!(self, pool, pg => {
            let row = sqlx::query(AssertSqlSafe(dialect(pg, SELECT)))
                .bind(subject)
                .fetch_optional(pool)
                .await?;
            decode(row.map(|r| r.try_get::<String, _>(0)).transpose()?)
        })
    }

    async fn update(&self, _ctx: &Ctx, subject: &str, f: UpdateFn<'_>) -> Result<Prefs, Error> {
        on_pool!(self, pool, pg => {
            let mut tx = if pg {
                pool.begin().await?
            } else {
                pool.begin_with("BEGIN IMMEDIATE").await?
            };
            // PostgreSQL: two writers of the same user do not lose each other's keys
            let lock = if pg { " FOR UPDATE" } else { "" };
            let res: Result<Prefs, Error> = async {
                let row = sqlx::query(AssertSqlSafe(dialect(pg, &format!("{SELECT}{lock}"))))
                    .bind(subject)
                    .fetch_optional(&mut *tx)
                    .await?;
                let cur = decode(row.map(|r| r.try_get::<String, _>(0)).transpose()?)?;
                let out = f(cur)?;
                let now = Utc::now().to_rfc3339_opts(SecondsFormat::Secs, true);
                sqlx::query(AssertSqlSafe(dialect(pg, UPSERT)))
                    .bind(subject)
                    .bind(go_json(&out))
                    .bind(now)
                    .execute(&mut *tx)
                    .await?;
                Ok(out)
            }
            .await;
            // a failure rolls back at once: a dropped transaction keeps its row lock until its connection is used
            match res {
                Ok(out) => {
                    tx.commit().await?;
                    Ok(out)
                }
                Err(e) => {
                    let _ = tx.rollback().await;
                    Err(e)
                }
            }
        })
    }

    async fn delete(&self, _ctx: &Ctx, subject: &str) -> Result<(), Error> {
        on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, DELETE)))
                .bind(subject)
                .execute(pool)
                .await?;
            Ok(())
        })
    }
}
