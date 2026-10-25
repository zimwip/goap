//! The writes of the graph ([`graph_core::store::WriteTx`]) in a write transaction of either dialect: the statements
//! and the encodings of the Go repositories (`graph/graph/postgres.go`, `sqlite.go`), so that a row reads the same
//! whichever service wrote it.
use std::collections::HashMap;

use graph_core::{
    GraphError,
    change::Change,
    gojson,
    impact::{ChangeImpact, LogEntry},
    model::{Baseline, Branch, ChangeId, Link, LinkId, Node, NodeId, NodeRef, Target, branch_of, namespace_of},
    store::{LinkSet, OutboxMessage, WriteTx},
};

use super::{Arg, Row, Session, Stmt};
use crate::StoreError;

/// The columns of the inserts both repositories run, in the order of their arguments (`sqlbuild.go`).
const NODE_VERSION_COLUMNS: &[&str] = &[
    "node_id",
    "version",
    "props",
    "deleted",
    "change_id",
    "created_at",
    "branch",
    "parents",
    "reason",
    "state",
    "change_impact",
    "comment",
    "execution",
    "owner",
    "origins",
    "committed",
];
const LINK_COLUMNS: &[&str] = &[
    "id",
    "type",
    "from_id",
    "from_version",
    "to_id",
    "to_version",
    "props",
    "change_id",
    "removes",
    "to_url",
];
const BASELINE_COLUMNS: &[&str] = &[
    "id",
    "name",
    "parent_id",
    "merged_from",
    "change_id",
    "created_at",
    "branch",
    "namespace",
    "kind",
    "rebased_from",
    "seq",
];
const CHANGE_LOG_COLUMNS: &[&str] = &[
    "id",
    "change_id",
    "type",
    "flow",
    "process_id",
    "execution",
    "subject",
    "by_whom",
    "causation",
    "at",
    "payload",
];

/// The number of links one statement writes (ten arguments each).
const LINK_ROWS_PER_INSERT: usize = 1000;

/// The kind of a baseline that names none.
const BASELINE_SNAPSHOT: &str = "snapshot";

/// An error of a write, as Go's `mapErr` / `sqliteErr` sort them: a duplicate is a conflict, a broken reference
/// invalid, a deadlock a conflict the caller retries.
fn map_err(e: StoreError, what: &str) -> GraphError {
    match &e {
        StoreError::Postgres(pe) => {
            if let Some(db) = pe.as_db_error() {
                let code = db.code().code();
                let detail = db.detail().unwrap_or_default();
                return match code {
                    "23505" => GraphError::conflict(format!("{what}: {detail}")),
                    "23503" | "22P02" => GraphError::invalid(format!("{what}: {}", db.message())),
                    "40P01" | "40001" => GraphError::conflict(format!("{what}: {}", db.message())),
                    _ => GraphError::internal(format!("{what}: {e}")),
                };
            }
            GraphError::internal(format!("{what}: {e}"))
        }
        StoreError::Sqlite(se) => {
            let msg = se.to_string();
            if msg.contains("UNIQUE constraint failed") || msg.contains("PRIMARY KEY constraint failed") {
                GraphError::conflict(format!("{what}: {msg}"))
            } else if msg.contains("FOREIGN KEY constraint failed") {
                GraphError::invalid(format!("{what}: {msg}"))
            } else {
                GraphError::internal(format!("{what}: {e}"))
            }
        }
        _ => GraphError::internal(format!("{what}: {e}")),
    }
}

/// A JSON object column: `{}` when empty (Go's `jsonb`).
fn jsonb(p: &graph_core::model::Props) -> String {
    match p {
        Some(m) => gojson::to_string(m),
        None => "{}".into(),
    }
}

/// A JSON array column: `[]` when empty.
fn json_list<T: serde::Serialize>(xs: &[T]) -> String {
    if xs.is_empty() {
        "[]".into()
    } else {
        gojson::to_string(xs)
    }
}

