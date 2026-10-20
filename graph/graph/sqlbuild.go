package graph

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
)

// The SQL text and the arguments of the queries both SQL repositories run (ADR 0074). The repositories keep their own
// executors (pgx for PostgreSQL, database/sql for SQLite), their row scanning and their value encodings (uuid / text,
// jsonb / text, arrays, timestamps); what they share is the statement, written once here with the few words that
// differ between the dialects named on the dialect value: the placeholder, the text cast of a uuid column, the test
// for a nullable column, the true literal and the order tie-break (SQLite orders by rowid, PostgreSQL by a column).
// A query whose text is not common (LatestNodes, the upserts, the bulk inserts of baseline entries) stays in its
// repository. sqlbuild_test.go holds the text the repositories ran before, per query and dialect.

// dialect names the differences between the SQL dialects of the repositories.
type dialect struct{ pg bool }

var (
	dialectPG     = dialect{pg: true}
	dialectSQLite = dialect{}
)

// ph is the placeholder of the n-th argument (1-based): `$n` for PostgreSQL, `?n` for SQLite. Both bind by number, so a
// value used twice is one argument.
func (d dialect) ph(n int) string {
	if d.pg {
		return "$" + strconv.Itoa(n)
	}
	return "?" + strconv.Itoa(n)
}

// id is a uuid column read as text (PostgreSQL stores uuids, SQLite stores text).
func (d dialect) id(col string) string {
	if d.pg {
		return col + "::text"
	}
	return col
}

// idOr is a nullable uuid column read as text, empty when null; fn is the spelling of the function (coalesce / COALESCE).
func (d dialect) idOr(fn, col string) string { return fn + "(" + d.id(col) + ", '')" }

// orderBy orders by cols, then by the tie-break: pgTie in PostgreSQL (e.g. ", id"), the insertion order in SQLite.
func (d dialect) orderBy(cols, pgTie string) string {
	if d.pg {
		return " ORDER BY " + cols + pgTie
	}
	return " ORDER BY " + cols + ", rowid"
}

// where1 starts a WHERE clause filters are appended to with AND.
func (d dialect) where1() string {
	if d.pg {
		return " WHERE true"
	}
	return " WHERE 1 = 1"
}

// differs tests that a nullable uuid column is not the value p: IS DISTINCT FROM in PostgreSQL, the explicit null test in
// SQLite.
func (d dialect) differs(col, p string) string {
	if d.pg {
		return col + " IS DISTINCT FROM " + p + "::uuid"
	}
	return "(" + col + " IS NULL OR " + col + " != " + p + ")"
}

// cols joins the columns of a select list, each read through id when it is a uuid.
func (d dialect) cols(cs ...sqlCol) string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.name
		if c.uuid {
			out[i] = d.id(c.name)
		}
	}
	return strings.Join(out, ", ")
}

// sqlCol is a column of a select list; uuid marks the ones PostgreSQL must cast to text.
type sqlCol struct {
	name string
	uuid bool
}

func sc(name string) sqlCol { return sqlCol{name: name} }
func su(name string) sqlCol { return sqlCol{name: name, uuid: true} }

// nodeCols is the select list of a node version (the order sqliteScanNode and scanNode read).
func (d dialect) nodeCols() string {
	return d.cols(su("n.id"), sc("v.version"), sc("n.namespace"), sc("n.key"), sc("n.type"), sc("v.props"), sc("v.deleted"), su("v.change_id"),
		sc("v.created_at"), sc("v.branch"), sc("v.parents"), sc("v.reason"), sc("v.state"), su("v.change_impact"), sc("v.comment"),
		sc("v.execution"), su("v.owner_id"), su("n.project_id"), sc("v.origins"))
}

const nodeFrom = ` FROM node n JOIN node_version v ON v.node_id = n.id`

// onBranchSQL selects the versions v that are part of the branch bound to the placeholder p: written there, or joined
// (ADR 0032).
func onBranchSQL(p string) string {
	return `(v.branch = ` + p + ` OR EXISTS (SELECT 1 FROM node_branch j WHERE j.node_id = v.node_id AND j.version = v.version AND j.branch = ` + p + `))`
}

// sqlNode reads one version of a node.
func (d dialect) sqlNode(ref domain.NodeRef) (string, []any) {
	return `SELECT ` + d.nodeCols() + nodeFrom + ` WHERE n.id = ` + d.ph(1) + ` AND v.version = ` + d.ph(2), []any{string(ref.ID), int(ref.Version)}
}

// sqlLatestOn reads the latest version of a node on a branch (the branch as domain.BranchOf names it): latestOn.
func (d dialect) sqlLatestOn(id domain.NodeID, branch string) (string, []any) {
	return `SELECT ` + d.nodeCols() + nodeFrom + ` WHERE n.id = ` + d.ph(1) + ` AND v.version = ` + d.latestOn(d.ph(1), d.ph(2)),
		[]any{string(id), domain.BranchOf(branch)}
}

// latestOn is the number of the latest version of the node node on the branch branch (expressions), 0 when it has
// none there: the greatest of the last version written there (index node_version_branch) and the last one joined to it
// (node_branch_pkey), each one step of an index in version order, never a walk of the history of the node nor of the
// joins of the branch.
func (d dialect) latestOn(node, branch string) string {
	last := func(table string) string {
		return `coalesce((SELECT version FROM ` + table + ` WHERE node_id = ` + node + ` AND branch = ` + branch + ` ORDER BY version DESC LIMIT 1), 0)`
	}
	greatest := "max"
	if d.pg {
		greatest = "greatest"
	}
	return greatest + `(` + last("node_version") + `, ` + last("node_branch") + `)`
}

// sqlVersions reads every version of a node, oldest first.
func (d dialect) sqlVersions(id domain.NodeID) (string, []any) {
	return `SELECT ` + d.nodeCols() + nodeFrom + ` WHERE n.id = ` + d.ph(1) + ` ORDER BY v.version`, []any{string(id)}
}

