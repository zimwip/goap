//! The transaction of the SQL repositories, written once for both dialects (no Go counterpart: Go's `sqliteTx` and
//! `pgTx` repeat it, each with its driver's scanning).
//!
//! A [`SqlConn`] is what a dialect gives: run a statement whose arguments are [`Arg`] values and whose result columns
//! are read as [`Kind`]s, and say which [`Dialect`] it speaks. [`SqlTx`] implements [`Tx`] over it with the statements
//! of `sqlbuild.rs` and the few that differ between the dialects (the upserts, `latest_nodes`, the locks of
//! PostgreSQL). The value encodings (uuid / text, jsonb / text, arrays, timestamps) are the connection's: `sqlite.rs`,
//! `postgres.rs`.

use std::collections::BTreeMap;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::{
    Baseline, BaselineID, Branch, Change, ChangeID, ChangeImpact, ChangeImpactID, ChangeStatus,
    GuardianRef, Link, LinkID, LogEntry, LogFilter, MAIN_BRANCH, Node, NodeID, NodeRef,
    OptionIntent, Tag, TagFilter, TagID, Version, branch_of, gotime, namespace_of,
};
use serde_json::{Map, Value};

use crate::baselinedelta::stored_entries;
use crate::changeimpact_sql::{CnRow, to_cn_row};
use crate::changelog::{facts_filter, items_of};
use crate::repo::{BaselineEntry, BaselineHeader, Tx};
use crate::sqlbuild::{
    Arg, BASELINE_COLUMNS, CHANGE_LOG_COLUMNS, Dialect, LINK_COLUMNS, NODE_FROM,
    NODE_VERSION_COLUMNS, on_branch_sql,
};
use crate::state::kind_of;
use crate::{Error, ErrorKind, Result};

/// How a result column is read.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Kind {
    Text,
    OptText,
    Int,
    OptInt,
    Bool,
    Json,
    Time,
    Ints,
}

/// A value read from a result column.
#[derive(Debug, Clone, PartialEq)]
pub(crate) enum Cell {
    Text(String),
    OptText(Option<String>),
    Int(i64),
    OptInt(Option<i64>),
    Bool(bool),
    Json(Value),
    Time(DateTime<Utc>),
    Ints(Vec<i64>),
}

/// The cells of one row, read in order.
pub(crate) struct Cells(std::vec::IntoIter<Cell>);

impl Cells {
    pub(crate) fn new(cells: Vec<Cell>) -> Cells {
        Cells(cells.into_iter())
    }

    /// A text column (a null one reads empty).
    fn s(&mut self) -> String {
        match self.0.next() {
            Some(Cell::Text(s)) => s,
            Some(Cell::OptText(o)) => o.unwrap_or_default(),
            _ => String::new(),
        }
    }

    fn os(&mut self) -> Option<String> {
        match self.0.next() {
            Some(Cell::Text(s)) => Some(s),
            Some(Cell::OptText(o)) => o,
            _ => None,
        }
    }

    fn i(&mut self) -> i64 {
        match self.0.next() {
            Some(Cell::Int(i)) => i,
            Some(Cell::OptInt(o)) => o.unwrap_or_default(),
            _ => 0,
        }
    }

    fn oi(&mut self) -> Option<i64> {
        match self.0.next() {
            Some(Cell::Int(i)) => Some(i),
            Some(Cell::OptInt(o)) => o,
            _ => None,
        }
    }

    fn b(&mut self) -> bool {
        matches!(self.0.next(), Some(Cell::Bool(true)))
    }

    fn j(&mut self) -> Value {
        match self.0.next() {
            Some(Cell::Json(v)) => v,
            _ => Value::Null,
        }
    }

    fn t(&mut self) -> DateTime<Utc> {
        match self.0.next() {
            Some(Cell::Time(t)) => t,
            _ => gotime::zero(),
        }
    }

    fn is(&mut self) -> Vec<i64> {
        match self.0.next() {
            Some(Cell::Ints(v)) => v,
            _ => Vec::new(),
        }
    }
}

/// SqlConn is the connection of a transaction of one dialect.
#[async_trait]
pub(crate) trait SqlConn: Send + 'static {
    /// The dialect the connection speaks.
    const D: Dialect;
    /// rows runs a query and reads its rows as the kinds; errors are mapped with what (Go's `mapErr` /
    /// `sqliteErr`).
    async fn rows(
        &mut self,
        q: &str,
        args: &[Arg],
        kinds: &[Kind],
        what: &str,
    ) -> Result<Vec<Cells>>;
    /// exec runs a statement and returns the number of rows it touched.
    async fn exec(&mut self, q: &str, args: &[Arg], what: &str) -> Result<u64>;
    /// commit commits the transaction.
    async fn commit(self: Box<Self>) -> Result<()>;
    /// rollback rolls the transaction back now (sqlx only rolls a dropped transaction back when its connection is
    /// next used, keeping its locks meanwhile).
    async fn rollback(self: Box<Self>) -> Result<()>;
}

// ---- encodings shared by both dialects (Go: postgres.go) -------------------------------------------------------

/// jsonb is the JSON object of properties (an empty object when there are none).
pub(crate) fn jsonb(m: &Map<String, Value>) -> Value {
    Value::Object(m.clone())
}

/// originsJSON encodes the origins of a node version (both dialects store them as a JSON array).
pub(crate) fn origins_json(o: &[NodeRef]) -> Value {
    serde_json::to_value(o).unwrap_or_else(|_| Value::Array(vec![]))
}

pub(crate) fn origins_of(v: Value) -> Vec<NodeRef> {
    serde_json::from_value::<Vec<NodeRef>>(v).unwrap_or_default()
}

/// guardiansJSON encodes the guardian chain of a change (ADR 0106): a JSON array, `[]` when empty.
pub(crate) fn guardians_json(gs: &[GuardianRef]) -> Value {
    serde_json::to_value(gs).unwrap_or_else(|_| Value::Array(vec![]))
}

