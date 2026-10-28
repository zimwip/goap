//! The reads of the graph ([`graph_core::store::ReadTx`]) over a read-only session of either dialect.
mod domains;
pub use domains::{NewDomain, StoredDomain};
mod browse;
pub use browse::Tag;
mod log;
pub use log::LogFilter;
mod outbox;
pub use outbox::Pending;
mod smarttags;
pub use smarttags::{SmartTag, Which};
mod sql;
mod value;
mod versions;
mod write;

pub use versions::Versions;

use std::collections::{BTreeMap, HashMap};

use graph_core::{
    GraphError,
    change::{Change, ChangesFilter, GuardianRef},
    lnk::Url,
    model::{
        Baseline, BaselineId, Branch, ChangeId, Link, LinkId, Node, NodeId, NodePage, NodeQuery, NodeRef,
        REASON_REVERT, Target, Version,
    },
    page::type_counts,
    store::{REVERTED, ReadTx},
};
pub use sql::like_pattern;
pub use value::{Arg, Row, Val};

use crate::{
    StoreError,
    sqlite::{SqliteConn, SqliteSession, SqliteTx},
};
use sql::{Sql, Stmt};

/// A read-only session: a reader of the pool of its dialect, held for the request.
pub struct Session {
    conn: Conn,
    sql: Sql,
    /// What the request read of the platform graph, in a content graph ([`ReadTx::platform`]).
    platform: Option<std::sync::Arc<graph_core::platform::Platform>>,
    /// The type catalogue in force ([`ReadTx::catalog`]).
    catalog: Option<std::sync::Arc<graph_catalog::Catalog>>,
    /// What the methodologies of the changes are asked through ([`ReadTx::guardian`]).
    guardian: Option<std::sync::Arc<dyn graph_core::guardian::Guardian>>,
    /// The versions kept between the transactions of the store, and what this one read ([`versions`]).
    versions: std::sync::Arc<Versions>,
    tx: versions::Read,
    /// The statements of a write transaction nothing waits on, sent with its next statement or its COMMIT in one
    /// round trip (a pipeline): its BEGIN, a lock, an insert whose answer is known. The statements of a pipeline run
    /// one after the other, each with a snapshot of its own (READ COMMITTED), so a read sent after a lock reads what
    /// the lock holds.
    queued: std::sync::Mutex<Vec<Queued>>,
}

/// A statement queued: a statement of no argument, or one with what an error of it is about.
enum Queued {
    Simple(&'static str),
    Stmt(Stmt, String),
}

enum Conn {
    Pg(deadpool_postgres::Object),
    Sqlite(SqliteSession),
    /// A write transaction on a writer of the pool: none once it committed.
    PgTx(Option<deadpool_postgres::Object>),
    SqliteTx(Option<SqliteTx>),
}

impl Drop for Session {
    /// A PostgreSQL transaction neither committed nor rolled back is rolled back before its connection goes back to
    /// the pool (the SQLite one rolls itself back).
    fn drop(&mut self) {
        if let Conn::PgTx(c) = &mut self.conn
            && let Some(c) = c.take()
            && let Ok(h) = tokio::runtime::Handle::try_current()
        {
            h.spawn(async move {
                if c.batch_execute("ROLLBACK").await.is_err() {
                    // a connection whose state is unknown leaves the pool
                    drop(deadpool_postgres::Object::take(c));
                }
            });
        }
    }
}

impl From<StoreError> for GraphError {
    fn from(e: StoreError) -> GraphError {
        GraphError::internal(e)
    }
}

impl Session {
    async fn committed_rows(
        &self,
        namespace: &str,
        branch: &str,
        after: i64,
        up_to: i64,
        valid: Option<&[NodeId]>,
    ) -> Result<HashMap<NodeId, Version>, GraphError> {
        let rows = self
            .rows(self.sql.committed_versions(namespace, branch, after, up_to, valid))
            .await?;
        Ok(rows
            .iter()
            .map(|r| {
                let v = match (r.bool(2), r.text(3) == REASON_REVERT) {
                    (_, true) => REVERTED,
                    (true, false) => 0,
                    (false, false) => r.int32(1),
                };
                (NodeId(r.text(0)), v)
            })
            .collect())
    }

    /// The session knowing the type catalogue in force.
    pub fn with_catalog(mut self, c: std::sync::Arc<graph_catalog::Catalog>) -> Session {
        self.catalog = Some(c);
        self
    }

    /// The session asking the methodologies of its changes of a guardian (ADR 0168).
    pub fn with_guardian(mut self, g: Option<std::sync::Arc<dyn graph_core::guardian::Guardian>>) -> Session {
        self.guardian = g;
        self
    }

    /// The session reading the platform's namespaces from what the request read of the platform graph.
    pub fn with_platform(mut self, p: Option<std::sync::Arc<graph_core::platform::Platform>>) -> Session {
        self.platform = p;
        self
    }

    pub(crate) fn postgres(c: deadpool_postgres::Object, versions: std::sync::Arc<Versions>) -> Session {
        Session {
            conn: Conn::Pg(c),
            sql: Sql { pg: true },
            platform: None,
            catalog: None,
            guardian: None,
            versions,
            tx: Default::default(),
            queued: Default::default(),
        }
    }

    pub(crate) fn sqlite(s: SqliteSession, versions: std::sync::Arc<Versions>) -> Session {
        Session {
            conn: Conn::Sqlite(s),
            sql: Sql { pg: false },
            platform: None,
            catalog: None,
            guardian: None,
            versions,
            tx: Default::default(),
            queued: Default::default(),
        }
    }

    /// A write transaction on a writer of PostgreSQL.
    pub(crate) async fn postgres_tx(
        c: deadpool_postgres::Object,
        versions: std::sync::Arc<Versions>,
    ) -> Result<Session, StoreError> {
        // sent with the first statement
        Ok(Session {
            conn: Conn::PgTx(Some(c)),
            sql: Sql { pg: true },
            platform: None,
            catalog: None,
            guardian: None,
            versions,
            tx: Default::default(),
            queued: std::sync::Mutex::new(vec![Queued::Simple("BEGIN")]),
        })
    }

    pub(crate) fn sqlite_tx(t: SqliteTx, versions: std::sync::Arc<Versions>) -> Session {
        Session {
            conn: Conn::SqliteTx(Some(t)),
            sql: Sql { pg: false },
            platform: None,
            catalog: None,
            guardian: None,
            versions,
            tx: Default::default(),
            queued: Default::default(),
        }
    }

    /// Whether the session is a write transaction.
    pub fn writes(&self) -> bool {
        matches!(self.conn, Conn::PgTx(_) | Conn::SqliteTx(_))
    }