impl Session {
    fn insert(&self, table: &str, columns: &[&str], tail: &str) -> String {
        let ps: Vec<String> = (1..=columns.len()).map(|i| self.sql.ph(i)).collect();
        format!(
            "INSERT INTO {table} ({}) VALUES ({}){tail}",
            columns.join(", "),
            ps.join(", ")
        )
    }

    fn insert_rows(&self, table: &str, columns: &[&str], n: usize) -> String {
        let mut q = format!("INSERT INTO {table} ({}) VALUES ", columns.join(", "));
        for r in 0..n {
            if r > 0 {
                q.push_str(", ");
            }
            let ps: Vec<String> = (0..columns.len())
                .map(|c| self.sql.ph(r * columns.len() + c + 1))
                .collect();
            q.push('(');
            q.push_str(&ps.join(", "));
            q.push(')');
        }
        q
    }

    /// Appends messages to an outbox table (`lib/outbox.SQL.Append`): the graph's, or another service's sharing its
    /// database (the domain registry's `domain_outbox`).
    pub(super) async fn append_to_outbox(&self, table: &str, msgs: &[OutboxMessage]) -> Result<(), GraphError> {
        let q = format!(
            "INSERT INTO {table} (subject, payload, headers, created_at) VALUES ({}, {}, {}, {})",
            self.sql.ph(1),
            self.sql.ph(2),
            self.sql.ph(3),
            self.sql.ph(4)
        );
        for m in msgs {
            let args = vec![
                m.subject.as_str().into(),
                m.data.as_str().into(),
                Arg::Text(gojson::to_string(&m.headers)),
                Arg::Time(m.created_at),
            ];
            self.run((q.clone(), args), "outbox").await?;
        }
        Ok(())
    }

    pub(super) async fn run(&self, stmt: Stmt, what: &str) -> Result<u64, GraphError> {
        self.exec(stmt).await.map_err(|e| map_err(e, what))
    }

    pub(super) async fn select(&self, stmt: Stmt, what: &str) -> Result<Vec<Row>, GraphError> {
        self.query(stmt).await.map_err(|e| map_err(e, what))
    }

    /// The root of the tree a change belongs to, the change itself when it is not stored yet.
    async fn change_root(&self, id: &ChangeId) -> Result<String, GraphError> {
        if uuid::Uuid::try_parse(id.as_str()).is_err() {
            return Err(GraphError::invalid(format!(
                "lock change {id}: invalid input syntax for type uuid: \"{id}\""
            )));
        }
        let q = "WITH RECURSIVE up (id, parent_id) AS (
			SELECT id, parent_id FROM change WHERE id = $1
			UNION ALL SELECT c.id, c.parent_id FROM change c JOIN up ON c.id = up.parent_id)
		SELECT id::text FROM up WHERE parent_id IS NULL";
        let rows = self
            .select((q.into(), vec![id.as_str().into()]), &format!("lock change {id}"))
            .await?;
        Ok(rows.first().map_or_else(|| id.to_string(), |r| r.text(0)))
    }

    async fn advisory_lock(&self, key: &str) -> Result<(), GraphError> {
        self.select(
            (
                "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))::text".into(),
                vec![key.into()],
            ),
            key,
        )
        .await?;
        Ok(())
    }

    fn link_args(&self, l: &Link, removes: Arg, args: &mut Vec<Arg>) {
        let (to, tv, url) = match &l.to {
            Target::Node(r) => (Arg::Text(r.id.to_string()), Arg::Int(r.version.into()), String::new()),
            Target::External(u) => (Arg::Null, Arg::Null, u.to_string()),
        };
        args.extend([
            Arg::Text(l.id.to_string()),
            Arg::Text(l.typ.clone()),
            Arg::Text(l.from.id.to_string()),
            Arg::Int(l.from.version.into()),
            to,
            tv,
            Arg::Text(jsonb(&l.props)),
            Arg::or_null(l.change_id.as_str()),
            removes,
            Arg::Text(url),
        ]);
    }
}

impl WriteTx for Session {
    async fn lock_change(&self, id: &ChangeId) -> Result<(), GraphError> {
        if !self.sql.pg {
            // SQLite has one writer: the transaction holds it
            return Ok(());
        }
        let mut root = self.change_root(id).await?;
        loop {
            self.advisory_lock(&format!("goap.change:{root}")).await?;
            let again = self.change_root(id).await?;
            if again == root {
                return Ok(());
            }
            root = again;
        }
    }

