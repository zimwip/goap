use std::collections::HashMap;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
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

/// Roles and statuses of a message.
pub const ROLE_USER: &str = "user";
pub const ROLE_ASSISTANT: &str = "assistant";
pub const STATUS_PENDING: &str = "pending";
pub const STATUS_DONE: &str = "done";
pub const STATUS_ERROR: &str = "error";
/// StatusCancelled is an answer the person stopped: final, it keeps what the turn recorded before it stopped.
pub const STATUS_CANCELLED: &str = "cancelled";

/// Conversation is a thread of messages owned by one subject.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct Conversation {
    pub id: String,
    pub subject: String,
    pub title: String,
    pub created_at: DateTime<Utc>,
    pub updated_at: DateTime<Utc>,
}

/// Action is a structured UI action the assistant asked the web to run: {type, args, result?}.
pub type Action = Map<String, Value>;

/// Message is one entry of a conversation.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct Message {
    pub id: String,
    pub conversation_id: String,
    /// Seq is the position in the conversation, from 1.
    pub seq: i64,
    pub role: String,
    pub text: String,
    pub actions: Vec<Action>,
    /// ProcessID is the engine run that produced an assistant message (optional).
    pub process_id: String,
    pub status: String,
    pub error: String,
    /// Context is a short description of what the user was looking at when they wrote a user message (never the
    /// page itself, never form contents).
    pub context: String,
    pub created_at: DateTime<Utc>,
}

/// Summary is the rolling summary of the older part of a conversation (compaction): the messages up to the seq
/// `up_to` are summarised in `text`.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct Summary {
    pub conversation_id: String,
    pub text: String,
    pub up_to: i64,
    pub updated_at: DateTime<Utc>,
}

/// Cursor is the position after which a page of conversations starts (default: the first page).
#[derive(Debug, Clone, PartialEq, Default)]
pub struct Cursor {
    /// None: the zero time.
    pub updated_at: Option<DateTime<Utc>>,
    pub id: String,
}

impl Cursor {
    fn zero(&self) -> bool {
        self.id.is_empty() && self.updated_at.is_none()
    }

    fn micros(&self) -> i64 {
        self.updated_at.map(|t| micros(&t)).unwrap_or(ZERO_MICROS)
    }

    /// admits reports whether c comes after the cursor in the listing order (newest first).
    fn admits(&self, c: &Conversation) -> bool {
        if self.zero() {
            return true;
        }
        let (u, v) = (micros(&c.updated_at), self.micros());
        u < v || (u == v && c.id < self.id)
    }
}

/// The change [`Store::update_message`] applies to a message.
pub type MessageFn<'a> = Box<dyn FnOnce(Message) -> Result<Message, Error> + Send + 'a>;
/// The change [`crate::Service::update_action`] applies to an action.
pub type ActionFn<'a> = Box<dyn FnOnce(Action) -> Result<Action, Error> + Send + 'a>;

