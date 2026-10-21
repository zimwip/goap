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
	goldPGNodeCols     = `n.id::text, v.version, n.namespace, n.key, n.type, v.props, v.deleted, v.change_id::text, v.created_at, v.branch, v.parents, v.reason, v.state, v.change_impact::text, v.comment, v.execution, v.owner, n.project, v.origins`
	goldSQLiteNodeCols = `n.id, v.version, n.namespace, n.key, n.type, v.props, v.deleted, v.change_id, v.created_at, v.branch, v.parents, v.reason, v.state, v.change_impact, v.comment, v.execution, v.owner, n.project, v.origins`
	goldPGOnBranch     = `(v.branch = $2 OR EXISTS (SELECT 1 FROM node_branch j WHERE j.node_id = v.node_id AND j.version = v.version AND j.branch = $2))`
	goldSQLiteOnBranch = `(v.branch = ? OR EXISTS (SELECT 1 FROM node_branch j WHERE j.node_id = v.node_id AND j.version = v.version AND j.branch = ?))`
	goldPGImpactCols   = `id::text, node_id::text, key, type, intent, rationale, pre_version, post_version, committed_version, review, reviews, COALESCE(via::text, ''), recheck, produced_by, derived_from, items, execution, created_at, flow, superseded`
	goldSQLiteImpact   = `id, node_id, key, type, intent, rationale, pre_version, post_version, committed_version, review, reviews, COALESCE(via, ''), recheck, produced_by, derived_from, items, execution, created_at, flow, superseded`
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
	check("pg LatestOn", pg, q, a, `SELECT `+goldPGNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = $1 AND v.version = greatest(coalesce((SELECT version FROM node_version WHERE node_id = $1 AND branch = $2 ORDER BY version DESC LIMIT 1), 0), coalesce((SELECT version FROM node_branch WHERE node_id = $1 AND branch = $2 ORDER BY version DESC LIMIT 1), 0))`, []any{"n1", "main"})
	q, a = lite.sqlLatestOn("n1", "")
	check("sqlite LatestOn", lite, q, a, `SELECT `+goldSQLiteNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = ? AND v.version = max(coalesce((SELECT version FROM node_version WHERE node_id = ? AND branch = ? ORDER BY version DESC LIMIT 1), 0), coalesce((SELECT version FROM node_branch WHERE node_id = ? AND branch = ? ORDER BY version DESC LIMIT 1), 0))`, []any{"n1", "main"})

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
		check("VersionBranches", d, q, a, `SELECT version, branch FROM node_branch WHERE node_id = `+p+` AND version >= `+p2+` ORDER BY branch`, []any{"n1", 3})
		q, a = d.sqlVersionRange("n1", 0, 1)
		check("VersionRange latest", d, q, a, `SELECT `+cols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = `+p+` ORDER BY v.version DESC LIMIT `+p2, []any{"n1", 1})
		q, a = d.sqlVersionRange("n1", 7, 20)
		check("VersionRange page", d, q, a, `SELECT `+cols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = `+p+` AND v.version < `+p2+` ORDER BY v.version DESC LIMIT `+p3, []any{"n1", 7, 20})
		q, a = d.sqlVersionJoins("n1")
		cid := "change_id"
		if d.pg {
			cid = "change_id::text"
		}
		check("VersionJoins", d, q, a, `SELECT version, branch, `+cid+` FROM node_branch WHERE node_id = `+p+` ORDER BY version, branch`, []any{"n1"})
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

	// The state of a baseline (ADR 0143): the versions committed on its branch
	ids := []domain.NodeID{"n1", "n2"}
	committed := func(id, inV, inJ, ns string) string {
		return `SELECT ` + id + `, version, deleted, reason FROM (
		SELECT x.node_id, x.version, x.deleted, x.reason, row_number() OVER (PARTITION BY x.node_id ORDER BY x.committed DESC, x.version DESC) AS rn
		FROM (
			SELECT v.node_id, v.version, v.deleted, v.reason, v.committed FROM node_version v
			WHERE v.branch = $1 AND v.committed > $2 AND v.committed <= $3` + inV + `
			UNION ALL
			SELECT j.node_id, j.version, v.deleted, v.reason, j.committed FROM node_branch j JOIN node_version v ON v.node_id = j.node_id AND v.version = j.version
			WHERE j.branch = $1 AND j.committed > $2 AND j.committed <= $3` + inJ + `
		) x JOIN node n ON n.id = x.node_id WHERE n.namespace = ` + ns + `
	) y WHERE rn = 1`
	}
	q, a = pg.sqlCommittedVersions("", "main", 3, 9, nil)
	check("pg CommittedVersions", pg, q, a, committed("node_id::text", "", "", "$4"), []any{"main", int64(3), int64(9), "default"})
	q, a = pg.sqlCommittedVersions("", "main", 3, 9, ids)
	check("pg CommittedVersions ids", pg, q, a, `SELECT q.id::text, x.version,
			(SELECT w.deleted FROM node_version w WHERE w.node_id = q.id AND w.version = x.version),
			(SELECT w.reason FROM node_version w WHERE w.node_id = q.id AND w.version = x.version)
		FROM unnest($5::uuid[]) AS q(id) JOIN node n ON n.id = q.id AND n.namespace = $4
		CROSS JOIN LATERAL (
			SELECT u.version FROM (
				(SELECT v.version, v.committed FROM node_version v
				WHERE v.node_id = q.id AND v.branch = $1 AND v.committed > $2 AND v.committed <= $3 ORDER BY v.committed DESC, v.version DESC LIMIT 1)
				UNION ALL
				(SELECT j.version, j.committed FROM node_branch j
				WHERE j.node_id = q.id AND j.branch = $1 AND j.committed > $2 AND j.committed <= $3 ORDER BY j.committed DESC, j.version DESC LIMIT 1)
			) u ORDER BY u.committed DESC, u.version DESC LIMIT 1
		) x`, []any{"main", int64(3), int64(9), "default", []string{"n1", "n2"}})
	q, a = lite.sqlCommittedVersions("", "main", 3, 9, ids)
	check("sqlite CommittedVersions ids", lite, q, a, strings.NewReplacer("$1", "?", "$2", "?", "$3", "?").Replace(committed("node_id",
		" AND v.node_id IN (SELECT value FROM json_each(?))", " AND j.node_id IN (SELECT value FROM json_each(?))", "?")), []any{"main", int64(3), int64(9), "default", `["n1","n2"]`})
	q, a = pg.sqlBaselineHeader("b1")
	check("pg BaselineHeader", pg, q, a, `SELECT id::text, name, coalesce(parent_id::text, ''), coalesce(merged_from::text, ''), coalesce(change_id::text, ''), created_at, branch, namespace, kind, coalesce(rebased_from::text, ''), seq FROM baseline WHERE id = $1`, []any{"b1"})
	q, a = lite.sqlBaselineHeader("b1")
	check("sqlite BaselineHeader", lite, q, a, `SELECT id, name, coalesce(parent_id, ''), coalesce(merged_from, ''), coalesce(change_id, ''), created_at, branch, namespace, kind, coalesce(rebased_from, ''), seq FROM baseline WHERE id = ?`, []any{"b1"})
	q, a = pg.sqlLastCommit("change-1")
	check("pg LastCommit", pg, q, a, `SELECT coalesce(max(committed), 0) FROM (SELECT max(committed) AS committed FROM node_version WHERE branch = $1 UNION ALL SELECT max(committed) FROM node_branch WHERE branch = $1) l`, []any{"change-1"})
	q, a = lite.sqlLastCommit("change-1")
	check("sqlite LastCommit", lite, q, a, `SELECT coalesce(max(committed), 0) FROM (SELECT max(committed) AS committed FROM node_version WHERE branch = ? UNION ALL SELECT max(committed) FROM node_branch WHERE branch = ?) l`, []any{"change-1"})
	q, a = pg.sqlBranchNodeIDs("change-1")
	check("pg BranchNodeIDs", pg, q, a, `SELECT node_id::text FROM node_version WHERE branch = $1 UNION SELECT node_id::text FROM node_branch WHERE branch = $1`, []any{"change-1"})
	q, a = lite.sqlBranchNodeIDs("change-1")
	check("sqlite BranchNodeIDs", lite, q, a, `SELECT node_id FROM node_version WHERE branch = ? UNION SELECT node_id FROM node_branch WHERE branch = ?`, []any{"change-1"})
	q, a = pg.sqlNamespaceNodeIDs("alm")
	check("pg NamespaceNodeIDs", pg, q, a, `SELECT id::text FROM node WHERE namespace = $1 ORDER BY id`, []any{"alm"})
	q, a = lite.sqlNamespaceNodeIDs("alm")
	check("sqlite NamespaceNodeIDs", lite, q, a, `SELECT id FROM node WHERE namespace = ? ORDER BY id`, []any{"alm"})

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
	held := func(refs string) string {
		join := " JOIN "
		if strings.Contains(refs, "json_each") {
			join = " CROSS JOIN " // SQLite reads the chain first
		}
		return `WITH RECURSIVE r(id, version) AS (` + refs + `), c(nid, rv, sv) AS ( SELECT v.node_id, v.version, coalesce(v.links, v.version) FROM node_version v JOIN r ON r.id = v.node_id AND r.version = v.version UNION ALL SELECT c.nid, c.rv, s.links_base FROM c JOIN node_version s ON s.node_id = c.nid AND s.version = c.sv WHERE s.links_depth > 0 ), h AS (SELECT l.id, l.type, l.from_id, c.rv, l.to_id, l.to_version, l.props, l.change_id, l.removes, l.to_url FROM c` + join + `link l ON l.from_id = c.nid AND l.from_version = c.sv) SELECT h.id, h.type, h.from_id, h.rv AS from_version, h.to_id, h.to_version, h.props, h.change_id, h.to_url FROM h WHERE h.removes IS NULL AND (h.id, h.from_id, h.rv) NOT IN (SELECT x.removes, x.from_id, x.rv FROM h x WHERE x.removes IS NOT NULL)`
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
	check("pg LatestVersions", pg, q, a, `SELECT id::text, latest FROM node WHERE id = ANY($1::uuid[])`, []any{[]string{"n1", "n2"}})
	q, a = lite.sqlLatestVersions(ids)
	check("sqlite LatestVersions", lite, q, a, `SELECT id, latest FROM node WHERE id IN (SELECT value FROM json_each(?))`, []any{`["n1","n2"]`})
	q, a = pg.sqlLatestVersionsOn(ids, "b")
	check("pg LatestVersionsOn", pg, q, a, `SELECT id::text, version FROM (SELECT q.id, greatest(coalesce((SELECT version FROM node_version WHERE node_id = q.id AND branch = $2 ORDER BY version DESC LIMIT 1), 0), coalesce((SELECT version FROM node_branch WHERE node_id = q.id AND branch = $2 ORDER BY version DESC LIMIT 1), 0)) AS version FROM unnest($1::uuid[]) AS q(id)) l WHERE version > 0`, []any{[]string{"n1", "n2"}, "b"})
	q, a = lite.sqlLatestVersionsOn(ids, "b")
	check("sqlite LatestVersionsOn", lite, q, a, `SELECT id, version FROM (SELECT q.id, max(coalesce((SELECT version FROM node_version WHERE node_id = q.id AND branch = ? ORDER BY version DESC LIMIT 1), 0), coalesce((SELECT version FROM node_branch WHERE node_id = q.id AND branch = ? ORDER BY version DESC LIMIT 1), 0)) AS version FROM (SELECT value AS id FROM json_each(?)) q) l WHERE version > 0`, []any{`["n1","n2"]`, "b"})

	// Branches
	const pgBranchCols = `namespace, name, parent, coalesce(fork_baseline::text, ''), coalesce(head_baseline::text, ''), origin, status, created_at, description, intent, coalesce(rebased_from::text, ''), ephemeral, fork_seq`
	const liteBranchCols = `namespace, name, parent, coalesce(fork_baseline, ''), coalesce(head_baseline, ''), origin, status, created_at, description, intent, coalesce(rebased_from, ''), ephemeral, fork_seq`
	q, a = pg.sqlBranch("", "b")
	check("pg Branch", pg, q, a, `SELECT `+pgBranchCols+` FROM branch WHERE namespace = $1 AND name = $2`, []any{"default", "b"})
	q, a = lite.sqlBranch("", "b")
	check("sqlite Branch", lite, q, a, `SELECT `+liteBranchCols+` FROM branch WHERE namespace = ? AND name = ?`, []any{"default", "b"})
	q, a = pg.sqlBranches("")
	check("pg Branches", pg, q, a, `SELECT `+pgBranchCols+` FROM branch WHERE namespace = $1 ORDER BY created_at`, []any{"default"})
	q, a = lite.sqlBranches("")
	check("sqlite Branches", lite, q, a, `SELECT `+liteBranchCols+` FROM branch WHERE namespace = ? ORDER BY created_at, rowid`, []any{"default"})

	// Baselines, changes
	q, a = pg.sqlNamespaceBaselineHeaders("")
	check("pg NamespaceBaselineHeaders", pg, q, a, `SELECT id::text, name, coalesce(parent_id::text, ''), coalesce(merged_from::text, ''), coalesce(change_id::text, ''), created_at, branch, kind, coalesce(rebased_from::text, ''), seq FROM baseline WHERE namespace = $1 ORDER BY created_at, id`, []any{"default"})
	q, a = lite.sqlNamespaceBaselineHeaders("")
	check("sqlite NamespaceBaselineHeaders", lite, q, a, `SELECT id, name, coalesce(parent_id, ''), coalesce(merged_from, ''), coalesce(change_id, ''), created_at, branch, kind, coalesce(rebased_from, ''), seq FROM baseline WHERE namespace = ? ORDER BY created_at, rowid`, []any{"default"})
	const pgHead = `SELECT id::text, name, coalesce(parent_id::text, ''), coalesce(merged_from::text, ''), coalesce(change_id::text, ''), created_at, branch, kind, coalesce(rebased_from::text, ''), seq FROM baseline WHERE namespace = $1`
	const liteHead = `SELECT id, name, coalesce(parent_id, ''), coalesce(merged_from, ''), coalesce(change_id, ''), created_at, branch, kind, coalesce(rebased_from, ''), seq FROM baseline WHERE namespace = ?`
	q, a = pg.sqlBaselinePage("", "", 9, 3)
	check("pg BaselinePage", pg, q, a, pgHead+` AND branch = $2 AND ($3 = 0 OR seq < $3) ORDER BY seq DESC LIMIT $4`, []any{"default", "main", int64(9), 3})
	q, a = lite.sqlBaselinePage("", "", 9, 3)
	check("sqlite BaselinePage", lite, q, a, liteHead+` AND branch = ? AND (? = 0 OR seq < ?) ORDER BY seq DESC LIMIT ?`, []any{"default", "main", int64(9), 3})
	q, a = pg.sqlBaselineHeadersOf("", []domain.BaselineID{"b1", "b2"})
	check("pg BaselineHeadersOf", pg, q, a, pgHead+` AND id = ANY($2::uuid[]) ORDER BY seq`, []any{"default", []string{"b1", "b2"}})
	q, a = lite.sqlBaselineHeadersOf("", []domain.BaselineID{"b1", "b2"})
	check("sqlite BaselineHeadersOf", lite, q, a, liteHead+` AND id IN (SELECT value FROM json_each(?)) ORDER BY seq`, []any{"default", `["b1","b2"]`})
	q, a = pg.sqlChangeBaselineHeaders("", []domain.ChangeID{"c1"})
	check("pg ChangeBaselineHeaders", pg, q, a, pgHead+` AND change_id = ANY($2::uuid[]) ORDER BY seq`, []any{"default", []string{"c1"}})
	q, a = lite.sqlChangeBaselineHeaders("", []domain.ChangeID{"c1"})
	check("sqlite ChangeBaselineHeaders", lite, q, a, liteHead+` AND change_id IN (SELECT value FROM json_each(?)) ORDER BY seq`, []any{"default", `["c1"]`})
	q, a = pg.sqlBaselineCounts("")
	check("pg BaselineCounts", pg, q, a, `SELECT branch, count(*) FROM baseline WHERE namespace = $1 GROUP BY branch`, []any{"default"})
	q, a = lite.sqlBaselineCounts("")
	check("sqlite BaselineCounts", lite, q, a, `SELECT branch, count(*) FROM baseline WHERE namespace = ? GROUP BY branch`, []any{"default"})
	q, a = pg.sqlBranchBaselineHeaders("", "b1")
	check("pg BranchBaselineHeaders", pg, q, a, `SELECT id::text, name, coalesce(parent_id::text, ''), coalesce(merged_from::text, ''), coalesce(change_id::text, ''), created_at, branch, kind, coalesce(rebased_from::text, ''), seq FROM baseline WHERE namespace = $1 AND branch = $2 ORDER BY seq`, []any{"default", "b1"})
	q, a = lite.sqlBranchBaselineHeaders("", "b1")
	check("sqlite BranchBaselineHeaders", lite, q, a, `SELECT id, name, coalesce(parent_id, ''), coalesce(merged_from, ''), coalesce(change_id, ''), created_at, branch, kind, coalesce(rebased_from, ''), seq FROM baseline WHERE namespace = ? AND branch = ? ORDER BY seq`, []any{"default", "b1"})
	q, a = pg.sqlBaselineIDs("")
	check("pg Baselines", pg, q, a, `SELECT id::text FROM baseline WHERE namespace = $1 ORDER BY created_at`, []any{"default"})
	q, a = lite.sqlBaselineIDs("")
	check("sqlite Baselines", lite, q, a, `SELECT id FROM baseline WHERE namespace = ? ORDER BY created_at, rowid`, []any{"default"})
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
	q, a = pg.sqlSubChangeIDs("c1")
	check("pg SubChanges", pg, q, a, `SELECT id::text FROM change WHERE parent_id = $1 ORDER BY created_at`, []any{"c1"})
	q, a = lite.sqlSubChangeIDs("c1")
	check("sqlite SubChanges", lite, q, a, `SELECT id FROM change WHERE parent_id = ? ORDER BY created_at, rowid`, []any{"c1"})
	check("pg OpenChangeIDs", pg, pg.sqlOpenChangeIDs(), nil, `SELECT id::text FROM change WHERE status NOT IN ('applied', 'abandoned') ORDER BY created_at`, nil)
	check("sqlite OpenChangeIDs", lite, lite.sqlOpenChangeIDs(), nil, `SELECT id FROM change WHERE status NOT IN ('applied', 'abandoned') ORDER BY created_at, rowid`, nil)

	// Change impacts
	q, a = pg.sqlChangeImpacts("c1")
	check("pg ChangeImpacts", pg, q, a, `SELECT `+goldPGImpactCols+` FROM change_impact WHERE change_id = $1 ORDER BY seq`, []any{"c1"})
	q, a = lite.sqlChangeImpacts("c1")
	check("sqlite ChangeImpacts", lite, q, a, `SELECT `+goldSQLiteImpact+`
			FROM change_impact WHERE change_id = ? ORDER BY seq`, []any{"c1"})
	q, a = pg.sqlChangeImpactKeys("c1", 200)
	check("pg ChangeImpactKeys", pg, q, a, `SELECT key, type FROM change_impact WHERE change_id = $1 ORDER BY seq LIMIT $2`, []any{"c1", 200})
	q, a = lite.sqlChangeImpactKeys("c1", 200)
	check("sqlite ChangeImpactKeys", lite, q, a, `SELECT key, type FROM change_impact WHERE change_id = ? ORDER BY seq LIMIT ?`, []any{"c1", 200})
	q, a = pg.sqlNodeChangePage("n1", 0, 3)
	check("pg NodeChangePage", pg, q, a, `SELECT change_id::text, min(seq) FROM change_impact WHERE node_id = $1 GROUP BY change_id ORDER BY 2 DESC LIMIT $2`, []any{"n1", 3})
	q, a = lite.sqlNodeChangePage("n1", 9, 3)
	check("sqlite NodeChangePage", lite, q, a, `SELECT change_id, min(seq) FROM change_impact WHERE node_id = ? GROUP BY change_id HAVING min(seq) < ? ORDER BY 2 DESC LIMIT ?`, []any{"n1", int64(9), 3})

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
	checkSQL(t, "pg DeleteChangeImpact", pg, pg.sqlDeleteChangeImpact(), `DELETE FROM change_impact WHERE change_id = $1 AND id = $2`)
	checkSQL(t, "sqlite DeleteChangeImpact", lite, lite.sqlDeleteChangeImpact(), `DELETE FROM change_impact WHERE change_id = ? AND id = ?`)
	checkSQL(t, "pg SetNodeOrigin", pg, pg.sqlSetNodeOrigin(), `UPDATE node_version SET change_id = $3, change_impact = $4, comment = $5 WHERE node_id = $1 AND version = $2`)
	checkSQL(t, "sqlite SetNodeOrigin", lite, lite.sqlSetNodeOrigin(), `UPDATE node_version SET change_id = ?, change_impact = ?, comment = ? WHERE node_id = ? AND version = ?`)
	checkSQL(t, "pg JoinBranch", pg, pg.sqlJoinBranch(), `INSERT INTO node_branch (node_id, version, branch, change_id, committed) SELECT node_id, version, $3, $4, $5 FROM node_version
		WHERE node_id = $1 AND version = $2 AND branch <> $3 ON CONFLICT DO NOTHING`)
	checkSQL(t, "sqlite JoinBranch", lite, lite.sqlJoinBranch(), `INSERT INTO node_branch (node_id, version, branch, change_id, committed) SELECT node_id, version, ?, ?, ? FROM node_version
		WHERE node_id = ? AND version = ? AND branch <> ? ON CONFLICT DO NOTHING`)

	// Inserts
	nodeVersionCols := []string{"node_id", "version", "props", "deleted", "change_id", "created_at", "branch", "parents", "reason", "state", "change_impact", "comment", "execution", "owner"}
	checkSQL(t, "pg insert node_version", pg, pg.sqlInsert("node_version", nodeVersionCols, ""), `INSERT INTO node_version (node_id, version, props, deleted, change_id, created_at, branch, parents, reason, state, change_impact, comment, execution, owner)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`)
	checkSQL(t, "sqlite insert node_version", lite, lite.sqlInsert("node_version", nodeVersionCols, ""), `INSERT INTO node_version (node_id, version, props, deleted, change_id, created_at, branch, parents, reason, state, change_impact, comment, execution, owner)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	linkCols := []string{"id", "type", "from_id", "from_version", "to_id", "to_version", "props", "change_id"}
	checkSQL(t, "pg insert link", pg, pg.sqlInsert("link", linkCols, ""), `INSERT INTO link (id, type, from_id, from_version, to_id, to_version, props, change_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`)
	checkSQL(t, "sqlite insert link", lite, lite.sqlInsert("link", linkCols, ""), `INSERT INTO link (id, type, from_id, from_version, to_id, to_version, props, change_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	baselineCols := []string{"id", "name", "parent_id", "merged_from", "change_id", "created_at", "branch", "namespace", "depth", "gap", "kind"}
	checkSQL(t, "pg insert baseline", pg, pg.sqlInsert("baseline", baselineCols, ""), `INSERT INTO baseline (id, name, parent_id, merged_from, change_id, created_at, branch, namespace, depth, gap, kind) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`)
	checkSQL(t, "sqlite insert baseline", lite, lite.sqlInsert("baseline", baselineCols, ""), `INSERT INTO baseline (id, name, parent_id, merged_from, change_id, created_at, branch, namespace, depth, gap, kind) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	checkSQL(t, "pg insert change_log", pg, pg.sqlInsert("change_log", changeLogColumns, " RETURNING seq"), `INSERT INTO change_log (id, change_id, type, flow, process_id, execution, subject, by_whom, causation, at, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING seq`)
	checkSQL(t, "sqlite insert change_log", lite, lite.sqlInsert("change_log", changeLogColumns, ""), `INSERT INTO change_log (id, change_id, type, flow, process_id, execution, subject, by_whom, causation, at, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)

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
		`SELECT count(*) FROM baseline WHERE change_id = $1`,
		`SELECT count(*) FROM link l JOIN node_version v ON l.to_id = v.node_id AND l.to_version = v.version WHERE v.change_id = $1 AND l.change_id IS DISTINCT FROM $1::uuid`,
		`SELECT count(*) FROM node_version o JOIN node_version v ON o.node_id = v.node_id AND o.version > v.version WHERE v.change_id = $1 AND o.change_id IS DISTINCT FROM $1::uuid`,
	})
	checkList("sqlite DeleteChange used", lite, lite.deleteChangeUsed(), []string{
		`SELECT count(*) FROM baseline WHERE change_id = ?1`,
		`SELECT count(*) FROM link l JOIN node_version v ON l.to_id = v.node_id AND l.to_version = v.version WHERE v.change_id = ?1 AND (l.change_id IS NULL OR l.change_id != ?1)`,
		`SELECT count(*) FROM node_version o JOIN node_version v ON o.node_id = v.node_id AND o.version > v.version WHERE v.change_id = ?1 AND (o.change_id IS NULL OR o.change_id != ?1)`,
	})
	checkSQL(t, "pg change nodes", pg, pg.sqlChangeNodes(), `SELECT DISTINCT node_id::text FROM node_version WHERE change_id = $1`)
	checkSQL(t, "sqlite change nodes", lite, lite.sqlChangeNodes(), `SELECT DISTINCT node_id FROM node_version WHERE change_id = ?`)
	checkList("pg DeleteChange rows", pg, pg.deleteChangeRows(), []string{
		`DELETE FROM tag WHERE (node_id, version) IN (SELECT node_id, version FROM node_version WHERE change_id = $1)`,
		`DELETE FROM link WHERE change_id = $1 OR (from_id, from_version) IN (SELECT node_id, version FROM node_version WHERE change_id = $1)`,
		`DELETE FROM node_branch WHERE change_id = $1 OR (node_id, version) IN (SELECT node_id, version FROM node_version WHERE change_id = $1)`,
		`DELETE FROM node_version WHERE change_id = $1`,
		`DELETE FROM change_impact WHERE change_id = $1`,
		`DELETE FROM change_log WHERE change_id = $1`,
	})
	checkList("sqlite DeleteChange rows", lite, lite.deleteChangeRows(), []string{
		`DELETE FROM tag WHERE (node_id, version) IN (SELECT node_id, version FROM node_version WHERE change_id = ?1)`,
		`DELETE FROM link WHERE change_id = ?1 OR (from_id, from_version) IN (SELECT node_id, version FROM node_version WHERE change_id = ?1)`,
		`DELETE FROM node_branch WHERE change_id = ?1 OR (node_id, version) IN (SELECT node_id, version FROM node_version WHERE change_id = ?1)`,
		`DELETE FROM node_version WHERE change_id = ?1`,
		`DELETE FROM change_impact WHERE change_id = ?1`,
		`DELETE FROM change_log WHERE change_id = ?1`,
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

// The statements of the store's statistics (graph statistics view): one per measure, the same text for both dialects but
// the placeholder.
func TestSQLStoreCounts(t *testing.T) {
	pg, lite := dialectPG.sqlStoreCounts("ns"), dialectSQLite.sqlStoreCounts("ns")
	if len(pg) != len(StoreMeasures) || len(lite) != len(StoreMeasures) {
		t.Fatalf("%d / %d statements for %d measures", len(pg), len(lite), len(StoreMeasures))
	}
	for _, m := range StoreMeasures {
		if pg[m] == "" || qmarkNn.ReplaceAllString(lite[m], "$$1") != pg[m] {
			t.Errorf("%s:\n pg     %s\n sqlite %s", m, pg[m], lite[m])
		}
	}
	checkSQL(t, "pg linkSetsByMode", dialectPG, pg[StatLinkSetsByMode], `SELECT CASE WHEN v.links IS NOT NULL THEN 'shared' WHEN v.links_base IS NOT NULL THEN 'delta' ELSE 'own' END AS mode, count(*) FROM node_version v JOIN node n ON n.id = v.node_id WHERE n.namespace = $1 GROUP BY mode`)
	checkSQL(t, "sqlite logByType", dialectSQLite, lite[StatLogByType], `SELECT l.type, count(*) FROM change_log l JOIN change c ON c.id = l.change_id WHERE c.namespace = ? GROUP BY l.type`)
}
