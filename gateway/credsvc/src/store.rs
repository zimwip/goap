use std::collections::HashMap;

use async_trait::async_trait;
use chrono::{DateTime, SecondsFormat, Utc};
use goap_ctx::Ctx;
use include_dir::{Dir, include_dir};
use parking_lot::Mutex;
use sqlx::{AssertSqlSafe, PgPool, Row, SqlitePool};

use crate::service::Error;

/// Migrations holds the PostgreSQL schema (its `*.sql` files at the root: `goap_platform::migrate(ctx, pool,
/// &MIGRATIONS, ".")`); SQLITE_MIGRATIONS the local mode one.
pub static MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations");
pub static SQLITE_MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations_sqlite");

/// Store keeps the password hashes.
#[async_trait]
pub trait Store: Send + Sync {
    /// Create inserts the hash of a new subject. [`Error::Exists`] if one is already there.
    async fn create(&self, ctx: &Ctx, subject: &str, hash: &str) -> Result<(), Error>;
    /// Hash returns the stored hash of a subject. [`Error::NotFound`] if there is none.
    async fn hash(&self, ctx: &Ctx, subject: &str) -> Result<String, Error>;
    /// Set replaces the hash of a subject. [`Error::NotFound`] if there is none.
    async fn set(&self, ctx: &Ctx, subject: &str, hash: &str) -> Result<(), Error>;
    /// Exists reports whether a subject has a credential.
    async fn exists(&self, ctx: &Ctx, subject: &str) -> Result<bool, Error>;

    /// CreateSession records a new sign-in session (ADR 0045), and forgets the sessions expired before
    /// purge_before.
    async fn create_session(
        &self,
        ctx: &Ctx,
        sess: Session,
        purge_before: DateTime<Utc>,
    ) -> Result<(), Error>;
    /// Session returns a session. [`Error::NotFound`] if there is none.
    async fn session(&self, ctx: &Ctx, id: &str) -> Result<Session, Error>;
    /// RevokeSession ends a session (no error when it is unknown or already ended).
    async fn revoke_session(&self, ctx: &Ctx, id: &str, at: DateTime<Utc>) -> Result<(), Error>;
    /// RevokeSessions ends every session of a subject still open.
    async fn revoke_sessions(
        &self,
        ctx: &Ctx,
        subject: &str,
        at: DateTime<Utc>,
    ) -> Result<(), Error>;
}

/// Session is a sign-in (ADR 0045): every token issued from it carries its id; it ends when its subject signs out
/// (revoked), changes their password, or it expires.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct Session {
    pub id: String,
    pub subject: String,
    pub created: DateTime<Utc>,
    pub expires: DateTime<Utc>,
    /// Revoked is when the session was ended, None while it is open.
    pub revoked: Option<DateTime<Utc>>,
}

impl Session {
    /// Active reports whether the session still accepts its tokens at t.
    pub fn active(&self, t: DateTime<Utc>) -> bool {
        self.revoked.is_none() && t < self.expires
    }
}

#[derive(Default)]
struct Memory {
    hashes: HashMap<String, String>,
    sessions: HashMap<String, Session>,
}

/// MemoryStore is an in-memory Store (tests, GOAP_STORE=memory).
#[derive(Default)]
pub struct MemoryStore {
    inner: Mutex<Memory>,
}

impl MemoryStore {
    /// An empty in-memory store.
    pub fn new() -> Self {
        Self::default()
    }
}

#[async_trait]
impl Store for MemoryStore {
    async fn create(&self, _ctx: &Ctx, subject: &str, hash: &str) -> Result<(), Error> {
        let mut s = self.inner.lock();
        if s.hashes.contains_key(subject) {
            return Err(Error::Exists);
        }
        s.hashes.insert(subject.to_string(), hash.to_string());
        Ok(())
    }

    async fn hash(&self, _ctx: &Ctx, subject: &str) -> Result<String, Error> {
        self.inner
            .lock()
            .hashes
            .get(subject)
            .cloned()
            .ok_or(Error::NotFound)
    }

    async fn set(&self, _ctx: &Ctx, subject: &str, hash: &str) -> Result<(), Error> {
        let mut s = self.inner.lock();
        let h = s.hashes.get_mut(subject).ok_or(Error::NotFound)?;
        *h = hash.to_string();
        Ok(())
    }

