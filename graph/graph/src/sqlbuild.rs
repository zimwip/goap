//! Port of `sqlbuild.go`: the SQL text and the arguments of the queries both SQL repositories run (ADR 0074).
//!
//! The repositories keep their own executors and their value encodings (uuid / text, jsonb / text, arrays,
//! timestamps); what they share is the statement, written once here with the few words that differ between the
//! dialects named on the dialect value: the placeholder, the text cast of a uuid column, the test for a nullable
//! column, the true literal and the order tie-break (SQLite orders by rowid, PostgreSQL by a column). A query whose text
//! is not common (`latest_nodes`, the upserts) stays in its repository. The tests hold the text the repositories ran
//! before, per query and dialect.
//!
//! Go passes the arguments as `[]any`; here they are [`Arg`] values, which say how a value is bound: an [`Arg::Id`]
//! is a uuid column in PostgreSQL (bound as a `uuid`), text in SQLite.

use chrono::{DateTime, Utc};
use goap_domain::{
    BaselineID, ChangeID, LinkID, LogFilter, NodeID, NodeRef, TagFilter, TagID, branch_of,
    namespace_of,
};
use serde_json::Value;

/// A bound value of a statement.
#[derive(Debug, Clone, PartialEq)]
pub enum Arg {
    /// A text value.
    Text(String),
    /// An id: a uuid in PostgreSQL, text in SQLite.
    Id(String),
    /// A nullable id (Go's `nullUUID`): None (or empty) binds NULL.
    OptId(Option<String>),
    /// An integer.
    Int(i64),
    /// A nullable integer.
    OptInt(Option<i64>),
    /// A boolean (an integer in SQLite).
    Bool(bool),
    /// A JSON document: jsonb in PostgreSQL, its text in SQLite.
    Json(Value),
    /// A time: timestamptz in PostgreSQL, the fixed-width text of `sqlite::ts_text` in SQLite.
    Time(DateTime<Utc>),
    /// A list of integers: integer[] in PostgreSQL, its JSON text in SQLite.
    Ints(Vec<i64>),
}

impl Arg {
    /// An id argument.
    pub fn id(s: impl Into<String>) -> Arg {
        Arg::Id(s.into())
    }

    /// A text argument.
    pub fn text(s: impl Into<String>) -> Arg {
        Arg::Text(s.into())
    }

    /// A nullable id: NULL when empty (Go's `nullUUID`).
    pub fn null_id(s: &str) -> Arg {
        if s.is_empty() {
            Arg::OptId(None)
        } else {
            Arg::OptId(Some(s.to_string()))
        }
    }
}

/// Dialect names the differences between the SQL dialects of the repositories.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Dialect {
    pub pg: bool,
}

pub const DIALECT_PG: Dialect = Dialect { pg: true };
pub const DIALECT_SQLITE: Dialect = Dialect { pg: false };

/// A column of a select list; uuid marks the ones PostgreSQL must cast to text.
#[derive(Debug, Clone)]
struct SqlCol {
    name: String,
    uuid: bool,
}

fn sc(name: &str) -> SqlCol {
    SqlCol {
        name: name.to_string(),
        uuid: false,
    }
}

fn su(name: &str) -> SqlCol {
    SqlCol {
        name: name.to_string(),
        uuid: true,
    }
}

pub(crate) const NODE_FROM: &str = " FROM node n JOIN node_version v ON v.node_id = n.id";

/// onBranchSQL selects the versions v that are part of the branch bound to the placeholder p: written there, or
/// joined (ADR 0032).
pub(crate) fn on_branch_sql(p: &str) -> String {
    format!(
        "(v.branch = {p} OR EXISTS (SELECT 1 FROM node_branch j WHERE j.node_id = v.node_id AND j.version = v.version AND j.branch = {p}))"
    )
}