    async fn lock_branch(&self, namespace: &str, name: &str) -> Result<(), GraphError> {
        if !self.sql.pg {
            return Ok(());
        }
        self.advisory_lock(&format!("goap.branch:{}/{name}", namespace_of(namespace)))
            .await
    }

    async fn put_change(&self, c: &Change) -> Result<(), GraphError> {
        let p = |n| self.sql.ph(n);
        let ex = if self.sql.pg { "EXCLUDED" } else { "excluded" };
        let ps: Vec<String> = (1..=20).map(p).collect();
        let q = format!(
            "INSERT INTO change (id, title, intent, methodology, goal, status, baseline_id, result_baseline_id, data, created_at, branch, namespace, parent_id, owner_org, project_id, lifecycle, state, guardians, depends_on, methodology_version)
		VALUES ({})
		ON CONFLICT (id) DO UPDATE SET title = {ex}.title, intent = {ex}.intent, goal = {ex}.goal, status = {ex}.status,
		  result_baseline_id = {ex}.result_baseline_id, data = {ex}.data, baseline_id = {ex}.baseline_id, branch = {ex}.branch,
		  lifecycle = {ex}.lifecycle, state = {ex}.state, project_id = {ex}.project_id,
		  parent_id = {ex}.parent_id, guardians = {ex}.guardians, depends_on = {ex}.depends_on,
		  methodology = {ex}.methodology, methodology_version = {ex}.methodology_version",
            ps.join(", ")
        );
        let depends: Vec<&str> = c.depends_on.iter().map(ChangeId::as_str).collect();
        let args = vec![
            Arg::Text(c.id.to_string()),
            c.title.as_str().into(),
            c.intent.as_str().into(),
            c.methodology.as_str().into(),
            c.goal.as_str().into(),
            c.status.as_str().into(),
            Arg::or_null(c.baseline_id.as_str()),
            Arg::or_null(c.result_baseline_id.as_str()),
            Arg::Text(jsonb(&c.data)),
            Arg::Time(c.created_at.unwrap_or_else(gojson::zero_time)),
            Arg::Text(branch_of(&c.branch).to_string()),
            Arg::Text(namespace_of(&c.namespace).to_string()),
            Arg::or_null(c.parent_id.as_str()),
            Arg::Text(c.owner_org.to_string()),
            Arg::Text(c.project_id.to_string()),
            c.lifecycle.as_str().into(),
            c.state.as_str().into(),
            Arg::Text(json_list(&c.guardians)),
            Arg::Text(json_list(&depends)),
            c.methodology_version.as_str().into(),
        ];
        self.run((q, args), "change").await?;
        Ok(())
    }

    async fn put_branch(&self, b: &Branch) -> Result<(), GraphError> {
        let ex = if self.sql.pg { "EXCLUDED" } else { "excluded" };
        let ps: Vec<String> = (1..=13).map(|n| self.sql.ph(n)).collect();
        let q = format!(
            "INSERT INTO branch (namespace, name, parent, fork_baseline, head_baseline, origin, status, created_at, description, intent, rebased_from, ephemeral, fork_seq) VALUES ({})
		ON CONFLICT (namespace, name) DO UPDATE SET status = {ex}.status, head_baseline = {ex}.head_baseline, description = {ex}.description,
			fork_baseline = {ex}.fork_baseline, rebased_from = {ex}.rebased_from, parent = {ex}.parent, fork_seq = {ex}.fork_seq",
            ps.join(", ")
        );
        let args = vec![
            Arg::Text(namespace_of(&b.namespace).to_string()),
            b.name.as_str().into(),
            b.parent.as_str().into(),
            Arg::or_null(b.fork_baseline.as_str()),
            Arg::or_null(b.head.as_str()),
            b.origin.as_str().into(),
            b.status.as_str().into(),
            Arg::Time(b.created_at.unwrap_or_else(gojson::zero_time)),
            b.description.as_str().into(),
            b.intent.as_str().into(),
            Arg::or_null(b.rebased_from.as_str()),
            Arg::Bool(b.ephemeral),
            Arg::Int(b.fork_seq),
        ];
        self.run((q, args), &format!("branch {}", b.name)).await?;
        Ok(())
    }