    /// Commits a write transaction (a read session has nothing to commit).
    pub async fn commit(mut self) -> Result<(), StoreError> {
        self.write_held().await?;
        let res = self.commit_conn().await;
        if res.is_ok() {
            self.versions.moved_heads(self.tx.wrote_named_branches());
            let moved = self.tx.wrote_branches();
            if !moved.is_empty() {
                let mut m = self.versions.branch_moves.lock().unwrap_or_else(|e| e.into_inner());
                for ns in moved {
                    *m.entry(ns).or_default() += 1;
                }
            }
            self.versions.keep_found(self.tx.take_found());
            for r in self.tx.take_origins() {
                self.versions.forget_node(&r);
            }
            let (folds, logs) = self.tx.take_folds();
            for c in &logs {
                self.versions.forget_fold(c);
            }
            for (c, f) in folds {
                // a fold of a log the transaction appended to carries what it appended, when that is all of it
                let f = match logs.contains(&c).then(|| self.tx.tail_of(&c, f.last)).flatten() {
                    Some(tail) if !tail.is_empty() => {
                        let mut k = std::sync::Arc::try_unwrap(f).unwrap_or_else(|f| (*f).clone());
                        k.tail = tail;
                        std::sync::Arc::new(k)
                    }
                    _ => f,
                };
                self.versions.keep_fold(&c, f);
            }
        }
        res
    }

    async fn commit_conn(&mut self) -> Result<(), StoreError> {
        match &mut self.conn {
            Conn::PgTx(c) => {
                let queued = std::mem::take(&mut *self.queued.lock().unwrap_or_else(|e| e.into_inner()));
                let c = c.take().ok_or_else(|| StoreError::Other("transaction over".into()))?;
                if matches!(queued.as_slice(), [Queued::Simple("BEGIN")]) {
                    // nothing was sent: nothing to commit
                    return Ok(());
                }
                // the statements queued and the COMMIT in one round trip
                let at = crate::stats::texts().then(|| ("COMMIT".to_string(), std::time::Instant::now()));
                let res = match prepared(&c, &queued).await {
                    Ok(st) => {
                        crate::stats::trip();
                        let (before, commit) = futures::join!(sent(&c, &queued, &st), c.batch_execute("COMMIT"));
                        before.and(commit.map_err(StoreError::from))
                    }
                    Err(e) => Err(e),
                };
                match res {
                    Ok(()) => {
                        crate::stats::note(at, 0);
                        Ok(())
                    }
                    Err(e) => {
                        drop(deadpool_postgres::Object::take(c));
                        Err(e)
                    }
                }
            }
            Conn::SqliteTx(t) => match t.take() {
                Some(t) => t.commit().await,
                None => Err(StoreError::Other("transaction over".into())),
            },
            _ => Ok(()),
        }
    }

    /// Runs a statement that writes and returns the number of rows it touched.
    pub async fn exec(&self, stmt: Stmt) -> Result<u64, StoreError> {
        self.write_held().await?;
        self.tx.written_any();
        let text = crate::stats::texts().then(|| (stmt.0.clone(), std::time::Instant::now()));
        let n = self.exec_counted(stmt).await;
        if let Ok(n) = &n {
            crate::stats::note(text, *n);
        }
        n
    }

    /// Whether the session is a PostgreSQL transaction, whose statements nothing waits on are queued.
    pub(crate) fn pipes(&self) -> bool {
        matches!(self.conn, Conn::PgTx(Some(_)))
    }

    /// Queues a statement nothing waits on, sent with the next statement of the transaction or its COMMIT.
    pub(crate) fn queue(&self, stmt: Stmt, what: &str) {
        self.queued
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .push(Queued::Stmt(stmt, what.to_string()));
    }

    /// Runs a statement of a PostgreSQL transaction after the statements queued, all sent in one round trip: the
    /// first error of the queued ones, else the statement's answer.
    async fn piped<T, F>(&self, c: &deadpool_postgres::Object, main: F) -> Result<T, StoreError>
    where
        F: std::future::Future<Output = Result<T, StoreError>>,
    {
        let queued = std::mem::take(&mut *self.queued.lock().unwrap_or_else(|e| e.into_inner()));
        crate::stats::trip();
        if queued.is_empty() {
            return main.await;
        }
        let st = prepared(c, &queued).await?;
        let (before, out) = futures::join!(sent(c, &queued, &st), main);
        before?;
        out
    }

    async fn exec_counted(&self, stmt: Stmt) -> Result<u64, StoreError> {
        if let Conn::PgTx(Some(c)) = &self.conn {
            // prepared before anything is sent, so that the statements leave in their order
            let st = c.prepare_cached(&stmt.0).await?;
            return self.piped(c, pg_exec(c, st, stmt)).await;
        }
        self.exec_alone(stmt).await
    }

    async fn exec_alone(&self, (text, args): Stmt) -> Result<u64, StoreError> {
        match &self.conn {
            Conn::Pg(c) | Conn::PgTx(Some(c)) => {
                crate::stats::trip();
                let st = c.prepare_cached(&text).await?;
                if custom_plan(&text, &args) {
                    return Ok(c.execute_typed(&text, &typed(&st, &args)).await?);
                }
                let params: Vec<&(dyn tokio_postgres::types::ToSql + Sync)> = args
                    .iter()
                    .map(|a| a as &(dyn tokio_postgres::types::ToSql + Sync))
                    .collect();
                Ok(c.execute(&st, &params).await?)
            }
            Conn::SqliteTx(Some(t)) => {
                t.run(move |c: &SqliteConn| {
                    let mut st = c.prepare_cached(&text)?;
                    Ok(st.execute(rusqlite::params_from_iter(args.iter()))? as u64)
                })
                .await
            }
            Conn::Sqlite(s) => {
                s.run(move |c: &SqliteConn| {
                    let mut st = c.prepare_cached(&text)?;
                    Ok(st.execute(rusqlite::params_from_iter(args.iter()))? as u64)
                })
                .await
            }
            Conn::PgTx(None) | Conn::SqliteTx(None) => Err(StoreError::Other("transaction over".into())),
        }
    }

    /// Runs a statement and returns its rows.
    pub async fn query(&self, stmt: Stmt) -> Result<Vec<Row>, StoreError> {
        self.write_held().await?;
        self.query_now(stmt).await
    }