/// baselineEntriesSQL selects the entries in effect in a baseline (the first parameter), in both SQL dialects: the
/// chain of the baseline up to its checkpoint (d = distance from the baseline), and for each node the entry of the
/// nearest baseline of the chain that has one. The caller filters `rn = 1 AND NOT removed`.
pub(crate) fn baseline_entries_sql(param: &str) -> String {
    format!(
        "WITH RECURSIVE chain(id, parent_id, depth, d) AS (
		SELECT id, parent_id, depth, 0 FROM baseline WHERE id = {param}
		UNION ALL
		SELECT b.id, b.parent_id, b.depth, c.d + 1 FROM baseline b JOIN chain c ON b.id = c.parent_id WHERE c.depth > 0
	), eff AS (
		SELECT e.node_id, e.version, e.removed, row_number() OVER (PARTITION BY e.node_id ORDER BY c.d) AS rn
		FROM chain c JOIN baseline_entry e ON e.baseline_id = c.id
	)"
    )
}

/// logWhere is the WHERE clause of a log filter for the SQL repositories (the limit aside); ph gives the placeholder
/// of the n-th argument (1-based).
pub(crate) fn log_where(f: &LogFilter, ph: impl Fn(usize) -> String) -> (String, Vec<Arg>) {
    let mut conds: Vec<String> = Vec::new();
    let mut args: Vec<Arg> = Vec::new();
    let arg = |v: Arg, args: &mut Vec<Arg>| -> String {
        args.push(v);
        ph(args.len())
    };
    if !f.change.is_empty() {
        let p = arg(Arg::Id(f.change.to_string()), &mut args);
        conds.push(format!("change_id = {p}"));
    }
    if !f.types.is_empty() {
        let mut or = Vec::new();
        for t in &f.types {
            if t.ends_with('.') {
                let p = arg(Arg::Text(format!("{t}%")), &mut args);
                or.push(format!("type LIKE {p}"));
            } else {
                let p = arg(Arg::Text(t.clone()), &mut args);
                or.push(format!("type = {p}"));
            }
        }
        conds.push(format!("({})", or.join(" OR ")));
    }
    let in_ = |col: &str, vals: &[String], args: &mut Vec<Arg>, conds: &mut Vec<String>| {
        let ps: Vec<String> = vals
            .iter()
            .map(|v| arg(Arg::Text(v.clone()), args))
            .collect();
        conds.push(format!("{col} IN ({})", ps.join(", ")));
    };
    if let Some(flows) = &f.flows {
        if flows.is_empty() {
            conds.push("1 = 0".to_string());
        } else {
            in_("flow", flows, &mut args, &mut conds);
        }
    }
    if !f.processes.is_empty() {
        in_("process_id", &f.processes, &mut args, &mut conds);
    }
    if !f.execution.is_empty() {
        let p = arg(Arg::Text(f.execution.clone()), &mut args);
        conds.push(format!("execution = {p}"));
    }
    if f.after_seq > 0 {
        let p = arg(Arg::Int(f.after_seq), &mut args);
        conds.push(format!("seq > {p}"));
    }
    if conds.is_empty() {
        return (String::new(), Vec::new());
    }
    (format!(" WHERE {}", conds.join(" AND ")), args)
}

impl Dialect {
    /// ph is the placeholder of the n-th argument (1-based): `$n` for PostgreSQL, `?n` for SQLite. Both bind by
    /// number, so a value used twice is one argument.
    pub fn ph(self, n: usize) -> String {
        if self.pg {
            format!("${n}")
        } else {
            format!("?{n}")
        }
    }

    /// id is a uuid column read as text (PostgreSQL stores uuids, SQLite stores text).
    pub(crate) fn id(self, col: &str) -> String {
        if self.pg {
            format!("{col}::text")
        } else {
            col.to_string()
        }
    }

    /// idOr is a nullable uuid column read as text, empty when null; fun is the spelling of the function
    /// (coalesce / COALESCE).
    pub(crate) fn id_or(self, fun: &str, col: &str) -> String {
        format!("{fun}({}, '')", self.id(col))
    }

    /// orderBy orders by cols, then by the tie-break: pg_tie in PostgreSQL (e.g. ", id"), the insertion order in
    /// SQLite.
    pub(crate) fn order_by(self, cols: &str, pg_tie: &str) -> String {
        if self.pg {
            format!(" ORDER BY {cols}{pg_tie}")
        } else {
            format!(" ORDER BY {cols}, rowid")
        }
    }

