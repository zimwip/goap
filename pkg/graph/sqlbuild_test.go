package graph

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// The golden texts below are the statements postgres.go and sqlite.go ran before the builders of sqlbuild.go (ADR 0074),
// copied from the code of that day. Whitespace is collapsed on both sides; PostgreSQL placeholders are compared as they
// were; SQLite placeholders are compared after normalisation (`?` and `?n` are one: the builders number them, so a value
// used twice is one argument, where the former code repeated it).

var (
	spaces  = regexp.MustCompile(`\s+`)
	qmarkNn = regexp.MustCompile(`\?\d*`)
)

func squash(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }

func checkSQL(t *testing.T, name string, d dialect, got, want string) {
	t.Helper()
	got, want = squash(got), squash(want)
	if !d.pg {
		got, want = qmarkNn.ReplaceAllString(got, "?"), qmarkNn.ReplaceAllString(want, "?")
	}
	if got != want {
		t.Errorf("%s:\n got  %s\n want %s", name, got, want)
	}
}

const (
	goldPGNodeCols     = `n.id::text, v.version, n.namespace, n.key, n.type, v.props, v.deleted, v.change_id::text, v.created_at, v.branch, v.parents, v.reason, v.state, v.change_impact::text, v.comment, v.execution, v.owner_id::text, n.project_id::text, v.origins`
	goldSQLiteNodeCols = `n.id, v.version, n.namespace, n.key, n.type, v.props, v.deleted, v.change_id, v.created_at, v.branch, v.parents, v.reason, v.state, v.change_impact, v.comment, v.execution, v.owner_id, n.project_id, v.origins`
	goldPGOnBranch     = `(v.branch = $2 OR EXISTS (SELECT 1 FROM node_branch j WHERE j.node_id = v.node_id AND j.version = v.version AND j.branch = $2))`
	goldSQLiteOnBranch = `(v.branch = ? OR EXISTS (SELECT 1 FROM node_branch j WHERE j.node_id = v.node_id AND j.version = v.version AND j.branch = ?))`
	goldPGImpactCols   = `id::text, node_id::text, key, type, intent, rationale, pre_version, post_version, landed_version, review, reviews, COALESCE(via::text, ''), recheck, produced_by, derived_from, items, execution, created_at, flow, superseded`
	goldSQLiteImpact   = `id, node_id, key, type, intent, rationale, pre_version, post_version, landed_version, review, reviews, COALESCE(via, ''), recheck, produced_by, derived_from, items, execution, created_at, flow, superseded`
	goldEntries        = `WITH RECURSIVE chain(id, parent_id, depth, d) AS (
		SELECT id, parent_id, depth, 0 FROM baseline WHERE id = %s
		UNION ALL
		SELECT b.id, b.parent_id, b.depth, c.d + 1 FROM baseline b JOIN chain c ON b.id = c.parent_id WHERE c.depth > 0
	), eff AS (
		SELECT e.node_id, e.version, e.removed, row_number() OVER (PARTITION BY e.node_id ORDER BY c.d) AS rn
		FROM chain c JOIN baseline_entry e ON e.baseline_id = c.id
	)`
)

func entries(p string) string { return strings.Replace(goldEntries, "%s", p, 1) }