/// Store keeps the conversations. It enforces no ownership: the Service does.
#[async_trait]
pub trait Store: Send + Sync {
    /// Create adds a conversation, refusing it ([`Error::Full`]) when its subject already holds `max_per_subject` of
    /// them.
    async fn create(&self, ctx: &Ctx, c: Conversation, max_per_subject: i64) -> Result<(), Error>;
    async fn get(&self, ctx: &Ctx, id: &str) -> Result<Conversation, Error>;
    /// List returns the conversations of a subject, most recently updated first, after the cursor.
    async fn list(
        &self,
        ctx: &Ctx,
        subject: &str,
        after: &Cursor,
        limit: i64,
    ) -> Result<Vec<Conversation>, Error>;
    async fn rename(
        &self,
        ctx: &Ctx,
        id: &str,
        title: &str,
        now: DateTime<Utc>,
    ) -> Result<Conversation, Error>;
    /// Delete removes a conversation and its messages.
    async fn delete(&self, ctx: &Ctx, id: &str) -> Result<(), Error>;
    /// Messages returns the messages of a conversation in order.
    async fn messages(&self, ctx: &Ctx, conversation_id: &str) -> Result<Vec<Message>, Error>;
    async fn get_message(&self, ctx: &Ctx, id: &str) -> Result<Message, Error>;
    /// Append adds a message at the next position, refusing it ([`Error::Full`]) beyond `max_messages`, and
    /// touches the conversation.
    async fn append(&self, ctx: &Ctx, m: Message, max_messages: i64) -> Result<Message, Error>;
    /// UpdateMessage reads a message, lets f change it, stores the result and touches the conversation,
    /// atomically.
    async fn update_message(
        &self,
        ctx: &Ctx,
        id: &str,
        now: DateTime<Utc>,
        f: MessageFn<'_>,
    ) -> Result<Message, Error>;
    /// Summary returns the summary of a conversation ([`Error::NotFound`] when it has none); set_summary stores one
    /// unless the stored one already covers as many messages (up_to only grows).
    async fn summary(&self, ctx: &Ctx, conversation_id: &str) -> Result<Summary, Error>;
    async fn set_summary(&self, ctx: &Ctx, sum: Summary) -> Result<(), Error>;
}

/// The stored form of Go's zero time (`time.Time{}.UnixMicro()`).
const ZERO_MICROS: i64 = -62_135_596_800_000_000;

/// micros is the stored form of a time.
pub(crate) fn micros(t: &DateTime<Utc>) -> i64 {
    t.timestamp_micros()
}

pub(crate) fn from_micros(n: i64) -> DateTime<Utc> {
    DateTime::from_timestamp_micros(n).unwrap_or_default()
}