    async fn put_baseline(&self, b: &Baseline) -> Result<(), GraphError> {
        let q = self.insert("baseline", BASELINE_COLUMNS, "");
        let kind = if b.kind.is_empty() { BASELINE_SNAPSHOT } else { &b.kind };
        let args = vec![
            Arg::Text(b.id.to_string()),
            b.name.as_str().into(),
            Arg::or_null(b.parent_id.as_str()),
            Arg::or_null(b.merged_from.as_str()),
            Arg::or_null(b.change_id.as_str()),
            Arg::Time(b.created_at.unwrap_or_else(gojson::zero_time)),
            Arg::Text(branch_of(&b.branch).to_string()),
            Arg::Text(namespace_of(&b.namespace).to_string()),
            kind.into(),
            Arg::or_null(b.rebased_from.as_str()),
            Arg::Int(b.seq),
        ];
        self.run((q, args), "baseline").await?;
        Ok(())
    }

    async fn append_log(&self, e: &LogEntry) -> Result<i64, GraphError> {
        let q = self.insert("change_log", CHANGE_LOG_COLUMNS, " RETURNING seq");
        let payload = if e.payload.is_empty() && self.sql.pg {
            "null".to_string() // the column holds JSON, never NULL
        } else {
            e.payload.clone()
        };
        let args = vec![
            e.id.as_str().into(),
            Arg::Text(e.change.to_string()),
            e.typ.as_str().into(),
            e.flow.as_str().into(),
            e.process.as_str().into(),
            e.execution.as_str().into(),
            e.subject.as_str().into(),
            e.by.as_str().into(),
            e.causation.as_str().into(),
            Arg::Time(e.at),
            Arg::Text(payload),
        ];
        let rows = self.select((q, args), &format!("log entry {}", e.typ)).await?;
        Ok(rows.first().map_or(0, |r| r.int(0)))
    }

    async fn put_change_impact(&self, change: &ChangeId, cn: &ChangeImpact) -> Result<(), GraphError> {
        let ver = |r: &Option<NodeRef>| r.as_ref().map_or(Arg::Null, |r| Arg::Int(r.version.into()));
        let node = cn
            .pre
            .as_ref()
            .or(cn.post.as_ref())
            .map_or(Arg::Null, |r| Arg::Text(r.id.to_string()));
        let args = vec![
            Arg::Text(cn.id.clone()),
            Arg::Text(change.to_string()),
            node,
            cn.key.as_str().into(),
            cn.typ.as_str().into(),
            cn.intent.as_str().into(),
            cn.rationale.as_str().into(),
            ver(&cn.pre),
            ver(&cn.post),
            ver(&cn.committed),
            cn.review.as_str().into(),
            Arg::Text(json_list_always(&cn.reviews)),
            Arg::or_null(&cn.via),
            Arg::Bool(cn.recheck),
            cn.produced_by.as_str().into(),
            Arg::Text(json_list_always(&cn.derived_from)),
            Arg::Text(json_list_always(&cn.items)),
            cn.execution.as_str().into(),
            Arg::Time(cn.created_at),
            cn.flow.as_str().into(),
            Arg::Bool(cn.superseded),
        ];
        let q = if self.sql.pg {
            "INSERT INTO change_impact (id, change_id, node_id, key, type, intent, rationale, pre_version, post_version, committed_version, review, reviews, via, recheck, produced_by, derived_from, items, execution, created_at, flow, superseded)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		ON CONFLICT (id) DO UPDATE SET node_id = $3, key = $4, type = $5, intent = $6, rationale = $7, pre_version = $8, post_version = $9, committed_version = $10,
			review = $11, reviews = $12, via = $13, recheck = $14, produced_by = $15, derived_from = $16, items = $17, execution = $18, flow = $20, superseded = $21".to_string()
        } else {
            // the ?n placeholders bind by number: the order of the columns after seq
            "INSERT INTO change_impact (id, seq, change_id, node_id, key, type, intent, rationale, pre_version, post_version, committed_version, review, reviews, via, recheck, produced_by, derived_from, items, execution, created_at, flow, superseded)
		VALUES (?1, (SELECT COALESCE(MAX(seq), 0) + 1 FROM change_impact), ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18, ?19, ?20, ?21)
		ON CONFLICT (id) DO UPDATE SET node_id = excluded.node_id, key = excluded.key, type = excluded.type, intent = excluded.intent, rationale = excluded.rationale,
			pre_version = excluded.pre_version, post_version = excluded.post_version, committed_version = excluded.committed_version, review = excluded.review,
			reviews = excluded.reviews, via = excluded.via, recheck = excluded.recheck, produced_by = excluded.produced_by, derived_from = excluded.derived_from, items = excluded.items, execution = excluded.execution, flow = excluded.flow, superseded = excluded.superseded".to_string()
        };
        self.run((q, args), &format!("change impact {}", cn.key)).await?;
        Ok(())
    }