    async fn exists(&self, _ctx: &Ctx, subject: &str) -> Result<bool, Error> {
        Ok(self.inner.lock().hashes.contains_key(subject))
    }

    async fn create_session(
        &self,
        _ctx: &Ctx,
        sess: Session,
        purge_before: DateTime<Utc>,
    ) -> Result<(), Error> {
        let mut s = self.inner.lock();
        s.sessions.retain(|_, o| o.expires >= purge_before);
        if s.sessions.contains_key(&sess.id) {
            return Err(Error::Exists);
        }
        s.sessions.insert(sess.id.clone(), sess);
        Ok(())
    }

    async fn session(&self, _ctx: &Ctx, id: &str) -> Result<Session, Error> {
        self.inner
            .lock()
            .sessions
            .get(id)
            .cloned()
            .ok_or(Error::NotFound)
    }

    async fn revoke_session(&self, _ctx: &Ctx, id: &str, at: DateTime<Utc>) -> Result<(), Error> {
        if let Some(sess) = self.inner.lock().sessions.get_mut(id)
            && sess.revoked.is_none()
        {
            sess.revoked = Some(at);
        }
        Ok(())
    }

    async fn revoke_sessions(
        &self,
        _ctx: &Ctx,
        subject: &str,
        at: DateTime<Utc>,
    ) -> Result<(), Error> {
        for sess in self.inner.lock().sessions.values_mut() {
            if sess.subject == subject && sess.revoked.is_none() {
                sess.revoked = Some(at);
            }
        }
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

/// The queries are written once with `?` placeholders and PostgreSQL casts (`?::timestamptz`): numbered for
/// PostgreSQL, the casts dropped for SQLite.
fn dialect(postgres: bool, query: &str) -> String {
    if !postgres {
        return query.replace("::timestamptz", "");
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

/// Go's `time.RFC3339` of now, in UTC (the `created_at` / `updated_at` of a credential).
fn now_rfc3339() -> String {
    Utc::now().to_rfc3339_opts(SecondsFormat::Secs, true)
}

/// stamp is how the SQL store writes a time: Go's RFC 3339 with nanoseconds (trailing zeros dropped) in UTC, which
/// sorts as text (SQLite) and casts to timestamptz (PostgreSQL).
fn stamp(t: &DateTime<Utc>) -> String {
    let base = t.format("%Y-%m-%dT%H:%M:%S").to_string();
    let nanos = t.timestamp_subsec_nanos();
    if nanos == 0 {
        return format!("{base}Z");
    }
    let frac = format!("{nanos:09}");
    format!("{base}.{}Z", frac.trim_end_matches('0'))
}

fn parse_time(v: &str) -> Result<DateTime<Utc>, Error> {
    DateTime::parse_from_rfc3339(v)
        .map(|t| t.with_timezone(&Utc))
        .map_err(|e| Error::Time(format!("parsing time {v:?}: {e}")))
}

fn unique(e: &sqlx::Error) -> bool {
    e.as_database_error()
        .is_some_and(|d| d.is_unique_violation())
        || e.to_string().to_lowercase().contains("unique")
}

#[async_trait]
impl Store for SqlStore {
    async fn create(&self, _ctx: &Ctx, subject: &str, hash: &str) -> Result<(), Error> {
        let now = now_rfc3339();
        let r = on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, "INSERT INTO credential (subject, password_hash, created_at, updated_at) VALUES (?, ?, ?::timestamptz, ?::timestamptz)")))
                .bind(subject)
                .bind(hash)
                .bind(&now)
                .bind(&now)
                .execute(pool)
                .await
                .map(|_| ())
        });
        match r {
            Err(e) if unique(&e) => Err(Error::Exists),
            r => Ok(r?),
        }
    }

    async fn hash(&self, _ctx: &Ctx, subject: &str) -> Result<String, Error> {
        let h: Option<String> = on_pool!(self, pool, pg => {
            sqlx::query_scalar(AssertSqlSafe(dialect(pg, "SELECT password_hash FROM credential WHERE subject = ?")))
                .bind(subject)
                .fetch_optional(pool)
                .await?
        });
        h.ok_or(Error::NotFound)
    }

    async fn set(&self, _ctx: &Ctx, subject: &str, hash: &str) -> Result<(), Error> {
        let now = now_rfc3339();
        let n = on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, "UPDATE credential SET password_hash = ?, updated_at = ?::timestamptz WHERE subject = ?")))
                .bind(hash)
                .bind(&now)
                .bind(subject)
                .execute(pool)
                .await?
                .rows_affected()
        });
        if n == 0 {
            return Err(Error::NotFound);
        }
        Ok(())
    }

    async fn exists(&self, _ctx: &Ctx, subject: &str) -> Result<bool, Error> {
        let row = on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, "SELECT 1 FROM credential WHERE subject = ?")))
                .bind(subject)
                .fetch_optional(pool)
                .await?
                .is_some()
        });
        Ok(row)
    }

    async fn create_session(
        &self,
        _ctx: &Ctx,
        sess: Session,
        purge_before: DateTime<Utc>,
    ) -> Result<(), Error> {
        let r = on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, "DELETE FROM auth_session WHERE expires_at < ?::timestamptz")))
                .bind(stamp(&purge_before))
                .execute(pool)
                .await?;
            sqlx::query(AssertSqlSafe(dialect(pg, "INSERT INTO auth_session (id, subject, created_at, expires_at) VALUES (?, ?, ?::timestamptz, ?::timestamptz)")))
                .bind(&sess.id)
                .bind(&sess.subject)
                .bind(stamp(&sess.created))
                .bind(stamp(&sess.expires))
                .execute(pool)
                .await
                .map(|_| ())
        });
        match r {
            Err(e) if unique(&e) => Err(Error::Exists),
            r => Ok(r?),
        }
    }

    async fn session(&self, _ctx: &Ctx, id: &str) -> Result<Session, Error> {
        const Q: &str =
            "SELECT subject, created_at, expires_at, revoked_at FROM auth_session WHERE id = ?";
        match self {
            // timestamptz in PostgreSQL
            SqlStore::Postgres(pool) => {
                let r = sqlx::query(AssertSqlSafe(dialect(true, Q)))
                    .bind(id)
                    .fetch_optional(pool)
                    .await?
                    .ok_or(Error::NotFound)?;
                Ok(Session {
                    id: id.to_string(),
                    subject: r.try_get(0)?,
                    created: r.try_get(1)?,
                    expires: r.try_get(2)?,
                    revoked: r.try_get(3)?,
                })
            }
            // text written by stamp in SQLite
            SqlStore::Sqlite(pool) => {
                let r = sqlx::query(AssertSqlSafe(dialect(false, Q)))
                    .bind(id)
                    .fetch_optional(pool)
                    .await?
                    .ok_or(Error::NotFound)?;
                let revoked: Option<String> = r.try_get(3)?;
                Ok(Session {
                    id: id.to_string(),
                    subject: r.try_get(0)?,
                    created: parse_time(&r.try_get::<String, _>(1)?)?,
                    expires: parse_time(&r.try_get::<String, _>(2)?)?,
                    revoked: revoked.as_deref().map(parse_time).transpose()?,
                })
            }
        }
    }

    async fn revoke_session(&self, _ctx: &Ctx, id: &str, at: DateTime<Utc>) -> Result<(), Error> {
        on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, "UPDATE auth_session SET revoked_at = ?::timestamptz WHERE id = ? AND revoked_at IS NULL")))
                .bind(stamp(&at))
                .bind(id)
                .execute(pool)
                .await?;
        });
        Ok(())
    }

    async fn revoke_sessions(
        &self,
        _ctx: &Ctx,
        subject: &str,
        at: DateTime<Utc>,
    ) -> Result<(), Error> {
        on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, "UPDATE auth_session SET revoked_at = ?::timestamptz WHERE subject = ? AND revoked_at IS NULL")))
                .bind(stamp(&at))
                .bind(subject)
                .execute(pool)
                .await?;
        });
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use chrono::TimeZone;

    use super::*;

    #[test]
    fn stamp_is_go_rfc3339_nano() {
        let t = Utc.with_ymd_and_hms(2026, 1, 2, 3, 4, 5).unwrap();
        assert_eq!(stamp(&t), "2026-01-02T03:04:05Z");
        let t = t + chrono::TimeDelta::milliseconds(120);
        assert_eq!(stamp(&t), "2026-01-02T03:04:05.12Z");
        assert_eq!(parse_time(&stamp(&t)).unwrap(), t);
    }
}