// sqlDerivedNodes reads the versions naming a node among their origins (ADR 0077); ref.Version 0 matches any version.
func (d dialect) sqlDerivedNodes(ref domain.NodeRef) (string, []any) {
	o := struct {
		ID      domain.NodeID  `json:"id"`
		Version domain.Version `json:"version,omitempty"`
	}{ref.ID, ref.Version}
	b, _ := json.Marshal([]any{o})
	if d.pg {
		return `SELECT ` + d.nodeCols() + nodeFrom + ` WHERE v.origins @> ` + d.ph(1) + `::jsonb ORDER BY n.key, v.version`, []any{string(b)}
	}
	return `SELECT ` + d.nodeCols() + nodeFrom + ` WHERE EXISTS (SELECT 1 FROM json_each(v.origins) o WHERE json_extract(o.value, '$.id') = ` + d.ph(1) +
		` AND (` + d.ph(2) + ` = 0 OR json_extract(o.value, '$.version') = ` + d.ph(2) + `)) ORDER BY n.key, v.version`, []any{string(ref.ID), int(ref.Version)}
}

// sqlVersionRange reads the last limit versions of a node below the version before (0: from its latest), newest first:
// a page of its history, read through the primary key whatever its length (ADR 0140).
func (d dialect) sqlVersionRange(id domain.NodeID, before domain.Version, limit int) (string, []any) {
	q, args := `SELECT `+d.nodeCols()+nodeFrom+` WHERE n.id = `+d.ph(1), []any{string(id)}
	if before > 0 {
		q, args = q+` AND v.version < `+d.ph(2), append(args, int(before))
	}
	return q + ` ORDER BY v.version DESC LIMIT ` + d.ph(len(args)+1), append(args, limit)
}

// sqlVersionBranches reads the branches the versions of a node joined, from the version from up (0: every version).
func (d dialect) sqlVersionBranches(id domain.NodeID, from domain.Version) (string, []any) {
	return `SELECT version, branch FROM node_branch WHERE node_id = ` + d.ph(1) + ` AND version >= ` + d.ph(2) + ` ORDER BY branch`, []any{string(id), int(from)}
}

// sqlVersionJoins reads the branches the versions of a node joined and the changes whose merge made them join (ADR 0133).
func (d dialect) sqlVersionJoins(id domain.NodeID) (string, []any) {
	return `SELECT version, branch, ` + d.id("change_id") + ` FROM node_branch WHERE node_id = ` + d.ph(1) + ` ORDER BY version, branch`, []any{string(id)}
}

// sqlNodeIDByKey reads the id of a node by its key.
func (d dialect) sqlNodeIDByKey(namespace, key string) (string, []any) {
	return `SELECT ` + d.id("id") + ` FROM node WHERE namespace = ` + d.ph(1) + ` AND key = ` + d.ph(2), []any{domain.NamespaceOf(namespace), key}
}

// sqlNodesAt reads node versions by reference, of one type when nodeType is not empty, ordered by key.
func (d dialect) sqlNodesAt(refs []domain.NodeRef, nodeType string) (string, []any) {
	if d.pg {
		ids, vs := make([]string, len(refs)), make([]int32, len(refs))
		for i, r := range refs {
			ids[i], vs[i] = string(r.ID), int32(r.Version)
		}
		return `SELECT ` + d.nodeCols() + nodeFrom + ` JOIN unnest($1::uuid[], $2::int[]) AS r(id, version) ON r.id = v.node_id AND r.version = v.version
		      WHERE ($3 = '' OR n.type = $3) ORDER BY n.key`, []any{ids, vs, nodeType}
	}
	pairs := make([][2]any, len(refs))
	for i, r := range refs {
		pairs[i] = [2]any{string(r.ID), int(r.Version)}
	}
	b, _ := json.Marshal(pairs)
	return `SELECT ` + d.nodeCols() + nodeFrom + ` JOIN json_each(?1) r ON json_extract(r.value, '$[0]') = v.node_id AND json_extract(r.value, '$[1]') = v.version
		      WHERE (?2 = '' OR n.type = ?2) ORDER BY n.key`, []any{string(b), nodeType}
}

// nodeIDList tests col against a list of node ids given as the n-th argument: an array for PostgreSQL, a JSON list for
// SQLite.
func (d dialect) nodeIDList(col string, n int, ids []domain.NodeID) (string, any) {
	list := nodeIDStrings(ids)
	if d.pg {
		return col + ` = ANY(` + d.ph(n) + `::uuid[])`, list
	}
	b, _ := json.Marshal(list)
	return col + ` IN (SELECT value FROM json_each(` + d.ph(n) + `))`, string(b)
}

// sqlLandedVersions reads the versions that landed on a branch of a namespace in the baselines after the order after,
// up to upTo (Baseline.Seq, ADR 0140): written on the branch or joined to it, the last landing of each node, for some
// nodes (nil: every node), with whether that version deletes the node and why it was written (a revert, on the branch
// of a change).
func (d dialect) sqlLandedVersions(namespace, branch string, after, upTo int64, ids []domain.NodeID) (string, []any) {
	args := []any{domain.BranchOf(branch), after, upTo, domain.NamespaceOf(namespace)}
	if ids != nil && d.pg {
		return d.sqlFoldVersions(args, ids)
	}
	inV, inJ := "", ""
	if ids != nil {
		in, arg := d.nodeIDList("v.node_id", 5, ids)
		inJ = ` AND ` + strings.Replace(in, "v.node_id", "j.node_id", 1)
		inV, args = ` AND `+in, append(args, arg)
	}
	return `SELECT ` + d.id("node_id") + `, version, deleted, reason FROM (
		SELECT x.node_id, x.version, x.deleted, x.reason, row_number() OVER (PARTITION BY x.node_id ORDER BY x.landed DESC, x.version DESC) AS rn
		FROM (
			SELECT v.node_id, v.version, v.deleted, v.reason, v.landed FROM node_version v
			WHERE v.branch = ` + d.ph(1) + ` AND v.landed > ` + d.ph(2) + ` AND v.landed <= ` + d.ph(3) + inV + `
			UNION ALL
			SELECT j.node_id, j.version, v.deleted, v.reason, j.landed FROM node_branch j JOIN node_version v ON v.node_id = j.node_id AND v.version = j.version
			WHERE j.branch = ` + d.ph(1) + ` AND j.landed > ` + d.ph(2) + ` AND j.landed <= ` + d.ph(3) + inJ + `
		) x JOIN node n ON n.id = x.node_id WHERE n.namespace = ` + d.ph(4) + `
	) y WHERE rn = 1`, args
}