/// dependsOnJSON encodes the dependencies of a change (ADR 0108): a JSON array, `[]` when empty.
pub(crate) fn depends_on_json(ds: &[ChangeID]) -> Value {
    serde_json::to_value(ds).unwrap_or_else(|_| Value::Array(vec![]))
}

pub(crate) fn depends_on_of(v: Value) -> Result<Vec<ChangeID>> {
    if v.is_null() {
        return Ok(Vec::new());
    }
    serde_json::from_value::<Vec<ChangeID>>(v)
        .map_err(|e| Error::msg(format!("dependencies of a change: {e}")))
}

pub(crate) fn guardians_of(v: Value) -> Result<Vec<GuardianRef>> {
    if v.is_null() {
        return Ok(Vec::new());
    }
    serde_json::from_value::<Vec<GuardianRef>>(v)
        .map_err(|e| Error::msg(format!("guardians of a change: {e}")))
}

/// props reads a JSON object of properties (anything else is no property).
pub(crate) fn props(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}

/// The time of a log entry: None stands for Go's zero time.
fn log_time(t: DateTime<Utc>) -> Option<DateTime<Utc>> {
    if t == gotime::zero() { None } else { Some(t) }
}

const NODE_KINDS: &[Kind] = &[
    Kind::Text,
    Kind::Int,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Json,
    Kind::Bool,
    Kind::OptText,
    Kind::Time,
    Kind::Text,
    Kind::Ints,
    Kind::Text,
    Kind::Text,
    Kind::OptText,
    Kind::Text,
    Kind::Text,
    Kind::OptText,
    Kind::OptText,
    Kind::Json,
];

fn scan_node(mut r: Cells) -> Node {
    Node {
        id: NodeID::new(r.s()),
        version: r.i(),
        namespace: r.s(),
        key: r.s(),
        r#type: r.s(),
        properties: props(r.j()),
        deleted: r.b(),
        change_id: ChangeID::new(r.s()),
        created_at: r.t(),
        branch: r.s(),
        parents: r.is(),
        reason: r.s(),
        state: r.s(),
        change_impact: ChangeImpactID::new(r.s()),
        comment: r.s(),
        execution: r.s(),
        owner: NodeID::new(r.s()),
        project: NodeID::new(r.s()),
        origins: origins_of(r.j()),
        ..Default::default()
    }
}

const LINK_KINDS: &[Kind] = &[
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Int,
    Kind::Text,
    Kind::Int,
    Kind::Json,
    Kind::OptText,
];

fn scan_link(mut r: Cells) -> Link {
    let id = LinkID::new(r.s());
    let r#type = r.s();
    let from = NodeRef {
        id: NodeID::new(r.s()),
        version: r.i(),
    };
    let to = NodeRef {
        id: NodeID::new(r.s()),
        version: r.i(),
    };
    Link {
        id,
        r#type,
        from,
        to,
        properties: props(r.j()),
        change_id: ChangeID::new(r.s()),
    }
}

const BRANCH_KINDS: &[Kind] = &[
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Time,
    Kind::Text,
    Kind::Text,
    Kind::Text,
];

fn scan_branch(mut r: Cells) -> Branch {
    Branch {
        namespace: r.s(),
        name: r.s(),
        parent: r.s(),
        fork_baseline: BaselineID::new(r.s()),
        head: BaselineID::new(r.s()),
        origin: r.s(),
        status: r.s(),
        created_at: r.t(),
        description: r.s(),
        intent: OptionIntent::new(r.s()),
        rebased_from: BaselineID::new(r.s()),
    }
}

const CHANGE_IMPACT_KINDS: &[Kind] = &[
    Kind::Text,
    Kind::OptText,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::OptInt,
    Kind::OptInt,
    Kind::OptInt,
    Kind::Text,
    Kind::Json,
    Kind::Text,
    Kind::Bool,
    Kind::Text,
    Kind::Json,
    Kind::Json,
    Kind::Text,
    Kind::Time,
    Kind::Text,
    Kind::Bool,
];

fn scan_change_impact(mut r: Cells) -> Result<ChangeImpact> {
    let row = CnRow {
        id: r.s(),
        node_id: r.os(),
        key: r.s(),
        r#type: r.s(),
        intent: r.s(),
        rationale: r.s(),
        pre: r.oi(),
        post: r.oi(),
        landed: r.oi(),
        review: r.s(),
        reviews: r.j(),
        via: r.s(),
        recheck: r.b(),
        produced_by: r.s(),
        derived_from: r.j(),
        items: r.j(),
        execution: r.s(),
        ..Default::default()
    };
    let created = r.t();
    let (flow, superseded) = (r.s(), r.b());
    let mut cn = CnRow {
        flow,
        superseded,
        ..row
    }
    .node()?;
    cn.created_at = created;
    Ok(cn)
}

const LOG_KINDS: &[Kind] = &[
    Kind::Int,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Text,
    Kind::Time,
    Kind::Json,
];

fn scan_log(mut r: Cells) -> LogEntry {
    LogEntry {
        seq: r.i(),
        id: r.s(),
        change: ChangeID::new(r.s()),
        r#type: r.s(),
        flow: r.s(),
        process: r.s(),
        execution: r.s(),
        subject: r.s(),
        by: r.s(),
        causation: r.s(),
        at: log_time(r.t()),
        payload: r.j(),
    }
}

/// SqlTx is a transaction of a SQL repository.
pub(crate) struct SqlTx<C: SqlConn> {
    pub(crate) c: C,
}

impl<C: SqlConn> SqlTx<C> {
    const D: Dialect = C::D;

    async fn one(&mut self, q: &str, args: &[Arg], kinds: &[Kind], what: &str) -> Result<Cells> {
        let mut rows = self.c.rows(q, args, kinds, what).await?;
        if rows.is_empty() {
            return Err(Error::wrap(ErrorKind::NotFound, what));
        }
        Ok(rows.swap_remove(0))
    }