    async fn delete_change_impact(&self, change: &ChangeId, id: &str) -> Result<(), GraphError> {
        let q = format!(
            "DELETE FROM change_impact WHERE change_id = {} AND id = {}",
            self.sql.ph(1),
            self.sql.ph(2)
        );
        let what = format!("change impact {id}");
        if self.run((q, vec![change.as_str().into(), id.into()]), &what).await? != 1 {
            return Err(GraphError::not_found(what));
        }
        Ok(())
    }

    async fn change_impact_keys(&self, change: &ChangeId, limit: usize) -> Result<Vec<(String, String)>, GraphError> {
        let q = format!(
            "SELECT key, type FROM change_impact WHERE change_id = {} ORDER BY seq LIMIT {}",
            self.sql.ph(1),
            self.sql.ph(2)
        );
        let rows = self
            .select(
                (q, vec![change.as_str().into(), Arg::Int(limit as i64)]),
                "change impacts",
            )
            .await?;
        Ok(rows.iter().map(|r| (r.text(0), r.text(1))).collect())
    }

    async fn append_outbox(&self, msgs: &[OutboxMessage]) -> Result<(), GraphError> {
        self.append_to_outbox("event_outbox", msgs).await
    }

    async fn put_node(&self, n: &Node, committed: i64) -> Result<(), GraphError> {
        let s = self.sql;
        if n.version == 1 {
            let q = format!(
                "INSERT INTO node (id, namespace, key, type, latest, project) VALUES ({}, {}, {}, {}, 1, {})",
                s.ph(1),
                s.ph(2),
                s.ph(3),
                s.ph(4),
                s.ph(5)
            );
            let args = vec![
                Arg::Text(n.id.to_string()),
                Arg::Text(namespace_of(&n.namespace).to_string()),
                n.key.as_str().into(),
                n.typ.as_str().into(),
                Arg::Text(n.project.to_string()),
            ];
            self.run((q, args), &format!("node {}", n.key)).await?;
        } else {
            let q = format!(
                "UPDATE node SET latest = {} WHERE id = {} AND latest = {} - 1",
                s.ph(2),
                s.ph(1),
                s.ph(2)
            );
            let what = format!("node {}", n.node_ref());
            if self
                .run(
                    (q, vec![Arg::Text(n.id.to_string()), Arg::Int(n.version.into())]),
                    &what,
                )
                .await?
                != 1
            {
                return Err(GraphError::conflict(format!("{what}: not the next version")));
            }
        }
        let q = self.insert("node_version", NODE_VERSION_COLUMNS, "");
        let args = vec![
            Arg::Text(n.id.to_string()),
            Arg::Int(n.version.into()),
            Arg::Text(jsonb(&n.props)),
            Arg::Bool(n.deleted),
            Arg::or_null(n.change_id.as_str()),
            Arg::Time(n.created_at.unwrap_or_else(gojson::zero_time)),
            Arg::Text(branch_of(&n.branch).to_string()),
            Arg::Ints(n.parents.clone()),
            n.reason.as_str().into(),
            n.state.as_str().into(),
            Arg::or_null(&n.change_impact),
            n.comment.as_str().into(),
            n.execution.as_str().into(),
            Arg::Text(n.owner.to_string()),
            Arg::Text(json_list(&n.origins)),
            Arg::Int(committed),
        ];
        self.run((q, args), &format!("node {}", n.node_ref())).await?;
        Ok(())
    }