// sqlFoldVersions is sqlLandedVersions for some nodes on PostgreSQL: for each node its last landing on the branch in the
// range, one lookup of the indexes node_version_fold / node_branch_fold whatever the length of its history (the planner
// of a generic plan cannot estimate the range, a join over the list could walk every landing of the branch); what the
// version found holds is read by its key once it is found (scalar subqueries: a join would let the planner read the
// versions of the node first, one by one, a node estimated to have a single one).
func (d dialect) sqlFoldVersions(args []any, ids []domain.NodeID) (string, []any) {
	return `SELECT q.id::text, x.version,
			(SELECT w.deleted FROM node_version w WHERE w.node_id = q.id AND w.version = x.version),
			(SELECT w.reason FROM node_version w WHERE w.node_id = q.id AND w.version = x.version)
		FROM unnest($5::uuid[]) AS q(id) JOIN node n ON n.id = q.id AND n.namespace = $4
		CROSS JOIN LATERAL (
			SELECT u.version FROM (
				(SELECT v.version, v.landed FROM node_version v
				WHERE v.node_id = q.id AND v.branch = $1 AND v.landed > $2 AND v.landed <= $3 ORDER BY v.landed DESC, v.version DESC LIMIT 1)
				UNION ALL
				(SELECT j.version, j.landed FROM node_branch j
				WHERE j.node_id = q.id AND j.branch = $1 AND j.landed > $2 AND j.landed <= $3 ORDER BY j.landed DESC, j.version DESC LIMIT 1)
			) u ORDER BY u.landed DESC, u.version DESC LIMIT 1
		) x`, append(args, nodeIDStrings(ids))
}

func nodeIDStrings(ids []domain.NodeID) []string {
	list := make([]string, len(ids))
	for i, id := range ids {
		list[i] = string(id)
	}
	return list
}

// sqlNamespaceNodeIDs reads the nodes of a namespace (ADR 0143 §4), in the order of their ids.
func (d dialect) sqlNamespaceNodeIDs(namespace string) (string, []any) {
	return `SELECT ` + d.id("id") + ` FROM node WHERE namespace = ` + d.ph(1) + ` ORDER BY id`, []any{domain.NamespaceOf(namespace)}
}

// sqlLastLanding reads the order of the last landing on a branch (index node_version_landed, node_branch_landed).
func (d dialect) sqlLastLanding(branch string) (string, []any) {
	return `SELECT coalesce(max(landed), 0) FROM (SELECT max(landed) AS landed FROM node_version WHERE branch = ` + d.ph(1) +
		` UNION ALL SELECT max(landed) FROM node_branch WHERE branch = ` + d.ph(1) + `) l`, []any{domain.BranchOf(branch)}
}

// sqlBranchNodeIDs reads the nodes a branch holds a version of: written on it or joined to it (ADR 0137).
func (d dialect) sqlBranchNodeIDs(branch string) (string, []any) {
	return `SELECT ` + d.id("node_id") + ` FROM node_version WHERE branch = ` + d.ph(1) +
		` UNION SELECT ` + d.id("node_id") + ` FROM node_branch WHERE branch = ` + d.ph(1), []any{domain.BranchOf(branch)}
}

// sqlBaselineHeader reads the header of a baseline.
func (d dialect) sqlBaselineHeader(id domain.BaselineID) (string, []any) {
	return `SELECT ` + d.id("id") + `, name, ` + d.idOr("coalesce", "parent_id") + `, ` + d.idOr("coalesce", "merged_from") + `, ` + d.idOr("coalesce", "change_id") +
		`, created_at, branch, namespace, kind, ` + d.idOr("coalesce", "rebased_from") + `, seq FROM baseline WHERE id = ` + d.ph(1), []any{string(id)}
}

// refsRows is the rows (id, version) of the refs of refsArgs at placeholder n.
func (d dialect) refsRows(n int) string {
	if d.pg {
		return `SELECT * FROM unnest(` + d.ph(n) + `::uuid[], ` + d.ph(n+1) + `::int[])`
	}
	return `SELECT json_extract(value, '$[0]'), json_extract(value, '$[1]') FROM json_each(` + d.ph(n) + `)`
}