    async fn nodes(&mut self, q: &str, args: &[Arg], what: &str) -> Result<Vec<Node>> {
        let rows = self.c.rows(q, args, NODE_KINDS, what).await?;
        Ok(rows.into_iter().map(scan_node).collect())
    }

    async fn texts(&mut self, q: &str, args: &[Arg], what: &str) -> Result<Vec<String>> {
        let rows = self.c.rows(q, args, &[Kind::Text], what).await?;
        Ok(rows.into_iter().map(|mut r| r.s()).collect())
    }

    async fn query_links(&mut self, q: &str, args: &[Arg]) -> Result<Vec<Link>> {
        let rows = self.c.rows(q, args, LINK_KINDS, "links").await?;
        Ok(rows.into_iter().map(scan_link).collect())
    }

    /// exec1 runs a statement that must touch exactly one row (not found otherwise).
    async fn exec1(&mut self, what: &str, q: &str, args: &[Arg]) -> Result<()> {
        let n = self.c.exec(q, args, what).await?;
        if n != 1 {
            return Err(Error::wrap(ErrorKind::NotFound, what));
        }
        Ok(())
    }

    async fn put_entries(&mut self, id: &BaselineID, entries: &[BaselineEntry]) -> Result<()> {
        let d = Self::D;
        let q = d.sql_insert(
            "baseline_entry",
            &["baseline_id", "node_id", "version", "removed"],
            "",
        );
        for e in entries {
            self.c
                .exec(
                    &q,
                    &[
                        Arg::Id(id.to_string()),
                        Arg::Id(e.node.to_string()),
                        Arg::Int(e.version),
                        Arg::Bool(e.removed),
                    ],
                    "baseline entries",
                )
                .await?;
        }
        Ok(())
    }

    /// changeRoot is the root change of the tree id belongs to, id itself when it is not stored yet (PostgreSQL).
    async fn change_root(&mut self, id: &ChangeID, what: &str) -> Result<String> {
        let rows = self
            .c
            .rows(
                "WITH RECURSIVE up (id, parent_id) AS (
			SELECT id, parent_id FROM change WHERE id = $1
			UNION ALL SELECT c.id, c.parent_id FROM change c JOIN up ON c.id = up.parent_id)
		SELECT id::text FROM up WHERE parent_id IS NULL",
                &[Arg::Id(id.to_string())],
                &[Kind::Text],
                what,
            )
            .await?;
        Ok(rows
            .into_iter()
            .next()
            .map(|mut r| r.s())
            .unwrap_or_else(|| id.to_string()))
    }

    async fn advisory_lock(&mut self, key: &str, what: &str) -> Result<()> {
        self.c
            .exec(
                "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
                &[Arg::Text(key.to_string())],
                what,
            )
            .await?;
        Ok(())
    }
}

#[async_trait]
impl<C: SqlConn> Tx for SqlTx<C> {
    async fn node(&mut self, ctx: &Ctx, r: &NodeRef) -> Result<Node> {
        if r.version == 0 {
            return self.latest_on(ctx, &r.id, MAIN_BRANCH).await;
        }
        let (q, args) = Self::D.sql_node(r);
        let what = format!("node {r}");
        let row = self.one(&q, &args, NODE_KINDS, &what).await?;
        Ok(scan_node(row))
    }

    async fn latest_on(&mut self, _ctx: &Ctx, id: &NodeID, branch: &str) -> Result<Node> {
        let (q, args) = Self::D.sql_latest_on(id, branch);
        let what = format!("node {id} on {}", branch_of(branch));
        let row = self.one(&q, &args, NODE_KINDS, &what).await?;
        Ok(scan_node(row))
    }

    async fn versions(&mut self, _ctx: &Ctx, id: &NodeID) -> Result<Vec<Node>> {
        let (q, args) = Self::D.sql_versions(id);
        let mut out = self.nodes(&q, &args, &format!("node {id}")).await?;
        if out.is_empty() {
            return Err(Error::wrap(ErrorKind::NotFound, format!("node {id}")));
        }
        let (q, args) = Self::D.sql_version_branches(id);
        let rows = self
            .c
            .rows(&q, &args, &[Kind::Int, Kind::Text], "version branches")
            .await?;
        for mut r in rows {
            let (v, b) = (r.i(), r.s());
            if v >= 1 && (v as usize) <= out.len() {
                out[(v - 1) as usize].joined.push(b);
            }
        }
        Ok(out)
    }

    async fn derived_nodes(&mut self, _ctx: &Ctx, r: &NodeRef) -> Result<Vec<Node>> {
        let (q, args) = Self::D.sql_derived_nodes(r);
        self.nodes(&q, &args, "derived nodes").await
    }

    async fn branch(&mut self, _ctx: &Ctx, namespace: &str, name: &str) -> Result<Branch> {
        let (q, args) = Self::D.sql_branch(namespace, name);
        let row = self
            .one(&q, &args, BRANCH_KINDS, &format!("branch {name}"))
            .await?;
        Ok(scan_branch(row))
    }

    async fn branches(&mut self, _ctx: &Ctx, namespace: &str) -> Result<Vec<Branch>> {
        let (q, args) = Self::D.sql_branches(namespace);
        let rows = self.c.rows(&q, &args, BRANCH_KINDS, "branches").await?;
        Ok(rows.into_iter().map(scan_branch).collect())
    }