    async fn put_links(&self, ls: &[Link]) -> Result<(), GraphError> {
        for rows in ls.chunks(LINK_ROWS_PER_INSERT) {
            let mut args = Vec::with_capacity(rows.len() * LINK_COLUMNS.len());
            for l in rows {
                self.link_args(l, Arg::Null, &mut args);
            }
            let q = self.insert_rows("link", LINK_COLUMNS, rows.len());
            self.run((q, args), "link").await?;
        }
        Ok(())
    }

    async fn drop_link(&self, from: &NodeRef, row: &LinkId, l: &Link) -> Result<(), GraphError> {
        let mut drop = l.clone();
        drop.id = row.clone();
        drop.from = from.clone();
        let mut args = Vec::with_capacity(LINK_COLUMNS.len());
        self.link_args(&drop, Arg::Text(l.id.to_string()), &mut args);
        self.run((self.insert("link", LINK_COLUMNS, ""), args), "link").await?;
        Ok(())
    }

    async fn put_link_set(&self, from: &NodeRef, set: LinkSet) -> Result<(), GraphError> {
        let s = self.sql;
        let q = format!(
            "UPDATE node_version SET links = {}, links_base = {}, links_depth = {}, links_seq = {} WHERE node_id = {} AND version = {}",
            s.ph(3),
            s.ph(4),
            s.ph(5),
            s.ph(6),
            s.ph(1),
            s.ph(2)
        );
        let of = if set.of != 0 && set.of != from.version {
            Arg::Int(set.of.into())
        } else {
            Arg::Null
        };
        let base = if set.base != 0 {
            Arg::Int(set.base.into())
        } else {
            Arg::Null
        };
        let args = vec![
            Arg::Text(from.id.to_string()),
            Arg::Int(from.version.into()),
            of,
            base,
            Arg::Int(set.depth.into()),
            Arg::Int(set.seq),
        ];
        self.run((q, args), "link set").await?;
        Ok(())
    }

    async fn link_sets_of(&self, refs: &[NodeRef]) -> Result<HashMap<NodeRef, LinkSet>, GraphError> {
        let mut out = HashMap::with_capacity(refs.len());
        if refs.is_empty() {
            return Ok(out);
        }
        for r in self.select(self.sql.link_sets_of(refs), "link sets").await? {
            out.insert(
                NodeRef::new(NodeId(r.text(0)), r.int32(1)),
                LinkSet {
                    of: r.int32(2),
                    base: r.int32(3),
                    depth: r.int32(4),
                    seq: r.int(5),
                },
            );
        }
        Ok(out)
    }

    async fn join_branch(
        &self,
        r: &NodeRef,
        branch: &str,
        change: &ChangeId,
        committed: i64,
    ) -> Result<(), GraphError> {
        let s = self.sql;
        let q = format!(
            "INSERT INTO node_branch (node_id, version, branch, change_id, committed) SELECT node_id, version, {}, {}, {} FROM node_version
		WHERE node_id = {} AND version = {} AND branch <> {} ON CONFLICT DO NOTHING",
            s.ph(3),
            s.ph(4),
            s.ph(5),
            s.ph(1),
            s.ph(2),
            s.ph(3)
        );
        let args = vec![
            Arg::Text(r.id.to_string()),
            Arg::Int(r.version.into()),
            Arg::Text(branch_of(branch).to_string()),
            Arg::or_null(change.as_str()),
            Arg::Int(committed),
        ];
        self.run((q, args), &format!("node {r}")).await?;
        Ok(())
    }