    /// Runs a statement and returns its rows, leaving the rows held for later (an entry of a log reads none).
    pub(crate) async fn query_now(&self, stmt: Stmt) -> Result<Vec<Row>, StoreError> {
        let text = crate::stats::texts().then(|| (stmt.0.clone(), std::time::Instant::now()));
        let rows = self.query_counted(stmt).await;
        if let Ok(r) = &rows {
            crate::stats::note(text, r.len() as u64);
        }
        rows
    }

    async fn query_counted(&self, stmt: Stmt) -> Result<Vec<Row>, StoreError> {
        if let Conn::PgTx(Some(c)) = &self.conn {
            let st = c.prepare_cached(&stmt.0).await?;
            return self.piped(c, pg_query(c, st, stmt)).await;
        }
        self.query_alone(stmt).await
    }

    async fn query_alone(&self, (text, args): Stmt) -> Result<Vec<Row>, StoreError> {
        match &self.conn {
            Conn::Pg(c) | Conn::PgTx(Some(c)) => {
                crate::stats::trip();
                let st = c.prepare_cached(&text).await?;
                if custom_plan(&text, &args) {
                    return c
                        .query_typed(&text, &typed(&st, &args))
                        .await?
                        .iter()
                        .map(Row::from_pg)
                        .collect();
                }
                let params: Vec<&(dyn tokio_postgres::types::ToSql + Sync)> = args
                    .iter()
                    .map(|a| a as &(dyn tokio_postgres::types::ToSql + Sync))
                    .collect();
                c.query(&st, &params).await?.iter().map(Row::from_pg).collect()
            }
            Conn::Sqlite(s) => s.run(move |c: &SqliteConn| sqlite_rows(c, &text, &args)).await,
            Conn::SqliteTx(Some(t)) => t.run(move |c: &SqliteConn| sqlite_rows(c, &text, &args)).await,
            Conn::PgTx(None) | Conn::SqliteTx(None) => Err(StoreError::Other("transaction over".into())),
        }
    }

    async fn rows(&self, stmt: Stmt) -> Result<Vec<Row>, GraphError> {
        Ok(self.query(stmt).await?)
    }

    async fn nodes(&self, stmt: Stmt) -> Result<Vec<Node>, GraphError> {
        Ok(self.rows(stmt).await?.iter().map(node_of).collect())
    }

    async fn links(&self, stmt: Stmt) -> Result<Vec<Link>, GraphError> {
        self.rows(stmt).await?.iter().map(link_of).collect()
    }
}

/// The statements queued, prepared (none for a statement of no argument).
async fn prepared(
    c: &deadpool_postgres::Object,
    queued: &[Queued],
) -> Result<Vec<Option<tokio_postgres::Statement>>, StoreError> {
    let mut out = Vec::with_capacity(queued.len());
    for q in queued {
        out.push(match q {
            Queued::Simple(_) => None,
            Queued::Stmt((text, _), _) => Some(c.prepare_cached(text).await?),
        });
    }
    Ok(out)
}

/// Sends the statements queued, in order, and reads their answers: the first error, about its statement.
async fn sent(
    c: &deadpool_postgres::Object,
    queued: &[Queued],
    st: &[Option<tokio_postgres::Statement>],
) -> Result<(), StoreError> {
    let runs = queued.iter().zip(st).map(|(q, st)| async move {
        match (q, st) {
            (Queued::Stmt(stmt, what), Some(st)) => {
                let text = crate::stats::texts().then(|| (stmt.0.clone(), std::time::Instant::now()));
                let n = pg_exec(c, st.clone(), stmt.clone())
                    .await
                    .map_err(|e| StoreError::Queued {
                        what: what.clone(),
                        source: Box::new(e),
                    })?;
                crate::stats::note(text, n);
                Ok(())
            }
            (Queued::Simple(text), _) => {
                c.batch_execute(text).await?;
                crate::stats::note_text(text);
                Ok(())
            }
            (Queued::Stmt(..), None) => Err(StoreError::Other("statement not prepared".into())),
        }
    });
    // polled in order: each sends its statement at its first poll, before the next one does
    futures::future::join_all(runs).await.into_iter().collect()
}

/// Runs a prepared statement that writes.
async fn pg_exec(
    c: &deadpool_postgres::Object,
    st: tokio_postgres::Statement,
    (text, args): Stmt,
) -> Result<u64, StoreError> {
    if custom_plan(&text, &args) {
        return Ok(c.execute_typed(&text, &typed(&st, &args)).await?);
    }
    let params: Vec<&(dyn tokio_postgres::types::ToSql + Sync)> = args
        .iter()
        .map(|a| a as &(dyn tokio_postgres::types::ToSql + Sync))
        .collect();
    Ok(c.execute(&st, &params).await?)
}

/// Runs a prepared statement and returns its rows.
async fn pg_query(
    c: &deadpool_postgres::Object,
    st: tokio_postgres::Statement,
    (text, args): Stmt,
) -> Result<Vec<Row>, StoreError> {
    if custom_plan(&text, &args) {
        return c
            .query_typed(&text, &typed(&st, &args))
            .await?
            .iter()
            .map(Row::from_pg)
            .collect();
    }
    let params: Vec<&(dyn tokio_postgres::types::ToSql + Sync)> = args
        .iter()
        .map(|a| a as &(dyn tokio_postgres::types::ToSql + Sync))
        .collect();
    c.query(&st, &params).await?.iter().map(Row::from_pg).collect()
}

/// Whether a statement runs with a plan of its own each time, as Go's `customPlan`: a prepared statement's generic
/// plan does not know how many ids an array holds nor what a pattern matches, and the one PostgreSQL settles on after
/// the first runs may read the tables whole for the next ones.
fn custom_plan(text: &str, args: &[Arg]) -> bool {
    !args.is_empty() && (text.contains("unnest(") || text.contains("ANY(") || text.contains(" LIKE "))
}

/// The arguments of a statement with the types of its cached preparation, for an unnamed statement planned with them.
fn typed<'a>(
    st: &tokio_postgres::Statement,
    args: &'a [Arg],
) -> Vec<(
    &'a (dyn tokio_postgres::types::ToSql + Sync),
    tokio_postgres::types::Type,
)> {
    args.iter()
        .zip(st.params())
        .map(|(a, t)| (a as &(dyn tokio_postgres::types::ToSql + Sync), t.clone()))
        .collect()
}

fn sqlite_rows(c: &SqliteConn, text: &str, args: &[Arg]) -> Result<Vec<Row>, StoreError> {
    let mut st = c.prepare_cached(text)?;
    let n = st.column_count();
    let rows = st.query_map(rusqlite::params_from_iter(args.iter()), |r| Row::from_sqlite(r, n))?;
    Ok(rows.collect::<Result<Vec<_>, _>>()?)
}