// heldLinks is the query of the links the versions of refsArgs at placeholder n hold (ADR 0141), each read as leaving
// that version: the rows of its set and of the sets it is a delta over (c, the chain of sets of each version: rv the
// version, sv a set), minus those a row of the chain drops. Its columns are the link's, unqualified. A set is a delta
// exactly when it is deep (links_depth > 0): asked so, the next set of the chain is read by its key (node_version_pkey),
// never by the index of the bases (links_base IS NOT NULL would let the planner scan every delta of the table).
func (d dialect) heldLinks(n int) string {
	return `WITH RECURSIVE r(id, version) AS (` + d.refsRows(n) + `),
		c(nid, rv, sv) AS (
			SELECT v.node_id, v.version, coalesce(v.links, v.version) FROM node_version v JOIN r ON r.id = v.node_id AND r.version = v.version
			UNION ALL
			SELECT c.nid, c.rv, s.links_base FROM c JOIN node_version s ON s.node_id = c.nid AND s.version = c.sv WHERE s.links_depth > 0
		),
		h AS (SELECT l.id, l.type, l.from_id, c.rv, l.to_id, l.to_version, l.props, l.change_id, l.removes FROM c JOIN link l ON l.from_id = c.nid AND l.from_version = c.sv)
		SELECT h.id, h.type, h.from_id, h.rv AS from_version, h.to_id, h.to_version, h.props, h.change_id FROM h
		WHERE h.removes IS NULL AND (h.id, h.from_id, h.rv) NOT IN (SELECT x.removes, x.from_id, x.rv FROM h x WHERE x.removes IS NOT NULL)`
}

// sqlLinksTo reads the links entering the version ref (any version of the node when ref.Version is 0), one per
// version holding it (ADR 0141), by id then version: the rows naming the target (t), the sets holding each (s: the set
// of the row, then the deltas over a set holding it that do not drop it), the versions holding those sets.
func (d dialect) sqlLinksTo(ref domain.NodeRef) (string, []any) {
	target, args := `to_id = `+d.ph(1), []any{string(ref.ID)}
	if ref.Version != 0 {
		target, args = target+` AND to_version = `+d.ph(2), append(args, int(ref.Version))
	}
	return `WITH RECURSIVE t AS (SELECT id, type, from_id, from_version, to_id, to_version, props, change_id FROM link
			WHERE ` + target + ` AND removes IS NULL),
		` + d.linksToRest(), args
}

// sqlLinksInto is sqlLinksTo for the exact versions of refsArgs.
func (d dialect) sqlLinksInto(refs []domain.NodeRef) (string, []any) {
	args, _ := d.refsArgs(refs)
	return `WITH RECURSIVE r(id, version) AS (` + d.refsRows(1) + `),
		t AS (SELECT l.id, l.type, l.from_id, l.from_version, l.to_id, l.to_version, l.props, l.change_id FROM link l
			JOIN r ON l.to_id = r.id AND l.to_version = r.version WHERE l.removes IS NULL),
		` + d.linksToRest(), args
}

// linksToRest is what follows the rows t naming the targets in sqlLinksTo: the sets holding them, the versions holding
// those sets.
func (d dialect) linksToRest() string {
	return `s(lid, nid, sv) AS (
			SELECT id, from_id, from_version FROM t
			UNION ALL
			SELECT s.lid, v.node_id, v.version FROM s JOIN node_version v ON v.node_id = s.nid AND v.links_base = s.sv
			WHERE NOT EXISTS (SELECT 1 FROM link x WHERE x.from_id = v.node_id AND x.from_version = v.version AND x.removes = s.lid)
		)
		SELECT ` + d.cols(su("t.id"), sc("t.type"), su("t.from_id"), sc("v.version"), su("t.to_id"), sc("t.to_version"), sc("t.props"), su("t.change_id")) + ` FROM s
		JOIN t ON t.id = s.lid JOIN node_version v ON v.node_id = s.nid AND (v.links = s.sv OR (v.links IS NULL AND v.version = s.sv))
		ORDER BY t.id, v.version`
}

// sqlLatestVersions reads the number of the latest version of several nodes: node, version.
func (d dialect) sqlLatestVersions(ids []domain.NodeID) (string, []any) {
	in, arg := d.nodeIDList("id", 1, ids)
	return `SELECT ` + d.id("id") + `, latest FROM node WHERE ` + in, []any{arg}
}

// sqlLatestVersionsOn reads the version sqlLatestOn reads for several nodes: node, version.
func (d dialect) sqlLatestVersionsOn(ids []domain.NodeID, branch string) (string, []any) {
	list, arg := `unnest(` + d.ph(1) + `::uuid[]) AS q(id)`, any(nodeIDStrings(ids))
	if !d.pg {
		b, _ := json.Marshal(nodeIDStrings(ids))
		list, arg = `(SELECT value AS id FROM json_each(`+d.ph(1)+`)) q`, string(b)
	}
	return `SELECT ` + d.id("id") + `, version FROM (SELECT q.id, ` + d.latestOn("q.id", d.ph(2)) + ` AS version FROM ` + list + `) l WHERE version > 0`,
		[]any{arg, domain.BranchOf(branch)}
}

// sqlLinkSetOf reads how a version holds its links (ADR 0141).
func (d dialect) sqlLinkSetOf(ref domain.NodeRef) (string, []any) {
	return `SELECT coalesce(links, version), coalesce(links_base, 0), links_depth FROM node_version WHERE node_id = ` + d.ph(1) + ` AND version = ` + d.ph(2),
		[]any{string(ref.ID), int(ref.Version)}
}

// sqlLinkSetsOf reads how the versions of refsArgs hold their links: node, version, then the columns of sqlLinkSetOf.
func (d dialect) sqlLinkSetsOf(refs []domain.NodeRef) (string, []any) {
	args, _ := d.refsArgs(refs)
	return `SELECT ` + d.id("v.node_id") + `, v.version, coalesce(v.links, v.version), coalesce(v.links_base, 0), v.links_depth FROM node_version v` +
		d.refsJoin("r", "v.node_id", "v.version", 1), args
}

// sqlPutLinkSet sets how a version holds its links: arguments node, version, links (nil: its own), base (nil: a whole
// set), depth.
func (d dialect) sqlPutLinkSet() string {
	return `UPDATE node_version SET links = ` + d.ph(3) + `, links_base = ` + d.ph(4) + `, links_depth = ` + d.ph(5) + ` WHERE node_id = ` + d.ph(1) + ` AND version = ` + d.ph(2)
}