    async fn set_node_origin(
        &self,
        r: &NodeRef,
        change: &ChangeId,
        impact: &str,
        comment: &str,
    ) -> Result<(), GraphError> {
        let s = self.sql;
        let q = format!(
            "UPDATE node_version SET change_id = {}, change_impact = {}, comment = {} WHERE node_id = {} AND version = {}",
            s.ph(3),
            s.ph(4),
            s.ph(5),
            s.ph(1),
            s.ph(2)
        );
        let args = vec![
            Arg::Text(r.id.to_string()),
            Arg::Int(r.version.into()),
            Arg::or_null(change.as_str()),
            Arg::or_null(impact),
            Arg::Text(comment.to_string()),
        ];
        if self.run((q, args), &format!("node {r}")).await? != 1 {
            return Err(GraphError::not_found(format!("node {r}")));
        }
        Ok(())
    }

    async fn skip_declaration(&self) -> Result<(), GraphError> {
        if self.sql.pg {
            for table in ["change_log", "change_impact"] {
                let q = format!("SELECT nextval(pg_get_serial_sequence('{table}', 'seq'))");
                self.select((q, vec![]), "position of the log").await?;
            }
        }
        Ok(())
    }

    async fn skip_impact_positions(&self, n: usize) -> Result<(), GraphError> {
        if self.sql.pg && n > 0 {
            let q =
                format!("SELECT nextval(pg_get_serial_sequence('change_impact', 'seq')) FROM generate_series(1, {n})");
            self.select((q, vec![]), "position of the impacts").await?;
        }
        Ok(())
    }

    async fn savepoint(&self) -> Result<(), GraphError> {
        self.run(("SAVEPOINT rehearsal".into(), vec![]), "savepoint").await?;
        Ok(())
    }

    async fn rollback_to_savepoint(&self) -> Result<(), GraphError> {
        self.run(("ROLLBACK TO SAVEPOINT rehearsal".into(), vec![]), "savepoint")
            .await?;
        Ok(())
    }