    /// where1 starts a WHERE clause filters are appended to with AND.
    pub(crate) fn where1(self) -> &'static str {
        if self.pg {
            " WHERE true"
        } else {
            " WHERE 1 = 1"
        }
    }

    /// differs tests that a nullable uuid column is not the value p: IS DISTINCT FROM in PostgreSQL, the explicit null
    /// test in SQLite.
    pub(crate) fn differs(self, col: &str, p: &str) -> String {
        if self.pg {
            format!("{col} IS DISTINCT FROM {p}::uuid")
        } else {
            format!("({col} IS NULL OR {col} != {p})")
        }
    }

    /// cols joins the columns of a select list, each read through id when it is a uuid.
    fn cols(self, cs: &[SqlCol]) -> String {
        cs.iter()
            .map(|c| {
                if c.uuid {
                    self.id(&c.name)
                } else {
                    c.name.clone()
                }
            })
            .collect::<Vec<_>>()
            .join(", ")
    }

    /// nodeCols is the select list of a node version (the order the repositories scan).
    pub(crate) fn node_cols(self) -> String {
        self.cols(&[
            su("n.id"),
            sc("v.version"),
            sc("n.namespace"),
            sc("n.key"),
            sc("n.type"),
            sc("v.props"),
            sc("v.deleted"),
            su("v.change_id"),
            sc("v.created_at"),
            sc("v.branch"),
            sc("v.parents"),
            sc("v.reason"),
            sc("v.state"),
            su("v.change_impact"),
            sc("v.comment"),
            sc("v.execution"),
            su("v.owner_id"),
            su("n.project_id"),
            sc("v.origins"),
        ])
    }

    /// sqlNode reads one version of a node.
    pub(crate) fn sql_node(self, r: &NodeRef) -> (String, Vec<Arg>) {
        (
            format!(
                "SELECT {}{NODE_FROM} WHERE n.id = {} AND v.version = {}",
                self.node_cols(),
                self.ph(1),
                self.ph(2)
            ),
            vec![Arg::Id(r.id.to_string()), Arg::Int(r.version)],
        )
    }

    /// sqlLatestOn reads the latest version of a node on a branch (the branch as branch_of names it).
    pub(crate) fn sql_latest_on(self, id: &NodeID, branch: &str) -> (String, Vec<Arg>) {
        (
            format!(
                "SELECT {}{NODE_FROM} WHERE n.id = {} AND {} ORDER BY v.version DESC LIMIT 1",
                self.node_cols(),
                self.ph(1),
                on_branch_sql(&self.ph(2))
            ),
            vec![
                Arg::Id(id.to_string()),
                Arg::Text(branch_of(branch).to_string()),
            ],
        )
    }

    /// sqlVersions reads every version of a node, oldest first.
    pub(crate) fn sql_versions(self, id: &NodeID) -> (String, Vec<Arg>) {
        (
            format!(
                "SELECT {}{NODE_FROM} WHERE n.id = {} ORDER BY v.version",
                self.node_cols(),
                self.ph(1)
            ),
            vec![Arg::Id(id.to_string())],
        )
    }

    /// sqlDerivedNodes reads the versions naming a node among their origins (ADR 0077); r.version 0 matches any
    /// version.
    pub(crate) fn sql_derived_nodes(self, r: &NodeRef) -> (String, Vec<Arg>) {
        if self.pg {
            let mut o = serde_json::Map::new();
            o.insert("id".into(), Value::from(r.id.to_string()));
            if r.version != 0 {
                o.insert("version".into(), Value::from(r.version));
            }
            let b = Value::Array(vec![Value::Object(o)]).to_string();
            return (
                format!(
                    "SELECT {}{NODE_FROM} WHERE v.origins @> {}::jsonb ORDER BY n.key, v.version",
                    self.node_cols(),
                    self.ph(1)
                ),
                vec![Arg::Text(b)],
            );
        }
        (
            format!(
                "SELECT {}{NODE_FROM} WHERE EXISTS (SELECT 1 FROM json_each(v.origins) o WHERE json_extract(o.value, '$.id') = {} AND ({} = 0 OR json_extract(o.value, '$.version') = {})) ORDER BY n.key, v.version",
                self.node_cols(),
                self.ph(1),
                self.ph(2),
                self.ph(2)
            ),
            vec![Arg::Id(r.id.to_string()), Arg::Int(r.version)],
        )
    }

    /// sqlVersionBranches reads the branches the versions of a node joined.
    pub(crate) fn sql_version_branches(self, id: &NodeID) -> (String, Vec<Arg>) {
        (
            format!(
                "SELECT version, branch FROM node_branch WHERE node_id = {} ORDER BY branch",
                self.ph(1)
            ),
            vec![Arg::Id(id.to_string())],
        )
    }

    /// sqlNodeIDByKey reads the id of a node by its key.
    pub(crate) fn sql_node_id_by_key(self, namespace: &str, key: &str) -> (String, Vec<Arg>) {
        (
            format!(
                "SELECT {} FROM node WHERE namespace = {} AND key = {}",
                self.id("id"),
                self.ph(1),
                self.ph(2)
            ),
            vec![
                Arg::Text(namespace_of(namespace).to_string()),
                Arg::Text(key.to_string()),
            ],
        )
    }

    /// sqlNodesIn reads the nodes of a baseline, of one type when node_type is not empty.
    pub(crate) fn sql_nodes_in(self, baseline: &BaselineID, node_type: &str) -> (String, Vec<Arg>) {
        (
            format!(
                "{} SELECT {} FROM eff e JOIN node n ON n.id = e.node_id
		      JOIN node_version v ON v.node_id = e.node_id AND v.version = e.version
		      WHERE e.rn = 1 AND NOT e.removed AND ({} = '' OR n.type = {}) ORDER BY n.key",
                baseline_entries_sql(&self.ph(1)),
                self.node_cols(),
                self.ph(2),
                self.ph(2)
            ),
            vec![
                Arg::Id(baseline.to_string()),
                Arg::Text(node_type.to_string()),
            ],
        )
    }

    /// sqlBaselineEntries reads the entries in effect in a baseline.
    pub(crate) fn sql_baseline_entries(self, id: &BaselineID) -> (String, Vec<Arg>) {
        (
            format!(
                "{} SELECT {}, version FROM eff WHERE rn = 1 AND NOT removed",
                baseline_entries_sql(&self.ph(1)),
                self.id("node_id")
            ),
            vec![Arg::Id(id.to_string())],
        )
    }

    /// sqlLinks reads the links leaving (out) or entering the version r.
    pub(crate) fn sql_links(self, out: bool, r: &NodeRef) -> (String, Vec<Arg>) {
        let end = if out { "from" } else { "to" };
        (
            format!(
                "SELECT {} FROM link WHERE {end}_id = {} AND {end}_version = {} ORDER BY id",
                self.link_cols(),
                self.ph(1),
                self.ph(2)
            ),
            vec![Arg::Id(r.id.to_string()), Arg::Int(r.version)],
        )
    }

    /// linkCols is the select list of a link.
    pub(crate) fn link_cols(self) -> String {
        self.cols(&[
            su("id"),
            sc("type"),
            su("from_id"),
            sc("from_version"),
            su("to_id"),
            sc("to_version"),
            sc("props"),
            su("change_id"),
        ])
    }

    /// branchCols is the select list of a branch.
    pub(crate) fn branch_cols(self) -> String {
        format!(
            "namespace, name, parent, {}, {}, origin, status, created_at, description, intent, {}",
            self.id_or("coalesce", "fork_baseline"),
            self.id_or("coalesce", "head_baseline"),
            self.id_or("coalesce", "rebased_from")
        )
    }

    /// sqlBranch reads a branch.
    pub(crate) fn sql_branch(self, namespace: &str, name: &str) -> (String, Vec<Arg>) {
        (
            format!(
                "SELECT {} FROM branch WHERE namespace = {} AND name = {}",
                self.branch_cols(),
                self.ph(1),
                self.ph(2)
            ),
            vec![
                Arg::Text(namespace_of(namespace).to_string()),
                Arg::Text(name.to_string()),
            ],
        )
    }

    /// sqlBranches reads the branches of a namespace, oldest first.
    pub(crate) fn sql_branches(self, namespace: &str) -> (String, Vec<Arg>) {
        (
            format!(
                "SELECT {} FROM branch WHERE namespace = {}{}",
                self.branch_cols(),
                self.ph(1),
                self.order_by("created_at", "")
            ),
            vec![Arg::Text(namespace_of(namespace).to_string())],
        )
    }

    /// sqlBranchJoins reads the node versions a change made join a branch.
    pub(crate) fn sql_branch_joins(
        self,
        namespace: &str,
        branch: &str,
        change: &ChangeID,
    ) -> (String, Vec<Arg>) {
        (
            format!(
                "SELECT {}, j.version FROM node_branch j JOIN node n ON n.id = j.node_id WHERE n.namespace = {} AND j.branch = {} AND j.change_id = {}",
                self.id("j.node_id"),
                self.ph(1),
                self.ph(2),
                self.ph(3)
            ),
            vec![
                Arg::Text(namespace_of(namespace).to_string()),
                Arg::Text(branch_of(branch).to_string()),
                Arg::Id(change.to_string()),
            ],
        )
    }

    /// sqlBaselineHeaders reads the header of every materialised baseline.
    pub(crate) fn sql_baseline_headers(self) -> String {
        format!(
            "SELECT {}, {}, depth FROM baseline WHERE gap = 0{}",
            self.id("id"),
            self.id_or("coalesce", "parent_id"),
            self.order_by("created_at", ", id")
        )
    }

    /// sqlBaselineIDs reads the ids of the baselines of a namespace, oldest first.
    pub(crate) fn sql_baseline_ids(self, namespace: &str) -> (String, Vec<Arg>) {
        (
            format!(
                "SELECT {} FROM baseline WHERE namespace = {}{}",
                self.id("id"),
                self.ph(1),
                self.order_by("created_at", "")
            ),
            vec![Arg::Text(namespace_of(namespace).to_string())],
        )
    }

    /// sqlChangeIDs reads the ids of every change, oldest first.
    pub(crate) fn sql_change_ids(self) -> String {
        format!(
            "SELECT {} FROM change{}",
            self.id("id"),
            self.order_by("created_at", "")
        )
    }

    /// sqlOpenChangeIDs reads the ids of the changes neither applied nor abandoned, oldest first.
    pub(crate) fn sql_open_change_ids(self) -> String {
        format!(
            "SELECT {} FROM change WHERE status NOT IN ('applied', 'abandoned'){}",
            self.id("id"),
            self.order_by("created_at", "")
        )
    }

    /// sqlNodeChangeImpacts reads the changes that have an impact on a node.
    pub(crate) fn sql_node_change_impacts(self, node: &NodeID) -> (String, Vec<Arg>) {
        (
            format!(
                "SELECT {} FROM change_impact WHERE node_id = {} ORDER BY seq",
                self.id("change_id"),
                self.ph(1)
            ),
            vec![Arg::Id(node.to_string())],
        )
    }

    /// changeImpactCols is the select list of a change impact (the order both repositories scan).
    pub(crate) fn change_impact_cols(self) -> String {
        let via = SqlCol {
            name: self.id_or("COALESCE", "via"),
            uuid: false,
        };
        self.cols(&[
            su("id"),
            su("node_id"),
            sc("key"),
            sc("type"),
            sc("intent"),
            sc("rationale"),
            sc("pre_version"),
            sc("post_version"),
            sc("landed_version"),
            sc("review"),
            sc("reviews"),
            via,
            sc("recheck"),
            sc("produced_by"),
            sc("derived_from"),
            sc("items"),
            sc("execution"),
            sc("created_at"),
            sc("flow"),
            sc("superseded"),
        ])
    }

    /// sqlChangeImpacts reads the impacts of a change in the order they were written.
    pub(crate) fn sql_change_impacts(self, change: &ChangeID) -> (String, Vec<Arg>) {
        (
            format!(
                "SELECT {} FROM change_impact WHERE change_id = {} ORDER BY seq",
                self.change_impact_cols(),
                self.ph(1)
            ),
            vec![Arg::Id(change.to_string())],
        )
    }

    /// logCols is the select list of a log entry.
    pub(crate) fn log_cols(self) -> String {
        self.cols(&[
            sc("seq"),
            sc("id"),
            su("change_id"),
            sc("type"),
            sc("flow"),
            sc("process_id"),
            sc("execution"),
            sc("subject"),
            sc("by_whom"),
            sc("causation"),
            sc("at"),
            sc("payload"),
        ])
    }

    /// sqlLog reads the entries of the log matching f, in order.
    pub(crate) fn sql_log(self, f: &LogFilter) -> (String, Vec<Arg>) {
        let (w, args) = log_where(f, |n| self.ph(n));
        let mut q = format!("SELECT {} FROM change_log{w} ORDER BY seq", self.log_cols());
        if f.limit > 0 {
            q.push_str(&format!(" LIMIT {}", f.limit));
        }
        (q, args)
    }

    /// sqlLogCounts counts the entries of the log matching f by type (f.after_seq does not apply).
    pub(crate) fn sql_log_counts(self, f: &LogFilter) -> (String, Vec<Arg>) {
        let mut f = f.clone();
        f.after_seq = 0;
        let (w, args) = log_where(&f, |n| self.ph(n));
        (
            format!("SELECT type, count(*) FROM change_log{w} GROUP BY type"),
            args,
        )
    }

    /// sqlTags reads the tags matching f, oldest first.
    pub(crate) fn sql_tags(self, f: &TagFilter) -> (String, Vec<Arg>) {
        let baseline = SqlCol {
            name: self.id_or("COALESCE", "baseline_id"),
            uuid: false,
        };
        let mut q = format!(
            "SELECT {} FROM tag{}",
            self.cols(&[
                su("id"),
                sc("name"),
                sc("namespace"),
                su("change_id"),
                baseline,
                sc("by"),
                sc("created_at"),
            ]),
            self.where1()
        );
        let mut args = Vec::new();
        let mut add = |cond: &str, v: Arg, q: &mut String| {
            args.push(v);
            q.push_str(&format!(" AND {cond} = {}", self.ph(args.len())));
        };
        if !f.namespace.is_empty() {
            add(
                "namespace",
                Arg::Text(namespace_of(&f.namespace).to_string()),
                &mut q,
            );
        }
        if !f.name.is_empty() {
            add("name", Arg::Text(f.name.clone()), &mut q);
        }
        if !f.change.is_empty() {
            add("change_id", Arg::Id(f.change.to_string()), &mut q);
        }
        q.push_str(&self.order_by("created_at", ", id"));
        (q, args)
    }

    /// sqlInsert is an INSERT of one row of the columns, bound to the arguments in order; tail is appended (a conflict
    /// or RETURNING clause).
    pub(crate) fn sql_insert(self, table: &str, columns: &[&str], tail: &str) -> String {
        let ps: Vec<String> = (1..=columns.len()).map(|i| self.ph(i)).collect();
        format!(
            "INSERT INTO {table} ({}) VALUES ({}){tail}",
            columns.join(", "),
            ps.join(", ")
        )
    }

    /// sqlDeleteTag deletes a tag.
    pub(crate) fn sql_delete_tag(self, id: &TagID) -> (String, Vec<Arg>) {
        (
            format!("DELETE FROM tag WHERE id = {}", self.ph(1)),
            vec![Arg::Id(id.to_string())],
        )
    }

    /// sqlDeleteChangeImpact removes a change impact from the projection: arguments change, change impact.
    pub(crate) fn sql_delete_change_impact(self) -> String {
        format!(
            "DELETE FROM change_impact WHERE change_id = {} AND id = {}",
            self.ph(1),
            self.ph(2)
        )
    }

    /// sqlLinkByID reads one link.
    pub(crate) fn sql_link_by_id(self, id: &LinkID) -> (String, Vec<Arg>) {
        (
            format!(
                "SELECT {} FROM link WHERE id = {}",
                self.link_cols(),
                self.ph(1)
            ),
            vec![Arg::Id(id.to_string())],
        )
    }

    /// sqlSetNodeOrigin sets the origin of a version: arguments node, version, change, change impact, comment.
    pub(crate) fn sql_set_node_origin(self) -> String {
        format!(
            "UPDATE node_version SET change_id = {}, change_impact = {}, comment = {} WHERE node_id = {} AND version = {}",
            self.ph(3),
            self.ph(4),
            self.ph(5),
            self.ph(1),
            self.ph(2)
        )
    }

    /// sqlJoinBranch makes a version join a branch: arguments node, version, branch, change.
    pub(crate) fn sql_join_branch(self) -> String {
        format!(
            "INSERT INTO node_branch (node_id, version, branch, change_id) SELECT node_id, version, {}, {} FROM node_version
		WHERE node_id = {} AND version = {} AND branch <> {} ON CONFLICT DO NOTHING",
            self.ph(3),
            self.ph(4),
            self.ph(1),
            self.ph(2),
            self.ph(3)
        )
    }

    /// deleteChangeUsed are the counts that must be zero for a change to be deleted: what it wrote is used by nothing
    /// it did not write. The one argument is the change.
    pub(crate) fn delete_change_used(self) -> Vec<String> {
        let p = self.ph(1);
        vec![
            format!(
                "SELECT count(*) FROM baseline_entry e JOIN node_version v ON e.node_id = v.node_id AND e.version = v.version WHERE v.change_id = {p}"
            ),
            format!("SELECT count(*) FROM baseline WHERE change_id = {p}"),
            format!(
                "SELECT count(*) FROM link l JOIN node_version v ON l.to_id = v.node_id AND l.to_version = v.version WHERE v.change_id = {p} AND {}",
                self.differs("l.change_id", &p)
            ),
            format!(
                "SELECT count(*) FROM node_version o JOIN node_version v ON o.node_id = v.node_id AND o.version > v.version WHERE v.change_id = {p} AND {}",
                self.differs("o.change_id", &p)
            ),
        ]
    }

    /// sqlChangeNodes reads the nodes a change wrote versions of (one argument: the change).
    pub(crate) fn sql_change_nodes(self) -> String {
        format!(
            "SELECT DISTINCT {} FROM node_version WHERE change_id = {}",
            self.id("node_id"),
            self.ph(1)
        )
    }

    /// deleteChangeRows are the deletions of what a change wrote (one argument: the change).
    pub(crate) fn delete_change_rows(self) -> Vec<String> {
        let p = self.ph(1);
        let mine = format!("(SELECT node_id, version FROM node_version WHERE change_id = {p})");
        vec![
            format!("DELETE FROM link WHERE change_id = {p} OR (from_id, from_version) IN {mine}"),
            format!(
                "DELETE FROM node_branch WHERE change_id = {p} OR (node_id, version) IN {mine}"
            ),
            format!("DELETE FROM node_version WHERE change_id = {p}"),
            format!("DELETE FROM change_impact WHERE change_id = {p}"),
            format!("DELETE FROM change_log WHERE change_id = {p}"),
            format!("DELETE FROM tag WHERE change_id = {p}"),
        ]
    }

    /// sqlDeleteOrphanNode deletes a node left with no version (one argument: the node).
    pub(crate) fn sql_delete_orphan_node(self) -> String {
        format!(
            "DELETE FROM node WHERE id = {} AND NOT EXISTS (SELECT 1 FROM node_version WHERE node_id = {})",
            self.ph(1),
            self.ph(1)
        )
    }

    /// sqlRefreshLatest sets the latest version of a node to the last it has (one argument: the node).
    pub(crate) fn sql_refresh_latest(self) -> String {
        format!(
            "UPDATE node SET latest = (SELECT max(version) FROM node_version WHERE node_id = {}) WHERE id = {}",
            self.ph(1),
            self.ph(1)
        )
    }

    /// sqlDeleteBranch deletes a branch (arguments namespace, name).
    pub(crate) fn sql_delete_branch(self) -> String {
        format!(
            "DELETE FROM branch WHERE namespace = {} AND name = {}",
            self.ph(1),
            self.ph(2)
        )
    }

    /// sqlDeleteChange deletes a change (one argument).
    pub(crate) fn sql_delete_change(self) -> String {
        format!("DELETE FROM change WHERE id = {}", self.ph(1))
    }
}

/// The columns of the inserts both repositories run, in the order of their arguments.
pub(crate) const NODE_VERSION_COLUMNS: &[&str] = &[
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
    "owner_id",
    "origins",
];
pub(crate) const LINK_COLUMNS: &[&str] = &[
    "id",
    "type",
    "from_id",
    "from_version",
    "to_id",
    "to_version",
    "props",
    "change_id",
];
pub(crate) const BASELINE_COLUMNS: &[&str] = &[
    "id",
    "name",
    "parent_id",
    "merged_from",
    "change_id",
    "created_at",
    "branch",
    "namespace",
    "depth",
    "gap",
    "kind",
    "rebased_from",
];
pub(crate) const CHANGE_LOG_COLUMNS: &[&str] = &[
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