/// A uuid, as the SQL stores key their nodes: an id that is none names no node (Go's `uuidIDs`).
pub(crate) fn is_uuid(s: &str) -> bool {
    uuid::Uuid::try_parse(s).is_ok()
}

/// What PostgreSQL says of an id that is no uuid (its message of SQLSTATE 22P02).
pub(crate) fn not_uuid(id: &str) -> String {
    format!("invalid input syntax for type uuid: \"{id}\"")
}

/// The error pgx gives of an id that is no uuid, when Go's store passes it on as is.
pub(crate) fn not_uuid_raw(id: &str) -> GraphError {
    GraphError::internal(format!("ERROR: {} (SQLSTATE 22P02)", not_uuid(id)))
}

fn props_of(v: serde_json::Value) -> graph_core::model::Props {
    match v {
        serde_json::Value::Object(m) if !m.is_empty() => Some(m),
        _ => None,
    }
}

/// A node version from the columns of `Sql::node_cols`.
fn node_of(r: &Row) -> Node {
    let origins: Vec<NodeRef> = match r.json(18) {
        serde_json::Value::Array(xs) => xs
            .iter()
            .map(|o| {
                NodeRef::new(
                    NodeId(o.get("id").and_then(|v| v.as_str()).unwrap_or_default().to_string()),
                    o.get("version").and_then(|v| v.as_i64()).unwrap_or_default() as Version,
                )
            })
            .collect(),
        _ => Vec::new(),
    };
    Node {
        id: NodeId(r.text(0)),
        version: r.int32(1),
        namespace: r.text(2),
        key: r.text(3),
        typ: r.text(4),
        props: props_of(r.json(5)),
        deleted: r.bool(6),
        change_id: ChangeId(r.text(7)),
        created_at: r.time(8),
        branch: r.text(9),
        parents: r.ints(10).into_iter().map(|v| v as Version).collect(),
        reason: r.text(11),
        state: r.text(12),
        change_impact: r.text(13),
        comment: r.text(14),
        execution: r.text(15),
        joined: Vec::new(),
        responsible: Url::of(&r.text(16)),
        project: Url::of(&r.text(17)),
        origins,
        creator: graph_core::model::Creator {
            subject: r.text(19),
            unit: Url::of(&r.text(20)),
        },
        editor: r.text(21),
        visibility: r.text(22),
    }
}

/// A link from the columns of `Sql::link_cols`.
fn link_of(r: &Row) -> Result<Link, GraphError> {
    let (id, to) = (r.text(0), r.text(4));
    let to = if to.is_empty() {
        Target::External(Url::parse(&r.text(8)).map_err(|e| GraphError::internal(format!("link {id}: {e}")))?)
    } else {
        Target::Node(NodeRef::new(NodeId(to), r.int32(5)))
    };
    Ok(Link {
        id: LinkId(id),
        typ: r.text(1),
        from: NodeRef::new(NodeId(r.text(2)), r.int32(3)),
        to,
        props: props_of(r.json(6)),
        change_id: ChangeId(r.text(7)),
    })
}

/// A baseline header from the columns of `Sql::header_cols`.
/// A log entry as the select list of [`LOG_COLS`] reads it.
fn log_entry_of(r: &Row) -> graph_core::impact::LogEntry {
    graph_core::impact::LogEntry {
        seq: r.int(0),
        id: r.text(1),
        change: ChangeId(r.text(2)),
        typ: r.text(3),
        flow: r.text(4),
        process: r.text(5),
        execution: r.text(6),
        subject: r.text(7),
        by: r.text(8),
        causation: r.text(9),
        at: r.time(10).unwrap_or_else(graph_core::jsontext::zero_time),
        payload: r.text(11),
    }
}

/// A branch as the select list of `branch_cols` reads it.
fn branch_of_row(r: &Row) -> Branch {
    branch_at(r, 0)
}

/// A branch read from the columns of a row starting at an offset.
pub(super) fn branch_at(r: &Row, o: usize) -> Branch {
    Branch {
        namespace: r.text(o),
        name: r.text(o + 1),
        parent: r.text(o + 2),
        fork_baseline: BaselineId(r.text(o + 3)),
        head: BaselineId(r.text(o + 4)),
        origin: r.text(o + 5),
        status: r.text(o + 6),
        created_at: r.time(o + 7),
        description: r.text(o + 8),
        intent: r.text(o + 9),
        rebased_from: BaselineId(r.text(o + 10)),
        ephemeral: r.bool(o + 11),
        fork_seq: r.int(o + 12),
    }
}

fn header_of(r: &Row) -> Baseline {
    Baseline {
        id: BaselineId(r.text(0)),
        name: r.text(1),
        parent_id: BaselineId(r.text(2)),
        merged_from: BaselineId(r.text(3)),
        change_id: ChangeId(r.text(4)),
        created_at: r.time(5),
        branch: r.text(6),
        namespace: r.text(7),
        kind: r.text(8),
        rebased_from: BaselineId(r.text(9)),
        seq: r.int(10),
    }
}

/// A change header from the columns of `Sql::change_heads`.
pub(crate) fn change_of(r: &Row) -> Result<Change, GraphError> {
    let id = r.text(0);
    let list = |i: usize, what: &str| -> Result<serde_json::Value, GraphError> {
        match r.json(i) {
            serde_json::Value::Null => Ok(serde_json::Value::Array(Vec::new())),
            v @ serde_json::Value::Array(_) => Ok(v),
            v => Err(GraphError::internal(format!("{what} of change {id}: {v}"))),
        }
    };
    let guardians: Vec<GuardianRef> = serde_json::from_value(list(17, "guardians")?)
        .map_err(|e| GraphError::internal(format!("guardians of a change: {e}")))?;
    let depends_on: Vec<String> = serde_json::from_value(list(18, "dependencies")?)
        .map_err(|e| GraphError::internal(format!("dependencies of a change: {e}")))?;
    Ok(Change {
        id: ChangeId(id.clone()),
        title: r.text(1),
        intent: r.text(2),
        methodology: r.text(3),
        goal: r.text(4),
        status: r.text(5),
        baseline_id: BaselineId(r.text(6)),
        result_baseline_id: BaselineId(r.text(7)),
        data: props_of(r.json(8)),
        created_at: r.time(9),
        branch: r.text(10),
        namespace: r.text(11),
        parent_id: ChangeId(r.text(12)),
        owner_org: Url::of(&r.text(13)),
        project_id: Url::of(&r.text(14)),
        lifecycle: r.text(15),
        state: r.text(16),
        guardians,
        depends_on: depends_on.into_iter().map(ChangeId).collect(),
        methodology_version: r.text(19),
    })
}