    async fn next_commit(&self) -> Result<i64, GraphError> {
        let q = if self.sql.pg {
            "SELECT nextval('commit_seq')"
        } else {
            // the greatest order stored plus one: the writer is held, no other transaction allocates meanwhile
            "SELECT max((SELECT coalesce(max(seq), 0) FROM baseline), (SELECT coalesce(max(committed), 0) FROM node_version),
			(SELECT coalesce(max(committed), 0) FROM node_branch)) + 1"
        };
        let rows = self.select((q.into(), vec![]), "commit order").await?;
        Ok(rows.first().map_or(1, |r| r.int(0)))
    }

    async fn log_types(&self, change: &ChangeId) -> Result<Vec<String>, GraphError> {
        let q = format!(
            "SELECT DISTINCT type FROM change_log WHERE change_id = {}",
            self.sql.ph(1)
        );
        let rows = self
            .select((q, vec![change.as_str().into()]), "log of a change")
            .await?;
        Ok(rows.iter().map(|r| r.text(0)).collect())
    }

    async fn delete_change(&self, id: &ChangeId, namespace: &str, branch: &str) -> Result<(), GraphError> {
        let s = self.sql;
        let p = s.ph(1);
        let differs = |col: &str| {
            if s.pg {
                format!("{col} IS DISTINCT FROM {p}::uuid")
            } else {
                format!("({col} IS NULL OR {col} != {p})")
            }
        };
        let arg = || vec![Arg::Text(id.to_string())];
        for q in [
            format!("SELECT count(*) FROM baseline WHERE change_id = {p}"),
            format!(
                "SELECT count(*) FROM link l JOIN node_version v ON l.to_id = v.node_id AND l.to_version = v.version WHERE v.change_id = {p} AND {}",
                differs("l.change_id")
            ),
            format!(
                "SELECT count(*) FROM node_version o JOIN node_version v ON o.node_id = v.node_id AND o.version > v.version WHERE v.change_id = {p} AND {}",
                differs("o.change_id")
            ),
        ] {
            let used = self.select((q, arg()), &format!("change {id}")).await?;
            if used.first().map_or(0, |r| r.int(0)) > 0 {
                return Err(GraphError::conflict(format!(
                    "change {id}: what it wrote is used by the graph"
                )));
            }
        }
        let nodes: Vec<String> = self
            .select(
                (
                    format!(
                        "SELECT DISTINCT {} FROM node_version WHERE change_id = {p}",
                        s.id("node_id")
                    ),
                    arg(),
                ),
                &format!("change {id}"),
            )
            .await?
            .iter()
            .map(|r| r.text(0))
            .collect();
        let mine = format!("(SELECT node_id, version FROM node_version WHERE change_id = {p})");
        for q in [
            format!("DELETE FROM tag WHERE (node_id, version) IN {mine}"),
            format!("DELETE FROM link WHERE change_id = {p} OR (from_id, from_version) IN {mine}"),
            format!("DELETE FROM node_branch WHERE change_id = {p} OR (node_id, version) IN {mine}"),
            format!("DELETE FROM node_version WHERE change_id = {p}"),
            format!("DELETE FROM change_impact WHERE change_id = {p}"),
            format!("DELETE FROM change_log WHERE change_id = {p}"),
        ] {
            self.run((q, arg()), &format!("change {id}")).await?;
        }
        for n in nodes {
            let one = || vec![Arg::Text(n.clone())];
            self.run(
                (
                    format!("DELETE FROM node WHERE id = {p} AND NOT EXISTS (SELECT 1 FROM node_version WHERE node_id = {p})"),
                    one(),
                ),
                &format!("node {n}"),
            )
            .await?;
            self.run(
                (
                    format!("UPDATE node SET latest = (SELECT max(version) FROM node_version WHERE node_id = {p}) WHERE id = {p}"),
                    one(),
                ),
                &format!("node {n}"),
            )
            .await?;
        }
        if !branch.is_empty() {
            self.run(
                (
                    format!("DELETE FROM branch WHERE namespace = {p} AND name = {}", s.ph(2)),
                    vec![namespace.into(), branch.into()],
                ),
                &format!("branch {branch}"),
            )
            .await?;
        }
        let what = format!("change {id}");
        if self
            .run((format!("DELETE FROM change WHERE id = {p}"), arg()), &what)
            .await?
            != 1
        {
            return Err(GraphError::not_found(what));
        }
        Ok(())
    }
}

/// A JSON array column that is never null: `[]` when empty (Go's `json.Marshal(nonNil(...))`).
fn json_list_always<T: serde::Serialize>(xs: &[T]) -> String {
    json_list(xs)
}

impl Session {
    /// Writes a tag (`Tx.PutTag`): a tag of the same id is renamed.
    pub async fn put_tag(&self, t: &super::Tag) -> Result<(), GraphError> {
        let tail = format!(
            " ON CONFLICT (id) DO UPDATE SET name = {}.name",
            if self.sql.pg { "EXCLUDED" } else { "excluded" }
        );
        let q = self.insert(
            "tag",
            &["id", "name", "namespace", "node_id", "version", "by", "created_at"],
            &tail,
        );
        let args = vec![
            t.id.as_str().into(),
            t.name.as_str().into(),
            namespace_of(&t.namespace).into(),
            t.node.as_str().into(),
            Arg::Int(t.version.into()),
            t.by.as_str().into(),
            Arg::Time(t.created_at.unwrap_or_else(gojson::zero_time)),
        ];
        self.run((q, args), &format!("tag {}", t.name)).await?;
        Ok(())
    }

    /// Deletes a tag; none of that id is not found (`Tx.DeleteTag`).
    pub async fn delete_tag(&self, id: &str) -> Result<(), GraphError> {
        let what = format!("tag {id}");
        if self.sql.pg && uuid::Uuid::try_parse(id).is_err() {
            return Err(GraphError::invalid(format!("{what}: {}", super::not_uuid(id))));
        }
        let n = self
            .run(
                (
                    format!("DELETE FROM tag WHERE id = {}", self.sql.ph(1)),
                    vec![id.into()],
                ),
                &what,
            )
            .await?;
        if n != 1 {
            return Err(GraphError::not_found(what));
        }
        Ok(())
    }
}