    async fn put_branch(&mut self, _ctx: &Ctx, b: Branch) -> Result<()> {
        let d = Self::D;
        let ps: Vec<String> = (1..=11).map(|i| d.ph(i)).collect();
        let q = format!(
            "INSERT INTO branch (namespace, name, parent, fork_baseline, head_baseline, origin, status, created_at, description, intent, rebased_from) VALUES ({})
		ON CONFLICT (namespace, name) DO UPDATE SET status = excluded.status, head_baseline = excluded.head_baseline, description = excluded.description,
			fork_baseline = excluded.fork_baseline, rebased_from = excluded.rebased_from, parent = excluded.parent",
            ps.join(", ")
        );
        let args = [
            Arg::text(namespace_of(&b.namespace)),
            Arg::text(b.name.clone()),
            Arg::text(b.parent.clone()),
            Arg::null_id(&b.fork_baseline),
            Arg::null_id(&b.head),
            Arg::text(b.origin.clone()),
            Arg::text(b.status.clone()),
            Arg::Time(b.created_at),
            Arg::text(b.description.clone()),
            Arg::text(b.intent.to_string()),
            Arg::null_id(&b.rebased_from),
        ];
        self.c
            .exec(&q, &args, &format!("branch {}", b.name))
            .await?;
        Ok(())
    }

    async fn node_by_key(&mut self, ctx: &Ctx, namespace: &str, key: &str) -> Result<Node> {
        let id = self.node_id_by_key(ctx, namespace, key).await?;
        self.latest_on(ctx, &id, MAIN_BRANCH).await
    }

    async fn node_id_by_key(&mut self, _ctx: &Ctx, namespace: &str, key: &str) -> Result<NodeID> {
        let (q, args) = Self::D.sql_node_id_by_key(namespace, key);
        let mut row = self
            .one(&q, &args, &[Kind::Text], &format!("node key {key}"))
            .await?;
        Ok(NodeID::new(row.s()))
    }

    async fn nodes_in(
        &mut self,
        ctx: &Ctx,
        baseline: &BaselineID,
        node_type: &str,
    ) -> Result<Vec<Node>> {
        self.baseline(ctx, baseline).await?;
        let (q, args) = Self::D.sql_nodes_in(baseline, node_type);
        self.nodes(&q, &args, "baseline nodes").await
    }

    async fn latest_nodes(
        &mut self,
        _ctx: &Ctx,
        namespace: &str,
        branch: &str,
    ) -> Result<Vec<Node>> {
        let d = Self::D;
        let (ns, br) = (namespace_of(namespace), branch_of(branch));
        if d.pg {
            let q = format!(
                "SELECT DISTINCT ON (n.key) {}{NODE_FROM}
	      WHERE n.namespace = $1 AND {} ORDER BY n.key, v.version DESC",
                d.node_cols(),
                on_branch_sql("$2")
            );
            return self
                .nodes(&q, &[Arg::text(ns), Arg::text(br)], "latest nodes")
                .await;
        }
        let q = format!(
            "SELECT {}{NODE_FROM}
		WHERE n.namespace = ? AND v.version = (SELECT max(version) FROM (
		  SELECT version FROM node_version WHERE node_id = n.id AND branch = ?
		  UNION ALL SELECT version FROM node_branch WHERE node_id = n.id AND branch = ?))
		ORDER BY n.key",
            d.node_cols()
        );
        self.nodes(
            &q,
            &[Arg::text(ns), Arg::text(br), Arg::text(br)],
            "latest nodes",
        )
        .await
    }

    async fn namespaces(&mut self, _ctx: &Ctx) -> Result<Vec<String>> {
        self.texts(
            "SELECT DISTINCT namespace FROM node ORDER BY namespace",
            &[],
            "namespaces",
        )
        .await
    }

    async fn out_links(&mut self, _ctx: &Ctx, r: &NodeRef) -> Result<Vec<Link>> {
        let (q, args) = Self::D.sql_links(true, r);
        self.query_links(&q, &args).await
    }

    async fn in_links(&mut self, _ctx: &Ctx, r: &NodeRef) -> Result<Vec<Link>> {
        let (q, args) = Self::D.sql_links(false, r);
        self.query_links(&q, &args).await
    }

    async fn baseline(&mut self, _ctx: &Ctx, id: &BaselineID) -> Result<Baseline> {
        let d = Self::D;
        let q = format!(
            "SELECT {}, name, {}, {}, {}, created_at, branch, namespace, gap, kind, {} FROM baseline WHERE id = {}",
            d.id("id"),
            d.id("parent_id"),
            d.id("merged_from"),
            d.id("change_id"),
            d.id("rebased_from"),
            d.ph(1)
        );
        let kinds = [
            Kind::Text,
            Kind::Text,
            Kind::OptText,
            Kind::OptText,
            Kind::OptText,
            Kind::Time,
            Kind::Text,
            Kind::Text,
            Kind::Int,
            Kind::Text,
            Kind::OptText,
        ];
        let mut r = self
            .one(
                &q,
                &[Arg::Id(id.to_string())],
                &kinds,
                &format!("baseline {id}"),
            )
            .await?;
        let mut b = Baseline {
            id: BaselineID::new(r.s()),
            name: r.s(),
            parent_id: BaselineID::new(r.s()),
            merged_from: BaselineID::new(r.s()),
            change_id: ChangeID::new(r.s()),
            created_at: r.t(),
            branch: r.s(),
            namespace: r.s(),
            gap: r.i(),
            kind: r.s(),
            rebased_from: BaselineID::new(r.s()),
            ..Default::default()
        };
        if b.gap > 0 {
            return Ok(b); // only the header is stored
        }
        let (q, args) = d.sql_baseline_entries(id);
        let rows = self
            .c
            .rows(&q, &args, &[Kind::Text, Kind::Int], "baseline entries")
            .await?;
        for mut r in rows {
            let (nid, v) = (r.s(), r.i());
            b.nodes.insert(NodeID::new(nid), v);
        }
        Ok(b)
    }

    async fn baselines(&mut self, ctx: &Ctx, namespace: &str) -> Result<Vec<Baseline>> {
        let (q, args) = Self::D.sql_baseline_ids(namespace);
        let ids = self.texts(&q, &args, "baselines").await?;
        let mut out = Vec::with_capacity(ids.len());
        for id in ids {
            out.push(self.baseline(ctx, &BaselineID::new(id)).await?);
        }
        Ok(out)
    }