// linkCols is the select list of a link.
func (d dialect) linkCols() string {
	return d.cols(su("id"), sc("type"), su("from_id"), sc("from_version"), su("to_id"), sc("to_version"), sc("props"), su("change_id"))
}

const branchColsText = `namespace, name, parent, %s, %s, origin, status, created_at, description, intent, %s, ephemeral, fork_seq`

// branchCols is the select list of a branch.
func (d dialect) branchCols() string {
	return fmt.Sprintf(branchColsText, d.idOr("coalesce", "fork_baseline"), d.idOr("coalesce", "head_baseline"), d.idOr("coalesce", "rebased_from"))
}

// sqlBranch reads a branch.
func (d dialect) sqlBranch(namespace, name string) (string, []any) {
	return `SELECT ` + d.branchCols() + ` FROM branch WHERE namespace = ` + d.ph(1) + ` AND name = ` + d.ph(2), []any{domain.NamespaceOf(namespace), name}
}

// sqlBranches reads the branches of a namespace, oldest first.
func (d dialect) sqlBranches(namespace string) (string, []any) {
	return `SELECT ` + d.branchCols() + ` FROM branch WHERE namespace = ` + d.ph(1) + d.orderBy("created_at", ""), []any{domain.NamespaceOf(namespace)}
}

// sqlNamespaceBaselineHeaders reads the header of every baseline of a namespace, oldest first (ADR 0133).
func (d dialect) sqlNamespaceBaselineHeaders(namespace string) (string, []any) {
	return `SELECT ` + d.headerListCols() + ` FROM baseline WHERE namespace = ` + d.ph(1) + d.orderBy("created_at", ", id"), []any{domain.NamespaceOf(namespace)}
}

// headerListCols is the select list of a baseline header in a listing.
func (d dialect) headerListCols() string {
	return d.id("id") + `, name, ` + d.idOr("coalesce", "parent_id") + `, ` + d.idOr("coalesce", "merged_from") + `, ` + d.idOr("coalesce", "change_id") +
		`, created_at, branch, kind, ` + d.idOr("coalesce", "rebased_from") + `, seq`
}

// sqlBaselinePage reads the headers of the baselines of a branch landed before the order before (0: the newest), newest
// first, at most limit.
func (d dialect) sqlBaselinePage(namespace, branch string, before int64, limit int) (string, []any) {
	return `SELECT ` + d.headerListCols() + ` FROM baseline WHERE namespace = ` + d.ph(1) + ` AND branch = ` + d.ph(2) + ` AND (` + d.ph(3) + ` = 0 OR seq < ` + d.ph(3) +
		`) ORDER BY seq DESC LIMIT ` + d.ph(4), []any{domain.NamespaceOf(namespace), domain.BranchOf(branch), before, limit}
}

// sqlBaselineHeadersOf reads the headers of baselines of a namespace by id, oldest first.
func (d dialect) sqlBaselineHeadersOf(namespace string, ids []domain.BaselineID) (string, []any) {
	list := make([]string, len(ids))
	for i, id := range ids {
		list[i] = string(id)
	}
	in, arg := d.uuidList("id", 2, list)
	return `SELECT ` + d.headerListCols() + ` FROM baseline WHERE namespace = ` + d.ph(1) + ` AND ` + in + ` ORDER BY seq`, []any{domain.NamespaceOf(namespace), arg}
}

// sqlChangeBaselineHeaders reads the headers of the baselines the changes left in a namespace, oldest first.
func (d dialect) sqlChangeBaselineHeaders(namespace string, changes []domain.ChangeID) (string, []any) {
	list := make([]string, len(changes))
	for i, id := range changes {
		list[i] = string(id)
	}
	in, arg := d.uuidList("change_id", 2, list)
	return `SELECT ` + d.headerListCols() + ` FROM baseline WHERE namespace = ` + d.ph(1) + ` AND ` + in + ` ORDER BY seq`, []any{domain.NamespaceOf(namespace), arg}
}

// sqlBaselineCounts counts the baselines of each branch of a namespace.
func (d dialect) sqlBaselineCounts(namespace string) (string, []any) {
	return `SELECT branch, count(*) FROM baseline WHERE namespace = ` + d.ph(1) + ` GROUP BY branch`, []any{domain.NamespaceOf(namespace)}
}

// uuidList tests col against a list of uuids given as the n-th argument: an array for PostgreSQL, a JSON list for SQLite.
func (d dialect) uuidList(col string, n int, list []string) (string, any) {
	if d.pg {
		return col + ` = ANY(` + d.ph(n) + `::uuid[])`, list
	}
	b, _ := json.Marshal(list)
	return col + ` IN (SELECT value FROM json_each(` + d.ph(n) + `))`, string(b)
}

// sqlBranchBaselineHeaders reads the header of every baseline of a branch of a namespace, oldest first.
func (d dialect) sqlBranchBaselineHeaders(namespace, branch string) (string, []any) {
	return `SELECT ` + d.headerListCols() + ` FROM baseline WHERE namespace = ` + d.ph(1) + ` AND branch = ` + d.ph(2) + ` ORDER BY seq`,
		[]any{domain.NamespaceOf(namespace), domain.BranchOf(branch)}
}

// sqlBaselineIDs reads the ids of the baselines of a namespace, oldest first.
func (d dialect) sqlBaselineIDs(namespace string) (string, []any) {
	return `SELECT ` + d.id("id") + ` FROM baseline WHERE namespace = ` + d.ph(1) + d.orderBy("created_at", ""), []any{domain.NamespaceOf(namespace)}
}

// sqlChangeIDs reads the ids of every change, oldest first.
func (d dialect) sqlChangeIDs() string {
	return `SELECT ` + d.id("id") + ` FROM change` + d.orderBy("created_at", "")
}