impl ReadTx for Session {
    fn catalog(&self) -> Option<std::sync::Arc<graph_catalog::Catalog>> {
        self.catalog.clone()
    }

    fn platform(&self) -> Option<&graph_core::platform::Platform> {
        self.platform.as_deref()
    }

    fn guardian(&self) -> Option<std::sync::Arc<dyn graph_core::guardian::Guardian>> {
        self.guardian.clone()
    }

    fn take_fold(&self, change: &ChangeId) -> Option<graph_core::write::work::KeptFold> {
        if !self.writes() {
            return self.versions.fold(&change.0).map(|k| (*k).clone());
        }
        if self.tx.wrote_log_of(&change.0) {
            return None;
        }
        // the writers of a change follow each other under its lock: the one taking the fold gives it back at commit
        self.tx
            .take_staged(&change.0)
            .or_else(|| self.versions.take_fold(&change.0))
            .map(|k| std::sync::Arc::try_unwrap(k).unwrap_or_else(|k| (*k).clone()))
    }

    fn structure_found(&self, key: &str) -> bool {
        self.versions.found(key)
    }

    fn found_structure(&self, key: &str) {
        if self.writes() {
            self.tx.found(key);
        }
    }

    fn log_top(&self, change: &ChangeId) -> Option<i64> {
        if !self.writes() {
            return None;
        }
        self.tx.log_top(&change.0)
    }

    fn keep_fold(&self, change: &ChangeId, fold: &graph_core::write::work::KeptFold, moved: bool) {
        if !self.writes() {
            if moved {
                self.versions.keep_fold(&change.0, std::sync::Arc::new(fold.clone()));
            }
        } else if !self.tx.wrote_log_of(&change.0) {
            self.tx.stage_fold(&change.0, std::sync::Arc::new(fold.clone()));
        }
    }

    fn kept_snapshot(
        &self,
        head: &str,
        catalog: &std::sync::Arc<graph_catalog::Catalog>,
        mark: Option<u64>,
    ) -> Option<std::sync::Arc<graph_core::access::Snapshot>> {
        let mut kept = self.versions.organisation.lock().unwrap_or_else(|e| e.into_inner());
        let k = kept
            .as_mut()
            .filter(|k| k.head == head && std::sync::Arc::ptr_eq(&k.catalog, catalog))?;
        if let Some(m) = mark {
            k.checked = Some((m, std::time::Instant::now()));
        }
        Some(k.snapshot.clone())
    }

    fn keep_snapshot(
        &self,
        head: &str,
        catalog: &std::sync::Arc<graph_catalog::Catalog>,
        mark: Option<u64>,
        snapshot: std::sync::Arc<graph_core::access::Snapshot>,
    ) {
        *self.versions.organisation.lock().unwrap_or_else(|e| e.into_inner()) = Some(versions::KeptSnapshot {
            head: head.to_string(),
            catalog: catalog.clone(),
            snapshot,
            checked: mark.map(|m| (m, std::time::Instant::now())),
        });
    }

    fn fresh_snapshot(
        &self,
        catalog: &std::sync::Arc<graph_catalog::Catalog>,
        namespace: &str,
    ) -> Option<std::sync::Arc<graph_core::access::Snapshot>> {
        let mark = self.snapshot_mark(namespace)?;
        let kept = self.versions.organisation.lock().unwrap_or_else(|e| e.into_inner());
        kept.as_ref()
            .filter(|k| {
                std::sync::Arc::ptr_eq(&k.catalog, catalog)
                    && k.checked
                        .is_some_and(|(m, at)| m == mark && at.elapsed() < self.versions.snapshot_fresh())
            })
            .map(|k| k.snapshot.clone())
    }

    fn head_mark(&self, namespace: &str, name: &str) -> Option<graph_core::store::HeadMark> {
        if self.writes() && !self.tx.wrote_branches().is_empty() {
            return None;
        }
        let (ns, name) = (
            graph_core::model::namespace_of(namespace),
            graph_core::model::branch_of(name),
        );
        Some(graph_core::store::HeadMark {
            mark: self.versions.head_mark(ns, name),
            at: std::time::Instant::now(),
        })
    }

    fn kept_head(
        &self,
        namespace: &str,
        name: &str,
        mark: &graph_core::store::HeadMark,
    ) -> Option<graph_core::model::Baseline> {
        let (ns, name) = (
            graph_core::model::namespace_of(namespace),
            graph_core::model::branch_of(name),
        );
        self.versions.kept_head(ns, name, mark.mark, mark.at)
    }

    fn keep_head(
        &self,
        namespace: &str,
        name: &str,
        mark: graph_core::store::HeadMark,
        head: &graph_core::model::Baseline,
    ) {
        let (ns, name) = (
            graph_core::model::namespace_of(namespace),
            graph_core::model::branch_of(name),
        );
        self.versions.keep_head(ns, name, mark.mark, mark.at, head);
    }

