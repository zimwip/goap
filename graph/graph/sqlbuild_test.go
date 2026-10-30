package graph

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
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
	goldPGNodeCols     = `n.id::text, v.version, n.namespace, n.key, n.type, v.props, v.deleted, v.change_id::text, v.created_at, v.branch, v.parents, v.reason, v.state, v.change_impact::text, v.comment, v.execution, v.owner, n.project, v.origins, n.creator, n.creator_unit, v.editor, v.visibility, v.log_seq`
	goldSQLiteNodeCols = `n.id, v.version, n.namespace, n.key, n.type, v.props, v.deleted, v.change_id, v.created_at, v.branch, v.parents, v.reason, v.state, v.change_impact, v.comment, v.execution, v.owner, n.project, v.origins, n.creator, n.creator_unit, v.editor, v.visibility, v.log_seq`
	goldPGOnBranch     = `(v.branch = $2 OR EXISTS (SELECT 1 FROM node_branch j WHERE j.node_id = v.node_id AND j.version = v.version AND j.branch = $2))`
	goldSQLiteOnBranch = `(v.branch = ? OR EXISTS (SELECT 1 FROM node_branch j WHERE j.node_id = v.node_id AND j.version = v.version AND j.branch = ?))`
	goldPGImpactCols   = `id::text, node_id::text, key, type, intent, rationale, pre_version, post_version, committed_version, review, reviews, COALESCE(via::text, ''), recheck, produced_by, derived_from, items, execution, created_at, flow, superseded, effect`
	goldSQLiteImpact   = `id, node_id, key, type, intent, rationale, pre_version, post_version, committed_version, review, reviews, COALESCE(via, ''), recheck, produced_by, derived_from, items, execution, created_at, flow, superseded, effect`
)

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
	check("pg LatestOn", pg, q, a, `SELECT `+goldPGNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = $1 AND v.version = (SELECT v.version FROM node_version v WHERE v.node_id = $1 AND v.committed BETWEEN (SELECT min(k.seq) FROM branch_commit k WHERE k.branch = $2) AND (SELECT max(k.seq) FROM branch_commit k WHERE k.branch = $2) AND EXISTS (SELECT 1 FROM branch_commit j WHERE j.branch = $2 AND j.seq = v.committed OFFSET 0) ORDER BY v.committed DESC, v.version DESC LIMIT 1)`, []any{"n1", "main"})
	q, a = lite.sqlLatestOn("n1", "")
	check("sqlite LatestOn", lite, q, a, `SELECT `+goldSQLiteNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = ? AND v.version = (SELECT v.version FROM node_version v WHERE v.node_id = ? AND v.committed BETWEEN (SELECT min(k.seq) FROM branch_commit k WHERE k.branch = ?) AND (SELECT max(k.seq) FROM branch_commit k WHERE k.branch = ?) AND EXISTS (SELECT 1 FROM branch_commit j WHERE j.branch = ? AND j.seq = v.committed) ORDER BY v.committed DESC, v.version DESC LIMIT 1)`, []any{"n1", "main"})

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
		q, a = d.sqlVersionBranches("n1", 3)
		p, p2, p3 := "?", "?", "?"
		cols := goldSQLiteNodeCols
		if d.pg {
			p, p2, p3, cols = "$1", "$2", "$3", goldPGNodeCols
		}
		check("VersionBranches", d, q, a, `SELECT v.version, j.branch FROM node_version v JOIN branch_commit j ON j.seq = v.committed WHERE v.node_id = `+p+` AND v.version >= `+p2+` AND j.branch <> v.branch ORDER BY j.branch`, []any{"n1", 3})
		q, a = d.sqlVersionRange("n1", 0, 1)
		check("VersionRange latest", d, q, a, `SELECT `+cols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = `+p+` ORDER BY v.version DESC LIMIT `+p2, []any{"n1", 1})
		q, a = d.sqlVersionRange("n1", 7, 20)
		check("VersionRange page", d, q, a, `SELECT `+cols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = `+p+` AND v.version < `+p2+` ORDER BY v.version DESC LIMIT `+p3, []any{"n1", 7, 20})
		q, a = d.sqlVersionJoins("n1")
		cid := "j.change_id"
		if d.pg {
			cid = "j.change_id::text"
		}
		check("VersionJoins", d, q, a, `SELECT v.version, j.branch, `+cid+`, j.log_seq FROM node_version v JOIN branch_commit j ON j.seq = v.committed WHERE v.node_id = `+p+` AND j.branch <> v.branch ORDER BY v.version, j.branch`, []any{"n1"})
	}

	// NodeIDByKey
	q, a = pg.sqlNodeIDByKey("", "k")
	check("pg NodeIDByKey", pg, q, a, `SELECT id::text FROM node WHERE namespace = $1 AND key = $2`, []any{"default", "k"})
	q, a = lite.sqlNodeIDByKey("", "k")
	check("sqlite NodeIDByKey", lite, q, a, `SELECT id FROM node WHERE namespace = ? AND key = ?`, []any{"default", "k"})

	// NodesAt
	refs := []domain.NodeRef{{ID: "n1", Version: 2}, {ID: "n2", Version: 1}}
	q, a = pg.sqlNodesAt(refs, "t")
	check("pg NodesAt", pg, q, a, `SELECT `+goldPGNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id JOIN unnest($1::uuid[], $2::int[]) AS r(id, version) ON r.id = v.node_id AND r.version = v.version
		      WHERE ($3 = '' OR n.type = $3) ORDER BY n.key`, []any{[]string{"n1", "n2"}, []int32{2, 1}, "t"})
	q, a = lite.sqlNodesAt(refs, "t")
	check("sqlite NodesAt", lite, q, a, `SELECT `+goldSQLiteNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id JOIN json_each(?) r ON json_extract(r.value, '$[0]') = v.node_id AND json_extract(r.value, '$[1]') = v.version
		      WHERE (? = '' OR n.type = ?) ORDER BY n.key`, []any{`[["n1",2],["n2",1]]`, "t"})

	// The state of a commit (ADR 0143): the versions committed on its branch
	ids := []domain.NodeID{"n1", "n2"}
	committed := func(id, ns string) string {
		return `SELECT ` + id + `, version, deleted, reason, branch FROM (
		SELECT v.node_id, v.version, v.deleted, v.reason, v.branch, row_number() OVER (PARTITION BY v.node_id ORDER BY v.version DESC) AS rn
		FROM branch_commit j JOIN node_version v ON v.committed = j.seq JOIN node n ON n.id = v.node_id
		WHERE j.branch = $1 AND j.at > $2 AND j.at <= $3 AND n.namespace = ` + ns + `
	) y WHERE rn = 1`
	}
	q, a = pg.sqlCommittedVersions("", "main", 3, 9, nil)
	check("pg CommittedVersions", pg, q, a, committed("node_id::text", "$4"), []any{"main", int64(3), int64(9), "default"})
	q, a = pg.sqlCommittedVersions("", "main", 3, 9, ids)
	check("pg CommittedVersions ids", pg, q, a, `/* probes */ WITH x AS MATERIALIZED ( SELECT q.id AS id, (SELECT v.version FROM node_version v WHERE v.node_id = q.id AND v.committed BETWEEN (SELECT min(k.seq) FROM branch_commit k WHERE k.branch = $1) AND (SELECT max(k.seq) FROM branch_commit k WHERE k.branch = $1) AND (SELECT j.at FROM branch_commit j WHERE j.branch = $1 AND j.seq = v.committed) BETWEEN $2 + 1 AND $3 ORDER BY v.committed DESC, v.version DESC LIMIT 1) AS version FROM unnest((SELECT $5::uuid[])) AS q(id) WHERE (SELECT n.namespace FROM node n WHERE n.id = q.id) = $4 ) SELECT x.id::text, x.version, w.deleted, w.reason, w.branch FROM x JOIN node_version w ON w.node_id = x.id AND w.version = x.version`, []any{"main", int64(3), int64(9), "default", []string{"n1", "n2"}})
	q, a = lite.sqlCommittedVersions("", "main", 3, 9, ids)
	check("sqlite CommittedVersions ids", lite, q, a, `WITH x AS ( SELECT q.value AS id, (SELECT v.version FROM node_version v WHERE v.node_id = q.value AND v.committed BETWEEN (SELECT min(k.seq) FROM branch_commit k WHERE k.branch = ?) AND (SELECT max(k.seq) FROM branch_commit k WHERE k.branch = ?) AND (SELECT j.at FROM branch_commit j WHERE j.branch = ? AND j.seq = v.committed) BETWEEN ? + 1 AND ? ORDER BY v.committed DESC, v.version DESC LIMIT 1) AS version FROM json_each(?) q WHERE (SELECT n.namespace FROM node n WHERE n.id = q.value) = ? ) SELECT x.id, x.version, w.deleted, w.reason, w.branch FROM x JOIN node_version w ON w.node_id = x.id AND w.version = x.version`,
		[]any{"main", int64(3), int64(9), "default", `["n1","n2"]`})
	q, a = pg.sqlCommitHeader("b1")
	check("pg CommitHeader", pg, q, a, `SELECT id::text, name, coalesce(parent_id::text, ''), coalesce(merged_from::text, ''), coalesce(change_id::text, ''), created_at, branch, namespace, kind, coalesce(rebased_from::text, ''), seq, coalesce(fork_commit::text, '') FROM commits WHERE id = $1`, []any{"b1"})
	q, a = lite.sqlCommitHeader("b1")
	check("sqlite CommitHeader", lite, q, a, `SELECT id, name, coalesce(parent_id, ''), coalesce(merged_from, ''), coalesce(change_id, ''), created_at, branch, namespace, kind, coalesce(rebased_from, ''), seq, coalesce(fork_commit, '') FROM commits WHERE id = ?`, []any{"b1"})
	q, a = pg.sqlLastCommit("change-1")
	check("pg LastCommit", pg, q, a, `SELECT coalesce(max(at), 0) FROM branch_commit WHERE branch = $1`, []any{"change-1"})
	q, a = lite.sqlLastCommit("change-1")
	check("sqlite LastCommit", lite, q, a, `SELECT coalesce(max(at), 0) FROM branch_commit WHERE branch = ?`, []any{"change-1"})
	q, a = pg.sqlBranchNodeIDs("change-1", "main")
	check("pg BranchNodeIDs", pg, q, a, `SELECT DISTINCT v.node_id::text FROM branch_commit j CROSS JOIN LATERAL (SELECT v.node_id FROM node_version v WHERE v.committed = j.seq OFFSET 0) v WHERE j.branch = $1 AND NOT EXISTS (SELECT 1 FROM branch_commit e WHERE e.branch = $2 AND e.seq = j.seq OFFSET 0)`, []any{"change-1", "main"})
	q, a = lite.sqlBranchNodeIDs("change-1", "")
	check("sqlite BranchNodeIDs", lite, q, a, `SELECT DISTINCT v.node_id FROM branch_commit j JOIN node_version v ON v.committed = j.seq WHERE j.branch = ? AND NOT EXISTS (SELECT 1 FROM branch_commit e WHERE e.branch = ? AND e.seq = j.seq)`, []any{"change-1", ""})
	q, _ = pg.sqlTakeCommits("change-1", "c", 9, "default", "main", 3, 8)
	checkSQL(t, "pg TakeCommits", pg, q, `WITH n AS MATERIALIZED (SELECT nextval('commit_seq') AS at) INSERT INTO branch_commit (branch, seq, at, change_id, log_seq) SELECT $1, j.seq, n.at, $2, $3 FROM n, branch_commit j JOIN commits c ON c.seq = j.seq WHERE j.branch = $4 AND j.at > $5 AND j.at <= $6 AND c.namespace = $7 ON CONFLICT DO NOTHING`)
	q, a = pg.sqlPosition("change-1", "main", 7)
	check("pg Position", pg, q, a, `SELECT coalesce(max(x), 0) FROM ( SELECT (SELECT p.at FROM branch_commit p WHERE p.branch = $2 AND p.seq = b.seq) AS x FROM branch_commit b WHERE b.branch = $1 UNION ALL SELECT (SELECT p.at FROM branch_commit p WHERE p.branch = $2 AND p.seq = $3) ) y`, []any{"change-1", "main", int64(7)})
	q, a = lite.sqlPosition("change-1", "main", 7)
	check("sqlite Position", lite, q, a, `SELECT coalesce(max(x), 0) FROM ( SELECT (SELECT p.at FROM branch_commit p WHERE p.branch = ? AND p.seq = b.seq) AS x FROM branch_commit b WHERE b.branch = ? UNION ALL SELECT (SELECT p.at FROM branch_commit p WHERE p.branch = ? AND p.seq = ?) ) y`, []any{"change-1", "main", int64(7)})
	q, a = pg.sqlSharedVersions("", "change-1", "main", ids)
	check("pg SharedVersions ids", pg, q, a, `/* probes */ WITH x AS MATERIALIZED ( SELECT q.id AS id, (SELECT v.version FROM node_version v WHERE v.node_id = q.id AND v.committed BETWEEN (SELECT min(k.seq) FROM branch_commit k WHERE k.branch = $1) AND (SELECT max(k.seq) FROM branch_commit k WHERE k.branch = $1) AND EXISTS (SELECT 1 FROM branch_commit j WHERE j.branch = $1 AND j.seq = v.committed OFFSET 0) AND EXISTS (SELECT 1 FROM branch_commit j WHERE j.branch = $2 AND j.seq = v.committed OFFSET 0) ORDER BY v.committed DESC, v.version DESC LIMIT 1) AS version FROM unnest((SELECT $4::uuid[])) AS q(id) WHERE (SELECT n.namespace FROM node n WHERE n.id = q.id) = $3 ) SELECT x.id::text, x.version, w.deleted, w.reason, w.branch FROM x JOIN node_version w ON w.node_id = x.id AND w.version = x.version`, []any{"change-1", "main", "default", []string{"n1", "n2"}})
	q, a = lite.sqlSharedVersions("", "change-1", "main", nil)
	check("sqlite SharedVersions", lite, q, a, `SELECT node_id, version, deleted, reason, branch FROM ( SELECT v.node_id, v.version, v.deleted, v.reason, v.branch, row_number() OVER (PARTITION BY v.node_id ORDER BY v.version DESC) AS rn FROM branch_commit j JOIN branch_commit o ON o.branch = ? AND o.seq = j.seq JOIN node_version v ON v.committed = j.seq JOIN node n ON n.id = v.node_id WHERE j.branch = ? AND n.namespace = ? ) y WHERE rn = 1`, []any{"change-1", "main", "default"})
	q, a = pg.sqlNamespaceNodeIDs("alm")
	check("pg NamespaceNodeIDs", pg, q, a, `SELECT id::text FROM node WHERE namespace = $1 ORDER BY id`, []any{"alm"})
	q, a = lite.sqlNamespaceNodeIDs("alm")
	check("sqlite NamespaceNodeIDs", lite, q, a, `SELECT id FROM node WHERE namespace = ? ORDER BY id`, []any{"alm"})
	q, a = pg.sqlNodeIDsOfType("alm", "alm@R", "", 10)
	check("pg NodeIDsOfType", pg, q, a, `SELECT id::text FROM node WHERE namespace = $1 AND type = $2 ORDER BY id LIMIT $3`, []any{"alm", "alm@R", 10})
	q, a = lite.sqlNodeIDsOfType("alm", "alm@R", "n1", 10)
	check("sqlite NodeIDsOfType", lite, q, a, `SELECT id FROM node WHERE namespace = ?1 AND type = ?2 AND id > ?4 ORDER BY id LIMIT ?3`, []any{"alm", "alm@R", 10, "n1"})

	// Pages of a state (ADR 0140): filtered, counted and paged by the store
	pgRefs := []any{[]string{"n1", "n2"}, []int32{2, 1}}
	const liteRefs = `[["n1",2],["n2",1]]`
	const pgNodeWhere = ` FROM node n JOIN node_version v ON v.node_id = n.id JOIN unnest($1::uuid[], $2::int[]) AS r(id, version) ON r.id = v.node_id AND r.version = v.version WHERE ($3 = '' OR lower(n.key) LIKE $3 ESCAPE '\' OR lower(n.type) LIKE $3 ESCAPE '\' OR EXISTS (SELECT 1 FROM jsonb_each(v.props) p WHERE jsonb_typeof(p.value) = 'string' AND lower(p.value #>> '{}') LIKE $3 ESCAPE '\')) AND ($4 OR NOT v.deleted)`
	const liteNodeWhere = ` FROM node n JOIN node_version v ON v.node_id = n.id JOIN json_each(?) r ON json_extract(r.value, '$[0]') = v.node_id AND json_extract(r.value, '$[1]') = v.version WHERE (? = '' OR lower(n.key) LIKE ? ESCAPE '\' OR lower(n.type) LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM json_each(v.props) p WHERE p.type = 'text' AND lower(p.value) LIKE ? ESCAPE '\')) AND (? OR NOT v.deleted)`
	nq := NodeQuery{Type: "t", Text: " A%b ", Offset: 10, Limit: 5}
	q, a = pg.sqlNodePage(refs, nq)
	check("pg NodePage", pg, q, a, `SELECT `+goldPGNodeCols+pgNodeWhere+` AND ($5 = '' OR n.type = $5) ORDER BY n.key COLLATE "C" LIMIT $6 OFFSET $7`, append(pgRefs, `%a\%b%`, false, "t", 5, 10))
	q, a = lite.sqlNodePage(refs, nq)
	check("sqlite NodePage", lite, q, a, `SELECT `+goldSQLiteNodeCols+liteNodeWhere+` AND (? = '' OR n.type = ?) ORDER BY n.key LIMIT ? OFFSET ?`, []any{liteRefs, `%a\%b%`, false, "t", 5, 10})
	q, a = pg.sqlNodeTypeCounts(refs, NodeQuery{IncludeDeleted: true})
	check("pg NodeTypeCounts", pg, q, a, `SELECT n.type, count(*)`+pgNodeWhere+` GROUP BY n.type`, append(pgRefs, "", true))
	q, a = lite.sqlNodeTypeCounts(refs, NodeQuery{IncludeDeleted: true})
	check("sqlite NodeTypeCounts", lite, q, a, `SELECT n.type, count(*)`+liteNodeWhere+` GROUP BY n.type`, []any{liteRefs, "", true})
	const pgLinkCols = `l.id::text, l.type, l.from_id::text, l.from_version, coalesce(l.to_id::text, ''), coalesce(l.to_version, 0), l.props, l.change_id::text, l.to_url`
	const liteLinkCols = `l.id, l.type, l.from_id, l.from_version, coalesce(l.to_id, ''), coalesce(l.to_version, 0), l.props, l.change_id, l.to_url`
	heldJoin := func(refs, join string) string {
		return `WITH RECURSIVE r(id, version) AS (` + refs + `), c(nid, rv, sv) AS ( SELECT v.node_id, v.version, coalesce(v.links, v.version) FROM node_version v JOIN r ON r.id = v.node_id AND r.version = v.version UNION ALL SELECT c.nid, c.rv, s.links_base FROM c JOIN node_version s ON s.node_id = c.nid AND s.version = c.sv WHERE s.links_depth > 0 ), h AS (SELECT l.id, l.type, l.from_id, c.rv, l.to_id, l.to_version, l.props, l.change_id, l.removes, l.to_url FROM c` + join + `link l ON l.from_id = c.nid AND l.from_version = c.sv) SELECT h.id, h.type, h.from_id, h.rv AS from_version, h.to_id, h.to_version, h.props, h.change_id, h.to_url FROM h WHERE h.removes IS NULL AND (h.id, h.from_id, h.rv) NOT IN (SELECT x.removes, x.from_id, x.rv FROM h x WHERE x.removes IS NOT NULL)`
	}
	held := func(refs string) string {
		if strings.Contains(refs, "json_each") {
			return heldJoin(refs, " CROSS JOIN ") // SQLite reads the chain first
		}
		return heldJoin(refs, " JOIN ")
	}
	pgHeld, liteHeld := held(`SELECT * FROM unnest($1::uuid[], $2::int[])`), held(`SELECT json_extract(value, '$[0]'), json_extract(value, '$[1]') FROM json_each(?)`)
	pgLinkWhere := ` FROM (` + pgHeld + `) l JOIN unnest($1::uuid[], $2::int[]) AS t(id, version) ON t.id = l.to_id AND t.version = l.to_version WHERE ($3 = '' OR lower(l.type) LIKE $3 ESCAPE '\' OR EXISTS (SELECT 1 FROM jsonb_each(l.props) p WHERE jsonb_typeof(p.value) = 'string' AND lower(p.value #>> '{}') LIKE $3 ESCAPE '\'))`
	liteLinkWhere := ` FROM (` + liteHeld + `) l JOIN json_each(?) t ON json_extract(t.value, '$[0]') = l.to_id AND json_extract(t.value, '$[1]') = l.to_version WHERE (? = '' OR lower(l.type) LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM json_each(l.props) p WHERE p.type = 'text' AND lower(p.value) LIKE ? ESCAPE '\'))`
	lq := LinkQuery{Type: "t", Text: "x", Offset: 2, Limit: 3}
	q, a = pg.sqlLinkPage(refs, lq)
	check("pg LinkPage", pg, q, a, `SELECT `+pgLinkCols+pgLinkWhere+` AND ($4 = '' OR l.type = $4) ORDER BY l.type COLLATE "C", l.id LIMIT $5 OFFSET $6`, append(pgRefs, "%x%", "t", 3, 2))
	q, a = lite.sqlLinkPage(refs, lq)
	check("sqlite LinkPage", lite, q, a, `SELECT `+liteLinkCols+liteLinkWhere+` AND (? = '' OR l.type = ?) ORDER BY l.type, l.id LIMIT ? OFFSET ?`, []any{liteRefs, "%x%", "t", 3, 2})
	q, a = pg.sqlLinkTypeCounts(refs, LinkQuery{})
	check("pg LinkTypeCounts", pg, q, a, `SELECT l.type, count(*)`+pgLinkWhere+` GROUP BY l.type`, append(pgRefs, ""))
	q, a = lite.sqlLinkTypeCounts(refs, LinkQuery{})
	check("sqlite LinkTypeCounts", lite, q, a, `SELECT l.type, count(*)`+liteLinkWhere+` GROUP BY l.type`, []any{liteRefs, ""})
	q, a = pg.sqlLinksFrom(refs)
	check("pg LinksFrom", pg, q, a, `SELECT `+pgLinkCols+` FROM (`+pgHeld+`) l ORDER BY l.id`, pgRefs)
	q, a = lite.sqlLinksFrom(refs)
	check("sqlite LinksFrom", lite, q, a, `SELECT `+liteLinkCols+` FROM (`+liteHeld+`) l ORDER BY l.id`, []any{liteRefs})
	pgTyped := strings.Replace(pgHeld, `l.from_version = c.sv)`, `l.from_version = c.sv AND l.type = ANY($3::text[]))`, 1)
	liteTyped := strings.Replace(liteHeld, `l.from_version = c.sv)`, `l.from_version = c.sv AND l.type IN (SELECT value FROM json_each(?)))`, 1)
	q, a = pg.sqlLinksFromOfType(refs, []string{"child"})
	check("pg LinksFromOfType", pg, q, a, `SELECT `+pgLinkCols+` FROM (`+pgTyped+`) l ORDER BY l.id`, append(pgRefs, []string{"child"}))
	q, a = lite.sqlLinksFromOfType(refs, []string{"child"})
	check("sqlite LinksFromOfType", lite, q, a, `SELECT `+liteLinkCols+` FROM (`+liteTyped+`) l ORDER BY l.id`, []any{liteRefs, `["child"]`})
	q, a = pg.sqlLinksFromInto(refs, []domain.NodeID{"n1"}, []string{"child"})
	check("pg LinksFromInto", pg, q, a, `SELECT `+pgLinkCols+` FROM (`+strings.Replace(pgHeld, `l.from_version = c.sv)`, `l.from_version = c.sv AND l.to_id = ANY($3::uuid[]) AND l.type = ANY($4::text[]))`, 1)+`) l ORDER BY l.id`, append(pgRefs, []string{"n1"}, []string{"child"}))
	q, a = lite.sqlLinksFromInto(refs, []domain.NodeID{"n1"}, nil)
	check("sqlite LinksFromInto", lite, q, a, `SELECT `+liteLinkCols+` FROM (`+strings.Replace(liteHeld, `l.from_version = c.sv)`, `l.from_version = c.sv AND l.to_id IN (SELECT value FROM json_each(?)))`, 1)+`) l ORDER BY l.id`, []any{liteRefs, `["n1"]`})
	q, a = pg.sqlLinkSources([]domain.NodeID{"n1"}, []string{"child"}, nil)
	check("pg LinkSources", pg, q, a, `SELECT DISTINCT to_id::text, from_id::text FROM link WHERE to_id = ANY($1::uuid[]) AND type = ANY($2::text[]) AND removes IS NULL`, []any{[]string{"n1"}, []string{"child"}})
	q, a = lite.sqlLinkSources([]domain.NodeID{"n1"}, nil, nil)
	check("sqlite LinkSources", lite, q, a, `SELECT DISTINCT to_id, from_id FROM link WHERE to_id IN (SELECT value FROM json_each(?)) AND removes IS NULL`, []any{`["n1"]`})
	// LinksEnteringAt (ADR 0160): the sources, their selection on the branch and the links their versions hold, in one
	// statement
	// the versions selected are kept (MATERIALIZED) and read first
	entering := func(lite bool, pre, rows, and string) string {
		join := " JOIN "
		if lite {
			join = " CROSS JOIN "
		}
		h := strings.Replace(heldJoin(rows, join), `WITH RECURSIVE r(id, version) AS (`, `WITH RECURSIVE `+pre+`r(id, version) AS MATERIALIZED (`, 1)
		h = strings.Replace(h, `FROM node_version v JOIN r ON`, `FROM r`+join+`node_version v ON`, 1)
		return strings.Replace(h, `l.from_version = c.sv)`, `l.from_version = c.sv`+and+`)`, 1)
	}
	pgPick := func(b, u string) string {
		return `(SELECT w.version FROM node_version w WHERE w.node_id = s.id AND w.version = (SELECT v.version FROM node_version v WHERE v.node_id = s.id AND v.committed BETWEEN (SELECT min(k.seq) FROM branch_commit k WHERE k.branch = ` + b + `) AND (SELECT max(k.seq) FROM branch_commit k WHERE k.branch = ` + b + `) AND (SELECT j.at FROM branch_commit j WHERE j.branch = ` + b + ` AND j.seq = v.committed) BETWEEN $3 + 1 AND ` + u + ` ORDER BY v.committed DESC, v.version DESC LIMIT 1) AND NOT (w.reason = 'revert' AND w.branch = ` + b + `))`
	}
	q, a = pg.sqlLinksEnteringAt("ns", []selectLevel{{Branch: "c1", UpTo: 9}, {Branch: "", UpTo: 7}}, []domain.NodeID{"n1"}, []string{"child"}, []domain.NodeID{"d1"})
	check("pg LinksEnteringAt", pg, q, a, `SELECT `+pgLinkCols+` FROM (`+entering(false,
		`s AS MATERIALIZED (SELECT DISTINCT from_id AS id FROM link WHERE to_id = ANY($2::uuid[]) AND type = ANY($4::text[]) AND NOT (from_id = ANY($5::uuid[])) AND removes IS NULL), x AS MATERIALIZED (SELECT s.id, coalesce(`+pgPick("$6", "$7")+`, `+pgPick("$8", "$9")+`) AS version FROM s WHERE (SELECT n.namespace FROM node n WHERE n.id = s.id) = $1), `,
		`SELECT x.id, x.version FROM x JOIN node_version w ON w.node_id = x.id AND w.version = x.version WHERE NOT w.deleted`,
		` AND l.to_id = ANY($2::uuid[]) AND l.type = ANY($4::text[])`)+`) l ORDER BY l.id`,
		[]any{"ns", []string{"n1"}, int64(0), []string{"child"}, []string{"d1"}, "c1", int64(9), "main", int64(7)})
	litePick := `(SELECT w.version FROM node_version w WHERE w.node_id = s.id AND w.version = (SELECT v.version FROM node_version v WHERE v.node_id = s.id AND v.committed BETWEEN (SELECT min(k.seq) FROM branch_commit k WHERE k.branch = ?) AND (SELECT max(k.seq) FROM branch_commit k WHERE k.branch = ?) AND (SELECT j.at FROM branch_commit j WHERE j.branch = ? AND j.seq = v.committed) BETWEEN ? + 1 AND ? ORDER BY v.committed DESC, v.version DESC LIMIT 1) AND NOT (w.reason = 'revert' AND w.branch = ?))`
	liteRows := `SELECT x.id, x.version FROM x CROSS JOIN node_version w ON w.node_id = x.id AND w.version = x.version WHERE NOT w.deleted`
	// SQLite: lists joined first (json_each), one value sought
	q, a = lite.sqlLinksEnteringAt("ns", []selectLevel{{Branch: "", UpTo: 7}}, []domain.NodeID{"n1", "n2"}, []string{"child", "ref"}, nil)
	check("sqlite LinksEnteringAt", lite, q, a, `SELECT `+liteLinkCols+` FROM (`+entering(true,
		`s AS MATERIALIZED (SELECT DISTINCT k.from_id AS id FROM json_each(?) q CROSS JOIN json_each(?) t CROSS JOIN link k WHERE k.to_id = q.value AND k.type = t.value AND k.removes IS NULL), x AS MATERIALIZED (SELECT s.id, `+litePick+` AS version FROM s WHERE (SELECT n.namespace FROM node n WHERE n.id = s.id) = ?), `,
		liteRows, ` AND l.to_id IN (SELECT value FROM json_each(?)) AND l.type IN (SELECT value FROM json_each(?))`)+`) l ORDER BY l.id`,
		[]any{"ns", `["n1","n2"]`, int64(0), `["child","ref"]`, "main", int64(7)})
	q, a = lite.sqlLinksEnteringAt("ns", []selectLevel{{Branch: "", UpTo: 7}}, []domain.NodeID{"n1"}, []string{"child"}, nil)
	check("sqlite LinksEnteringAt one", lite, q, a, `SELECT `+liteLinkCols+` FROM (`+entering(true,
		`s AS MATERIALIZED (SELECT DISTINCT k.from_id AS id FROM link k WHERE k.to_id = ? AND k.type = ? AND k.removes IS NULL), x AS MATERIALIZED (SELECT s.id, `+litePick+` AS version FROM s WHERE (SELECT n.namespace FROM node n WHERE n.id = s.id) = ?), `,
		liteRows, ` AND l.to_id = ? AND l.type = ?`)+`) l ORDER BY l.id`,
		[]any{"ns", "n1", int64(0), "child", "main", int64(7)})
	// a page of the sources of the links entering one node: how many sources and the last of the page, then the links
	q, a = pg.sqlLinksEnteringPage("ns", []selectLevel{{Branch: "", UpTo: 7}}, "n1", []string{"ref"}, nil, sourcePage{After: "a1", Limit: 10})
	ctes, final, _ := strings.Cut(entering(false,
		`s AS MATERIALIZED (SELECT DISTINCT from_id AS id FROM link WHERE to_id = ANY($2::uuid[]) AND type = ANY($4::text[]) AND from_id > $5::uuid AND removes IS NULL ORDER BY 1 LIMIT $6), s1 AS (SELECT id FROM s ORDER BY id LIMIT $7), x AS MATERIALIZED (SELECT s.id, `+pgPick("$8", "$9")+` AS version FROM s1 s WHERE (SELECT n.namespace FROM node n WHERE n.id = s.id) = $1), `,
		`SELECT x.id, x.version FROM x JOIN node_version w ON w.node_id = x.id AND w.version = x.version WHERE NOT w.deleted`,
		` AND l.to_id = ANY($2::uuid[]) AND l.type = ANY($4::text[])`), `) SELECT h.id`)
	check("pg LinksEnteringPage", pg, q, a, ctes+`), l AS (SELECT h.id`+final+`) SELECT k.n, coalesce(k.last, ''), `+pgLinkCols+` FROM (SELECT (SELECT count(*) FROM s) AS n, (SELECT id::text FROM s1 ORDER BY id DESC LIMIT 1) AS last) k LEFT JOIN l ON 1 = 1 ORDER BY l.id`,
		[]any{"ns", []string{"n1"}, int64(0), []string{"ref"}, "a1", 11, 10, "main", int64(7)})
	q, a = lite.sqlLinkSources([]domain.NodeID{"n1"}, nil, &sourcePage{After: "a1", Limit: 10})
	check("sqlite LinkSources page", lite, q, a, `SELECT DISTINCT to_id, from_id FROM link WHERE to_id IN (SELECT value FROM json_each(?)) AND removes IS NULL AND from_id > ? ORDER BY 2 LIMIT ?`, []any{`["n1"]`, "a1", 11})
	linksTo := func(cols, target string) string {
		return `WITH RECURSIVE t AS (SELECT id, type, from_id, from_version, to_id, to_version, props, change_id, to_url FROM link WHERE ` + target + ` AND removes IS NULL), s(lid, nid, sv) AS ( SELECT id, from_id, from_version FROM t UNION ALL SELECT s.lid, v.node_id, v.version FROM s JOIN node_version v ON v.node_id = s.nid AND v.links_base = s.sv WHERE NOT EXISTS (SELECT 1 FROM link x WHERE x.from_id = v.node_id AND x.from_version = v.version AND x.removes = s.lid) ) SELECT ` + cols + ` FROM s JOIN t ON t.id = s.lid JOIN node_version v ON v.node_id = s.nid AND v.version = s.sv AND v.links IS NULL UNION ALL SELECT ` + cols + ` FROM s JOIN t ON t.id = s.lid JOIN node_version v ON v.links = s.sv AND v.node_id = s.nid ORDER BY 1, 4`
	}
	const pgToCols = `t.id::text, t.type, t.from_id::text, v.version, t.to_id::text, t.to_version, t.props, t.change_id::text, t.to_url`
	const liteToCols = `t.id, t.type, t.from_id, v.version, t.to_id, t.to_version, t.props, t.change_id, t.to_url`
	q, a = pg.sqlLinksTo(domain.NodeRef{ID: "n1"})
	check("pg LinksTo", pg, q, a, linksTo(pgToCols, `to_id = $1`), []any{"n1"})
	q, a = lite.sqlLinksTo(domain.NodeRef{ID: "n1"})
	check("sqlite LinksTo", lite, q, a, linksTo(liteToCols, `to_id = ?`), []any{"n1"})
	q, a = pg.sqlLinksTo(ref)
	check("pg InLinks", pg, q, a, linksTo(pgToCols, `to_id = $1 AND to_version = $2`), []any{"n1", 3})
	q, a = lite.sqlLinksTo(ref)
	check("sqlite InLinks", lite, q, a, linksTo(liteToCols, `to_id = ? AND to_version = ?`), []any{"n1", 3})
	q, a = pg.sqlLinksToOfType(ref, []string{"child"})
	check("pg LinksToOfType", pg, q, a, linksTo(pgToCols, `to_id = $1 AND to_version = $2 AND type = ANY($3::text[])`), []any{"n1", 3, []string{"child"}})
	q, a = lite.sqlLinksToOfType(ref, []string{"child"})
	check("sqlite LinksToOfType", lite, q, a, linksTo(liteToCols, `to_id = ? AND to_version = ? AND type IN (SELECT value FROM json_each(?))`), []any{"n1", 3, `["child"]`})
	q, a = pg.sqlLinkSetOf(ref)
	check("pg LinkSetOf", pg, q, a, `SELECT coalesce(links, version), coalesce(links_base, 0), links_depth, links_seq FROM node_version WHERE node_id = $1 AND version = $2`, []any{"n1", 3})
	q, a = pg.sqlLinkSetsOf(refs)
	check("pg LinkSetsOf", pg, q, a, `SELECT v.node_id::text, v.version, coalesce(v.links, v.version), coalesce(v.links_base, 0), v.links_depth, v.links_seq FROM node_version v JOIN unnest($1::uuid[], $2::int[]) AS r(id, version) ON r.id = v.node_id AND r.version = v.version`, pgRefs)
	q, a = lite.sqlLinkSetsOf(refs)
	check("sqlite LinkSetsOf", lite, q, a, `SELECT v.node_id, v.version, coalesce(v.links, v.version), coalesce(v.links_base, 0), v.links_depth, v.links_seq FROM node_version v JOIN json_each(?) r ON json_extract(r.value, '$[0]') = v.node_id AND json_extract(r.value, '$[1]') = v.version`, []any{liteRefs})
	linksInto := func(cols, refs string) string {
		return strings.Replace(squash(linksTo(cols, "")), `WITH RECURSIVE t AS (SELECT id, type, from_id, from_version, to_id, to_version, props, change_id, to_url FROM link WHERE AND removes IS NULL)`,
			`WITH RECURSIVE r(id, version) AS (`+refs+`), t AS (SELECT l.id, l.type, l.from_id, l.from_version, l.to_id, l.to_version, l.props, l.change_id, l.to_url FROM link l JOIN r ON l.to_id = r.id AND l.to_version = r.version WHERE l.removes IS NULL)`, 1)
	}
	q, a = pg.sqlLinksInto(refs)
	check("pg LinksInto", pg, q, a, linksInto(pgToCols, `SELECT * FROM unnest($1::uuid[], $2::int[])`), pgRefs)
	q, a = lite.sqlLinksInto(refs)
	check("sqlite LinksInto", lite, q, a, linksInto(liteToCols, `SELECT json_extract(value, '$[0]'), json_extract(value, '$[1]') FROM json_each(?)`), []any{liteRefs})
	q, a = pg.sqlLatestVersions(ids)
	check("pg LatestVersions", pg, q, a, `SELECT node_id::text, max(version) FROM node_version WHERE node_id = ANY($1::uuid[]) GROUP BY node_id`, []any{[]string{"n1", "n2"}})
	q, a = lite.sqlLatestVersions(ids)
	check("sqlite LatestVersions", lite, q, a, `SELECT node_id, max(version) FROM node_version WHERE node_id IN (SELECT value FROM json_each(?)) GROUP BY node_id`, []any{`["n1","n2"]`})
	q, a = pg.sqlLatestVersionsOn(ids, "b")
	check("pg LatestVersionsOn", pg, q, a, `/* probes */ WITH l AS MATERIALIZED (SELECT q.id, (SELECT v.version FROM node_version v WHERE v.node_id = q.id AND v.committed BETWEEN (SELECT min(k.seq) FROM branch_commit k WHERE k.branch = $2) AND (SELECT max(k.seq) FROM branch_commit k WHERE k.branch = $2) AND EXISTS (SELECT 1 FROM branch_commit j WHERE j.branch = $2 AND j.seq = v.committed OFFSET 0) ORDER BY v.committed DESC, v.version DESC LIMIT 1) AS version FROM unnest((SELECT $1::uuid[])) AS q(id)) SELECT id::text, version FROM l WHERE version IS NOT NULL`, []any{[]string{"n1", "n2"}, "b"})
	q, a = lite.sqlLatestVersionsOn(ids, "b")
	check("sqlite LatestVersionsOn", lite, q, a, `SELECT id, version FROM (SELECT q.id, (SELECT v.version FROM node_version v WHERE v.node_id = q.id AND v.committed BETWEEN (SELECT min(k.seq) FROM branch_commit k WHERE k.branch = ?) AND (SELECT max(k.seq) FROM branch_commit k WHERE k.branch = ?) AND EXISTS (SELECT 1 FROM branch_commit j WHERE j.branch = ? AND j.seq = v.committed) ORDER BY v.committed DESC, v.version DESC LIMIT 1) AS version FROM (SELECT value AS id FROM json_each(?)) q) l WHERE version IS NOT NULL`, []any{`["n1","n2"]`, "b"})

	// Branches
	const pgBranchCols = `namespace, name, parent, coalesce(fork_commit::text, ''), coalesce((SELECT h.id::text FROM commits h WHERE h.namespace = branch.namespace AND h.branch = branch.name ORDER BY h.seq DESC LIMIT 1), branch.head_commit::text, ''), origin, status, created_at, description, intent, coalesce(rebased_from::text, ''), ephemeral`
	const liteBranchCols = `namespace, name, parent, coalesce(fork_commit, ''), coalesce((SELECT h.id FROM commits h WHERE h.namespace = branch.namespace AND h.branch = branch.name ORDER BY h.seq DESC LIMIT 1), branch.head_commit, ''), origin, status, created_at, description, intent, coalesce(rebased_from, ''), ephemeral`
	q, a = pg.sqlBranch("", "b")
	check("pg Branch", pg, q, a, `SELECT `+pgBranchCols+` FROM branch WHERE namespace = $1 AND name = $2`, []any{"default", "b"})
	q, a = lite.sqlBranch("", "b")
	check("sqlite Branch", lite, q, a, `SELECT `+liteBranchCols+` FROM branch WHERE namespace = ? AND name = ?`, []any{"default", "b"})
	q, a = pg.sqlBranches("")
	check("pg Branches", pg, q, a, `SELECT `+pgBranchCols+` FROM branch WHERE namespace = $1 ORDER BY created_at`, []any{"default"})
	q, a = lite.sqlBranches("")
	check("sqlite Branches", lite, q, a, `SELECT `+liteBranchCols+` FROM branch WHERE namespace = ? ORDER BY created_at, rowid`, []any{"default"})

	// Commits, changes
	q, a = pg.sqlNamespaceCommitHeaders("")
	check("pg NamespaceCommitHeaders", pg, q, a, `SELECT id::text, name, coalesce(parent_id::text, ''), coalesce(merged_from::text, ''), coalesce(change_id::text, ''), created_at, branch, kind, coalesce(rebased_from::text, ''), seq, coalesce(fork_commit::text, '') FROM commits WHERE namespace = $1 ORDER BY created_at, id`, []any{"default"})
	q, a = lite.sqlNamespaceCommitHeaders("")
	check("sqlite NamespaceCommitHeaders", lite, q, a, `SELECT id, name, coalesce(parent_id, ''), coalesce(merged_from, ''), coalesce(change_id, ''), created_at, branch, kind, coalesce(rebased_from, ''), seq, coalesce(fork_commit, '') FROM commits WHERE namespace = ? ORDER BY created_at, rowid`, []any{"default"})
	const pgHead = `SELECT id::text, name, coalesce(parent_id::text, ''), coalesce(merged_from::text, ''), coalesce(change_id::text, ''), created_at, branch, kind, coalesce(rebased_from::text, ''), seq, coalesce(fork_commit::text, '') FROM commits WHERE namespace = $1`
	const liteHead = `SELECT id, name, coalesce(parent_id, ''), coalesce(merged_from, ''), coalesce(change_id, ''), created_at, branch, kind, coalesce(rebased_from, ''), seq, coalesce(fork_commit, '') FROM commits WHERE namespace = ?`
	q, a = pg.sqlCommitPage("", "", 9, 3)
	check("pg CommitPage", pg, q, a, pgHead+` AND branch = $2 AND ($3 = 0 OR seq < $3) ORDER BY seq DESC LIMIT $4`, []any{"default", "main", int64(9), 3})
	q, a = lite.sqlCommitPage("", "", 9, 3)
	check("sqlite CommitPage", lite, q, a, liteHead+` AND branch = ? AND (? = 0 OR seq < ?) ORDER BY seq DESC LIMIT ?`, []any{"default", "main", int64(9), 3})
	q, a = pg.sqlCommitHeadersOf("", []domain.CommitID{"b1", "b2"})
	check("pg CommitHeadersOf", pg, q, a, pgHead+` AND id = ANY($2::uuid[]) ORDER BY seq`, []any{"default", []string{"b1", "b2"}})
	q, a = lite.sqlCommitHeadersOf("", []domain.CommitID{"b1", "b2"})
	check("sqlite CommitHeadersOf", lite, q, a, liteHead+` AND id IN (SELECT value FROM json_each(?)) ORDER BY seq`, []any{"default", `["b1","b2"]`})
	q, a = pg.sqlChangeCommitHeaders("", []domain.ChangeID{"c1"})
	check("pg ChangeCommitHeaders", pg, q, a, pgHead+` AND change_id = ANY($2::uuid[]) ORDER BY seq`, []any{"default", []string{"c1"}})
	q, a = lite.sqlChangeCommitHeaders("", []domain.ChangeID{"c1"})
	check("sqlite ChangeCommitHeaders", lite, q, a, liteHead+` AND change_id IN (SELECT value FROM json_each(?)) ORDER BY seq`, []any{"default", `["c1"]`})
	q, a = pg.sqlCommitCounts("")
	check("pg CommitCounts", pg, q, a, `SELECT branch, count(*) FROM commits WHERE namespace = $1 GROUP BY branch`, []any{"default"})
	q, a = lite.sqlCommitCounts("")
	check("sqlite CommitCounts", lite, q, a, `SELECT branch, count(*) FROM commits WHERE namespace = ? GROUP BY branch`, []any{"default"})
	q, a = pg.sqlBranchCommitHeaders("", "b1")
	check("pg BranchCommitHeaders", pg, q, a, `SELECT id::text, name, coalesce(parent_id::text, ''), coalesce(merged_from::text, ''), coalesce(change_id::text, ''), created_at, branch, kind, coalesce(rebased_from::text, ''), seq, coalesce(fork_commit::text, '') FROM commits WHERE namespace = $1 AND branch = $2 ORDER BY seq`, []any{"default", "b1"})
	q, a = lite.sqlBranchCommitHeaders("", "b1")
	check("sqlite BranchCommitHeaders", lite, q, a, `SELECT id, name, coalesce(parent_id, ''), coalesce(merged_from, ''), coalesce(change_id, ''), created_at, branch, kind, coalesce(rebased_from, ''), seq, coalesce(fork_commit, '') FROM commits WHERE namespace = ? AND branch = ? ORDER BY seq`, []any{"default", "b1"})
	q, a = pg.sqlCommitIDs("")
	check("pg Commits", pg, q, a, `SELECT id::text FROM commits WHERE namespace = $1 ORDER BY created_at`, []any{"default"})
	q, a = lite.sqlCommitIDs("")
	check("sqlite Commits", lite, q, a, `SELECT id FROM commits WHERE namespace = ? ORDER BY created_at, rowid`, []any{"default"})
	check("pg Changes", pg, pg.sqlChangeIDs(), nil, `SELECT id::text FROM change ORDER BY created_at`, nil)
	check("sqlite Changes", lite, lite.sqlChangeIDs(), nil, `SELECT id FROM change ORDER BY created_at, rowid`, nil)
	hf := ChangesFilter{Namespace: "alm", OwnerOrg: "ORG-A", Project: "PROJ-A", Status: []domain.ChangeStatus{domain.ChangeDraft, domain.ChangeActive}, Query: "ignored"}
	q, a = pg.sqlChangeHeads("id", hf)
	check("pg ChangeHeads", pg, q, a, `SELECT id FROM change WHERE true AND namespace = $1 AND owner_org = $2 AND project_id = $3 AND status IN ($4, $5) ORDER BY created_at`, []any{"alm", "ORG-A", "PROJ-A", "draft", "active"})
	q, a = lite.sqlChangeHeads("id", hf)
	check("sqlite ChangeHeads", lite, q, a, `SELECT id FROM change WHERE 1 = 1 AND namespace = ?1 AND owner_org = ?2 AND project_id = ?3 AND status IN (?4, ?5) ORDER BY created_at, rowid`, []any{"alm", "ORG-A", "PROJ-A", "draft", "active"})
	q, a = pg.sqlChangeHeads("id", ChangesFilter{})
	check("pg ChangeHeads all", pg, q, a, `SELECT id FROM change WHERE true ORDER BY created_at`, []any{})
	df := ChangesFilter{Parent: "c1", DependsOn: "c2"}
	q, a = pg.sqlChangeHeads("id", df)
	check("pg ChangeHeads related", pg, q, a, `SELECT id FROM change WHERE true AND parent_id = $1 AND depends_on ? $2 ORDER BY created_at`, []any{"c1", "c2"})
	q, a = lite.sqlChangeHeads("id", df)
	check("sqlite ChangeHeads related", lite, q, a, `SELECT id FROM change WHERE 1 = 1 AND parent_id = ?1 AND EXISTS (SELECT 1 FROM json_each(change.depends_on) WHERE value = ?2) ORDER BY created_at, rowid`, []any{"c1", "c2"})
	idf := ChangesFilter{IDs: []domain.ChangeID{"c1", "c2"}}
	q, a = pg.sqlChangeHeads("id", idf)
	check("pg ChangeHeads ids", pg, q, a, `SELECT id FROM change WHERE true AND id = ANY($1::uuid[]) ORDER BY created_at`, []any{[]string{"c1", "c2"}})
	q, a = lite.sqlChangeHeads("id", idf)
	check("sqlite ChangeHeads ids", lite, q, a, `SELECT id FROM change WHERE 1 = 1 AND id IN (SELECT value FROM json_each(?1)) ORDER BY created_at, rowid`, []any{`["c1","c2"]`})
	q, a = pg.sqlSubChangeIDs("c1")
	check("pg SubChanges", pg, q, a, `SELECT id::text FROM change WHERE parent_id = $1 ORDER BY created_at`, []any{"c1"})
	q, a = lite.sqlSubChangeIDs("c1")
	check("sqlite SubChanges", lite, q, a, `SELECT id FROM change WHERE parent_id = ? ORDER BY created_at, rowid`, []any{"c1"})
	check("pg OpenChangeIDs", pg, pg.sqlOpenChangeIDs(), nil, `SELECT id::text FROM change WHERE status NOT IN ('applied', 'abandoned') ORDER BY created_at`, nil)
	check("sqlite OpenChangeIDs", lite, lite.sqlOpenChangeIDs(), nil, `SELECT id FROM change WHERE status NOT IN ('applied', 'abandoned') ORDER BY created_at, rowid`, nil)

	// Change impacts (ADR 0174)
	q, a = pg.sqlNodeChangePage("n1", 0, 3)
	check("pg NodeChangePage", pg, q, a, `SELECT change_id::text, min(seq) FROM change_log WHERE node = $1 GROUP BY change_id ORDER BY 2 DESC LIMIT $2`, []any{"n1", 3})
	q, a = lite.sqlNodeChangePage("n1", 9, 3)
	check("sqlite NodeChangePage", lite, q, a, `SELECT change_id, min(seq) FROM change_log WHERE node = ? GROUP BY change_id HAVING min(seq) < ? ORDER BY 2 DESC LIMIT ?`, []any{"n1", int64(9), 3})

	// Log, LogCounts: the filter exercises every condition (the where builder is shared and unchanged)
	f := domain.LogFilter{Change: "c1", Types: []string{"fact.", "journal.schedule"}, Flows: []string{"main"}, Processes: []string{"p1", "p2"}, Execution: "e1", AfterSeq: 7, Limit: 5}
	wantArgs := []any{"c1", "fact.%", "journal.schedule", "main", "p1", "p2", "e1", int64(7)}
	if f.AfterSeq != 7 {
		t.Fatal("AfterSeq type")
	}
	q, a = pg.sqlLog(f)
	check("pg Log", pg, q, normSeq(a), `SELECT seq, id, change_id::text, type, flow, process_id, execution, subject, by_whom, causation, at, payload FROM change_log WHERE change_id = $1 AND (type LIKE $2 OR type = $3) AND flow IN ($4) AND process_id IN ($5, $6) AND execution = $7 AND seq > $8 ORDER BY seq LIMIT 5`, normSeq(wantArgs))
	q, a = lite.sqlLog(f)
	check("sqlite Log", lite, q, normSeq(a), `SELECT seq, id, change_id, type, flow, process_id, execution, subject, by_whom, causation, at, payload FROM change_log WHERE change_id = ? AND (type LIKE ? OR type = ?) AND flow IN (?) AND process_id IN (?, ?) AND execution = ? AND seq > ? ORDER BY seq LIMIT 5`, normSeq(wantArgs))
	q, a = pg.sqlLogCounts(f)
	check("pg LogCounts", pg, q, normSeq(a), `SELECT type, count(*) FROM change_log WHERE change_id = $1 AND (type LIKE $2 OR type = $3) AND flow IN ($4) AND process_id IN ($5, $6) AND execution = $7 GROUP BY type`, normSeq(wantArgs[:7]))
	q, a = lite.sqlLogCounts(f)
	check("sqlite LogCounts", lite, q, normSeq(a), `SELECT type, count(*) FROM change_log WHERE change_id = ? AND (type LIKE ? OR type = ?) AND flow IN (?) AND process_id IN (?, ?) AND execution = ? GROUP BY type`, normSeq(wantArgs[:7]))
	q, a = pg.sqlLog(domain.LogFilter{Change: "c1", Flows: []string{}})
	check("pg Log no flows", pg, q, a, `SELECT seq, id, change_id::text, type, flow, process_id, execution, subject, by_whom, causation, at, payload FROM change_log WHERE change_id = $1 AND 1 = 0 ORDER BY seq`, []any{"c1"})

	// Tags
	tf := domain.TagFilter{Namespace: "ns", Name: "v1", Node: "n1"}
	q, a = pg.sqlTags(tf)
	check("pg Tags", pg, q, a, `SELECT id::text, name, namespace, node_id::text, version, by, created_at FROM tag WHERE true AND namespace = $1 AND name = $2 AND node_id = $3 ORDER BY created_at, id`, []any{"ns", "v1", "n1"})
	q, a = lite.sqlTags(tf)
	check("sqlite Tags", lite, q, a, `SELECT id, name, namespace, node_id, version, by, created_at FROM tag WHERE 1 = 1 AND namespace = ? AND name = ? AND node_id = ? ORDER BY created_at, rowid`, []any{"ns", "v1", "n1"})
	q, a = pg.sqlTags(domain.TagFilter{})
	check("pg Tags unfiltered", pg, q, a, `SELECT id::text, name, namespace, node_id::text, version, by, created_at FROM tag WHERE true ORDER BY created_at, id`, []any{})
	q, a = lite.sqlTags(domain.TagFilter{})
	check("sqlite Tags unfiltered", lite, q, a, `SELECT id, name, namespace, node_id, version, by, created_at FROM tag WHERE 1 = 1 ORDER BY created_at, rowid`, []any{})
	checkSQL(t, "pg PutTag", pg, pg.sqlPutTag(), `INSERT INTO tag (id, name, namespace, node_id, version, by, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (id) DO UPDATE SET name = excluded.name`)
	checkSQL(t, "sqlite PutTag", lite, lite.sqlPutTag(), `INSERT INTO tag (id, name, namespace, node_id, version, by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO UPDATE SET name = excluded.name`)

	// Single statements
	q, _ = pg.sqlDeleteTag("t1")
	checkSQL(t, "pg DeleteTag", pg, q, `DELETE FROM tag WHERE id = $1`)
	q, _ = lite.sqlDeleteTag("t1")
	checkSQL(t, "sqlite DeleteTag", lite, q, `DELETE FROM tag WHERE id = ?`)
	q, _ = pg.sqlJoinCommits("main", "c1", 9, 12, []int64{3, 4})
	checkSQL(t, "pg JoinCommits", pg, q, `INSERT INTO branch_commit (branch, seq, at, change_id, log_seq) SELECT $1, s, $3, $2, $4 FROM unnest($5::bigint[]) AS s ON CONFLICT DO NOTHING`)
	q, _ = lite.sqlJoinCommits("main", "c1", 9, 12, []int64{3, 4})
	checkSQL(t, "sqlite JoinCommits", lite, q, `INSERT INTO branch_commit (branch, seq, at, change_id, log_seq) SELECT ?1, value, ?3, ?2, ?4 FROM json_each(?5) WHERE true ON CONFLICT DO NOTHING`)

	// Inserts
	nodeVersionCols := []string{"node_id", "version", "props", "deleted", "change_id", "created_at", "branch", "parents", "reason", "state", "change_impact", "comment", "execution", "owner"}
	checkSQL(t, "pg insert node_version", pg, pg.sqlInsert("node_version", nodeVersionCols, ""), `INSERT INTO node_version (node_id, version, props, deleted, change_id, created_at, branch, parents, reason, state, change_impact, comment, execution, owner)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`)
	checkSQL(t, "sqlite insert node_version", lite, lite.sqlInsert("node_version", nodeVersionCols, ""), `INSERT INTO node_version (node_id, version, props, deleted, change_id, created_at, branch, parents, reason, state, change_impact, comment, execution, owner)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	linkCols := []string{"id", "type", "from_id", "from_version", "to_id", "to_version", "props", "change_id"}
	checkSQL(t, "pg insert link", pg, pg.sqlInsert("link", linkCols, ""), `INSERT INTO link (id, type, from_id, from_version, to_id, to_version, props, change_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`)
	checkSQL(t, "sqlite insert link", lite, lite.sqlInsert("link", linkCols, ""), `INSERT INTO link (id, type, from_id, from_version, to_id, to_version, props, change_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	checkSQL(t, "pg insert link rows", pg, pg.sqlInsertRows("link", []string{"id", "type"}, 2), `INSERT INTO link (id, type) VALUES ($1, $2), ($3, $4)`)
	checkSQL(t, "sqlite insert link rows", lite, lite.sqlInsertRows("link", []string{"id", "type"}, 2), `INSERT INTO link (id, type) VALUES (?, ?), (?, ?)`)
	commitCols := []string{"id", "name", "parent_id", "merged_from", "change_id", "created_at", "branch", "namespace", "depth", "gap", "kind"}
	checkSQL(t, "pg insert commit", pg, pg.sqlInsert("commits", commitCols, ""), `INSERT INTO commits (id, name, parent_id, merged_from, change_id, created_at, branch, namespace, depth, gap, kind) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`)
	checkSQL(t, "sqlite insert commit", lite, lite.sqlInsert("commits", commitCols, ""), `INSERT INTO commits (id, name, parent_id, merged_from, change_id, created_at, branch, namespace, depth, gap, kind) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	checkSQL(t, "pg insert change_log", pg, pg.sqlInsert("change_log", changeLogColumns, " RETURNING seq"), `INSERT INTO change_log (id, change_id, type, flow, process_id, execution, subject, by_whom, causation, at, payload, node)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING seq`)
	checkSQL(t, "sqlite insert change_log", lite, lite.sqlInsert("change_log", changeLogColumns, ""), `INSERT INTO change_log (id, change_id, type, flow, process_id, execution, subject, by_whom, causation, at, payload, node)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)

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
		`SELECT count(*) FROM commits WHERE change_id = $1`,
		`SELECT count(*) FROM link l JOIN node_version v ON l.to_id = v.node_id AND l.to_version = v.version WHERE v.change_id = $1 AND l.change_id IS DISTINCT FROM $1::uuid`,
		`SELECT count(*) FROM node_version o JOIN node_version v ON o.node_id = v.node_id AND o.version > v.version WHERE v.change_id = $1 AND o.change_id IS DISTINCT FROM $1::uuid`,
	})
	checkList("sqlite DeleteChange used", lite, lite.deleteChangeUsed(), []string{
		`SELECT count(*) FROM commits WHERE change_id = ?1`,
		`SELECT count(*) FROM link l JOIN node_version v ON l.to_id = v.node_id AND l.to_version = v.version WHERE v.change_id = ?1 AND (l.change_id IS NULL OR l.change_id != ?1)`,
		`SELECT count(*) FROM node_version o JOIN node_version v ON o.node_id = v.node_id AND o.version > v.version WHERE v.change_id = ?1 AND (o.change_id IS NULL OR o.change_id != ?1)`,
	})
	checkSQL(t, "pg change nodes", pg, pg.sqlChangeNodes(), `SELECT DISTINCT node_id::text FROM node_version WHERE change_id = $1`)
	checkSQL(t, "sqlite change nodes", lite, lite.sqlChangeNodes(), `SELECT DISTINCT node_id FROM node_version WHERE change_id = ?`)
	checkList("pg DeleteChange rows", pg, pg.deleteChangeRows(), []string{
		`DELETE FROM tag WHERE (node_id, version) IN (SELECT node_id, version FROM node_version WHERE change_id = $1)`,
		`DELETE FROM link WHERE change_id = $1`,
		`DELETE FROM link WHERE (from_id, from_version) IN (SELECT node_id, version FROM node_version WHERE change_id = $1)`,
		`DELETE FROM branch_commit WHERE change_id = $1`,
		`DELETE FROM node_version WHERE change_id = $1`,
		`DELETE FROM change_log WHERE change_id = $1`,
	})
	checkList("sqlite DeleteChange rows", lite, lite.deleteChangeRows(), []string{
		`DELETE FROM tag WHERE (node_id, version) IN (SELECT node_id, version FROM node_version WHERE change_id = ?1)`,
		`DELETE FROM link WHERE change_id = ?1`,
		`DELETE FROM link WHERE (from_id, from_version) IN (SELECT node_id, version FROM node_version WHERE change_id = ?1)`,
		`DELETE FROM branch_commit WHERE change_id = ?1`,
		`DELETE FROM node_version WHERE change_id = ?1`,
		`DELETE FROM change_log WHERE change_id = ?1`,
	})
	checkSQL(t, "pg orphan", pg, pg.sqlDeleteOrphanNode(), `DELETE FROM node WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM node_version WHERE node_id = $1)`)
	checkSQL(t, "sqlite orphan", lite, lite.sqlDeleteOrphanNode(), `DELETE FROM node WHERE id = ?1 AND NOT EXISTS (SELECT 1 FROM node_version WHERE node_id = ?1)`)
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

// The statements of the store's statistics (graph statistics view): one per measure, the same text for both dialects but
// the placeholder.
func TestSQLStoreCounts(t *testing.T) {
	pg, lite := dialectPG.sqlStoreCounts("ns"), dialectSQLite.sqlStoreCounts("ns")
	if len(pg) != len(StoreMeasures) || len(lite) != len(StoreMeasures) {
		t.Fatalf("%d / %d statements for %d measures", len(pg), len(lite), len(StoreMeasures))
	}
	for _, m := range StoreMeasures {
		if pg[m] == "" || strings.ReplaceAll(qmarkNn.ReplaceAllString(lite[m], "$$1"), "json_each(", "jsonb_each_text(") != pg[m] {
			t.Errorf("%s:\n pg     %s\n sqlite %s", m, pg[m], lite[m])
		}
	}
	checkSQL(t, "pg linkSetsByMode", dialectPG, pg[StatLinkSetsByMode], `SELECT CASE WHEN v.links IS NOT NULL THEN 'shared' WHEN v.links_base IS NOT NULL THEN 'delta' ELSE 'own' END AS mode, count(*) FROM node_version v JOIN node n ON n.id = v.node_id WHERE n.namespace = $1 GROUP BY mode`)
	checkSQL(t, "pg changeCounters", dialectPG, pg[StatChangeCounters], `SELECT s.key, sum(CAST(s.value AS integer)) FROM change c, jsonb_each_text(c.stats) s WHERE c.namespace = $1 AND c.parent_id IS NULL GROUP BY s.key`)
	checkSQL(t, "sqlite logByType", dialectSQLite, lite[StatLogByType], `SELECT l.type, count(*) FROM change_log l JOIN change c ON c.id = l.change_id WHERE c.namespace = ? GROUP BY l.type`)
}
