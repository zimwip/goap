//! The statements of the reads, written once for both dialects (as `graph/graph/sqlbuild.go` writes them for the Go
//! service): the placeholder, the text cast of a uuid, lists (arrays on PostgreSQL, JSON texts on SQLite) and the
//! order tie-break are the words that differ.
use graph_core::{
    change::ChangesFilter,
    model::{BaselineId, ChangeId, LinkId, NodeId, NodeQuery, NodeRef, branch_of, namespace_of},
};

use super::value::Arg;

/// The dialect of the statements.
#[derive(Debug, Clone, Copy)]
pub struct Sql {
    pub pg: bool,
}

/// A statement and its arguments.
pub type Stmt = (String, Vec<Arg>);

const NODE_FROM: &str = " FROM node n JOIN node_version v ON v.node_id = n.id";

impl Sql {
    /// The placeholder of the n-th argument (1-based).
    pub(crate) fn ph(self, n: usize) -> String {
        if self.pg { format!("${n}") } else { format!("?{n}") }
    }

    /// A uuid column read as text.
    pub(crate) fn id(self, col: &str) -> String {
        if self.pg {
            format!("{col}::text")
        } else {
            col.to_string()
        }
    }

    /// A nullable uuid column read as text, empty when null.
    pub(crate) fn id_or(self, col: &str) -> String {
        format!("coalesce({}, '')", self.id(col))
    }

    /// The select list of a node version.
    fn node_cols(self) -> String {
        [
            self.id("n.id"),
            "v.version".into(),
            "n.namespace".into(),
            "n.key".into(),
            "n.type".into(),
            "v.props".into(),
            "v.deleted".into(),
            self.id("v.change_id"),
            "v.created_at".into(),
            "v.branch".into(),
            "v.parents".into(),
            "v.reason".into(),
            "v.state".into(),
            self.id("v.change_impact"),
            "v.comment".into(),
            "v.execution".into(),
            "v.owner".into(),
            "n.project".into(),
            "v.origins".into(),
        ]
        .join(", ")
    }

    /// The select list of a link read as the alias `l`.
    fn link_cols(self) -> String {
        format!(
            "{}, l.type, {}, l.from_version, coalesce({}, ''), coalesce(l.to_version, 0), l.props, {}, l.to_url",
            self.id("l.id"),
            self.id("l.from_id"),
            self.id("l.to_id"),
            self.id("l.change_id")
        )
    }

    /// The arguments of a list of versions, and the number of the next placeholder after them.
    fn refs_args(self, refs: &[NodeRef]) -> (Vec<Arg>, usize) {
        if self.pg {
            let ids = refs.iter().map(|r| r.id.0.clone()).collect();
            let vs = refs.iter().map(|r| r.version).collect();
            return (vec![Arg::Texts(ids), Arg::Ints(vs)], 3);
        }
        let pairs: Vec<serde_json::Value> = refs.iter().map(|r| serde_json::json!([r.id.0, r.version])).collect();
        (vec![Arg::Text(serde_json::Value::Array(pairs).to_string())], 2)
    }

    /// The rows (id, version) of the versions at placeholder n.
    fn refs_rows(self, n: usize) -> String {
        if self.pg {
            format!(
                "SELECT * FROM unnest({}::uuid[], {}::int[])",
                self.ph(n),
                self.ph(n + 1)
            )
        } else {
            format!(
                "SELECT json_extract(value, '$[0]'), json_extract(value, '$[1]') FROM json_each({})",
                self.ph(n)
            )
        }
    }

    /// Joins the versions at placeholder n as the alias `a` on the columns `id_col` / `ver_col`.
    fn refs_join(self, a: &str, id_col: &str, ver_col: &str, n: usize) -> String {
        if self.pg {
            format!(
                " JOIN unnest({}::uuid[], {}::int[]) AS {a}(id, version) ON {a}.id = {id_col} AND {a}.version = {ver_col}",
                self.ph(n),
                self.ph(n + 1)
            )
        } else {
            format!(
                " JOIN json_each({}) {a} ON json_extract({a}.value, '$[0]') = {id_col} AND json_extract({a}.value, '$[1]') = {ver_col}",
                self.ph(n)
            )
        }
    }