// sqlChangeHeads reads the heads of the changes (cols: the select list of the dialect) matching the parts of f a column
// answers, oldest first (indexes change_status, change_project): the namespace, the owner unit, the statuses and the
// project. The text query is the caller's (ChangesFilter.Match).
func (d dialect) sqlChangeHeads(cols string, f ChangesFilter) (string, []any) {
	q, args := `SELECT `+cols+` FROM change`+d.where1(), []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		q += " AND " + cond + " = " + d.ph(len(args))
	}
	if f.Namespace != "" {
		add("namespace", domain.NamespaceOf(f.Namespace))
	}
	if f.OwnerOrg != "" {
		add("owner_org", f.OwnerOrg)
	}
	if f.Project != "" {
		add("project_id", f.Project)
	}
	if len(f.Status) > 0 {
		ps := make([]string, len(f.Status))
		for i, st := range f.Status {
			args = append(args, string(st))
			ps[i] = d.ph(len(args))
		}
		q += " AND status IN (" + strings.Join(ps, ", ") + ")"
	}
	return q + d.orderBy("created_at", ""), args
}

// sqlSubChangeIDs reads the ids of the changes whose parent is a change, oldest first (index change_parent).
func (d dialect) sqlSubChangeIDs(parent domain.ChangeID) (string, []any) {
	return `SELECT ` + d.id("id") + ` FROM change WHERE parent_id = ` + d.ph(1) + d.orderBy("created_at", ""), []any{string(parent)}
}

// sqlOpenChangeIDs reads the ids of the changes neither applied nor abandoned, oldest first.
func (d dialect) sqlOpenChangeIDs() string {
	return `SELECT ` + d.id("id") + ` FROM change WHERE status NOT IN ('applied', 'abandoned')` + d.orderBy("created_at", "")
}

// sqlNodeChangePage reads the last limit changes that have an impact on a node, each with the order of its first one,
// whose order is below before (0: any), newest first.
func (d dialect) sqlNodeChangePage(node domain.NodeID, before int64, limit int) (string, []any) {
	q, args := `SELECT `+d.id("change_id")+`, min(seq) FROM change_impact WHERE node_id = `+d.ph(1)+` GROUP BY change_id`, []any{string(node)}
	if before > 0 {
		q, args = q+` HAVING min(seq) < `+d.ph(2), append(args, before)
	}
	return q + ` ORDER BY 2 DESC LIMIT ` + d.ph(len(args)+1), append(args, limit)
}

// changeImpactCols is the select list of a change impact (the order both repositories scan).
func (d dialect) changeImpactCols() string {
	return d.cols(su("id"), su("node_id"), sc("key"), sc("type"), sc("intent"), sc("rationale"), sc("pre_version"), sc("post_version"), sc("landed_version"),
		sc("review"), sc("reviews"), sqlCol{name: d.idOr("COALESCE", "via")}, sc("recheck"), sc("produced_by"), sc("derived_from"), sc("items"), sc("execution"),
		sc("created_at"), sc("flow"), sc("superseded"))
}

// sqlChangeImpacts reads the impacts of a change in the order they were written.
func (d dialect) sqlChangeImpacts(change domain.ChangeID) (string, []any) {
	return `SELECT ` + d.changeImpactCols() + ` FROM change_impact WHERE change_id = ` + d.ph(1) + ` ORDER BY seq`, []any{string(change)}
}

// logCols is the select list of a log entry.
func (d dialect) logCols() string {
	return d.cols(sc("seq"), sc("id"), su("change_id"), sc("type"), sc("flow"), sc("process_id"), sc("execution"), sc("subject"), sc("by_whom"), sc("causation"), sc("at"), sc("payload"))
}

// sqlLog reads the entries of the log matching f, in order.
func (d dialect) sqlLog(f domain.LogFilter) (string, []any) {
	where, args := logWhere(f, d.ph)
	q := `SELECT ` + d.logCols() + ` FROM change_log` + where + ` ORDER BY seq`
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	return q, args
}

// sqlLogCounts counts the entries of the log matching f by type (f.AfterSeq does not apply).
func (d dialect) sqlLogCounts(f domain.LogFilter) (string, []any) {
	f.AfterSeq = 0
	where, args := logWhere(f, d.ph)
	return `SELECT type, count(*) FROM change_log` + where + ` GROUP BY type`, args
}

// sqlTags reads the tags matching f, oldest first.
func (d dialect) sqlTags(f domain.TagFilter) (string, []any) {
	q, args := `SELECT `+d.cols(su("id"), sc("name"), sc("namespace"), su("node_id"), sc("version"), sc("by"), sc("created_at"))+` FROM tag`+d.where1(), []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		q += " AND " + cond + " = " + d.ph(len(args))
	}
	if f.Namespace != "" {
		add("namespace", domain.NamespaceOf(f.Namespace))
	}
	if f.Name != "" {
		add("name", f.Name)
	}
	if f.Node != "" {
		add("node_id", string(f.Node))
	}
	return q + d.orderBy("created_at", ", id"), args
}

// sqlInsert is an INSERT of one row of the columns, bound to the arguments in order; tail is appended (a conflict or
// RETURNING clause).
func (d dialect) sqlInsert(table string, columns []string, tail string) string {
	ps := make([]string, len(columns))
	for i := range columns {
		ps[i] = d.ph(i + 1)
	}
	return `INSERT INTO ` + table + ` (` + strings.Join(columns, ", ") + `) VALUES (` + strings.Join(ps, ", ") + `)` + tail
}

// sqlPutTag inserts a tag (ADR 0143 §6), its arguments in the order of the columns.
func (d dialect) sqlPutTag() string {
	return d.sqlInsert("tag", []string{"id", "name", "namespace", "node_id", "version", "by", "created_at"}, ` ON CONFLICT (id) DO UPDATE SET name = excluded.name`)
}

// sqlDeleteTag deletes a tag.
func (d dialect) sqlDeleteTag(id domain.TagID) (string, []any) {
	return `DELETE FROM tag WHERE id = ` + d.ph(1), []any{string(id)}
}

