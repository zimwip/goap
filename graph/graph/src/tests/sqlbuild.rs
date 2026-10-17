//! Port of `sqlbuild_test.go`. The golden texts are the statements postgres.go and sqlite.go ran before the builders
//! of sqlbuild.go (ADR 0074). Whitespace is collapsed on both sides; PostgreSQL placeholders are compared as they
//! were; SQLite placeholders are compared after normalisation (`?` and `?n` are one).

use goap_domain::{BaselineID, ChangeID, LogFilter, NodeID, NodeRef, TagFilter, TagID};
use serde_json::{Value, json};

use crate::sqlbuild::{Arg, CHANGE_LOG_COLUMNS, DIALECT_PG, DIALECT_SQLITE, Dialect};

fn squash(s: &str) -> String {
    s.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// Replaces `?` followed by digits by `?`.
fn qmark(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut it = s.chars().peekable();
    while let Some(c) = it.next() {
        out.push(c);
        if c == '?' {
            while it.peek().is_some_and(|d| d.is_ascii_digit()) {
                it.next();
            }
        }
    }
    out
}

#[track_caller]
fn check_sql(name: &str, d: Dialect, got: &str, want: &str) {
    let (mut got, mut want) = (squash(got), squash(want));
    if !d.pg {
        got = qmark(&got);
        want = qmark(&want);
    }
    assert_eq!(got, want, "{name}");
}

fn arg_value(a: &Arg) -> Value {
    match a {
        Arg::Text(s) | Arg::Id(s) => json!(s),
        Arg::OptId(s) => json!(s),
        Arg::Int(n) => json!(n),
        Arg::OptInt(n) => json!(n),
        Arg::Bool(b) => json!(b),
        Arg::Json(v) => v.clone(),
        Arg::Time(t) => json!(t.to_rfc3339()),
        Arg::Ints(v) => json!(v),
    }
}

#[track_caller]
fn check(name: &str, d: Dialect, (q, a): (String, Vec<Arg>), want: &str, want_args: Value) {
    check_sql(name, d, &q, want);
    let got: Vec<Value> = a.iter().map(arg_value).collect();
    assert_eq!(Value::Array(got), want_args, "{name} args");
}

const GOLD_PG_NODE_COLS: &str = "n.id::text, v.version, n.namespace, n.key, n.type, v.props, v.deleted, v.change_id::text, v.created_at, v.branch, v.parents, v.reason, v.state, v.change_impact::text, v.comment, v.execution, v.owner_id::text, n.project_id::text, v.origins";
const GOLD_SQLITE_NODE_COLS: &str = "n.id, v.version, n.namespace, n.key, n.type, v.props, v.deleted, v.change_id, v.created_at, v.branch, v.parents, v.reason, v.state, v.change_impact, v.comment, v.execution, v.owner_id, n.project_id, v.origins";
const GOLD_PG_ON_BRANCH: &str = "(v.branch = $2 OR EXISTS (SELECT 1 FROM node_branch j WHERE j.node_id = v.node_id AND j.version = v.version AND j.branch = $2))";
const GOLD_SQLITE_ON_BRANCH: &str = "(v.branch = ? OR EXISTS (SELECT 1 FROM node_branch j WHERE j.node_id = v.node_id AND j.version = v.version AND j.branch = ?))";
const GOLD_PG_IMPACT_COLS: &str = "id::text, node_id::text, key, type, intent, rationale, pre_version, post_version, landed_version, review, reviews, COALESCE(via::text, ''), recheck, produced_by, derived_from, items, execution, created_at, flow, superseded";
const GOLD_SQLITE_IMPACT: &str = "id, node_id, key, type, intent, rationale, pre_version, post_version, landed_version, review, reviews, COALESCE(via, ''), recheck, produced_by, derived_from, items, execution, created_at, flow, superseded";
const GOLD_ENTRIES: &str = "WITH RECURSIVE chain(id, parent_id, depth, d) AS (
		SELECT id, parent_id, depth, 0 FROM baseline WHERE id = %s
		UNION ALL
		SELECT b.id, b.parent_id, b.depth, c.d + 1 FROM baseline b JOIN chain c ON b.id = c.parent_id WHERE c.depth > 0
	), eff AS (
		SELECT e.node_id, e.version, e.removed, row_number() OVER (PARTITION BY e.node_id ORDER BY c.d) AS rn
		FROM chain c JOIN baseline_entry e ON e.baseline_id = c.id
	)";