    /// `col` in the list of texts at placeholder n (uuids when `uuid`), and its argument.
    fn in_list(self, col: &str, n: usize, list: Vec<String>, uuid: bool) -> (String, Arg) {
        if self.pg {
            let cast = if uuid { "uuid" } else { "text" };
            return (format!("{col} = ANY({}::{cast}[])", self.ph(n)), Arg::Texts(list));
        }
        (
            format!("{col} IN (SELECT value FROM json_each({}))", self.ph(n)),
            Arg::Texts(list),
        )
    }

    /// The join of the rows of the left read first: SQLite would otherwise scan the links and probe the rows.
    fn outer_join(self) -> &'static str {
        if self.pg { " JOIN " } else { " CROSS JOIN " }
    }

    pub fn node(self, r: &NodeRef) -> Stmt {
        (
            format!(
                "SELECT {}{NODE_FROM} WHERE n.id = {} AND v.version = {}",
                self.node_cols(),
                self.ph(1),
                self.ph(2)
            ),
            vec![r.id.0.clone().into(), r.version.into()],
        )
    }

    /// The number of the latest version of a node on a branch: the greatest of the last written there and the last
    /// joined to it, each one step of an index.
    fn latest_on_expr(self, node: &str, branch: &str) -> String {
        let last = |table: &str| {
            format!(
                "coalesce((SELECT version FROM {table} WHERE node_id = {node} AND branch = {branch} ORDER BY version DESC LIMIT 1), 0)"
            )
        };
        let greatest = if self.pg { "greatest" } else { "max" };
        format!("{greatest}({}, {})", last("node_version"), last("node_branch"))
    }

    pub fn latest_on(self, id: &NodeId, branch: &str) -> Stmt {
        (
            format!(
                "SELECT {}{NODE_FROM} WHERE n.id = {} AND v.version = {}",
                self.node_cols(),
                self.ph(1),
                self.latest_on_expr(&self.ph(1), &self.ph(2))
            ),
            vec![id.0.clone().into(), branch_of(branch).into()],
        )
    }

    pub fn node_id_by_key(self, namespace: &str, key: &str) -> Stmt {
        (
            format!(
                "SELECT {} FROM node WHERE namespace = {} AND key = {}",
                self.id("id"),
                self.ph(1),
                self.ph(2)
            ),
            vec![namespace_of(namespace).into(), key.into()],
        )
    }

    pub fn nodes_at(self, refs: &[NodeRef], typ: &str) -> Stmt {
        let (mut args, n) = self.refs_args(refs);
        args.push(typ.into());
        (
            format!(
                "SELECT {}{NODE_FROM}{} WHERE ({t} = '' OR n.type = {t}) ORDER BY n.key",
                self.node_cols(),
                self.refs_join("r", "v.node_id", "v.version", 1),
                t = self.ph(n)
            ),
            args,
        )
    }

    /// The last commit of each node on a branch in the orders (after, up_to], with whether it deletes the node.
    pub fn committed_versions(
        self,
        namespace: &str,
        branch: &str,
        after: i64,
        up_to: i64,
        ids: Option<&[NodeId]>,
    ) -> Stmt {
        let mut args: Vec<Arg> = vec![
            branch_of(branch).into(),
            after.into(),
            up_to.into(),
            namespace_of(namespace).into(),
        ];
        let list = |ids: &[NodeId]| ids.iter().map(|i| i.0.clone()).collect::<Vec<_>>();
        if let (Some(ids), true) = (ids, self.pg) {
            // per node one lookup of the indexes node_version_fold / node_branch_fold, whatever the length of its
            // history; what the version found holds is read by its key once found
            args.push(Arg::Texts(list(ids)));
            return (
                "SELECT q.id::text, x.version,
                    (SELECT w.deleted FROM node_version w WHERE w.node_id = q.id AND w.version = x.version)
                FROM unnest($5::uuid[]) AS q(id) JOIN node n ON n.id = q.id AND n.namespace = $4
                CROSS JOIN LATERAL (
                    SELECT u.version FROM (
                        (SELECT v.version, v.committed FROM node_version v
                        WHERE v.node_id = q.id AND v.branch = $1 AND v.committed > $2 AND v.committed <= $3 ORDER BY v.committed DESC, v.version DESC LIMIT 1)
                        UNION ALL
                        (SELECT j.version, j.committed FROM node_branch j
                        WHERE j.node_id = q.id AND j.branch = $1 AND j.committed > $2 AND j.committed <= $3 ORDER BY j.committed DESC, j.version DESC LIMIT 1)
                    ) u ORDER BY u.committed DESC, u.version DESC LIMIT 1
                ) x"
                    .into(),
                args,
            );
        }
        let (mut in_v, mut in_j) = (String::new(), String::new());
        if let Some(ids) = ids {
            let (cond, arg) = self.in_list("v.node_id", 5, list(ids), true);
            in_j = format!(" AND {}", cond.replacen("v.node_id", "j.node_id", 1));
            in_v = format!(" AND {cond}");
            args.push(arg);
        }
        (
            format!(
                "SELECT {}, version, deleted FROM (
                    SELECT x.node_id, x.version, x.deleted, row_number() OVER (PARTITION BY x.node_id ORDER BY x.committed DESC, x.version DESC) AS rn
                    FROM (
                        SELECT v.node_id, v.version, v.deleted, v.committed FROM node_version v
                        WHERE v.branch = {p1} AND v.committed > {p2} AND v.committed <= {p3}{in_v}
                        UNION ALL
                        SELECT j.node_id, j.version, v.deleted, j.committed FROM node_branch j JOIN node_version v ON v.node_id = j.node_id AND v.version = j.version
                        WHERE j.branch = {p1} AND j.committed > {p2} AND j.committed <= {p3}{in_j}
                    ) x JOIN node n ON n.id = x.node_id WHERE n.namespace = {p4}
                ) y WHERE rn = 1",
                self.id("node_id"),
                p1 = self.ph(1),
                p2 = self.ph(2),
                p3 = self.ph(3),
                p4 = self.ph(4),
            ),
            args,
        )
    }

    pub fn namespace_node_ids(self, namespace: &str) -> Stmt {
        (
            format!(
                "SELECT {} FROM node WHERE namespace = {} ORDER BY id",
                self.id("id"),
                self.ph(1)
            ),
            vec![namespace_of(namespace).into()],
        )
    }

    /// The select list of a baseline header.
    fn header_cols(self) -> String {
        format!(
            "{}, name, {}, {}, {}, created_at, branch, namespace, kind, {}, seq",
            self.id("id"),
            self.id_or("parent_id"),
            self.id_or("merged_from"),
            self.id_or("change_id"),
            self.id_or("rebased_from")
        )
    }

    pub fn baseline_header(self, id: &str) -> Stmt {
        (
            format!("SELECT {} FROM baseline WHERE id = {}", self.header_cols(), self.ph(1)),
            vec![id.into()],
        )
    }

    /// The headers of the baselines of a namespace, oldest first.
    pub fn baseline_headers(self, namespace: &str) -> Stmt {
        let order = if self.pg { "created_at, id" } else { "created_at, rowid" };
        (
            format!(
                "SELECT {} FROM baseline WHERE namespace = {} ORDER BY {order}",
                self.header_cols(),
                self.ph(1)
            ),
            vec![namespace_of(namespace).into()],
        )
    }

    /// The select list of a branch.
    fn branch_cols(self) -> String {
        format!(
            "namespace, name, parent, {}, {}, origin, status, created_at, description, intent, {}, ephemeral, fork_seq",
            self.id_or("fork_baseline"),
            self.id_or("head_baseline"),
            self.id_or("rebased_from")
        )
    }

    pub fn branch(self, namespace: &str, name: &str) -> Stmt {
        (
            format!(
                "SELECT {} FROM branch WHERE namespace = {} AND name = {}",
                self.branch_cols(),
                self.ph(1),
                self.ph(2)
            ),
            vec![namespace_of(namespace).into(), name.into()],
        )
    }

    /// The branches of a namespace, oldest first.
    pub fn branches(self, namespace: &str) -> Stmt {
        (
            format!(
                "SELECT {} FROM branch WHERE namespace = {}{}",
                self.branch_cols(),
                self.ph(1),
                self.order_by("created_at")
            ),
            vec![namespace_of(namespace).into()],
        )
    }

    /// The headers of baselines of a namespace by id, in the order they committed.
    pub fn baseline_headers_of(self, namespace: &str, ids: &[BaselineId]) -> Stmt {
        let (cond, arg) = self.in_list("id", 2, ids.iter().map(|i| i.0.clone()).collect(), true);
        (
            format!(
                "SELECT {} FROM baseline WHERE namespace = {} AND {cond} ORDER BY seq",
                self.header_cols(),
                self.ph(1)
            ),
            vec![namespace_of(namespace).into(), arg],
        )
    }

    /// The number of baselines of each branch of a namespace.
    pub fn baseline_counts(self, namespace: &str) -> Stmt {
        (
            format!(
                "SELECT branch, count(*) FROM baseline WHERE namespace = {} GROUP BY branch",
                self.ph(1)
            ),
            vec![namespace_of(namespace).into()],
        )
    }

    /// The links the versions at placeholder n hold (ADR 0141), each read as leaving that version: the rows of its
    /// set and of the sets it is a delta over, minus those a row of the chain drops. `and` adds conditions on the rows.
    fn held_links(self, n: usize, and: &str) -> String {
        format!(
            "WITH RECURSIVE r(id, version) AS ({}),
            c(nid, rv, sv) AS (
                SELECT v.node_id, v.version, coalesce(v.links, v.version) FROM node_version v JOIN r ON r.id = v.node_id AND r.version = v.version
                UNION ALL
                SELECT c.nid, c.rv, s.links_base FROM c JOIN node_version s ON s.node_id = c.nid AND s.version = c.sv WHERE s.links_depth > 0
            ),
            h AS (SELECT l.id, l.type, l.from_id, c.rv, l.to_id, l.to_version, l.props, l.change_id, l.removes, l.to_url FROM c{}link l ON l.from_id = c.nid AND l.from_version = c.sv{and})
            SELECT h.id, h.type, h.from_id, h.rv AS from_version, h.to_id, h.to_version, h.props, h.change_id, h.to_url FROM h
            WHERE h.removes IS NULL AND (h.id, h.from_id, h.rv) NOT IN (SELECT x.removes, x.from_id, x.rv FROM h x WHERE x.removes IS NOT NULL)",
            self.refs_rows(n),
            self.outer_join()
        )
    }

    /// One link row by its id (`sqlLinkByID`).
    pub fn link_by_id(self, id: &LinkId) -> Stmt {
        (
            format!(
                "SELECT {} FROM link l WHERE l.id = {} AND l.removes IS NULL",
                self.link_cols(),
                self.ph(1)
            ),
            vec![id.0.clone().into()],
        )
    }

    pub fn links_from(self, refs: &[NodeRef], types: Option<&[String]>) -> Stmt {
        let (mut args, n) = self.refs_args(refs);
        let and = match types {
            None => String::new(),
            Some(ts) => {
                let (cond, arg) = self.in_list("l.type", n, ts.to_vec(), false);
                args.push(arg);
                format!(" AND {cond}")
            }
        };
        (
            format!(
                "SELECT {} FROM ({}) l ORDER BY l.id",
                self.link_cols(),
                self.held_links(1, &and)
            ),
            args,
        )
    }

    pub fn links_from_into(self, refs: &[NodeRef], to: &NodeId) -> Stmt {
        let (mut args, n) = self.refs_args(refs);
        args.push(to.0.clone().into());
        let and = format!(" AND l.to_id = {}", self.ph(n));
        (
            format!(
                "SELECT {} FROM ({}) l ORDER BY l.id",
                self.link_cols(),
                self.held_links(1, &and)
            ),
            args,
        )
    }

    /// How some versions hold their links (`sqlLinkSetsOf`).
    pub fn link_sets_of(self, refs: &[NodeRef]) -> Stmt {
        let (args, _) = self.refs_args(refs);
        (
            format!(
                "SELECT {}, v.version, coalesce(v.links, v.version), coalesce(v.links_base, 0), v.links_depth, v.links_seq FROM node_version v{}",
                self.id("v.node_id"),
                self.refs_join("r", "v.node_id", "v.version", 1)
            ),
            args,
        )
    }

    pub fn link_sources(self, id: &NodeId) -> Stmt {
        (
            format!(
                "SELECT DISTINCT {} FROM link WHERE to_id = {} AND removes IS NULL",
                self.id("from_id"),
                self.ph(1)
            ),
            vec![id.0.clone().into()],
        )
    }

    /// The links entering a version, one per version of its source holding it (ADR 0141): the rows naming the target,
    /// the sets holding each (the set of the row, then the deltas over a set holding it that do not drop it), the
    /// versions holding those sets.
    pub fn links_to(self, r: &NodeRef) -> Stmt {
        let mut target = format!("to_id = {}", self.ph(1));
        let mut args: Vec<Arg> = vec![r.id.0.clone().into()];
        if r.version != 0 {
            target.push_str(&format!(" AND to_version = {}", self.ph(2)));
            args.push(r.version.into());
        }
        let col = |c: &str, uuid: bool| if uuid { self.id(c) } else { c.to_string() };
        let cols = [
            col("t.id", true),
            col("t.type", false),
            col("t.from_id", true),
            col("v.version", false),
            format!("coalesce({}, '')", self.id("t.to_id")),
            "coalesce(t.to_version, 0)".into(),
            col("t.props", false),
            col("t.change_id", true),
            col("t.to_url", false),
        ]
        .join(", ");
        (
            format!(
                "WITH RECURSIVE t AS (SELECT id, type, from_id, from_version, to_id, to_version, props, change_id, to_url FROM link
                    WHERE {target} AND removes IS NULL),
                s(lid, nid, sv) AS (
                    SELECT id, from_id, from_version FROM t
                    UNION ALL
                    SELECT s.lid, v.node_id, v.version FROM s JOIN node_version v ON v.node_id = s.nid AND v.links_base = s.sv
                    WHERE NOT EXISTS (SELECT 1 FROM link x WHERE x.from_id = v.node_id AND x.from_version = v.version AND x.removes = s.lid)
                )
                SELECT {cols} FROM s
                JOIN t ON t.id = s.lid JOIN node_version v ON v.node_id = s.nid AND v.version = s.sv AND v.links IS NULL
                UNION ALL
                SELECT {cols} FROM s
                JOIN t ON t.id = s.lid JOIN node_version v ON v.links = s.sv AND v.node_id = s.nid
                ORDER BY 1, 4"
            ),
            args,
        )
    }

    /// The versions of a list matching the text and the deleted filter of a query (and the reach of its caller).
    fn node_refs_where(self, refs: &[NodeRef], q: &NodeQuery) -> (String, Vec<Arg>, usize) {
        let (mut args, mut n) = self.refs_args(refs);
        args.push(like_pattern(&q.text).into());
        args.push(q.include_deleted.into());
        let mut w = format!(
            "{NODE_FROM}{} WHERE {} AND ({} OR NOT v.deleted)",
            self.refs_join("r", "v.node_id", "v.version", 1),
            self.text_match(n, "v.props", &["n.key", "n.type"]),
            self.ph(n + 1)
        );
        n += 2;
        if let Some(ps) = &q.projects {
            let (cond, arg) = self.in_list("n.project", n, ps.clone(), false);
            w.push_str(&format!(" AND {cond}"));
            args.push(arg);
            n += 1;
        }
        (w, args, n)
    }

    /// Holds when the pattern at placeholder n is empty, or one of the columns or a string property matches it.
    fn text_match(self, n: usize, props: &str, cols: &[&str]) -> String {
        let p = self.ph(n);
        let mut out = format!("({p} = ''");
        for c in cols {
            out.push_str(&format!(" OR lower({c}) LIKE {p} ESCAPE '\\'"));
        }
        if self.pg {
            out.push_str(&format!(
                " OR EXISTS (SELECT 1 FROM jsonb_each({props}) p WHERE jsonb_typeof(p.value) = 'string' AND lower(p.value #>> '{{}}') LIKE {p} ESCAPE '\\'))"
            ));
        } else {
            out.push_str(&format!(
                " OR EXISTS (SELECT 1 FROM json_each({props}) p WHERE p.type = 'text' AND lower(p.value) LIKE {p} ESCAPE '\\'))"
            ));
        }
        out
    }

    /// A page of the versions of a list, by key.
    pub fn node_page(self, refs: &[NodeRef], q: &NodeQuery) -> Stmt {
        let (w, mut args, n) = self.node_refs_where(refs, q);
        args.push(q.typ.clone().into());
        args.push((q.limit as i64).into());
        args.push((q.offset as i64).into());
        let key = if self.pg { "n.key COLLATE \"C\"" } else { "n.key" };
        (
            format!(
                "SELECT {}{w} AND ({t} = '' OR n.type = {t}) ORDER BY {key} LIMIT {} OFFSET {}",
                self.node_cols(),
                self.ph(n + 1),
                self.ph(n + 2),
                t = self.ph(n)
            ),
            args,
        )
    }

    /// The counts by type of the versions of a list a query keeps.
    pub fn node_type_counts(self, refs: &[NodeRef], q: &NodeQuery) -> Stmt {
        let (w, args, _) = self.node_refs_where(refs, q);
        (format!("SELECT n.type, count(*){w} GROUP BY n.type"), args)
    }

    /// The last `limit` versions of a node below `before` (0: from its latest), newest first.
    pub fn version_range(self, id: &NodeId, before: i32, limit: usize) -> Stmt {
        let mut q = format!("SELECT {}{NODE_FROM} WHERE n.id = {}", self.node_cols(), self.ph(1));
        let mut args: Vec<Arg> = vec![id.0.clone().into()];
        if before > 0 {
            q.push_str(&format!(" AND v.version < {}", self.ph(2)));
            args.push(before.into());
        }
        q.push_str(&format!(" ORDER BY v.version DESC LIMIT {}", self.ph(args.len() + 1)));
        args.push((limit as i64).into());
        (q, args)
    }

    /// The branches the versions of a node joined, from a version up.
    pub fn version_branches(self, id: &NodeId, from: i32) -> Stmt {
        (
            format!(
                "SELECT version, branch FROM node_branch WHERE node_id = {} AND version >= {} ORDER BY branch",
                self.ph(1),
                self.ph(2)
            ),
            vec![id.0.clone().into(), from.into()],
        )
    }

    pub fn change_exists(self, change: &str) -> Stmt {
        (
            format!("SELECT 1 FROM change WHERE id = {}", self.ph(1)),
            vec![change.into()],
        )
    }

    /// The nodes the impacts of a change act on.
    pub fn change_nodes(self, change: &str) -> Stmt {
        (
            format!(
                "SELECT DISTINCT {} FROM change_impact WHERE change_id = {} AND node_id IS NOT NULL",
                self.id("node_id"),
                self.ph(1)
            ),
            vec![change.into()],
        )
    }

    /// The order of a listing, the rows of one time in the order they were written on SQLite.
    pub(crate) fn order_by(self, cols: &str) -> String {
        if self.pg {
            format!(" ORDER BY {cols}")
        } else {
            format!(" ORDER BY {cols}, rowid")
        }
    }

    /// The headers of the changes matching the parts of a filter a column answers, oldest first (indexes
    /// change_status, change_project, change_parent).
    pub fn change_heads(self, f: &ChangesFilter) -> Stmt {
        let cols = [
            self.id("id"),
            "title".into(),
            "intent".into(),
            "methodology".into(),
            "goal".into(),
            "status".into(),
            self.id_or("baseline_id"),
            self.id_or("result_baseline_id"),
            "data".into(),
            "created_at".into(),
            "branch".into(),
            "namespace".into(),
            self.id_or("parent_id"),
            "owner_org".into(),
            "project_id".into(),
            "lifecycle".into(),
            "state".into(),
            "guardians".into(),
            "depends_on".into(),
            "methodology_version".into(),
        ]
        .join(", ");
        let mut q = format!(
            "SELECT {cols} FROM change WHERE {}",
            if self.pg { "true" } else { "1 = 1" }
        );
        let mut args: Vec<Arg> = Vec::new();
        let add = |q: &mut String, args: &mut Vec<Arg>, cond: &str, v: Arg| {
            args.push(v);
            q.push_str(&format!(" AND {cond} = {}", self.ph(args.len())));
        };
        if !f.namespace.is_empty() {
            add(&mut q, &mut args, "namespace", namespace_of(&f.namespace).into());
        }
        if !f.owner_org.is_empty() {
            add(&mut q, &mut args, "owner_org", f.owner_org.as_str().into());
        }
        if !f.project.is_empty() {
            add(&mut q, &mut args, "project_id", f.project.as_str().into());
        }
        if !f.status.is_empty() {
            let mut ps = Vec::new();
            for st in &f.status {
                args.push(st.as_str().into());
                ps.push(self.ph(args.len()));
            }
            q.push_str(&format!(" AND status IN ({})", ps.join(", ")));
        }
        if !f.parent.is_empty() {
            add(&mut q, &mut args, "parent_id", f.parent.as_str().into());
        }
        if !f.depends_on.is_empty() {
            args.push(f.depends_on.as_str().into());
            if self.pg {
                q.push_str(&format!(" AND depends_on ? {}", self.ph(args.len())));
            } else {
                q.push_str(&format!(
                    " AND EXISTS (SELECT 1 FROM json_each(change.depends_on) WHERE value = {})",
                    self.ph(args.len())
                ));
            }
        }
        if !f.ids.is_empty() {
            let ids: Vec<String> = f.ids.iter().map(|i| i.0.clone()).collect();
            if self.pg {
                args.push(Arg::Texts(ids));
                q.push_str(&format!(" AND id = ANY({}::uuid[])", self.ph(args.len())));
            } else {
                args.push(Arg::Texts(ids));
                q.push_str(&format!(
                    " AND id IN (SELECT value FROM json_each({}))",
                    self.ph(args.len())
                ));
            }
        }
        q.push_str(&self.order_by("created_at"));
        (q, args)
    }

    /// The last `limit` changes with an impact on a node, each with the order of its first one, below `before`
    /// (0: any), newest first: from the index change_impact_node alone.
    pub fn node_change_page(self, node: &NodeId, before: i64, limit: usize) -> Stmt {
        let mut q = format!(
            "SELECT {}, min(seq) FROM change_impact WHERE node_id = {} GROUP BY change_id",
            self.id("change_id"),
            self.ph(1)
        );
        let mut args: Vec<Arg> = vec![node.as_str().into()];
        if before > 0 {
            q.push_str(&format!(" HAVING min(seq) < {}", self.ph(2)));
            args.push(before.into());
        }
        q.push_str(&format!(" ORDER BY 2 DESC LIMIT {}", self.ph(args.len() + 1)));
        args.push((limit as i64).into());
        (q, args)
    }

    /// The nodes a branch holds a version of: written on it or joined to it.
    pub fn branch_node_ids(self, branch: &str) -> Stmt {
        (
            format!(
                "SELECT {} FROM node_version WHERE branch = {p} UNION SELECT {} FROM node_branch WHERE branch = {p}",
                self.id("node_id"),
                self.id("node_id"),
                p = self.ph(1)
            ),
            vec![branch_of(branch).into()],
        )
    }

    /// The node and the versions (before, after, committed) of the impacts of a change, in the order written.
    pub fn impact_refs(self, change: &ChangeId) -> Stmt {
        (
            format!(
                "SELECT {}, pre_version, post_version, committed_version FROM change_impact WHERE change_id = {} ORDER BY seq",
                self.id_or("node_id"),
                self.ph(1)
            ),
            vec![change.as_str().into()],
        )
    }
}

/// The LIKE pattern of the rows containing a text (case-insensitive), empty for no text.
pub fn like_pattern(text: &str) -> String {
    let text = text.trim().to_lowercase();
    if text.is_empty() {
        return text;
    }
    let escaped = text.replace('\\', "\\\\").replace('%', "\\%").replace('_', "\\_");
    format!("%{escaped}%")
}