// sqlDeleteChangeImpact removes a change impact from the projection: arguments change, change impact.
func (d dialect) sqlDeleteChangeImpact() string {
	return `DELETE FROM change_impact WHERE change_id = ` + d.ph(1) + ` AND id = ` + d.ph(2)
}

// sqlLinkByID reads one link.
func (d dialect) sqlLinkByID(id domain.LinkID) (string, []any) {
	return `SELECT ` + d.linkCols() + ` FROM link WHERE id = ` + d.ph(1) + ` AND removes IS NULL`, []any{string(id)}
}

// sqlSetNodeOrigin sets the origin of a version: arguments node, version, change, change impact, comment.
func (d dialect) sqlSetNodeOrigin() string {
	return `UPDATE node_version SET change_id = ` + d.ph(3) + `, change_impact = ` + d.ph(4) + `, comment = ` + d.ph(5) + ` WHERE node_id = ` + d.ph(1) + ` AND version = ` + d.ph(2)
}

// sqlJoinBranch makes a version join a branch: arguments node, version, branch, change, the order of the baseline it
// lands in.
func (d dialect) sqlJoinBranch() string {
	return `INSERT INTO node_branch (node_id, version, branch, change_id, landed) SELECT node_id, version, ` + d.ph(3) + `, ` + d.ph(4) + `, ` + d.ph(5) + ` FROM node_version
		WHERE node_id = ` + d.ph(1) + ` AND version = ` + d.ph(2) + ` AND branch <> ` + d.ph(3) + ` ON CONFLICT DO NOTHING`
}

// deleteChangeUsed are the counts that must be zero for a change to be deleted: what it wrote is used by nothing it
// did not write. The one argument is the change.
func (d dialect) deleteChangeUsed() []string {
	p := d.ph(1)
	return []string{
		`SELECT count(*) FROM baseline WHERE change_id = ` + p,
		`SELECT count(*) FROM link l JOIN node_version v ON l.to_id = v.node_id AND l.to_version = v.version WHERE v.change_id = ` + p + ` AND ` + d.differs("l.change_id", p),
		`SELECT count(*) FROM node_version o JOIN node_version v ON o.node_id = v.node_id AND o.version > v.version WHERE v.change_id = ` + p + ` AND ` + d.differs("o.change_id", p),
	}
}

// sqlChangeNodes reads the nodes a change wrote versions of (one argument: the change).
func (d dialect) sqlChangeNodes() string {
	return `SELECT DISTINCT ` + d.id("node_id") + ` FROM node_version WHERE change_id = ` + d.ph(1)
}

// deleteChangeRows are the deletions of what a change wrote (one argument: the change).
func (d dialect) deleteChangeRows() []string {
	p := d.ph(1)
	mine := `(SELECT node_id, version FROM node_version WHERE change_id = ` + p + `)`
	return []string{
		`DELETE FROM tag WHERE (node_id, version) IN ` + mine,
		`DELETE FROM link WHERE change_id = ` + p + ` OR (from_id, from_version) IN ` + mine,
		`DELETE FROM node_branch WHERE change_id = ` + p + ` OR (node_id, version) IN ` + mine,
		`DELETE FROM node_version WHERE change_id = ` + p,
		`DELETE FROM change_impact WHERE change_id = ` + p,
		`DELETE FROM change_log WHERE change_id = ` + p,
	}
}

// sqlDeleteOrphanNode deletes a node left with no version (one argument: the node).
func (d dialect) sqlDeleteOrphanNode() string {
	return `DELETE FROM node WHERE id = ` + d.ph(1) + ` AND NOT EXISTS (SELECT 1 FROM node_version WHERE node_id = ` + d.ph(1) + `)`
}

// sqlRefreshLatest sets the latest version of a node to the last it has (one argument: the node).
func (d dialect) sqlRefreshLatest() string {
	return `UPDATE node SET latest = (SELECT max(version) FROM node_version WHERE node_id = ` + d.ph(1) + `) WHERE id = ` + d.ph(1)
}

// sqlDeleteBranch deletes a branch (arguments namespace, name).
func (d dialect) sqlDeleteBranch() string {
	return `DELETE FROM branch WHERE namespace = ` + d.ph(1) + ` AND name = ` + d.ph(2)
}

// sqlDeleteChange deletes a change (one argument).
func (d dialect) sqlDeleteChange() string { return `DELETE FROM change WHERE id = ` + d.ph(1) }

// The columns of the inserts both repositories run, in the order of their arguments.
var (
	nodeVersionColumns = []string{"node_id", "version", "props", "deleted", "change_id", "created_at", "branch", "parents", "reason", "state", "change_impact", "comment", "execution", "owner_id", "origins", "landed"}
	linkColumns        = []string{"id", "type", "from_id", "from_version", "to_id", "to_version", "props", "change_id", "removes"}
	baselineColumns    = []string{"id", "name", "parent_id", "merged_from", "change_id", "created_at", "branch", "namespace", "kind", "rebased_from", "seq"}
	changeLogColumns   = []string{"id", "change_id", "type", "flow", "process_id", "execution", "subject", "by_whom", "causation", "at", "payload"}
)

// refsJoin joins the versions refs as the alias a on the columns idCol / verCol: an array pair bound at n and n+1 for
// PostgreSQL, a JSON list bound at n for SQLite. The join may be repeated with the same arguments.
func (d dialect) refsJoin(a, idCol, verCol string, n int) string {
	if d.pg {
		return ` JOIN unnest(` + d.ph(n) + `::uuid[], ` + d.ph(n+1) + `::int[]) AS ` + a + `(id, version) ON ` + a + `.id = ` + idCol + ` AND ` + a + `.version = ` + verCol
	}
	return ` JOIN json_each(` + d.ph(n) + `) ` + a + ` ON json_extract(` + a + `.value, '$[0]') = ` + idCol + ` AND json_extract(` + a + `.value, '$[1]') = ` + verCol
}