    async fn node_at_key(
        &self,
        namespace: &str,
        branch: &str,
        up_to: i64,
        key: &str,
    ) -> Result<graph_core::store::KeyAt, GraphError> {
        use graph_core::store::KeyAt;
        if !self.sql.pg {
            return Ok(KeyAt::Unread);
        }
        // Go's CommittedAtKey: sqlFoldVersions for the node of the key, found by its key in the same statement
        let q = "WITH x AS MATERIALIZED (
			SELECT n.id, (
				SELECT u.version FROM (
					(SELECT v.version, v.committed FROM node_version v
					WHERE v.node_id = n.id AND v.branch = $1 AND v.committed + 0 > 0 AND v.committed + 0 <= $2 ORDER BY v.committed DESC, v.version DESC LIMIT 1)
					UNION ALL
					(SELECT j.version, j.committed FROM node_branch j
					WHERE j.node_id = n.id AND j.branch = $1 AND j.committed + 0 > 0 AND j.committed + 0 <= $2 ORDER BY j.committed DESC, j.version DESC LIMIT 1)
				) u ORDER BY u.committed DESC, u.version DESC LIMIT 1
			) AS version
			FROM node n WHERE n.namespace = $3 AND n.key = $4
		)
		SELECT x.id::text, x.version, w.deleted, w.reason FROM x LEFT JOIN node_version w ON w.node_id = x.id AND w.version = x.version";
        let rows = self
            .rows((
                q.into(),
                vec![
                    graph_core::model::branch_of(branch).into(),
                    Arg::Int(up_to),
                    graph_core::model::namespace_of(namespace).into(),
                    key.into(),
                ],
            ))
            .await?;
        let Some(r) = rows.first() else {
            return Ok(KeyAt::NoNode);
        };
        let id = NodeId(r.text(0));
        if r.is_null(1) {
            return Ok(KeyAt::Node(id, None));
        }
        let v = match (r.bool(2), r.text(3) == REASON_REVERT) {
            (_, true) => REVERTED,
            (true, false) => 0,
            (false, false) => r.int32(1),
        };
        Ok(KeyAt::Node(id, Some(v)))
    }

    fn snapshot_mark(&self, namespace: &str) -> Option<u64> {
        let namespace = graph_core::model::namespace_of(namespace);
        if self.writes() && self.tx.wrote_branch_of(namespace) {
            return None;
        }
        let m = self.versions.branch_moves.lock().unwrap_or_else(|e| e.into_inner());
        Some(m.get(namespace).copied().unwrap_or_default())
    }

    async fn last_commit(&self, branch: &str) -> Result<i64, GraphError> {
        let p = self.sql.ph(1);
        let q = format!(
            "SELECT coalesce(max(committed), 0) FROM (SELECT max(committed) AS committed FROM node_version WHERE branch = {p} \
             UNION ALL SELECT max(committed) FROM node_branch WHERE branch = {p}) l"
        );
        let rows = self
            .select((q, vec![graph_core::model::branch_of(branch).into()]), "last commit")
            .await?;
        Ok(rows.first().map_or(0, |r| r.int(0)))
    }

    async fn change(&self, id: &ChangeId) -> Result<Option<Change>, GraphError> {
        if self.sql.pg && uuid::Uuid::try_parse(id.as_str()).is_err() {
            return Err(GraphError::invalid(format!(
                "change {id}: invalid input syntax for type uuid: \"{id}\""
            )));
        }
        let f = ChangesFilter {
            ids: vec![id.clone()],
            ..ChangesFilter::default()
        };
        match self.rows(self.sql.change_heads(&f)).await?.first() {
            Some(r) => Ok(Some(change_of(r)?)),
            None => Ok(None),
        }
    }

    async fn log(
        &self,
        change: &ChangeId,
        after: i64,
        prefixes: &[&str],
    ) -> Result<Vec<graph_core::impact::LogEntry>, GraphError> {
        let s = self.sql;
        let mut q = format!(
            "SELECT seq, id, {}, type, flow, process_id, execution, subject, by_whom, causation, at, payload FROM change_log WHERE change_id = {} AND seq > {}",
            s.id("change_id"),
            s.ph(1),
            s.ph(2)
        );
        let mut args: Vec<Arg> = vec![change.as_str().into(), Arg::Int(after)];
        if !prefixes.is_empty() {
            let mut ors = Vec::new();
            for p in prefixes {
                args.push(Arg::Text(format!("{}%", sql::like_pattern(p))));
                ors.push(format!("type LIKE {} ESCAPE '\\'", s.ph(args.len())));
            }
            q.push_str(&format!(" AND ({})", ors.join(" OR ")));
        }
        q.push_str(" ORDER BY seq");
        let rows = self.select((q, args), &format!("log of change {change}")).await?;
        Ok(rows.iter().map(log_entry_of).collect())
    }

    async fn branch(&self, namespace: &str, name: &str) -> Result<Option<Branch>, GraphError> {
        // a write transaction reads a branch once until it takes a lock or writes one (a read session sees each
        // statement afresh)
        let namespace = graph_core::model::namespace_of(namespace);
        let keep = self.writes();
        if keep && let Some(b) = self.tx.branch_row(namespace, name) {
            return Ok(b);
        }
        let b = self
            .rows(self.sql.branch(namespace, name))
            .await?
            .first()
            .map(branch_of_row);
        if keep {
            self.tx.keep_branch_row(namespace, name, &b);
        }
        Ok(b)
    }

    async fn branches(&self, namespace: &str) -> Result<Vec<Branch>, GraphError> {
        Ok(self
            .rows(self.sql.branches(namespace))
            .await?
            .iter()
            .map(branch_of_row)
            .collect())
    }

    async fn baseline_headers_of(&self, namespace: &str, ids: &[BaselineId]) -> Result<Vec<Baseline>, GraphError> {
        if ids.is_empty() {
            return Ok(Vec::new());
        }
        if self.sql.pg
            && let Some(id) = ids.iter().find(|i| !is_uuid(i.as_str()))
        {
            return Err(not_uuid_raw(id.as_str()));
        }
        Ok(self
            .rows(self.sql.baseline_headers_of(namespace, ids))
            .await?
            .iter()
            .map(header_of)
            .collect())
    }

    async fn baseline_page(
        &self,
        namespace: &str,
        branch: &str,
        before: i64,
        limit: i64,
    ) -> Result<Vec<Baseline>, GraphError> {
        Ok(self
            .rows(self.sql.baseline_page(namespace, branch, before, limit))
            .await?
            .iter()
            .map(header_of)
            .collect())
    }

    async fn change_baseline_headers(
        &self,
        namespace: &str,
        changes: &[ChangeId],
    ) -> Result<Vec<Baseline>, GraphError> {
        if changes.is_empty() {
            return Ok(Vec::new());
        }
        Ok(self
            .rows(self.sql.change_baseline_headers(namespace, changes))
            .await?
            .iter()
            .map(header_of)
            .collect())
    }

    async fn branch_baseline_headers(&self, namespace: &str, branch: &str) -> Result<Vec<Baseline>, GraphError> {
        Ok(self
            .rows(self.sql.branch_baseline_headers(namespace, branch))
            .await?
            .iter()
            .map(header_of)
            .collect())
    }

    async fn store_counts(&self, namespace: &str) -> Result<HashMap<String, HashMap<String, i64>>, GraphError> {
        let mut out = HashMap::new();
        for (measure, stmt) in self.sql.store_counts(namespace) {
            let counts = self.rows(stmt).await?.iter().map(|r| (r.text(0), r.int(1))).collect();
            out.insert(measure.to_string(), counts);
        }
        Ok(out)
    }

    async fn baseline_counts(&self, namespace: &str) -> Result<HashMap<String, i64>, GraphError> {
        Ok(self
            .rows(self.sql.baseline_counts(namespace))
            .await?
            .iter()
            .map(|r| (r.text(0), r.int(1)))
            .collect())
    }

    async fn baseline_header(&self, id: &BaselineId) -> Result<Option<Baseline>, GraphError> {
        if self.sql.pg && !is_uuid(id.as_str()) {
            return Err(GraphError::invalid(format!(
                "baseline {id}: invalid input syntax for type uuid: \"{id}\""
            )));
        }
        // a baseline is written once and never deleted: its header read is not read again
        if let Some(b) = self.versions.header(id.as_str()) {
            return Ok(Some(b));
        }
        let b = self
            .rows(self.sql.baseline_header(id.as_str()))
            .await?
            .first()
            .map(header_of);
        if let Some(b) = &b
            && !self.tx.wrote_baseline_of(id.as_str())
        {
            self.versions.keep_header(b);
        }
        Ok(b)
    }

    async fn baseline_headers(&self, namespace: &str) -> Result<Vec<Baseline>, GraphError> {
        Ok(self
            .rows(self.sql.baseline_headers(namespace))
            .await?
            .iter()
            .map(header_of)
            .collect())
    }

    async fn committed_versions(
        &self,
        namespace: &str,
        branch: &str,
        after: i64,
        up_to: i64,
        ids: Option<&[NodeId]>,
    ) -> Result<HashMap<NodeId, Version>, GraphError> {
        let valid: Option<Vec<NodeId>> = ids.map(|ids| ids.iter().filter(|i| is_uuid(i.as_str())).cloned().collect());
        if valid.as_ref().is_some_and(Vec::is_empty) {
            return Ok(HashMap::new());
        }
        if let Some(valid) = &valid {
            // what the transaction read of the range is not read again until it writes
            let key = (namespace.to_string(), branch.to_string(), after, up_to);
            let (mut out, missing) = self.tx.selected(&key, valid);
            if !missing.is_empty() {
                let got = self
                    .committed_rows(namespace, branch, after, up_to, Some(&missing))
                    .await?;
                self.tx.keep_selected(key, &missing, &got);
                out.extend(got);
            }
            return Ok(out);
        }
        self.committed_rows(namespace, branch, after, up_to, None).await
    }

    async fn namespace_node_ids(&self, namespace: &str) -> Result<Vec<NodeId>, GraphError> {
        Ok(self
            .rows(self.sql.namespace_node_ids(namespace))
            .await?
            .iter()
            .map(|r| NodeId(r.text(0)))
            .collect())
    }

    async fn node(&self, r: &NodeRef) -> Result<Option<Node>, GraphError> {
        if self.sql.pg && !is_uuid(r.id.as_str()) {
            return Err(GraphError::invalid(format!(
                "node {r}: invalid input syntax for type uuid: \"{}\"",
                r.id
            )));
        }
        let fixed = self.tx.fixed(r);
        if fixed && let Some(n) = self.tx.node(r).or_else(|| self.versions.node(r)) {
            return Ok(Some(n));
        }
        let era = self.versions.generation();
        let n = self.nodes(self.sql.node(r)).await?.into_iter().next();
        if fixed && let Some(n) = &n {
            self.tx.keep(n);
            self.versions.keep_node(era, n);
        }
        Ok(n)
    }

    async fn latest_on(&self, id: &NodeId, branch: &str) -> Result<Option<Node>, GraphError> {
        if self.sql.pg && !is_uuid(id.as_str()) {
            return Err(GraphError::invalid(format!(
                "node {id} on {branch}: invalid input syntax for type uuid: \"{id}\""
            )));
        }
        Ok(self.nodes(self.sql.latest_on(id, branch)).await?.into_iter().next())
    }

    async fn latest_on_all(&self, ids: &[NodeId], branch: &str) -> Result<HashMap<NodeId, Node>, GraphError> {
        if ids.is_empty() {
            return Ok(HashMap::new());
        }
        if self.sql.pg
            && let Some(id) = ids.iter().find(|id| !is_uuid(id.as_str()))
        {
            return Err(GraphError::invalid(format!(
                "node {id} on {branch}: invalid input syntax for type uuid: \"{id}\""
            )));
        }
        Ok(self
            .nodes(self.sql.latest_on_all(ids, branch))
            .await?
            .into_iter()
            .map(|n| (n.id.clone(), n))
            .collect())
    }

    async fn node_id_by_key(&self, namespace: &str, key: &str) -> Result<Option<NodeId>, GraphError> {
        Ok(self
            .rows(self.sql.node_id_by_key(namespace, key))
            .await?
            .first()
            .map(|r| NodeId(r.text(0))))
    }

    async fn node_ids_by_keys(&self, namespace: &str, keys: &[String]) -> Result<HashMap<String, NodeId>, GraphError> {
        if keys.is_empty() {
            return Ok(HashMap::new());
        }
        Ok(self
            .rows(self.sql.node_ids_by_keys(namespace, keys))
            .await?
            .iter()
            .map(|r| (r.text(0), NodeId(r.text(1))))
            .collect())
    }

    async fn last_versions(&self, ids: &[NodeId]) -> Result<HashMap<NodeId, Version>, GraphError> {
        let ids: Vec<NodeId> = ids
            .iter()
            .filter(|i| !self.sql.pg || is_uuid(i.as_str()))
            .cloned()
            .collect();
        if ids.is_empty() {
            return Ok(HashMap::new());
        }
        Ok(self
            .rows(self.sql.last_versions(&ids))
            .await?
            .iter()
            .map(|r| (NodeId(r.text(0)), r.int32(1)))
            .collect())
    }

    async fn nodes_at(&self, refs: &[NodeRef], typ: &str) -> Result<Vec<Node>, GraphError> {
        if refs.is_empty() {
            return Ok(Vec::new());
        }
        // a version never changes once written (but its origin, set again at a merge): the ones kept between
        // transactions are not read again
        let era = self.versions.generation();
        let mut out = Vec::new();
        let mut missing = Vec::new();
        for r in refs {
            match self.tx.fixed(r).then(|| self.versions.node(r)).flatten() {
                Some(n) => {
                    if typ.is_empty() || n.typ == typ {
                        out.push(n);
                    }
                }
                None => missing.push(r.clone()),
            }
        }
        if missing.is_empty() {
            out.sort_by(|a, b| (&a.key, a.version).cmp(&(&b.key, b.version)));
            return Ok(out);
        }
        let ns = self.nodes(self.sql.nodes_at(&missing, typ)).await?;
        for n in &ns {
            let r = n.node_ref();
            if self.tx.fixed(&r) {
                self.tx.keep(n);
                self.versions.keep_node(era, n);
            }
        }
        if out.is_empty() {
            return Ok(ns);
        }
        out.extend(ns);
        out.sort_by(|a, b| (&a.key, a.version).cmp(&(&b.key, b.version)));
        Ok(out)
    }

    async fn link(&self, id: &LinkId) -> Result<Option<Link>, GraphError> {
        if !is_uuid(id.as_str()) {
            return Ok(None);
        }
        Ok(self.links(self.sql.link_by_id(id)).await?.into_iter().next())
    }

    async fn links_from(&self, refs: &[NodeRef], types: Option<&[String]>) -> Result<Vec<Link>, GraphError> {
        if refs.is_empty() {
            return Ok(Vec::new());
        }
        // The links of the versions already read come from the cache (filtered by type when asked), the others from one
        // statement; all the links of a version read whole are kept.
        let era = self.versions.generation();
        let mut out = Vec::new();
        let mut missing = Vec::new();
        for r in refs {
            match self.tx.fixed(r).then(|| self.versions.links(r)).flatten() {
                Some(ls) => out.extend(ls.into_iter().filter(|l| types.is_none_or(|ts| ts.contains(&l.typ)))),
                None => missing.push(r.clone()),
            }
        }
        if missing.len() == refs.len() && types.is_some() {
            return self.links(self.sql.links_from(refs, types)).await;
        }
        if !missing.is_empty() {
            let got = self.links(self.sql.links_from(&missing, types)).await?;
            if types.is_none() {
                let mut held: HashMap<&NodeRef, Vec<Link>> = HashMap::new();
                for l in &got {
                    held.entry(&l.from).or_default().push(l.clone());
                }
                for r in missing.iter().filter(|r| self.tx.fixed(r)) {
                    self.versions.keep_links(era, r, held.remove(r).unwrap_or_default());
                }
            }
            if missing.len() == refs.len() {
                return Ok(got);
            }
            out.extend(got);
        }
        out.sort_by(|a, b| a.id.as_str().cmp(b.id.as_str()));
        Ok(out)
    }

    async fn links_from_into(&self, refs: &[NodeRef], to: &NodeId) -> Result<Vec<Link>, GraphError> {
        if refs.is_empty() {
            return Ok(Vec::new());
        }
        self.links(self.sql.links_from_into(refs, to)).await
    }

    async fn links_to(&self, r: &NodeRef) -> Result<Vec<Link>, GraphError> {
        self.links(self.sql.links_to(r)).await
    }

    async fn link_sources(&self, id: &NodeId) -> Result<Vec<NodeId>, GraphError> {
        Ok(self
            .rows(self.sql.link_sources(id))
            .await?
            .iter()
            .map(|r| NodeId(r.text(0)))
            .collect())
    }

    async fn node_page(&self, refs: &[NodeRef], q: &NodeQuery) -> Result<NodePage, GraphError> {
        let counts: BTreeMap<String, usize> = self
            .rows(self.sql.node_type_counts(refs, q))
            .await?
            .iter()
            .map(|r| (r.text(0), r.int(1) as usize))
            .collect();
        let (types, total) = type_counts(&counts, &q.typ);
        Ok(NodePage {
            nodes: self.nodes(self.sql.node_page(refs, q)).await?,
            total,
            types,
            ..NodePage::default()
        })
    }

    async fn version_range(&self, id: &NodeId, before: Version, limit: usize) -> Result<Vec<Node>, GraphError> {
        if self.sql.pg && !is_uuid(id.as_str()) {
            return Ok(Vec::new());
        }
        let mut vs = self.nodes(self.sql.version_range(id, before, limit)).await?;
        if vs.is_empty() {
            return Ok(vs);
        }
        vs.reverse();
        let first = vs[0].version;
        for r in self.rows(self.sql.version_branches(id, first)).await? {
            let i = r.int32(0) - first;
            if let Some(v) = usize::try_from(i).ok().and_then(|i| vs.get_mut(i)) {
                v.joined.push(r.text(1));
            }
        }
        Ok(vs)
    }

    async fn version_joins(&self, id: &NodeId) -> Result<Vec<(Version, String, ChangeId)>, GraphError> {
        if self.sql.pg && !is_uuid(id.as_str()) {
            return Ok(Vec::new());
        }
        Ok(self
            .rows(self.sql.version_joins(id))
            .await?
            .iter()
            .map(|r| (r.int32(0), r.text(1), ChangeId(r.text(2))))
            .collect())
    }

    async fn change_nodes(&self, change: &ChangeId) -> Result<Option<Vec<NodeId>>, GraphError> {
        if self.sql.pg && !is_uuid(change.as_str()) {
            return Ok(None);
        }
        if self.rows(self.sql.change_exists(change.as_str())).await?.is_empty() {
            return Ok(None);
        }
        Ok(Some(
            self.rows(self.sql.change_nodes(change.as_str()))
                .await?
                .iter()
                .map(|r| NodeId(r.text(0)))
                .collect(),
        ))
    }

    async fn change_heads(&self, f: &ChangesFilter) -> Result<Vec<Change>, GraphError> {
        if self.sql.pg {
            // the uuid columns: an id that is none names no change, PostgreSQL refuses it
            let parent = !f.parent.is_empty() && !is_uuid(f.parent.as_str());
            if parent || f.ids.iter().any(|i| !is_uuid(i.as_str())) {
                return Err(GraphError::internal("invalid input syntax for type uuid"));
            }
        }
        self.rows(self.sql.change_heads(f))
            .await?
            .iter()
            .map(change_of)
            .collect()
    }

    async fn node_change_page(
        &self,
        node: &NodeId,
        before: i64,
        limit: usize,
    ) -> Result<Vec<(ChangeId, i64)>, GraphError> {
        if self.sql.pg && !is_uuid(node.as_str()) {
            return Err(GraphError::internal(format!(
                "invalid input syntax for type uuid: \"{node}\""
            )));
        }
        Ok(self
            .rows(self.sql.node_change_page(node, before, limit))
            .await?
            .iter()
            .map(|r| (ChangeId(r.text(0)), r.int(1)))
            .collect())
    }

    async fn branch_node_ids(&self, branch: &str) -> Result<Vec<NodeId>, GraphError> {
        Ok(self
            .rows(self.sql.branch_node_ids(branch))
            .await?
            .iter()
            .map(|r| NodeId(r.text(0)))
            .collect())
    }

    async fn impact_refs(&self, change: &ChangeId) -> Result<Vec<NodeRef>, GraphError> {
        if self.sql.pg && !is_uuid(change.as_str()) {
            return Ok(Vec::new());
        }
        let mut out = Vec::new();
        for r in self.rows(self.sql.impact_refs(change)).await? {
            let node = r.text(0);
            if node.is_empty() {
                continue;
            }
            for i in 1..=3 {
                if !r.is_null(i) {
                    out.push(NodeRef::new(NodeId(node.clone()), r.int32(i)));
                }
            }
        }
        Ok(out)
    }
}