    async fn change(&mut self, ctx: &Ctx, id: &ChangeID) -> Result<Change> {
        let d = Self::D;
        let q = format!(
            "SELECT {}, title, intent, methodology, goal, status, {}, {}, data, created_at, branch, namespace, {}, owner_org, project_id, lifecycle, state, guardians, depends_on, methodology_version
		FROM change WHERE id = {}",
            d.id("id"),
            d.id_or("COALESCE", "baseline_id"),
            d.id("result_baseline_id"),
            d.id_or("COALESCE", "parent_id"),
            d.ph(1)
        );
        let kinds = [
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::OptText,
            Kind::Json,
            Kind::Time,
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::Json,
            Kind::Json,
            Kind::Text,
        ];
        let mut r = self
            .one(
                &q,
                &[Arg::Id(id.to_string())],
                &kinds,
                &format!("change {id}"),
            )
            .await?;
        let mut c = Change {
            id: ChangeID::new(r.s()),
            title: r.s(),
            intent: r.s(),
            methodology: r.s(),
            goal: r.s(),
            status: ChangeStatus::new(r.s()),
            baseline_id: BaselineID::new(r.s()),
            result_baseline_id: BaselineID::new(r.s()),
            data: props(r.j()),
            created_at: r.t(),
            branch: r.s(),
            namespace: r.s(),
            parent_id: ChangeID::new(r.s()),
            owner_org: r.s(),
            project_id: r.s(),
            lifecycle: r.s(),
            state: r.s(),
            ..Default::default()
        };
        c.guardians = guardians_of(r.j())?;
        c.depends_on = depends_on_of(r.j())?;
        c.methodology_version = r.s();
        let facts = self.log(ctx, &facts_filter(id)).await?;
        c.items = items_of(&facts)?;
        c.nodes = self.change_impacts(ctx, id).await?;
        Ok(c)
    }

    async fn changes(&mut self, ctx: &Ctx) -> Result<Vec<Change>> {
        let ids = self
            .texts(&Self::D.sql_change_ids(), &[], "changes")
            .await?;
        let mut out = Vec::with_capacity(ids.len());
        for id in ids {
            out.push(self.change(ctx, &ChangeID::new(id)).await?);
        }
        Ok(out)
    }