// refsArgs are the arguments of refsJoin, and the number of the next placeholder after them.
func (d dialect) refsArgs(refs []domain.NodeRef) ([]any, int) {
	if d.pg {
		ids, vs := make([]string, len(refs)), make([]int32, len(refs))
		for i, r := range refs {
			ids[i], vs[i] = string(r.ID), int32(r.Version)
		}
		return []any{ids, vs}, 3
	}
	pairs := make([][2]any, len(refs))
	for i, r := range refs {
		pairs[i] = [2]any{string(r.ID), int(r.Version)}
	}
	b, _ := json.Marshal(pairs)
	return []any{string(b)}, 2
}

// likePattern is the LIKE pattern of the rows containing text (case-insensitive), "" for no text.
func likePattern(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return ""
	}
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text) + "%"
}

// textMatch holds when the pattern bound at n is empty, or one of cols or a string property of the JSON object props
// matches it (lower case).
func (d dialect) textMatch(n int, props string, cols ...string) string {
	p := d.ph(n)
	out := `(` + p + ` = ''`
	for _, c := range cols {
		out += ` OR lower(` + c + `) LIKE ` + p + ` ESCAPE '\'`
	}
	if d.pg {
		return out + ` OR EXISTS (SELECT 1 FROM jsonb_each(` + props + `) p WHERE jsonb_typeof(p.value) = 'string' AND lower(p.value #>> '{}') LIKE ` + p + ` ESCAPE '\'))`
	}
	return out + ` OR EXISTS (SELECT 1 FROM json_each(` + props + `) p WHERE p.type = 'text' AND lower(p.value) LIKE ` + p + ` ESCAPE '\'))`
}

// byteOrder orders a text column by its bytes, as Go compares strings.
func (d dialect) byteOrder(col string) string {
	if d.pg {
		return col + ` COLLATE "C"`
	}
	return col
}

// nodeRefsWhere selects the node versions refs matching the text and the deleted filter of q.
func (d dialect) nodeRefsWhere(refs []domain.NodeRef, q NodeQuery) (string, []any, int) {
	args, n := d.refsArgs(refs)
	args = append(args, likePattern(q.Text), q.IncludeDeleted)
	return nodeFrom + d.refsJoin("r", "v.node_id", "v.version", 1) + ` WHERE ` + d.textMatch(n, "v.props", "n.key", "n.type") +
		` AND (` + d.ph(n+1) + ` OR NOT v.deleted)`, args, n + 2
}

// sqlNodePage reads the page of q of the node versions refs, ordered by key.
func (d dialect) sqlNodePage(refs []domain.NodeRef, q NodeQuery) (string, []any) {
	where, args, n := d.nodeRefsWhere(refs, q)
	args = append(args, q.Type, q.Limit, q.Offset)
	return `SELECT ` + d.nodeCols() + where + ` AND (` + d.ph(n) + ` = '' OR n.type = ` + d.ph(n) + `) ORDER BY ` + d.byteOrder("n.key") +
		` LIMIT ` + d.ph(n+1) + ` OFFSET ` + d.ph(n+2), args
}

// sqlNodeTypeCounts counts the node versions refs matching the text and the deleted filter of q, by type.
func (d dialect) sqlNodeTypeCounts(refs []domain.NodeRef, q NodeQuery) (string, []any) {
	where, args, _ := d.nodeRefsWhere(refs, q)
	return `SELECT n.type, count(*)` + where + ` GROUP BY n.type`, args
}

// qualifiedLinkCols is the select list of a link read as the alias l.
func (d dialect) qualifiedLinkCols() string {
	return d.cols(su("l.id"), sc("l.type"), su("l.from_id"), sc("l.from_version"), su("l.to_id"), sc("l.to_version"), sc("l.props"), su("l.change_id"))
}

// sqlLinksFrom reads the links leaving the versions refs, by id.
func (d dialect) sqlLinksFrom(refs []domain.NodeRef) (string, []any) {
	args, _ := d.refsArgs(refs)
	return `SELECT ` + d.qualifiedLinkCols() + ` FROM (` + d.heldLinks(1) + `) l ORDER BY l.id`, args
}

// linkRefsWhere selects the links between two of the versions refs matching the text of q.
func (d dialect) linkRefsWhere(refs []domain.NodeRef, q LinkQuery) (string, []any, int) {
	args, n := d.refsArgs(refs)
	args = append(args, likePattern(q.Text))
	return ` FROM (` + d.heldLinks(1) + `) l` + d.refsJoin("t", "l.to_id", "l.to_version", 1) +
		` WHERE ` + d.textMatch(n, "l.props", "l.type"), args, n + 1
}

// sqlLinkPage reads the page of q of the links between two of the versions refs, by type then id.
func (d dialect) sqlLinkPage(refs []domain.NodeRef, q LinkQuery) (string, []any) {
	where, args, n := d.linkRefsWhere(refs, q)
	args = append(args, q.Type, q.Limit, q.Offset)
	return `SELECT ` + d.qualifiedLinkCols() + where + ` AND (` + d.ph(n) + ` = '' OR l.type = ` + d.ph(n) + `) ORDER BY ` + d.byteOrder("l.type") +
		`, l.id LIMIT ` + d.ph(n+1) + ` OFFSET ` + d.ph(n+2), args
}

// sqlLinkTypeCounts counts the links between two of the versions refs matching the text of q, by type.
func (d dialect) sqlLinkTypeCounts(refs []domain.NodeRef, q LinkQuery) (string, []any) {
	where, args, _ := d.linkRefsWhere(refs, q)
	return `SELECT l.type, count(*)` + where + ` GROUP BY l.type`, args
}