func TestSQLBuildersGolden(t *testing.T) {
	ref := domain.NodeRef{ID: "n1", Version: 3}
	pg, lite := dialectPG, dialectSQLite
	check := func(name string, d dialect, got string, args []any, want string, wantArgs []any) {
		t.Helper()
		checkSQL(t, name, d, got, want)
		if !reflect.DeepEqual(args, wantArgs) {
			t.Errorf("%s args: got %v, want %v", name, args, wantArgs)
		}
	}

	// Node
	q, a := pg.sqlNode(ref)
	check("pg Node", pg, q, a, `SELECT `+goldPGNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = $1 AND v.version = $2`, []any{"n1", 3})
	q, a = lite.sqlNode(ref)
	check("sqlite Node", lite, q, a, `SELECT `+goldSQLiteNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = ? AND v.version = ?`, []any{"n1", 3})

	// LatestOn (the branch is named by BranchOf: "" is main)
	q, a = pg.sqlLatestOn("n1", "")
	check("pg LatestOn", pg, q, a, `SELECT `+goldPGNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = $1 AND `+goldPGOnBranch+` ORDER BY v.version DESC LIMIT 1`, []any{"n1", "main"})
	q, a = lite.sqlLatestOn("n1", "")
	check("sqlite LatestOn", lite, q, a, `SELECT `+goldSQLiteNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = ? AND `+goldSQLiteOnBranch+` ORDER BY v.version DESC LIMIT 1`, []any{"n1", "main"})

	// DerivedNodes (ADR 0077): the versions naming a node among their origins
	q, a = pg.sqlDerivedNodes(ref)
	check("pg DerivedNodes", pg, q, a, `SELECT `+goldPGNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE v.origins @> $1::jsonb ORDER BY n.key, v.version`, []any{`[{"id":"n1","version":3}]`})
	q, a = pg.sqlDerivedNodes(domain.NodeRef{ID: "n1"})
	check("pg DerivedNodes any version", pg, q, a, `SELECT `+goldPGNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE v.origins @> $1::jsonb ORDER BY n.key, v.version`, []any{`[{"id":"n1"}]`})
	q, a = lite.sqlDerivedNodes(ref)
	check("sqlite DerivedNodes", lite, q, a, `SELECT `+goldSQLiteNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE EXISTS (SELECT 1 FROM json_each(v.origins) o WHERE json_extract(o.value, '$.id') = ? AND (? = 0 OR json_extract(o.value, '$.version') = ?)) ORDER BY n.key, v.version`, []any{"n1", 3})

	// Versions and joined branches
	q, a = pg.sqlVersions("n1")
	check("pg Versions", pg, q, a, `SELECT `+goldPGNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = $1 ORDER BY v.version`, []any{"n1"})
	q, a = lite.sqlVersions("n1")
	check("sqlite Versions", lite, q, a, `SELECT `+goldSQLiteNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = ? ORDER BY v.version`, []any{"n1"})
	for _, d := range []dialect{pg, lite} {
		q, a = d.sqlVersionBranches("n1")
		p := "?"
		if d.pg {
			p = "$1"
		}
		check("VersionBranches", d, q, a, `SELECT version, branch FROM node_branch WHERE node_id = `+p+` ORDER BY branch`, []any{"n1"})
	}

	// NodeIDByKey
	q, a = pg.sqlNodeIDByKey("", "k")
	check("pg NodeIDByKey", pg, q, a, `SELECT id::text FROM node WHERE namespace = $1 AND key = $2`, []any{"default", "k"})
	q, a = lite.sqlNodeIDByKey("", "k")
	check("sqlite NodeIDByKey", lite, q, a, `SELECT id FROM node WHERE namespace = ? AND key = ?`, []any{"default", "k"})

	// NodesIn
	q, a = pg.sqlNodesIn("b1", "t")
	check("pg NodesIn", pg, q, a, entries("$1")+` SELECT `+goldPGNodeCols+` FROM eff e JOIN node n ON n.id = e.node_id
		      JOIN node_version v ON v.node_id = e.node_id AND v.version = e.version
		      WHERE e.rn = 1 AND NOT e.removed AND ($2 = '' OR n.type = $2) ORDER BY n.key`, []any{"b1", "t"})
	q, a = lite.sqlNodesIn("b1", "t")
	check("sqlite NodesIn", lite, q, a, entries("?")+` SELECT `+goldSQLiteNodeCols+` FROM eff e JOIN node n ON n.id = e.node_id
		      JOIN node_version v ON v.node_id = e.node_id AND v.version = e.version
		      WHERE e.rn = 1 AND NOT e.removed AND (? = '' OR n.type = ?) ORDER BY n.key`, []any{"b1", "t"})

	// Baseline entries
	q, a = pg.sqlBaselineEntries("b1")
	check("pg BaselineEntries", pg, q, a, entries("$1")+` SELECT node_id::text, version FROM eff WHERE rn = 1 AND NOT removed`, []any{"b1"})
	q, a = lite.sqlBaselineEntries("b1")
	check("sqlite BaselineEntries", lite, q, a, entries("?")+` SELECT node_id, version FROM eff WHERE rn = 1 AND NOT removed`, []any{"b1"})

	// Links
	q, a = pg.sqlLinks(true, ref)
	check("pg OutLinks", pg, q, a, `SELECT id::text, type, from_id::text, from_version, to_id::text, to_version, props, change_id::text FROM link WHERE from_id = $1 AND from_version = $2 ORDER BY id`, []any{"n1", 3})
	q, a = pg.sqlLinks(false, ref)
	check("pg InLinks", pg, q, a, `SELECT id::text, type, from_id::text, from_version, to_id::text, to_version, props, change_id::text FROM link WHERE to_id = $1 AND to_version = $2 ORDER BY id`, []any{"n1", 3})
	q, a = lite.sqlLinks(true, ref)
	check("sqlite OutLinks", lite, q, a, `SELECT id, type, from_id, from_version, to_id, to_version, props, change_id FROM link WHERE from_id = ? AND from_version = ? ORDER BY id`, []any{"n1", 3})
	q, a = lite.sqlLinks(false, ref)
	check("sqlite InLinks", lite, q, a, `SELECT id, type, from_id, from_version, to_id, to_version, props, change_id FROM link WHERE to_id = ? AND to_version = ? ORDER BY id`, []any{"n1", 3})

	// Branches
	const pgBranchCols = `namespace, name, parent, coalesce(fork_baseline::text, ''), coalesce(head_baseline::text, ''), origin, status, created_at, description, intent`
	const liteBranchCols = `namespace, name, parent, coalesce(fork_baseline, ''), coalesce(head_baseline, ''), origin, status, created_at, description, intent`
	q, a = pg.sqlBranch("", "b")
	check("pg Branch", pg, q, a, `SELECT `+pgBranchCols+` FROM branch WHERE namespace = $1 AND name = $2`, []any{"default", "b"})
	q, a = lite.sqlBranch("", "b")
	check("sqlite Branch", lite, q, a, `SELECT `+liteBranchCols+` FROM branch WHERE namespace = ? AND name = ?`, []any{"default", "b"})
	q, a = pg.sqlBranches("")
	check("pg Branches", pg, q, a, `SELECT `+pgBranchCols+` FROM branch WHERE namespace = $1 ORDER BY created_at`, []any{"default"})
	q, a = lite.sqlBranches("")
	check("sqlite Branches", lite, q, a, `SELECT `+liteBranchCols+` FROM branch WHERE namespace = ? ORDER BY created_at, rowid`, []any{"default"})
	q, a = pg.sqlBranchJoins("", "", "c1")
	check("pg BranchJoins", pg, q, a, `SELECT j.node_id::text, j.version FROM node_branch j JOIN node n ON n.id = j.node_id WHERE n.namespace = $1 AND j.branch = $2 AND j.change_id = $3`, []any{"default", "main", "c1"})
	q, a = lite.sqlBranchJoins("", "", "c1")
	check("sqlite BranchJoins", lite, q, a, `SELECT j.node_id, j.version FROM node_branch j JOIN node n ON n.id = j.node_id WHERE n.namespace = ?1 AND j.branch = ?2 AND j.change_id = ?3`, []any{"default", "main", "c1"})

	// Baselines, changes
	check("pg BaselineHeaders", pg, pg.sqlBaselineHeaders(), nil, `SELECT id::text, coalesce(parent_id::text, ''), depth FROM baseline WHERE gap = 0 ORDER BY created_at, id`, nil)
	check("sqlite BaselineHeaders", lite, lite.sqlBaselineHeaders(), nil, `SELECT id, coalesce(parent_id, ''), depth FROM baseline WHERE gap = 0 ORDER BY created_at, rowid`, nil)
	q, a = pg.sqlBaselineIDs("")
	check("pg Baselines", pg, q, a, `SELECT id::text FROM baseline WHERE namespace = $1 ORDER BY created_at`, []any{"default"})
	q, a = lite.sqlBaselineIDs("")
	check("sqlite Baselines", lite, q, a, `SELECT id FROM baseline WHERE namespace = ? ORDER BY created_at, rowid`, []any{"default"})
	check("pg Changes", pg, pg.sqlChangeIDs(), nil, `SELECT id::text FROM change ORDER BY created_at`, nil)
	check("sqlite Changes", lite, lite.sqlChangeIDs(), nil, `SELECT id FROM change ORDER BY created_at, rowid`, nil)
	check("pg OpenChangeIDs", pg, pg.sqlOpenChangeIDs(), nil, `SELECT id::text FROM change WHERE status NOT IN ('applied', 'abandoned') ORDER BY created_at`, nil)
	check("sqlite OpenChangeIDs", lite, lite.sqlOpenChangeIDs(), nil, `SELECT id FROM change WHERE status NOT IN ('applied', 'abandoned') ORDER BY created_at, rowid`, nil)

	// Change impacts
	q, a = pg.sqlChangeImpacts("c1")
	check("pg ChangeImpacts", pg, q, a, `SELECT `+goldPGImpactCols+` FROM change_impact WHERE change_id = $1 ORDER BY seq`, []any{"c1"})
	q, a = lite.sqlChangeImpacts("c1")
	check("sqlite ChangeImpacts", lite, q, a, `SELECT `+goldSQLiteImpact+`
			FROM change_impact WHERE change_id = ? ORDER BY seq`, []any{"c1"})
	q, a = pg.sqlNodeChangeImpacts("n1")
	check("pg NodeChangeImpacts", pg, q, a, `SELECT change_id::text FROM change_impact WHERE node_id = $1 ORDER BY seq`, []any{"n1"})
	q, a = lite.sqlNodeChangeImpacts("n1")
	check("sqlite NodeChangeImpacts", lite, q, a, `SELECT change_id FROM change_impact WHERE node_id = ? ORDER BY seq`, []any{"n1"})

	// Log, LogCounts: the filter exercises every condition (the where builder is shared and unchanged)
	f := domain.LogFilter{Change: "c1", Types: []string{"fact.", "journal.schedule"}, Flows: []string{"main"}, Processes: []string{"p1", "p2"}, Execution: "e1", AfterSeq: 7, Limit: 5}
	wantArgs := []any{"c1", "fact.%", "journal.schedule", "main", "p1", "p2", "e1", int64(7)}
	if f.AfterSeq != 7 {
		t.Fatal("AfterSeq type")
	}
	q, a = pg.sqlLog(f)
	check("pg Log", pg, q, normSeq(a), `SELECT seq, id, change_id::text, type, flow, process_id, execution, subject, by_whom, at, payload, labels FROM change_log WHERE change_id = $1 AND (type LIKE $2 OR type = $3) AND flow IN ($4) AND process_id IN ($5, $6) AND execution = $7 AND seq > $8 ORDER BY seq LIMIT 5`, normSeq(wantArgs))
	q, a = lite.sqlLog(f)
	check("sqlite Log", lite, q, normSeq(a), `SELECT seq, id, change_id, type, flow, process_id, execution, subject, by_whom, at, payload, labels FROM change_log WHERE change_id = ? AND (type LIKE ? OR type = ?) AND flow IN (?) AND process_id IN (?, ?) AND execution = ? AND seq > ? ORDER BY seq LIMIT 5`, normSeq(wantArgs))
	q, a = pg.sqlLogCounts(f)
	check("pg LogCounts", pg, q, normSeq(a), `SELECT type, count(*) FROM change_log WHERE change_id = $1 AND (type LIKE $2 OR type = $3) AND flow IN ($4) AND process_id IN ($5, $6) AND execution = $7 GROUP BY type`, normSeq(wantArgs[:7]))
	q, a = lite.sqlLogCounts(f)
	check("sqlite LogCounts", lite, q, normSeq(a), `SELECT type, count(*) FROM change_log WHERE change_id = ? AND (type LIKE ? OR type = ?) AND flow IN (?) AND process_id IN (?, ?) AND execution = ? GROUP BY type`, normSeq(wantArgs[:7]))
	q, a = pg.sqlLog(domain.LogFilter{Change: "c1", Flows: []string{}})
	check("pg Log no flows", pg, q, a, `SELECT seq, id, change_id::text, type, flow, process_id, execution, subject, by_whom, at, payload, labels FROM change_log WHERE change_id = $1 AND 1 = 0 ORDER BY seq`, []any{"c1"})

	// labels (ADR 0098): the jsonb containment in PostgreSQL, one json_extract per label (sorted) in SQLite
	q, a = pg.sqlLog(domain.LogFilter{Change: "c1", Labels: map[string]string{"step": "s1", "process": "p1"}})
	check("pg Log labels", pg, q, a, `SELECT seq, id, change_id::text, type, flow, process_id, execution, subject, by_whom, at, payload, labels FROM change_log WHERE change_id = $1 AND labels @> $2::jsonb ORDER BY seq`,
		[]any{"c1", `{"process":"p1","step":"s1"}`})
	q, a = lite.sqlLog(domain.LogFilter{Change: "c1", Labels: map[string]string{"step": "s1", "process": "p1"}})
	check("sqlite Log labels", lite, q, a, `SELECT seq, id, change_id, type, flow, process_id, execution, subject, by_whom, at, payload, labels FROM change_log WHERE change_id = ? AND json_extract(labels, ?) = ? AND json_extract(labels, ?) = ? ORDER BY seq`,
		[]any{"c1", `$."process"`, "p1", `$."step"`, "s1"})

	// change objects (ADR 0098)
	of := domain.ObjectFilter{Types: []string{"risks@Risk"}, KeyPrefix: "RISK-", Workspaces: []string{""}, Labels: map[string]string{"step": "s1"}}
	q, a = pg.sqlChangeObjects("c1", of)
	check("pg ChangeObjects", pg, q, a, `SELECT change_id::text, type, key, workspace, version, seq, state, value, labels, by_whom, at FROM change_object WHERE change_id = $1 AND type IN ($2) AND substr(key, 1, 5) = $3 AND workspace IN ($4) AND labels @> $5::jsonb ORDER BY created_seq`,
		[]any{"c1", "risks@Risk", "RISK-", "", `{"step":"s1"}`})
	q, a = lite.sqlChangeObjects("c1", of)
	check("sqlite ChangeObjects", lite, q, a, `SELECT change_id, type, key, workspace, version, seq, state, value, labels, by_whom, at FROM change_object WHERE change_id = ? AND type IN (?) AND substr(key, 1, 5) = ? AND workspace IN (?) AND json_extract(labels, ?) = ? ORDER BY created_seq`,
		[]any{"c1", "risks@Risk", "RISK-", "", `$."step"`, "s1"})
	checkSQL(t, "pg UpsertChangeObject", pg, pg.sqlUpsertChangeObject(), `INSERT INTO change_object (change_id, type, key, workspace, version, seq, created_seq, state, value, labels, by_whom, at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) ON CONFLICT (change_id, type, key, workspace) DO UPDATE SET version = excluded.version,
		seq = excluded.seq, state = excluded.state, value = excluded.value, labels = excluded.labels, by_whom = excluded.by_whom, at = excluded.at`)

	// Tags
	tf := domain.TagFilter{Namespace: "ns", Name: "v1", Change: "c1"}
	q, a = pg.sqlTags(tf)
	check("pg Tags", pg, q, a, `SELECT id::text, name, namespace, change_id::text, COALESCE(baseline_id::text, ''), by, created_at FROM tag WHERE true AND namespace = $1 AND name = $2 AND change_id = $3 ORDER BY created_at, id`, []any{"ns", "v1", "c1"})
	q, a = lite.sqlTags(tf)
	check("sqlite Tags", lite, q, a, `SELECT id, name, namespace, change_id, COALESCE(baseline_id, ''), by, created_at FROM tag WHERE 1 = 1 AND namespace = ? AND name = ? AND change_id = ? ORDER BY created_at, rowid`, []any{"ns", "v1", "c1"})
	q, a = pg.sqlTags(domain.TagFilter{})
	check("pg Tags unfiltered", pg, q, a, `SELECT id::text, name, namespace, change_id::text, COALESCE(baseline_id::text, ''), by, created_at FROM tag WHERE true ORDER BY created_at, id`, []any{})
	q, a = lite.sqlTags(domain.TagFilter{})
	check("sqlite Tags unfiltered", lite, q, a, `SELECT id, name, namespace, change_id, COALESCE(baseline_id, ''), by, created_at FROM tag WHERE 1 = 1 ORDER BY created_at, rowid`, []any{})

	// Single statements
	q, _ = pg.sqlDeleteTag("t1")
	checkSQL(t, "pg DeleteTag", pg, q, `DELETE FROM tag WHERE id = $1`)
	q, _ = lite.sqlDeleteTag("t1")
	checkSQL(t, "sqlite DeleteTag", lite, q, `DELETE FROM tag WHERE id = ?`)
	checkSQL(t, "pg DeleteChangeImpact", pg, pg.sqlDeleteChangeImpact(), `DELETE FROM change_impact WHERE change_id = $1 AND id = $2`)
	checkSQL(t, "sqlite DeleteChangeImpact", lite, lite.sqlDeleteChangeImpact(), `DELETE FROM change_impact WHERE change_id = ? AND id = ?`)
	checkSQL(t, "pg SetNodeOrigin", pg, pg.sqlSetNodeOrigin(), `UPDATE node_version SET change_id = $3, change_impact = $4, comment = $5 WHERE node_id = $1 AND version = $2`)
	checkSQL(t, "sqlite SetNodeOrigin", lite, lite.sqlSetNodeOrigin(), `UPDATE node_version SET change_id = ?, change_impact = ?, comment = ? WHERE node_id = ? AND version = ?`)
	checkSQL(t, "pg JoinBranch", pg, pg.sqlJoinBranch(), `INSERT INTO node_branch (node_id, version, branch, change_id) SELECT node_id, version, $3, $4 FROM node_version
		WHERE node_id = $1 AND version = $2 AND branch <> $3 ON CONFLICT DO NOTHING`)
	checkSQL(t, "sqlite JoinBranch", lite, lite.sqlJoinBranch(), `INSERT INTO node_branch (node_id, version, branch, change_id) SELECT node_id, version, ?, ? FROM node_version
		WHERE node_id = ? AND version = ? AND branch <> ? ON CONFLICT DO NOTHING`)

	// Inserts
	nodeVersionCols := []string{"node_id", "version", "props", "deleted", "change_id", "created_at", "branch", "parents", "reason", "state", "change_impact", "comment", "execution", "owner_id"}
	checkSQL(t, "pg insert node_version", pg, pg.sqlInsert("node_version", nodeVersionCols, ""), `INSERT INTO node_version (node_id, version, props, deleted, change_id, created_at, branch, parents, reason, state, change_impact, comment, execution, owner_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`)
	checkSQL(t, "sqlite insert node_version", lite, lite.sqlInsert("node_version", nodeVersionCols, ""), `INSERT INTO node_version (node_id, version, props, deleted, change_id, created_at, branch, parents, reason, state, change_impact, comment, execution, owner_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	linkCols := []string{"id", "type", "from_id", "from_version", "to_id", "to_version", "props", "change_id"}
	checkSQL(t, "pg insert link", pg, pg.sqlInsert("link", linkCols, ""), `INSERT INTO link (id, type, from_id, from_version, to_id, to_version, props, change_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`)
	checkSQL(t, "sqlite insert link", lite, lite.sqlInsert("link", linkCols, ""), `INSERT INTO link (id, type, from_id, from_version, to_id, to_version, props, change_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	baselineCols := []string{"id", "name", "parent_id", "merged_from", "change_id", "created_at", "branch", "namespace", "depth", "gap", "kind"}
	checkSQL(t, "pg insert baseline", pg, pg.sqlInsert("baseline", baselineCols, ""), `INSERT INTO baseline (id, name, parent_id, merged_from, change_id, created_at, branch, namespace, depth, gap, kind) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`)
	checkSQL(t, "sqlite insert baseline", lite, lite.sqlInsert("baseline", baselineCols, ""), `INSERT INTO baseline (id, name, parent_id, merged_from, change_id, created_at, branch, namespace, depth, gap, kind) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	logCols := []string{"id", "change_id", "type", "flow", "process_id", "execution", "subject", "by_whom", "at", "payload"}
	checkSQL(t, "pg insert change_log", pg, pg.sqlInsert("change_log", logCols, " RETURNING seq"), `INSERT INTO change_log (id, change_id, type, flow, process_id, execution, subject, by_whom, at, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING seq`)
	checkSQL(t, "sqlite insert change_log", lite, lite.sqlInsert("change_log", logCols, ""), `INSERT INTO change_log (id, change_id, type, flow, process_id, execution, subject, by_whom, at, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)

	// DeleteChange
	checkList := func(name string, d dialect, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %d statements, want %d", name, len(got), len(want))
		}
		for i := range got {
			checkSQL(t, name, d, got[i], want[i])
		}
	}
	checkList("pg DeleteChange used", pg, pg.deleteChangeUsed(), []string{
		`SELECT count(*) FROM baseline_entry e JOIN node_version v ON e.node_id = v.node_id AND e.version = v.version WHERE v.change_id = $1`,
		`SELECT count(*) FROM baseline WHERE change_id = $1`,
		`SELECT count(*) FROM link l JOIN node_version v ON l.to_id = v.node_id AND l.to_version = v.version WHERE v.change_id = $1 AND l.change_id IS DISTINCT FROM $1::uuid`,
		`SELECT count(*) FROM node_version o JOIN node_version v ON o.node_id = v.node_id AND o.version > v.version WHERE v.change_id = $1 AND o.change_id IS DISTINCT FROM $1::uuid`,
	})
	checkList("sqlite DeleteChange used", lite, lite.deleteChangeUsed(), []string{
		`SELECT count(*) FROM baseline_entry e JOIN node_version v ON e.node_id = v.node_id AND e.version = v.version WHERE v.change_id = ?1`,
		`SELECT count(*) FROM baseline WHERE change_id = ?1`,
		`SELECT count(*) FROM link l JOIN node_version v ON l.to_id = v.node_id AND l.to_version = v.version WHERE v.change_id = ?1 AND (l.change_id IS NULL OR l.change_id != ?1)`,
		`SELECT count(*) FROM node_version o JOIN node_version v ON o.node_id = v.node_id AND o.version > v.version WHERE v.change_id = ?1 AND (o.change_id IS NULL OR o.change_id != ?1)`,
	})
	checkSQL(t, "pg change nodes", pg, pg.sqlChangeNodes(), `SELECT DISTINCT node_id::text FROM node_version WHERE change_id = $1`)
	checkSQL(t, "sqlite change nodes", lite, lite.sqlChangeNodes(), `SELECT DISTINCT node_id FROM node_version WHERE change_id = ?`)
	checkList("pg DeleteChange rows", pg, pg.deleteChangeRows(), []string{
		`DELETE FROM link WHERE change_id = $1 OR (from_id, from_version) IN (SELECT node_id, version FROM node_version WHERE change_id = $1)`,
		`DELETE FROM node_branch WHERE change_id = $1 OR (node_id, version) IN (SELECT node_id, version FROM node_version WHERE change_id = $1)`,
		`DELETE FROM node_version WHERE change_id = $1`,
		`DELETE FROM change_impact WHERE change_id = $1`,
		`DELETE FROM change_log WHERE change_id = $1`,
		`DELETE FROM change_object WHERE change_id = $1`,
		`DELETE FROM change_request WHERE change_id = $1`,
		`DELETE FROM tag WHERE change_id = $1`,
	})
	checkList("sqlite DeleteChange rows", lite, lite.deleteChangeRows(), []string{
		`DELETE FROM link WHERE change_id = ?1 OR (from_id, from_version) IN (SELECT node_id, version FROM node_version WHERE change_id = ?1)`,
		`DELETE FROM node_branch WHERE change_id = ?1 OR (node_id, version) IN (SELECT node_id, version FROM node_version WHERE change_id = ?1)`,
		`DELETE FROM node_version WHERE change_id = ?1`,
		`DELETE FROM change_impact WHERE change_id = ?1`,
		`DELETE FROM change_log WHERE change_id = ?1`,
		`DELETE FROM change_object WHERE change_id = ?1`,
		`DELETE FROM change_request WHERE change_id = ?1`,
		`DELETE FROM tag WHERE change_id = ?1`,
	})
	checkSQL(t, "pg orphan", pg, pg.sqlDeleteOrphanNode(), `DELETE FROM node WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM node_version WHERE node_id = $1)`)
	checkSQL(t, "sqlite orphan", lite, lite.sqlDeleteOrphanNode(), `DELETE FROM node WHERE id = ?1 AND NOT EXISTS (SELECT 1 FROM node_version WHERE node_id = ?1)`)
	checkSQL(t, "pg latest", pg, pg.sqlRefreshLatest(), `UPDATE node SET latest = (SELECT max(version) FROM node_version WHERE node_id = $1) WHERE id = $1`)
	checkSQL(t, "sqlite latest", lite, lite.sqlRefreshLatest(), `UPDATE node SET latest = (SELECT max(version) FROM node_version WHERE node_id = ?1) WHERE id = ?1`)
	checkSQL(t, "pg branch delete", pg, pg.sqlDeleteBranch(), `DELETE FROM branch WHERE namespace = $1 AND name = $2`)
	checkSQL(t, "sqlite branch delete", lite, lite.sqlDeleteBranch(), `DELETE FROM branch WHERE namespace = ? AND name = ?`)
	checkSQL(t, "pg change delete", pg, pg.sqlDeleteChange(), `DELETE FROM change WHERE id = $1`)
	checkSQL(t, "sqlite change delete", lite, lite.sqlDeleteChange(), `DELETE FROM change WHERE id = ?`)
}

// normSeq makes the seq argument comparable whatever its integer type.
func normSeq(a []any) []any {
	out := make([]any, len(a))
	for i, v := range a {
		switch n := v.(type) {
		case int:
			out[i] = int64(n)
		case int64:
			out[i] = n
		default:
			out[i] = v
		}
	}
	return out
}

// The placeholders of the two dialects differ and nothing else a caller sees.
func TestDialectPlaceholders(t *testing.T) {
	if got := dialectPG.ph(3); got != "$3" {
		t.Errorf("pg ph = %q", got)
	}
	if got := dialectSQLite.ph(3); got != "?3" {
		t.Errorf("sqlite ph = %q", got)
	}
}