    async fn put_node(&mut self, _ctx: &Ctx, n: Node) -> Result<()> {
        let d = Self::D;
        if n.version == 1 {
            let q = format!(
                "INSERT INTO node (id, namespace, key, type, latest, project_id) VALUES ({}, {}, {}, {}, 1, {})",
                d.ph(1),
                d.ph(2),
                d.ph(3),
                d.ph(4),
                d.ph(5)
            );
            self.c
                .exec(
                    &q,
                    &[
                        Arg::Id(n.id.to_string()),
                        Arg::text(namespace_of(&n.namespace)),
                        Arg::text(n.key.clone()),
                        Arg::text(n.r#type.clone()),
                        Arg::null_id(&n.project),
                    ],
                    &format!("node {}", n.key),
                )
                .await?;
        } else {
            let q = format!(
                "UPDATE node SET latest = {} WHERE id = {} AND latest = {} - 1",
                d.ph(2),
                d.ph(1),
                d.ph(2)
            );
            let k = self
                .c
                .exec(
                    &q,
                    &[Arg::Id(n.id.to_string()), Arg::Int(n.version)],
                    &format!("node {}", n.r#ref()),
                )
                .await?;
            if k != 1 {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("node {}: not the next version", n.r#ref()),
                ));
            }
        }
        let q = d.sql_insert("node_version", NODE_VERSION_COLUMNS, "");
        let args = [
            Arg::Id(n.id.to_string()),
            Arg::Int(n.version),
            Arg::Json(jsonb(&n.properties)),
            Arg::Bool(n.deleted),
            Arg::null_id(&n.change_id),
            Arg::Time(n.created_at),
            Arg::text(branch_of(&n.branch)),
            Arg::Ints(n.parents.clone()),
            Arg::text(n.reason.clone()),
            Arg::text(n.state.clone()),
            Arg::null_id(&n.change_impact),
            Arg::text(n.comment.clone()),
            Arg::text(n.execution.clone()),
            Arg::null_id(&n.owner),
            Arg::Json(origins_json(&n.origins)),
        ];
        self.c
            .exec(&q, &args, &format!("node {}", n.r#ref()))
            .await?;
        Ok(())
    }

    async fn put_link(&mut self, _ctx: &Ctx, l: Link) -> Result<()> {
        let q = Self::D.sql_insert("link", LINK_COLUMNS, "");
        let args = [
            Arg::Id(l.id.to_string()),
            Arg::text(l.r#type.clone()),
            Arg::Id(l.from.id.to_string()),
            Arg::Int(l.from.version),
            Arg::Id(l.to.id.to_string()),
            Arg::Int(l.to.version),
            Arg::Json(jsonb(&l.properties)),
            Arg::null_id(&l.change_id),
        ];
        self.c.exec(&q, &args, "link").await?;
        Ok(())
    }

    async fn link(&mut self, _ctx: &Ctx, id: &LinkID) -> Result<Link> {
        let (q, args) = Self::D.sql_link_by_id(id);
        let mut ls = self.query_links(&q, &args).await?;
        if ls.is_empty() {
            return Err(Error::wrap(ErrorKind::NotFound, format!("link {id}")));
        }
        Ok(ls.swap_remove(0))
    }

    /// PutBaseline stores the baseline as a delta from its parent, or whole at a checkpoint (ADR 0032).
    async fn put_baseline(&mut self, ctx: &Ctx, b: Baseline) -> Result<()> {
        let d = Self::D;
        let mut parent: Option<Baseline> = None;
        let mut parent_depth = 0;
        if !b.parent_id.is_empty() && b.gap == 0 {
            let q = format!("SELECT depth, gap FROM baseline WHERE id = {}", d.ph(1));
            let mut r = self
                .one(
                    &q,
                    &[Arg::Id(b.parent_id.to_string())],
                    &[Kind::Int, Kind::Int],
                    &format!("baseline {}", b.parent_id),
                )
                .await?;
            parent_depth = r.i();
            let parent_gap = r.i();
            // a delta is only stored over a parent whose entries are stored: else the baseline is stored whole
            if parent_gap == 0 {
                parent = Some(self.baseline(ctx, &b.parent_id).await?);
            }
        }
        let (mut depth, mut entries) = stored_entries(parent.as_ref(), parent_depth, &b.nodes);
        if b.gap > 0 {
            depth = 0;
            entries = Vec::new();
        }
        let q = d.sql_insert("baseline", BASELINE_COLUMNS, "");
        let args = [
            Arg::Id(b.id.to_string()),
            Arg::text(b.name.clone()),
            Arg::null_id(&b.parent_id),
            Arg::null_id(&b.merged_from),
            Arg::null_id(&b.change_id),
            Arg::Time(b.created_at),
            Arg::text(branch_of(&b.branch)),
            Arg::text(namespace_of(&b.namespace)),
            Arg::Int(depth),
            Arg::Int(b.gap),
            Arg::text(kind_of(&b)),
            Arg::null_id(&b.rebased_from),
        ];
        self.c.exec(&q, &args, "baseline").await?;
        self.put_entries(&b.id, &entries).await
    }

    async fn branch_joins(
        &mut self,
        _ctx: &Ctx,
        namespace: &str,
        branch: &str,
        change: &ChangeID,
    ) -> Result<Vec<NodeRef>> {
        let (q, args) = Self::D.sql_branch_joins(namespace, branch, change);
        let rows = self
            .c
            .rows(&q, &args, &[Kind::Text, Kind::Int], "branch joins")
            .await?;
        Ok(rows
            .into_iter()
            .map(|mut r| NodeRef {
                id: NodeID::new(r.s()),
                version: r.i(),
            })
            .collect())
    }

    async fn materialize_baseline(
        &mut self,
        _ctx: &Ctx,
        id: &BaselineID,
        nodes: &BTreeMap<NodeID, Version>,
    ) -> Result<()> {
        let d = Self::D;
        let what = format!("baseline {id}");
        self.c
            .exec(
                &format!("DELETE FROM baseline_entry WHERE baseline_id = {}", d.ph(1)),
                &[Arg::Id(id.to_string())],
                &what,
            )
            .await?;
        self.exec1(
            &what,
            &format!(
                "UPDATE baseline SET depth = 0, gap = 0 WHERE id = {}",
                d.ph(1)
            ),
            &[Arg::Id(id.to_string())],
        )
        .await?;
        let entries: Vec<BaselineEntry> = nodes
            .iter()
            .map(|(n, v)| BaselineEntry {
                node: n.clone(),
                version: *v,
                removed: false,
            })
            .collect();
        self.put_entries(id, &entries).await
    }

    async fn put_tag(&mut self, _ctx: &Ctx, t: Tag) -> Result<()> {
        let d = Self::D;
        let ps: Vec<String> = (1..=7).map(|i| d.ph(i)).collect();
        let q = format!(
            "INSERT INTO tag (id, name, namespace, change_id, baseline_id, by, created_at) VALUES ({})
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, baseline_id = excluded.baseline_id",
            ps.join(", ")
        );
        let args = [
            Arg::Id(t.id.to_string()),
            Arg::text(t.name.clone()),
            Arg::text(namespace_of(&t.namespace)),
            Arg::Id(t.change_id.to_string()),
            Arg::null_id(&t.baseline_id),
            Arg::text(t.by.clone()),
            Arg::Time(t.created_at),
        ];
        self.c.exec(&q, &args, &format!("tag {}", t.name)).await?;
        Ok(())
    }

    async fn delete_tag(&mut self, _ctx: &Ctx, id: &TagID) -> Result<()> {
        let (q, args) = Self::D.sql_delete_tag(id);
        self.exec1(&format!("tag {id}"), &q, &args).await
    }

    async fn tags(&mut self, _ctx: &Ctx, f: &TagFilter) -> Result<Vec<Tag>> {
        let (q, args) = Self::D.sql_tags(f);
        let kinds = [
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::Text,
            Kind::Time,
        ];
        let rows = self.c.rows(&q, &args, &kinds, "tags").await?;
        Ok(rows
            .into_iter()
            .map(|mut r| Tag {
                id: TagID::new(r.s()),
                name: r.s(),
                namespace: r.s(),
                change_id: ChangeID::new(r.s()),
                baseline_id: BaselineID::new(r.s()),
                by: r.s(),
                created_at: r.t(),
            })
            .collect())
    }

    async fn put_change(&mut self, _ctx: &Ctx, c: Change) -> Result<()> {
        let d = Self::D;
        let ps: Vec<String> = (1..=20).map(|i| d.ph(i)).collect();
        let q = format!(
            "INSERT INTO change (id, title, intent, methodology, goal, status, baseline_id, result_baseline_id, data, created_at, branch, namespace, parent_id, owner_org, project_id, lifecycle, state, guardians, depends_on, methodology_version)
		VALUES ({})
		ON CONFLICT (id) DO UPDATE SET title = excluded.title, intent = excluded.intent, goal = excluded.goal, status = excluded.status,
		  result_baseline_id = excluded.result_baseline_id, data = excluded.data, baseline_id = excluded.baseline_id, branch = excluded.branch,
		  lifecycle = excluded.lifecycle, state = excluded.state, project_id = excluded.project_id,
		  parent_id = excluded.parent_id, guardians = excluded.guardians, depends_on = excluded.depends_on,
		  methodology_version = excluded.methodology_version",
            ps.join(", ")
        );
        let args = [
            Arg::Id(c.id.to_string()),
            Arg::text(c.title.clone()),
            Arg::text(c.intent.clone()),
            Arg::text(c.methodology.clone()),
            Arg::text(c.goal.clone()),
            Arg::text(c.status.to_string()),
            Arg::null_id(&c.baseline_id),
            Arg::null_id(&c.result_baseline_id),
            Arg::Json(jsonb(&c.data)),
            Arg::Time(c.created_at),
            Arg::text(branch_of(&c.branch)),
            Arg::text(namespace_of(&c.namespace)),
            Arg::null_id(&c.parent_id),
            Arg::text(c.owner_org.clone()),
            Arg::text(c.project_id.clone()),
            Arg::text(c.lifecycle.clone()),
            Arg::text(c.state.clone()),
            Arg::Json(guardians_json(&c.guardians)),
            Arg::Json(depends_on_json(&c.depends_on)),
            Arg::text(c.methodology_version.clone()),
        ];
        self.c.exec(&q, &args, "change").await?;
        Ok(())
    }

    async fn append_log(&mut self, _ctx: &Ctx, mut e: LogEntry) -> Result<LogEntry> {
        let q = Self::D.sql_insert("change_log", CHANGE_LOG_COLUMNS, " RETURNING seq");
        let args = [
            Arg::text(e.id.clone()),
            Arg::Id(e.change.to_string()),
            Arg::text(e.r#type.clone()),
            Arg::text(e.flow.clone()),
            Arg::text(e.process.clone()),
            Arg::text(e.execution.clone()),
            Arg::text(e.subject.clone()),
            Arg::text(e.by.clone()),
            Arg::text(e.causation.clone()),
            Arg::Time(e.at.unwrap_or_else(gotime::zero)),
            Arg::Json(e.payload.clone()),
        ];
        let mut r = self
            .one(&q, &args, &[Kind::Int], &format!("log entry {}", e.r#type))
            .await?;
        e.seq = r.i();
        Ok(e)
    }

    async fn log(&mut self, _ctx: &Ctx, f: &LogFilter) -> Result<Vec<LogEntry>> {
        let (q, args) = Self::D.sql_log(f);
        let rows = self.c.rows(&q, &args, LOG_KINDS, "log").await?;
        Ok(rows.into_iter().map(scan_log).collect())
    }

    async fn log_counts(&mut self, _ctx: &Ctx, f: &LogFilter) -> Result<BTreeMap<String, i64>> {
        let (q, args) = Self::D.sql_log_counts(f);
        let rows = self
            .c
            .rows(&q, &args, &[Kind::Text, Kind::Int], "log counts")
            .await?;
        Ok(rows
            .into_iter()
            .map(|mut r| {
                let t = r.s();
                (t, r.i())
            })
            .collect())
    }

    async fn open_change_ids(&mut self, _ctx: &Ctx) -> Result<Vec<ChangeID>> {
        let ids = self
            .texts(&Self::D.sql_open_change_ids(), &[], "open changes")
            .await?;
        Ok(ids.into_iter().map(ChangeID::new).collect())
    }

    async fn put_change_impact(
        &mut self,
        _ctx: &Ctx,
        change: &ChangeID,
        cn: ChangeImpact,
    ) -> Result<()> {
        let r = to_cn_row(&cn)?;
        let q = if Self::D.pg {
            "INSERT INTO change_impact (id, change_id, node_id, key, type, intent, rationale, pre_version, post_version, landed_version, review, reviews, via, recheck, produced_by, derived_from, items, execution, created_at, flow, superseded)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		ON CONFLICT (id) DO UPDATE SET node_id = $3, key = $4, type = $5, intent = $6, rationale = $7, pre_version = $8, post_version = $9, landed_version = $10,
			review = $11, reviews = $12, via = $13, recheck = $14, produced_by = $15, derived_from = $16, items = $17, execution = $18, flow = $20, superseded = $21"
        } else {
            "INSERT INTO change_impact (id, seq, change_id, node_id, key, type, intent, rationale, pre_version, post_version, landed_version, review, reviews, via, recheck, produced_by, derived_from, items, execution, created_at, flow, superseded)
		VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM change_impact), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET node_id = excluded.node_id, key = excluded.key, type = excluded.type, intent = excluded.intent, rationale = excluded.rationale,
			pre_version = excluded.pre_version, post_version = excluded.post_version, landed_version = excluded.landed_version, review = excluded.review,
			reviews = excluded.reviews, via = excluded.via, recheck = excluded.recheck, produced_by = excluded.produced_by, derived_from = excluded.derived_from, items = excluded.items, execution = excluded.execution, flow = excluded.flow, superseded = excluded.superseded"
        };
        let args = [
            Arg::Id(r.id.clone()),
            Arg::Id(change.to_string()),
            Arg::OptId(r.node_id.clone()),
            Arg::text(r.key.clone()),
            Arg::text(r.r#type.clone()),
            Arg::text(r.intent.clone()),
            Arg::text(r.rationale.clone()),
            Arg::OptInt(r.pre),
            Arg::OptInt(r.post),
            Arg::OptInt(r.landed),
            Arg::text(r.review.clone()),
            Arg::Json(r.reviews.clone()),
            Arg::null_id(&r.via),
            Arg::Bool(r.recheck),
            Arg::text(r.produced_by.clone()),
            Arg::Json(r.derived_from.clone()),
            Arg::Json(r.items.clone()),
            Arg::text(r.execution.clone()),
            Arg::Time(cn.created_at),
            Arg::text(r.flow.clone()),
            Arg::Bool(r.superseded),
        ];
        self.c
            .exec(q, &args, &format!("change impact {}", cn.key))
            .await?;
        Ok(())
    }

    async fn delete_change_impact(
        &mut self,
        _ctx: &Ctx,
        change: &ChangeID,
        id: &ChangeImpactID,
    ) -> Result<()> {
        let q = Self::D.sql_delete_change_impact();
        self.exec1(
            &format!("change impact {id}"),
            &q,
            &[Arg::Id(change.to_string()), Arg::Id(id.to_string())],
        )
        .await
    }

    async fn change_impacts(&mut self, _ctx: &Ctx, change: &ChangeID) -> Result<Vec<ChangeImpact>> {
        let (q, args) = Self::D.sql_change_impacts(change);
        let rows = self
            .c
            .rows(&q, &args, CHANGE_IMPACT_KINDS, "change impacts")
            .await?;
        rows.into_iter().map(scan_change_impact).collect()
    }

    async fn node_change_impacts(&mut self, _ctx: &Ctx, node: &NodeID) -> Result<Vec<ChangeID>> {
        let (q, args) = Self::D.sql_node_change_impacts(node);
        let ids = self.texts(&q, &args, "node change impacts").await?;
        Ok(ids.into_iter().map(ChangeID::new).collect())
    }

    async fn set_node_origin(
        &mut self,
        _ctx: &Ctx,
        r: &NodeRef,
        change: &ChangeID,
        cn: &ChangeImpactID,
        comment: &str,
    ) -> Result<()> {
        let q = Self::D.sql_set_node_origin();
        self.exec1(
            &format!("node {r}"),
            &q,
            &[
                Arg::Id(r.id.to_string()),
                Arg::Int(r.version),
                Arg::null_id(change),
                Arg::null_id(cn),
                Arg::text(comment),
            ],
        )
        .await
    }

    async fn join_branch(
        &mut self,
        _ctx: &Ctx,
        r: &NodeRef,
        branch: &str,
        change: &ChangeID,
    ) -> Result<()> {
        let q = Self::D.sql_join_branch();
        self.c
            .exec(
                &q,
                &[
                    Arg::Id(r.id.to_string()),
                    Arg::Int(r.version),
                    Arg::text(branch_of(branch)),
                    Arg::null_id(change),
                ],
                &format!("node {r}"),
            )
            .await?;
        Ok(())
    }

    async fn delete_change(
        &mut self,
        _ctx: &Ctx,
        id: &ChangeID,
        namespace: &str,
        branch: &str,
    ) -> Result<()> {
        let d = Self::D;
        let what = format!("change {id}");
        let arg = [Arg::Id(id.to_string())];
        for q in d.delete_change_used() {
            let mut r = self.one(&q, &arg, &[Kind::Int], &what).await?;
            if r.i() > 0 {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("change {id}: what it wrote is used by the graph"),
                ));
            }
        }
        let nodes = self.texts(&d.sql_change_nodes(), &arg, &what).await?;
        for q in d.delete_change_rows() {
            self.c.exec(&q, &arg, &what).await?;
        }
        for n in nodes {
            let a = [Arg::Id(n.clone())];
            let w = format!("node {n}");
            self.c.exec(&d.sql_delete_orphan_node(), &a, &w).await?;
            self.c.exec(&d.sql_refresh_latest(), &a, &w).await?;
        }
        if !branch.is_empty() {
            self.c
                .exec(
                    &d.sql_delete_branch(),
                    &[Arg::text(namespace), Arg::text(branch)],
                    &format!("branch {branch}"),
                )
                .await?;
        }
        self.exec1(&what, &d.sql_delete_change(), &arg).await
    }

    /// LockChange: SQLite runs one transaction at a time (one connection, goap-platform), so the writers of a change
    /// are serialized already; PostgreSQL takes a transaction-level advisory lock on the root of the change's tree, read
    /// again once the lock is held (an attach or a detach that committed meanwhile moved the change to another tree,
    /// whose lock is taken in turn).
    async fn lock_change(&mut self, _ctx: &Ctx, id: &ChangeID) -> Result<()> {
        if !Self::D.pg {
            return Ok(());
        }
        let what = format!("lock change {id}");
        let mut root = self.change_root(id, &what).await?;
        loop {
            self.advisory_lock(&format!("goap.change:{root}"), &what)
                .await?;
            let again = self.change_root(id, &what).await?;
            if again == root {
                return Ok(());
            }
            root = again;
        }
    }

    /// LockBranch: a transaction-level advisory lock on the branch (PostgreSQL); nothing to do in SQLite.
    async fn lock_branch(&mut self, _ctx: &Ctx, namespace: &str, name: &str) -> Result<()> {
        if !Self::D.pg {
            return Ok(());
        }
        let key = format!("goap.branch:{}/{name}", namespace_of(namespace));
        self.advisory_lock(&key, &format!("lock branch {name}"))
            .await
    }

    async fn commit(self: Box<Self>, _ctx: &Ctx) -> Result<()> {
        let me = *self;
        Box::new(me.c).commit().await
    }

    async fn rollback(self: Box<Self>) -> Result<()> {
        let me = *self;
        Box::new(me.c).rollback().await
    }

    fn rolls_back_late(&self) -> bool {
        true
    }

    async fn baseline_headers(&mut self, _ctx: &Ctx) -> Result<Option<Vec<BaselineHeader>>> {
        let rows = self
            .c
            .rows(
                &Self::D.sql_baseline_headers(),
                &[],
                &[Kind::Text, Kind::Text, Kind::Int],
                "baseline headers",
            )
            .await?;
        Ok(Some(
            rows.into_iter()
                .map(|mut r| BaselineHeader {
                    id: BaselineID::new(r.s()),
                    parent: BaselineID::new(r.s()),
                    depth: r.i(),
                })
                .collect(),
        ))
    }

    async fn rewrite_baseline(
        &mut self,
        _ctx: &Ctx,
        id: &BaselineID,
        depth: i64,
        entries: &[BaselineEntry],
    ) -> Result<()> {
        let d = Self::D;
        let what = format!("baseline {id}");
        self.c
            .exec(
                &format!("DELETE FROM baseline_entry WHERE baseline_id = {}", d.ph(1)),
                &[Arg::Id(id.to_string())],
                &what,
            )
            .await?;
        self.c
            .exec(
                &format!(
                    "UPDATE baseline SET depth = {} WHERE id = {}",
                    d.ph(2),
                    d.ph(1)
                ),
                &[Arg::Id(id.to_string()), Arg::Int(depth)],
                &what,
            )
            .await?;
        self.put_entries(id, entries).await
    }
}
