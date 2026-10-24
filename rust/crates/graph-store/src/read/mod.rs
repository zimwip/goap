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
mod write;

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
    pub(crate) fn postgres(c: deadpool_postgres::Object) -> Session {
        Session {
            conn: Conn::Pg(c),
            sql: Sql { pg: true },
        }
    }

    pub(crate) fn sqlite(s: SqliteSession) -> Session {
        Session {
            conn: Conn::Sqlite(s),
            sql: Sql { pg: false },
        }
    }

    /// A write transaction on a writer of PostgreSQL.
    pub(crate) async fn postgres_tx(c: deadpool_postgres::Object) -> Result<Session, StoreError> {
        c.batch_execute("BEGIN").await?;
        Ok(Session {
            conn: Conn::PgTx(Some(c)),
            sql: Sql { pg: true },
        })
    }

    pub(crate) fn sqlite_tx(t: SqliteTx) -> Session {
        Session {
            conn: Conn::SqliteTx(Some(t)),
            sql: Sql { pg: false },
        }
    }

    /// Whether the session is a write transaction.
    pub fn writes(&self) -> bool {
        matches!(self.conn, Conn::PgTx(_) | Conn::SqliteTx(_))
    }

    /// Commits a write transaction (a read session has nothing to commit).
    pub async fn commit(mut self) -> Result<(), StoreError> {
        match &mut self.conn {
            Conn::PgTx(c) => {
                let c = c.take().ok_or_else(|| StoreError::Other("transaction over".into()))?;
                match c.batch_execute("COMMIT").await {
                    Ok(()) => Ok(()),
                    Err(e) => {
                        drop(deadpool_postgres::Object::take(c));
                        Err(e.into())
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
    pub async fn exec(&self, (text, args): Stmt) -> Result<u64, StoreError> {
        match &self.conn {
            Conn::Pg(c) | Conn::PgTx(Some(c)) => {
                let st = c.prepare_cached(&text).await?;
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
    pub async fn query(&self, (text, args): Stmt) -> Result<Vec<Row>, StoreError> {
        match &self.conn {
            Conn::Pg(c) | Conn::PgTx(Some(c)) => {
                let st = c.prepare_cached(&text).await?;
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

fn sqlite_rows(c: &SqliteConn, text: &str, args: &[Arg]) -> Result<Vec<Row>, StoreError> {
    let mut st = c.prepare_cached(text)?;
    let n = st.column_count();
    let rows = st.query_map(rusqlite::params_from_iter(args.iter()), |r| Row::from_sqlite(r, n))?;
    Ok(rows.collect::<Result<Vec<_>, _>>()?)
}

/// A uuid, as the SQL stores key their nodes: an id that is none names no node (Go's `uuidIDs`).
fn is_uuid(s: &str) -> bool {
    uuid::Uuid::try_parse(s).is_ok()
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
        owner: Url::of(&r.text(16)),
        project: Url::of(&r.text(17)),
        origins,
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
        at: r.time(10).unwrap_or_else(graph_core::gojson::zero_time),
        payload: r.text(11),
    }
}

/// A branch as the select list of `branch_cols` reads it.
fn branch_of_row(r: &Row) -> Branch {
    Branch {
        namespace: r.text(0),
        name: r.text(1),
        parent: r.text(2),
        fork_baseline: BaselineId(r.text(3)),
        head: BaselineId(r.text(4)),
        origin: r.text(5),
        status: r.text(6),
        created_at: r.time(7),
        description: r.text(8),
        intent: r.text(9),
        rebased_from: BaselineId(r.text(10)),
        ephemeral: r.bool(11),
        fork_seq: r.int(12),
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
        Ok(self
            .rows(self.sql.branch(namespace, name))
            .await?
            .first()
            .map(branch_of_row))
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
            return Err(GraphError::unimplemented(format!(
                "baseline {id}: not an id PostgreSQL reads, the error Go words is not served here yet"
            )));
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
        Ok(self
            .rows(self.sql.baseline_header(id.as_str()))
            .await?
            .first()
            .map(header_of))
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
        let rows = self
            .rows(
                self.sql
                    .committed_versions(namespace, branch, after, up_to, valid.as_deref()),
            )
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
        Ok(self.nodes(self.sql.node(r)).await?.into_iter().next())
    }

    async fn latest_on(&self, id: &NodeId, branch: &str) -> Result<Option<Node>, GraphError> {
        if self.sql.pg && !is_uuid(id.as_str()) {
            return Err(GraphError::invalid(format!(
                "node {id} on {branch}: invalid input syntax for type uuid: \"{id}\""
            )));
        }
        Ok(self.nodes(self.sql.latest_on(id, branch)).await?.into_iter().next())
    }

    async fn node_id_by_key(&self, namespace: &str, key: &str) -> Result<Option<NodeId>, GraphError> {
        Ok(self
            .rows(self.sql.node_id_by_key(namespace, key))
            .await?
            .first()
            .map(|r| NodeId(r.text(0))))
    }

    async fn nodes_at(&self, refs: &[NodeRef], typ: &str) -> Result<Vec<Node>, GraphError> {
        if refs.is_empty() {
            return Ok(Vec::new());
        }
        self.nodes(self.sql.nodes_at(refs, typ)).await
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
        self.links(self.sql.links_from(refs, types)).await
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