/// `json.Marshal`: `<`, `>`, `&`, U+2028 and U+2029 escaped the way Go escapes them.
pub(crate) fn go_json<T: serde::Serialize + ?Sized>(v: &T) -> String {
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

#[derive(Default)]
struct Memory {
    convs: HashMap<String, Conversation>,
    /// conversation id -> messages in order
    msgs: HashMap<String, Vec<Message>>,
    sums: HashMap<String, Summary>,
}

impl Memory {
    fn find(&self, id: &str) -> Option<(String, usize)> {
        self.msgs
            .iter()
            .find_map(|(cid, ms)| ms.iter().position(|m| m.id == id).map(|i| (cid.clone(), i)))
    }
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
    async fn summary(&self, _ctx: &Ctx, id: &str) -> Result<Summary, Error> {
        self.inner
            .lock()
            .sums
            .get(id)
            .cloned()
            .ok_or(Error::NotFound)
    }

    async fn set_summary(&self, _ctx: &Ctx, sum: Summary) -> Result<(), Error> {
        let mut s = self.inner.lock();
        if !s.convs.contains_key(&sum.conversation_id) {
            return Err(Error::NotFound);
        }
        if let Some(cur) = s.sums.get(&sum.conversation_id)
            && cur.up_to >= sum.up_to
        {
            return Ok(());
        }
        s.sums.insert(sum.conversation_id.clone(), sum);
        Ok(())
    }

    async fn create(&self, _ctx: &Ctx, c: Conversation, max_per_subject: i64) -> Result<(), Error> {
        let mut s = self.inner.lock();
        let n = s.convs.values().filter(|o| o.subject == c.subject).count() as i64;
        if n >= max_per_subject {
            return Err(Error::Full);
        }
        s.convs.insert(c.id.clone(), c);
        Ok(())
    }

    async fn get(&self, _ctx: &Ctx, id: &str) -> Result<Conversation, Error> {
        self.inner
            .lock()
            .convs
            .get(id)
            .cloned()
            .ok_or(Error::NotFound)
    }

    async fn list(
        &self,
        _ctx: &Ctx,
        subject: &str,
        after: &Cursor,
        limit: i64,
    ) -> Result<Vec<Conversation>, Error> {
        let s = self.inner.lock();
        let mut out: Vec<Conversation> = s
            .convs
            .values()
            .filter(|c| c.subject == subject && after.admits(c))
            .cloned()
            .collect();
        out.sort_by(|a, b| {
            b.updated_at
                .cmp(&a.updated_at)
                .then_with(|| b.id.cmp(&a.id))
        });
        out.truncate(limit.max(0) as usize);
        Ok(out)
    }

    async fn rename(
        &self,
        _ctx: &Ctx,
        id: &str,
        title: &str,
        now: DateTime<Utc>,
    ) -> Result<Conversation, Error> {
        let mut s = self.inner.lock();
        let c = s.convs.get_mut(id).ok_or(Error::NotFound)?;
        c.title = title.to_string();
        c.updated_at = now;
        Ok(c.clone())
    }

    async fn delete(&self, _ctx: &Ctx, id: &str) -> Result<(), Error> {
        let mut s = self.inner.lock();
        s.convs.remove(id);
        s.msgs.remove(id);
        s.sums.remove(id);
        Ok(())
    }

    async fn messages(&self, _ctx: &Ctx, id: &str) -> Result<Vec<Message>, Error> {
        Ok(self.inner.lock().msgs.get(id).cloned().unwrap_or_default())
    }

    async fn get_message(&self, _ctx: &Ctx, id: &str) -> Result<Message, Error> {
        let s = self.inner.lock();
        let (cid, i) = s.find(id).ok_or(Error::NotFound)?;
        Ok(s.msgs[&cid][i].clone())
    }

    async fn append(
        &self,
        _ctx: &Ctx,
        mut m: Message,
        max_messages: i64,
    ) -> Result<Message, Error> {
        let mut s = self.inner.lock();
        if !s.convs.contains_key(&m.conversation_id) {
            return Err(Error::NotFound);
        }
        let list = s.msgs.entry(m.conversation_id.clone()).or_default();
        if list.len() as i64 >= max_messages {
            return Err(Error::Full);
        }
        m.seq = list.len() as i64 + 1;
        list.push(m.clone());
        if let Some(c) = s.convs.get_mut(&m.conversation_id) {
            c.updated_at = m.created_at;
        }
        Ok(m)
    }

    async fn update_message(
        &self,
        _ctx: &Ctx,
        id: &str,
        now: DateTime<Utc>,
        f: MessageFn<'_>,
    ) -> Result<Message, Error> {
        let mut s = self.inner.lock();
        let (cid, i) = s.find(id).ok_or(Error::NotFound)?;
        let out = f(s.msgs[&cid][i].clone())?;
        if let Some(list) = s.msgs.get_mut(&cid) {
            list[i] = out.clone();
        }
        if let Some(c) = s.convs.get_mut(&cid) {
            c.updated_at = now;
        }
        Ok(out)
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

/// One body for both dialects: `$pool` is the pool of the variant, `$pg` whether it is PostgreSQL, `$begin` a
/// write transaction (`BEGIN IMMEDIATE` on SQLite).
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

macro_rules! begin {
    ($pool:expr, $pg:expr) => {
        if $pg {
            $pool.begin().await?
        } else {
            $pool.begin_with("BEGIN IMMEDIATE").await?
        }
    };
}

/// Runs the body of a transaction: committed when it succeeds, rolled back at once when it fails (a dropped
/// transaction would only be rolled back when its connection is next used, holding its locks meanwhile).
macro_rules! in_tx {
    ($tx:ident, $body:block) => {{
        let res: Result<_, Error> = async { $body }.await;
        match res {
            Ok(v) => {
                $tx.commit().await?;
                Ok(v)
            }
            Err(e) => {
                let _ = $tx.rollback().await;
                Err(e)
            }
        }
    }};
}

/// lock is the row lock of a PostgreSQL read-modify-write (SQLite serializes its writers).
fn lock(pg: bool) -> &'static str {
    if pg { " FOR UPDATE" } else { "" }
}

const CONV_COLS: &str = "id, subject, title, created_at, updated_at";
const MSG_COLS: &str =
    "id, conversation_id, seq, role, body, actions, process_id, status, error, context, created_at";
const MSG_SELECT: &str = "id, conversation_id, seq, role, body, CAST(actions AS TEXT), process_id, status, error, context, created_at";

fn scan_conv<R: Row>(r: &R) -> Result<Conversation, Error>
where
    usize: sqlx::ColumnIndex<R>,
    String: for<'r> sqlx::Decode<'r, R::Database> + sqlx::Type<R::Database>,
    i64: for<'r> sqlx::Decode<'r, R::Database> + sqlx::Type<R::Database>,
{
    Ok(Conversation {
        id: r.try_get(0)?,
        subject: r.try_get(1)?,
        title: r.try_get(2)?,
        created_at: from_micros(r.try_get(3)?),
        updated_at: from_micros(r.try_get(4)?),
    })
}

fn scan_msg<R: Row>(r: &R) -> Result<Message, Error>
where
    usize: sqlx::ColumnIndex<R>,
    String: for<'r> sqlx::Decode<'r, R::Database> + sqlx::Type<R::Database>,
    i64: for<'r> sqlx::Decode<'r, R::Database> + sqlx::Type<R::Database>,
    i32: for<'r> sqlx::Decode<'r, R::Database> + sqlx::Type<R::Database>,
{
    let actions: String = r.try_get(5)?;
    let actions = if actions.is_empty() {
        Vec::new()
    } else {
        serde_json::from_str(&actions).map_err(Error::StoredActions)?
    };
    Ok(Message {
        id: r.try_get(0)?,
        conversation_id: r.try_get(1)?,
        seq: r.try_get::<i32, _>(2)?.into(),
        role: r.try_get(3)?,
        text: r.try_get(4)?,
        actions,
        process_id: r.try_get(6)?,
        status: r.try_get(7)?,
        error: r.try_get(8)?,
        context: r.try_get(9)?,
        created_at: from_micros(r.try_get(10)?),
    })
}

fn not_found(e: sqlx::Error) -> Error {
    match e {
        sqlx::Error::RowNotFound => Error::NotFound,
        e => Error::Sql(e),
    }
}

fn encode_actions(a: &[Action]) -> String {
    go_json(a)
}

#[async_trait]
impl Store for SqlStore {
    async fn summary(&self, _ctx: &Ctx, id: &str) -> Result<Summary, Error> {
        on_pool!(self, pool, pg => {
            let r = sqlx::query(AssertSqlSafe(dialect(pg,
                "SELECT summary, up_to, updated_at FROM conversation_summary WHERE conversation_id = ?")))
                .bind(id)
                .fetch_one(pool)
                .await
                .map_err(not_found)?;
            Ok(Summary {
                conversation_id: id.to_string(),
                text: r.try_get(0)?,
                up_to: r.try_get::<i32, _>(1)?.into(),
                updated_at: from_micros(r.try_get(2)?),
            })
        })
    }

    async fn set_summary(&self, _ctx: &Ctx, sum: Summary) -> Result<(), Error> {
        // UpTo only grows: two compactions racing keep the one that covers more
        on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg, "INSERT INTO conversation_summary (conversation_id, summary, up_to, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (conversation_id) DO UPDATE SET summary = excluded.summary, up_to = excluded.up_to, updated_at = excluded.updated_at
		WHERE conversation_summary.up_to < excluded.up_to")))
                .bind(&sum.conversation_id)
                .bind(&sum.text)
                .bind(sum.up_to as i32)
                .bind(micros(&sum.updated_at))
                .execute(pool)
                .await?;
            Ok(())
        })
    }

    async fn create(&self, _ctx: &Ctx, c: Conversation, max_per_subject: i64) -> Result<(), Error> {
        on_pool!(self, pool, pg => {
            let mut tx = begin!(pool, pg);
            in_tx!(tx, {
                let n: i64 = sqlx::query_scalar(AssertSqlSafe(dialect(pg,
                    "SELECT count(*) FROM conversation WHERE subject = ?")))
                    .bind(&c.subject)
                    .fetch_one(&mut *tx)
                    .await?;
                if n >= max_per_subject {
                    return Err(Error::Full);
                }
                sqlx::query(AssertSqlSafe(dialect(pg, &format!(
                    "INSERT INTO conversation ({CONV_COLS}) VALUES (?, ?, ?, ?, ?)"))))
                    .bind(&c.id)
                    .bind(&c.subject)
                    .bind(&c.title)
                    .bind(micros(&c.created_at))
                    .bind(micros(&c.updated_at))
                    .execute(&mut *tx)
                    .await?;
                Ok(())
            })
        })
    }

    async fn get(&self, _ctx: &Ctx, id: &str) -> Result<Conversation, Error> {
        on_pool!(self, pool, pg => {
            let r = sqlx::query(AssertSqlSafe(dialect(pg, &format!(
                "SELECT {CONV_COLS} FROM conversation WHERE id = ?"))))
                .bind(id)
                .fetch_one(pool)
                .await
                .map_err(not_found)?;
            scan_conv(&r)
        })
    }

    async fn list(
        &self,
        _ctx: &Ctx,
        subject: &str,
        after: &Cursor,
        limit: i64,
    ) -> Result<Vec<Conversation>, Error> {
        let mut query = format!("SELECT {CONV_COLS} FROM conversation WHERE subject = ?");
        if !after.zero() {
            query.push_str(" AND (updated_at < ? OR (updated_at = ? AND id < ?))");
        }
        query.push_str(" ORDER BY updated_at DESC, id DESC LIMIT ?");
        on_pool!(self, pool, pg => {
            let q = dialect(pg, &query);
            let mut q = sqlx::query(AssertSqlSafe(q)).bind(subject);
            if !after.zero() {
                let u = after.micros();
                q = q.bind(u).bind(u).bind(after.id.clone());
            }
            let rows = q.bind(limit).fetch_all(pool).await?;
            rows.iter().map(scan_conv).collect()
        })
    }

    async fn rename(
        &self,
        ctx: &Ctx,
        id: &str,
        title: &str,
        now: DateTime<Utc>,
    ) -> Result<Conversation, Error> {
        let n = on_pool!(self, pool, pg => {
            sqlx::query(AssertSqlSafe(dialect(pg,
                "UPDATE conversation SET title = ?, updated_at = ? WHERE id = ?")))
                .bind(title)
                .bind(micros(&now))
                .bind(id)
                .execute(pool)
                .await?
                .rows_affected()
        });
        if n == 0 {
            return Err(Error::NotFound);
        }
        self.get(ctx, id).await
    }

    async fn delete(&self, _ctx: &Ctx, id: &str) -> Result<(), Error> {
        on_pool!(self, pool, pg => {
            let mut tx = begin!(pool, pg);
            in_tx!(tx, {
                // explicit: SQLite enforces foreign keys only when asked to
                for q in [
                    "DELETE FROM conversation_message WHERE conversation_id = ?",
                    "DELETE FROM conversation_summary WHERE conversation_id = ?",
                    "DELETE FROM conversation WHERE id = ?",
                ] {
                    sqlx::query(AssertSqlSafe(dialect(pg, q)))
                        .bind(id)
                        .execute(&mut *tx)
                        .await?;
                }
                Ok(())
            })
        })
    }

    async fn messages(&self, _ctx: &Ctx, conversation_id: &str) -> Result<Vec<Message>, Error> {
        on_pool!(self, pool, pg => {
            let rows = sqlx::query(AssertSqlSafe(dialect(pg, &format!(
                "SELECT {MSG_SELECT} FROM conversation_message WHERE conversation_id = ? ORDER BY seq"))))
                .bind(conversation_id)
                .fetch_all(pool)
                .await?;
            rows.iter().map(scan_msg).collect()
        })
    }

    async fn get_message(&self, _ctx: &Ctx, id: &str) -> Result<Message, Error> {
        on_pool!(self, pool, pg => {
            let r = sqlx::query(AssertSqlSafe(dialect(pg, &format!(
                "SELECT {MSG_SELECT} FROM conversation_message WHERE id = ?"))))
                .bind(id)
                .fetch_one(pool)
                .await
                .map_err(not_found)?;
            scan_msg(&r)
        })
    }

    async fn append(
        &self,
        _ctx: &Ctx,
        mut m: Message,
        max_messages: i64,
    ) -> Result<Message, Error> {
        on_pool!(self, pool, pg => {
            let mut tx = begin!(pool, pg);
            in_tx!(tx, {
                // the conversation row is the lock of its messages: two appenders do not take the same position
                let id: String = sqlx::query_scalar(AssertSqlSafe(dialect(pg, &format!(
                    "SELECT id FROM conversation WHERE id = ?{}", lock(pg)))))
                    .bind(&m.conversation_id)
                    .fetch_one(&mut *tx)
                    .await
                    .map_err(not_found)?;
                let n: i64 = sqlx::query_scalar(AssertSqlSafe(dialect(pg,
                    "SELECT count(*) FROM conversation_message WHERE conversation_id = ?")))
                    .bind(&id)
                    .fetch_one(&mut *tx)
                    .await?;
                if n >= max_messages {
                    return Err(Error::Full);
                }
                m.seq = n + 1;
                sqlx::query(AssertSqlSafe(dialect(pg, &format!(
                    "INSERT INTO conversation_message ({MSG_COLS}) VALUES (?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?)"))))
                    .bind(&m.id)
                    .bind(&m.conversation_id)
                    .bind(m.seq as i32)
                    .bind(&m.role)
                    .bind(&m.text)
                    .bind(encode_actions(&m.actions))
                    .bind(&m.process_id)
                    .bind(&m.status)
                    .bind(&m.error)
                    .bind(&m.context)
                    .bind(micros(&m.created_at))
                    .execute(&mut *tx)
                    .await?;
                sqlx::query(AssertSqlSafe(dialect(pg, "UPDATE conversation SET updated_at = ? WHERE id = ?")))
                    .bind(micros(&m.created_at))
                    .bind(&id)
                    .execute(&mut *tx)
                    .await?;
                Ok(m)
            })
        })
    }

    async fn update_message(
        &self,
        _ctx: &Ctx,
        id: &str,
        now: DateTime<Utc>,
        f: MessageFn<'_>,
    ) -> Result<Message, Error> {
        on_pool!(self, pool, pg => {
            let mut tx = begin!(pool, pg);
            in_tx!(tx, {
                let r = sqlx::query(AssertSqlSafe(dialect(pg, &format!(
                    "SELECT {MSG_SELECT} FROM conversation_message WHERE id = ?{}", lock(pg)))))
                    .bind(id)
                    .fetch_one(&mut *tx)
                    .await
                    .map_err(not_found)?;
                let cur = scan_msg(&r)?;
                let conversation = cur.conversation_id.clone();
                let out = f(cur)?;
                sqlx::query(AssertSqlSafe(dialect(pg,
                    "UPDATE conversation_message SET body = ?, actions = ?::jsonb, process_id = ?, status = ?, error = ? WHERE id = ?")))
                    .bind(&out.text)
                    .bind(encode_actions(&out.actions))
                    .bind(&out.process_id)
                    .bind(&out.status)
                    .bind(&out.error)
                    .bind(id)
                    .execute(&mut *tx)
                    .await?;
                sqlx::query(AssertSqlSafe(dialect(pg, "UPDATE conversation SET updated_at = ? WHERE id = ?")))
                    .bind(micros(&now))
                    .bind(&conversation)
                    .execute(&mut *tx)
                    .await?;
                Ok(out)
            })
        })
    }
}