fn entries(p: &str) -> String {
    GOLD_ENTRIES.replacen("%s", p, 1)
}

#[test]
fn sql_builders_golden() {
    let r = NodeRef {
        id: NodeID::new("n1"),
        version: 3,
    };
    let (pg, lite) = (DIALECT_PG, DIALECT_SQLITE);
    let n1 = NodeID::new("n1");

    check(
        "pg Node",
        pg,
        pg.sql_node(&r),
        &format!(
            "SELECT {GOLD_PG_NODE_COLS} FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = $1 AND v.version = $2"
        ),
        json!(["n1", 3]),
    );
    check(
        "sqlite Node",
        lite,
        lite.sql_node(&r),
        &format!(
            "SELECT {GOLD_SQLITE_NODE_COLS} FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = ? AND v.version = ?"
        ),
        json!(["n1", 3]),
    );
    check(
        "pg LatestOn",
        pg,
        pg.sql_latest_on(&n1, ""),
        &format!(
            "SELECT {GOLD_PG_NODE_COLS} FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = $1 AND {GOLD_PG_ON_BRANCH} ORDER BY v.version DESC LIMIT 1"
        ),
        json!(["n1", "main"]),
    );
    check(
        "sqlite LatestOn",
        lite,
        lite.sql_latest_on(&n1, ""),
        &format!(
            "SELECT {GOLD_SQLITE_NODE_COLS} FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = ? AND {GOLD_SQLITE_ON_BRANCH} ORDER BY v.version DESC LIMIT 1"
        ),
        json!(["n1", "main"]),
    );
    check(
        "pg DerivedNodes",
        pg,
        pg.sql_derived_nodes(&r),
        &format!(
            "SELECT {GOLD_PG_NODE_COLS} FROM node n JOIN node_version v ON v.node_id = n.id WHERE v.origins @> $1::jsonb ORDER BY n.key, v.version"
        ),
        json!([r#"[{"id":"n1","version":3}]"#]),
    );
    check(
        "pg DerivedNodes any version",
        pg,
        pg.sql_derived_nodes(&NodeRef {
            id: n1.clone(),
            version: 0,
        }),
        &format!(
            "SELECT {GOLD_PG_NODE_COLS} FROM node n JOIN node_version v ON v.node_id = n.id WHERE v.origins @> $1::jsonb ORDER BY n.key, v.version"
        ),
        json!([r#"[{"id":"n1"}]"#]),
    );
    check(
        "sqlite DerivedNodes",
        lite,
        lite.sql_derived_nodes(&r),
        &format!(
            "SELECT {GOLD_SQLITE_NODE_COLS} FROM node n JOIN node_version v ON v.node_id = n.id WHERE EXISTS (SELECT 1 FROM json_each(v.origins) o WHERE json_extract(o.value, '$.id') = ? AND (? = 0 OR json_extract(o.value, '$.version') = ?)) ORDER BY n.key, v.version"
        ),
        json!(["n1", 3]),
    );

    // Versions and joined branches
    check(
        "pg Versions",
        pg,
        pg.sql_versions(&n1),
        &format!(
            "SELECT {GOLD_PG_NODE_COLS} FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = $1 ORDER BY v.version"
        ),
        json!(["n1"]),
    );
    check(
        "sqlite Versions",
        lite,
        lite.sql_versions(&n1),
        &format!(
            "SELECT {GOLD_SQLITE_NODE_COLS} FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = ? ORDER BY v.version"
        ),
        json!(["n1"]),
    );
    for d in [pg, lite] {
        let p = if d.pg { "$1" } else { "?" };
        check(
            "VersionBranches",
            d,
            d.sql_version_branches(&n1),
            &format!("SELECT version, branch FROM node_branch WHERE node_id = {p} ORDER BY branch"),
            json!(["n1"]),
        );
    }

    // NodeIDByKey
    check(
        "pg NodeIDByKey",
        pg,
        pg.sql_node_id_by_key("", "k"),
        "SELECT id::text FROM node WHERE namespace = $1 AND key = $2",
        json!(["default", "k"]),
    );
    check(
        "sqlite NodeIDByKey",
        lite,
        lite.sql_node_id_by_key("", "k"),
        "SELECT id FROM node WHERE namespace = ? AND key = ?",
        json!(["default", "k"]),
    );

    // NodesIn
    let b1 = BaselineID::new("b1");
    check(
        "pg NodesIn",
        pg,
        pg.sql_nodes_in(&b1, "t"),
        &format!(
            "{} SELECT {GOLD_PG_NODE_COLS} FROM eff e JOIN node n ON n.id = e.node_id
		      JOIN node_version v ON v.node_id = e.node_id AND v.version = e.version
		      WHERE e.rn = 1 AND NOT e.removed AND ($2 = '' OR n.type = $2) ORDER BY n.key",
            entries("$1")
        ),
        json!(["b1", "t"]),
    );
    check(
        "sqlite NodesIn",
        lite,
        lite.sql_nodes_in(&b1, "t"),
        &format!(
            "{} SELECT {GOLD_SQLITE_NODE_COLS} FROM eff e JOIN node n ON n.id = e.node_id
		      JOIN node_version v ON v.node_id = e.node_id AND v.version = e.version
		      WHERE e.rn = 1 AND NOT e.removed AND (? = '' OR n.type = ?) ORDER BY n.key",
            entries("?")
        ),
        json!(["b1", "t"]),
    );

    // Baseline entries
    check(
        "pg BaselineEntries",
        pg,
        pg.sql_baseline_entries(&b1),
        &format!(
            "{} SELECT node_id::text, version FROM eff WHERE rn = 1 AND NOT removed",
            entries("$1")
        ),
        json!(["b1"]),
    );
    check(
        "sqlite BaselineEntries",
        lite,
        lite.sql_baseline_entries(&b1),
        &format!(
            "{} SELECT node_id, version FROM eff WHERE rn = 1 AND NOT removed",
            entries("?")
        ),
        json!(["b1"]),
    );

    // Links
    check(
        "pg OutLinks",
        pg,
        pg.sql_links(true, &r),
        "SELECT id::text, type, from_id::text, from_version, to_id::text, to_version, props, change_id::text FROM link WHERE from_id = $1 AND from_version = $2 ORDER BY id",
        json!(["n1", 3]),
    );
    check(
        "pg InLinks",
        pg,
        pg.sql_links(false, &r),
        "SELECT id::text, type, from_id::text, from_version, to_id::text, to_version, props, change_id::text FROM link WHERE to_id = $1 AND to_version = $2 ORDER BY id",
        json!(["n1", 3]),
    );
    check(
        "sqlite OutLinks",
        lite,
        lite.sql_links(true, &r),
        "SELECT id, type, from_id, from_version, to_id, to_version, props, change_id FROM link WHERE from_id = ? AND from_version = ? ORDER BY id",
        json!(["n1", 3]),
    );
    check(
        "sqlite InLinks",
        lite,
        lite.sql_links(false, &r),
        "SELECT id, type, from_id, from_version, to_id, to_version, props, change_id FROM link WHERE to_id = ? AND to_version = ? ORDER BY id",
        json!(["n1", 3]),
    );

    // Branches
    let pg_branch_cols = "namespace, name, parent, coalesce(fork_baseline::text, ''), coalesce(head_baseline::text, ''), origin, status, created_at, description, intent, coalesce(rebased_from::text, '')";
    let lite_branch_cols = "namespace, name, parent, coalesce(fork_baseline, ''), coalesce(head_baseline, ''), origin, status, created_at, description, intent, coalesce(rebased_from, '')";
    check(
        "pg Branch",
        pg,
        pg.sql_branch("", "b"),
        &format!("SELECT {pg_branch_cols} FROM branch WHERE namespace = $1 AND name = $2"),
        json!(["default", "b"]),
    );
    check(
        "sqlite Branch",
        lite,
        lite.sql_branch("", "b"),
        &format!("SELECT {lite_branch_cols} FROM branch WHERE namespace = ? AND name = ?"),
        json!(["default", "b"]),
    );
    check(
        "pg Branches",
        pg,
        pg.sql_branches(""),
        &format!("SELECT {pg_branch_cols} FROM branch WHERE namespace = $1 ORDER BY created_at"),
        json!(["default"]),
    );
    check(
        "sqlite Branches",
        lite,
        lite.sql_branches(""),
        &format!(
            "SELECT {lite_branch_cols} FROM branch WHERE namespace = ? ORDER BY created_at, rowid"
        ),
        json!(["default"]),
    );
    let c1 = ChangeID::new("c1");
    check(
        "pg BranchJoins",
        pg,
        pg.sql_branch_joins("", "", &c1),
        "SELECT j.node_id::text, j.version FROM node_branch j JOIN node n ON n.id = j.node_id WHERE n.namespace = $1 AND j.branch = $2 AND j.change_id = $3",
        json!(["default", "main", "c1"]),
    );
    check(
        "sqlite BranchJoins",
        lite,
        lite.sql_branch_joins("", "", &c1),
        "SELECT j.node_id, j.version FROM node_branch j JOIN node n ON n.id = j.node_id WHERE n.namespace = ?1 AND j.branch = ?2 AND j.change_id = ?3",
        json!(["default", "main", "c1"]),
    );

    // Baselines, changes
    check_sql(
        "pg BaselineHeaders",
        pg,
        &pg.sql_baseline_headers(),
        "SELECT id::text, coalesce(parent_id::text, ''), depth FROM baseline WHERE gap = 0 ORDER BY created_at, id",
    );
    check_sql(
        "sqlite BaselineHeaders",
        lite,
        &lite.sql_baseline_headers(),
        "SELECT id, coalesce(parent_id, ''), depth FROM baseline WHERE gap = 0 ORDER BY created_at, rowid",
    );
    check(
        "pg Baselines",
        pg,
        pg.sql_baseline_ids(""),
        "SELECT id::text FROM baseline WHERE namespace = $1 ORDER BY created_at",
        json!(["default"]),
    );
    check(
        "sqlite Baselines",
        lite,
        lite.sql_baseline_ids(""),
        "SELECT id FROM baseline WHERE namespace = ? ORDER BY created_at, rowid",
        json!(["default"]),
    );
    check_sql(
        "pg Changes",
        pg,
        &pg.sql_change_ids(),
        "SELECT id::text FROM change ORDER BY created_at",
    );
    check_sql(
        "sqlite Changes",
        lite,
        &lite.sql_change_ids(),
        "SELECT id FROM change ORDER BY created_at, rowid",
    );
    check_sql(
        "pg OpenChangeIDs",
        pg,
        &pg.sql_open_change_ids(),
        "SELECT id::text FROM change WHERE status NOT IN ('applied', 'abandoned') ORDER BY created_at",
    );
    check_sql(
        "sqlite OpenChangeIDs",
        lite,
        &lite.sql_open_change_ids(),
        "SELECT id FROM change WHERE status NOT IN ('applied', 'abandoned') ORDER BY created_at, rowid",
    );

    // Change impacts
    check(
        "pg ChangeImpacts",
        pg,
        pg.sql_change_impacts(&c1),
        &format!(
            "SELECT {GOLD_PG_IMPACT_COLS} FROM change_impact WHERE change_id = $1 ORDER BY seq"
        ),
        json!(["c1"]),
    );
    check(
        "sqlite ChangeImpacts",
        lite,
        lite.sql_change_impacts(&c1),
        &format!(
            "SELECT {GOLD_SQLITE_IMPACT}
			FROM change_impact WHERE change_id = ? ORDER BY seq"
        ),
        json!(["c1"]),
    );
    check(
        "pg NodeChangeImpacts",
        pg,
        pg.sql_node_change_impacts(&n1),
        "SELECT change_id::text FROM change_impact WHERE node_id = $1 ORDER BY seq",
        json!(["n1"]),
    );
    check(
        "sqlite NodeChangeImpacts",
        lite,
        lite.sql_node_change_impacts(&n1),
        "SELECT change_id FROM change_impact WHERE node_id = ? ORDER BY seq",
        json!(["n1"]),
    );

    // Log, LogCounts: the filter exercises every condition
    let f = LogFilter {
        change: c1.clone(),
        types: vec!["fact.".into(), "journal.schedule".into()],
        flows: Some(vec!["main".into()]),
        processes: vec!["p1".into(), "p2".into()],
        execution: "e1".into(),
        after_seq: 7,
        limit: 5,
    };
    let want_args = json!([
        "c1",
        "fact.%",
        "journal.schedule",
        "main",
        "p1",
        "p2",
        "e1",
        7
    ]);
    let want7 = json!(["c1", "fact.%", "journal.schedule", "main", "p1", "p2", "e1"]);
    check(
        "pg Log",
        pg,
        pg.sql_log(&f),
        "SELECT seq, id, change_id::text, type, flow, process_id, execution, subject, by_whom, causation, at, payload FROM change_log WHERE change_id = $1 AND (type LIKE $2 OR type = $3) AND flow IN ($4) AND process_id IN ($5, $6) AND execution = $7 AND seq > $8 ORDER BY seq LIMIT 5",
        want_args.clone(),
    );
    check(
        "sqlite Log",
        lite,
        lite.sql_log(&f),
        "SELECT seq, id, change_id, type, flow, process_id, execution, subject, by_whom, causation, at, payload FROM change_log WHERE change_id = ? AND (type LIKE ? OR type = ?) AND flow IN (?) AND process_id IN (?, ?) AND execution = ? AND seq > ? ORDER BY seq LIMIT 5",
        want_args,
    );
    check(
        "pg LogCounts",
        pg,
        pg.sql_log_counts(&f),
        "SELECT type, count(*) FROM change_log WHERE change_id = $1 AND (type LIKE $2 OR type = $3) AND flow IN ($4) AND process_id IN ($5, $6) AND execution = $7 GROUP BY type",
        want7.clone(),
    );
    check(
        "sqlite LogCounts",
        lite,
        lite.sql_log_counts(&f),
        "SELECT type, count(*) FROM change_log WHERE change_id = ? AND (type LIKE ? OR type = ?) AND flow IN (?) AND process_id IN (?, ?) AND execution = ? GROUP BY type",
        want7,
    );
    check(
        "pg Log no flows",
        pg,
        pg.sql_log(&LogFilter {
            change: c1.clone(),
            flows: Some(vec![]),
            ..Default::default()
        }),
        "SELECT seq, id, change_id::text, type, flow, process_id, execution, subject, by_whom, causation, at, payload FROM change_log WHERE change_id = $1 AND 1 = 0 ORDER BY seq",
        json!(["c1"]),
    );

    // Tags
    let tf = TagFilter {
        namespace: "ns".into(),
        name: "v1".into(),
        change: c1.clone(),
    };
    check(
        "pg Tags",
        pg,
        pg.sql_tags(&tf),
        "SELECT id::text, name, namespace, change_id::text, COALESCE(baseline_id::text, ''), by, created_at FROM tag WHERE true AND namespace = $1 AND name = $2 AND change_id = $3 ORDER BY created_at, id",
        json!(["ns", "v1", "c1"]),
    );
    check(
        "sqlite Tags",
        lite,
        lite.sql_tags(&tf),
        "SELECT id, name, namespace, change_id, COALESCE(baseline_id, ''), by, created_at FROM tag WHERE 1 = 1 AND namespace = ? AND name = ? AND change_id = ? ORDER BY created_at, rowid",
        json!(["ns", "v1", "c1"]),
    );
    check(
        "pg Tags unfiltered",
        pg,
        pg.sql_tags(&TagFilter::default()),
        "SELECT id::text, name, namespace, change_id::text, COALESCE(baseline_id::text, ''), by, created_at FROM tag WHERE true ORDER BY created_at, id",
        json!([]),
    );
    check(
        "sqlite Tags unfiltered",
        lite,
        lite.sql_tags(&TagFilter::default()),
        "SELECT id, name, namespace, change_id, COALESCE(baseline_id, ''), by, created_at FROM tag WHERE 1 = 1 ORDER BY created_at, rowid",
        json!([]),
    );

    // Single statements
    let t1 = TagID::new("t1");
    check_sql(
        "pg DeleteTag",
        pg,
        &pg.sql_delete_tag(&t1).0,
        "DELETE FROM tag WHERE id = $1",
    );
    check_sql(
        "sqlite DeleteTag",
        lite,
        &lite.sql_delete_tag(&t1).0,
        "DELETE FROM tag WHERE id = ?",
    );
    check_sql(
        "pg DeleteChangeImpact",
        pg,
        &pg.sql_delete_change_impact(),
        "DELETE FROM change_impact WHERE change_id = $1 AND id = $2",
    );
    check_sql(
        "sqlite DeleteChangeImpact",
        lite,
        &lite.sql_delete_change_impact(),
        "DELETE FROM change_impact WHERE change_id = ? AND id = ?",
    );
    check_sql(
        "pg SetNodeOrigin",
        pg,
        &pg.sql_set_node_origin(),
        "UPDATE node_version SET change_id = $3, change_impact = $4, comment = $5 WHERE node_id = $1 AND version = $2",
    );
    check_sql(
        "sqlite SetNodeOrigin",
        lite,
        &lite.sql_set_node_origin(),
        "UPDATE node_version SET change_id = ?, change_impact = ?, comment = ? WHERE node_id = ? AND version = ?",
    );
    check_sql("pg JoinBranch", pg, &pg.sql_join_branch(), "INSERT INTO node_branch (node_id, version, branch, change_id) SELECT node_id, version, $3, $4 FROM node_version
		WHERE node_id = $1 AND version = $2 AND branch <> $3 ON CONFLICT DO NOTHING");
    check_sql("sqlite JoinBranch", lite, &lite.sql_join_branch(), "INSERT INTO node_branch (node_id, version, branch, change_id) SELECT node_id, version, ?, ? FROM node_version
		WHERE node_id = ? AND version = ? AND branch <> ? ON CONFLICT DO NOTHING");

    // Inserts
    let node_version_cols = [
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
    ];
    check_sql("pg insert node_version", pg, &pg.sql_insert("node_version", &node_version_cols, ""), "INSERT INTO node_version (node_id, version, props, deleted, change_id, created_at, branch, parents, reason, state, change_impact, comment, execution, owner_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)");
    check_sql("sqlite insert node_version", lite, &lite.sql_insert("node_version", &node_version_cols, ""), "INSERT INTO node_version (node_id, version, props, deleted, change_id, created_at, branch, parents, reason, state, change_impact, comment, execution, owner_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)");
    let link_cols = [
        "id",
        "type",
        "from_id",
        "from_version",
        "to_id",
        "to_version",
        "props",
        "change_id",
    ];
    check_sql(
        "pg insert link",
        pg,
        &pg.sql_insert("link", &link_cols, ""),
        "INSERT INTO link (id, type, from_id, from_version, to_id, to_version, props, change_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
    );
    check_sql(
        "sqlite insert link",
        lite,
        &lite.sql_insert("link", &link_cols, ""),
        "INSERT INTO link (id, type, from_id, from_version, to_id, to_version, props, change_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
    );
    let baseline_cols = [
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
    ];
    check_sql(
        "pg insert baseline",
        pg,
        &pg.sql_insert("baseline", &baseline_cols, ""),
        "INSERT INTO baseline (id, name, parent_id, merged_from, change_id, created_at, branch, namespace, depth, gap, kind) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)",
    );
    check_sql(
        "sqlite insert baseline",
        lite,
        &lite.sql_insert("baseline", &baseline_cols, ""),
        "INSERT INTO baseline (id, name, parent_id, merged_from, change_id, created_at, branch, namespace, depth, gap, kind) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
    );
    check_sql("pg insert change_log", pg, &pg.sql_insert("change_log", CHANGE_LOG_COLUMNS, " RETURNING seq"), "INSERT INTO change_log (id, change_id, type, flow, process_id, execution, subject, by_whom, causation, at, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING seq");
    check_sql("sqlite insert change_log", lite, &lite.sql_insert("change_log", CHANGE_LOG_COLUMNS, ""), "INSERT INTO change_log (id, change_id, type, flow, process_id, execution, subject, by_whom, causation, at, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)");

    // DeleteChange
    let check_list = |name: &str, d: Dialect, got: Vec<String>, want: &[&str]| {
        assert_eq!(got.len(), want.len(), "{name}: statements");
        for (g, w) in got.iter().zip(want) {
            check_sql(name, d, g, w);
        }
    };
    check_list(
        "pg DeleteChange used",
        pg,
        pg.delete_change_used(),
        &[
            "SELECT count(*) FROM baseline_entry e JOIN node_version v ON e.node_id = v.node_id AND e.version = v.version WHERE v.change_id = $1",
            "SELECT count(*) FROM baseline WHERE change_id = $1",
            "SELECT count(*) FROM link l JOIN node_version v ON l.to_id = v.node_id AND l.to_version = v.version WHERE v.change_id = $1 AND l.change_id IS DISTINCT FROM $1::uuid",
            "SELECT count(*) FROM node_version o JOIN node_version v ON o.node_id = v.node_id AND o.version > v.version WHERE v.change_id = $1 AND o.change_id IS DISTINCT FROM $1::uuid",
        ],
    );
    check_list(
        "sqlite DeleteChange used",
        lite,
        lite.delete_change_used(),
        &[
            "SELECT count(*) FROM baseline_entry e JOIN node_version v ON e.node_id = v.node_id AND e.version = v.version WHERE v.change_id = ?1",
            "SELECT count(*) FROM baseline WHERE change_id = ?1",
            "SELECT count(*) FROM link l JOIN node_version v ON l.to_id = v.node_id AND l.to_version = v.version WHERE v.change_id = ?1 AND (l.change_id IS NULL OR l.change_id != ?1)",
            "SELECT count(*) FROM node_version o JOIN node_version v ON o.node_id = v.node_id AND o.version > v.version WHERE v.change_id = ?1 AND (o.change_id IS NULL OR o.change_id != ?1)",
        ],
    );
    check_sql(
        "pg change nodes",
        pg,
        &pg.sql_change_nodes(),
        "SELECT DISTINCT node_id::text FROM node_version WHERE change_id = $1",
    );
    check_sql(
        "sqlite change nodes",
        lite,
        &lite.sql_change_nodes(),
        "SELECT DISTINCT node_id FROM node_version WHERE change_id = ?",
    );
    check_list(
        "pg DeleteChange rows",
        pg,
        pg.delete_change_rows(),
        &[
            "DELETE FROM link WHERE change_id = $1 OR (from_id, from_version) IN (SELECT node_id, version FROM node_version WHERE change_id = $1)",
            "DELETE FROM node_branch WHERE change_id = $1 OR (node_id, version) IN (SELECT node_id, version FROM node_version WHERE change_id = $1)",
            "DELETE FROM node_version WHERE change_id = $1",
            "DELETE FROM change_impact WHERE change_id = $1",
            "DELETE FROM change_log WHERE change_id = $1",
            "DELETE FROM tag WHERE change_id = $1",
        ],
    );
    check_list(
        "sqlite DeleteChange rows",
        lite,
        lite.delete_change_rows(),
        &[
            "DELETE FROM link WHERE change_id = ?1 OR (from_id, from_version) IN (SELECT node_id, version FROM node_version WHERE change_id = ?1)",
            "DELETE FROM node_branch WHERE change_id = ?1 OR (node_id, version) IN (SELECT node_id, version FROM node_version WHERE change_id = ?1)",
            "DELETE FROM node_version WHERE change_id = ?1",
            "DELETE FROM change_impact WHERE change_id = ?1",
            "DELETE FROM change_log WHERE change_id = ?1",
            "DELETE FROM tag WHERE change_id = ?1",
        ],
    );
    check_sql(
        "pg orphan",
        pg,
        &pg.sql_delete_orphan_node(),
        "DELETE FROM node WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM node_version WHERE node_id = $1)",
    );
    check_sql(
        "sqlite orphan",
        lite,
        &lite.sql_delete_orphan_node(),
        "DELETE FROM node WHERE id = ?1 AND NOT EXISTS (SELECT 1 FROM node_version WHERE node_id = ?1)",
    );
    check_sql(
        "pg latest",
        pg,
        &pg.sql_refresh_latest(),
        "UPDATE node SET latest = (SELECT max(version) FROM node_version WHERE node_id = $1) WHERE id = $1",
    );
    check_sql(
        "sqlite latest",
        lite,
        &lite.sql_refresh_latest(),
        "UPDATE node SET latest = (SELECT max(version) FROM node_version WHERE node_id = ?1) WHERE id = ?1",
    );
    check_sql(
        "pg branch delete",
        pg,
        &pg.sql_delete_branch(),
        "DELETE FROM branch WHERE namespace = $1 AND name = $2",
    );
    check_sql(
        "sqlite branch delete",
        lite,
        &lite.sql_delete_branch(),
        "DELETE FROM branch WHERE namespace = ? AND name = ?",
    );
    check_sql(
        "pg change delete",
        pg,
        &pg.sql_delete_change(),
        "DELETE FROM change WHERE id = $1",
    );
    check_sql(
        "sqlite change delete",
        lite,
        &lite.sql_delete_change(),
        "DELETE FROM change WHERE id = ?",
    );
}

// The placeholders of the two dialects differ and nothing else a caller sees.
#[test]
fn dialect_placeholders() {
    assert_eq!(DIALECT_PG.ph(3), "$3");
    assert_eq!(DIALECT_SQLITE.ph(3), "?3");
}
