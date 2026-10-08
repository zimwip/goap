package graph

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
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

// sqlLatestOn reads the latest version of a node on a branch (the branch as domain.BranchOf names it).
func (d dialect) sqlLatestOn(id domain.NodeID, branch string) (string, []any) {
	return `SELECT ` + d.nodeCols() + nodeFrom + ` WHERE n.id = ` + d.ph(1) + ` AND ` + onBranchSQL(d.ph(2)) + ` ORDER BY v.version DESC LIMIT 1`,
		[]any{string(id), domain.BranchOf(branch)}
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

// sqlVersionBranches reads the branches the versions of a node joined.
func (d dialect) sqlVersionBranches(id domain.NodeID) (string, []any) {
	return `SELECT version, branch FROM node_branch WHERE node_id = ` + d.ph(1) + ` ORDER BY branch`, []any{string(id)}
}

// sqlNodeIDByKey reads the id of a node by its key.
func (d dialect) sqlNodeIDByKey(namespace, key string) (string, []any) {
	return `SELECT ` + d.id("id") + ` FROM node WHERE namespace = ` + d.ph(1) + ` AND key = ` + d.ph(2), []any{domain.NamespaceOf(namespace), key}
}

// sqlNodesIn reads the nodes of a baseline, of one type when nodeType is not empty.
func (d dialect) sqlNodesIn(baseline domain.BaselineID, nodeType string) (string, []any) {
	return baselineEntriesSQL(d.ph(1)) + ` SELECT ` + d.nodeCols() + ` FROM eff e JOIN node n ON n.id = e.node_id
		      JOIN node_version v ON v.node_id = e.node_id AND v.version = e.version
		      WHERE e.rn = 1 AND NOT e.removed AND (` + d.ph(2) + ` = '' OR n.type = ` + d.ph(2) + `) ORDER BY n.key`, []any{string(baseline), nodeType}
}

// sqlBaselineEntries reads the entries in effect in a baseline.
func (d dialect) sqlBaselineEntries(id domain.BaselineID) (string, []any) {
	return baselineEntriesSQL(d.ph(1)) + ` SELECT ` + d.id("node_id") + `, version FROM eff WHERE rn = 1 AND NOT removed`, []any{string(id)}
}

// sqlLinks reads the links leaving (out) or entering the version ref.
func (d dialect) sqlLinks(out bool, ref domain.NodeRef) (string, []any) {
	end := "to"
	if out {
		end = "from"
	}
	return `SELECT ` + d.linkCols() + ` FROM link WHERE ` + end + `_id = ` + d.ph(1) + ` AND ` + end + `_version = ` + d.ph(2) + ` ORDER BY id`, []any{string(ref.ID), int(ref.Version)}
}

// linkCols is the select list of a link.
func (d dialect) linkCols() string {
	return d.cols(su("id"), sc("type"), su("from_id"), sc("from_version"), su("to_id"), sc("to_version"), sc("props"), su("change_id"))
}

const branchColsText = `namespace, name, parent, %s, %s, origin, status, created_at, description, intent`

// branchCols is the select list of a branch.
func (d dialect) branchCols() string {
	return fmt.Sprintf(branchColsText, d.idOr("coalesce", "fork_baseline"), d.idOr("coalesce", "head_baseline"))
}

// sqlBranch reads a branch.
func (d dialect) sqlBranch(namespace, name string) (string, []any) {
	return `SELECT ` + d.branchCols() + ` FROM branch WHERE namespace = ` + d.ph(1) + ` AND name = ` + d.ph(2), []any{domain.NamespaceOf(namespace), name}
}

// sqlBranches reads the branches of a namespace, oldest first.
func (d dialect) sqlBranches(namespace string) (string, []any) {
	return `SELECT ` + d.branchCols() + ` FROM branch WHERE namespace = ` + d.ph(1) + d.orderBy("created_at", ""), []any{domain.NamespaceOf(namespace)}
}

// sqlBranchJoins reads the node versions a change made join a branch.
func (d dialect) sqlBranchJoins(namespace, branch string, change domain.ChangeID) (string, []any) {
	return `SELECT ` + d.id("j.node_id") + `, j.version FROM node_branch j JOIN node n ON n.id = j.node_id WHERE n.namespace = ` + d.ph(1) +
		` AND j.branch = ` + d.ph(2) + ` AND j.change_id = ` + d.ph(3), []any{domain.NamespaceOf(namespace), domain.BranchOf(branch), string(change)}
}

// sqlBaselineHeaders reads the header of every materialised baseline.
func (d dialect) sqlBaselineHeaders() string {
	return `SELECT ` + d.id("id") + `, ` + d.idOr("coalesce", "parent_id") + `, depth FROM baseline WHERE gap = 0` + d.orderBy("created_at", ", id")
}

// sqlBaselineIDs reads the ids of the baselines of a namespace, oldest first.
func (d dialect) sqlBaselineIDs(namespace string) (string, []any) {
	return `SELECT ` + d.id("id") + ` FROM baseline WHERE namespace = ` + d.ph(1) + d.orderBy("created_at", ""), []any{domain.NamespaceOf(namespace)}
}

// sqlChangeIDs reads the ids of every change, oldest first.
func (d dialect) sqlChangeIDs() string {
	return `SELECT ` + d.id("id") + ` FROM change` + d.orderBy("created_at", "")
}

// sqlOpenChangeIDs reads the ids of the changes neither applied nor abandoned, oldest first.
func (d dialect) sqlOpenChangeIDs() string {
	return `SELECT ` + d.id("id") + ` FROM change WHERE status NOT IN ('applied', 'abandoned')` + d.orderBy("created_at", "")
}

// sqlNodeChangeImpacts reads the changes that have an impact on a node.
func (d dialect) sqlNodeChangeImpacts(node domain.NodeID) (string, []any) {
	return `SELECT ` + d.id("change_id") + ` FROM change_impact WHERE node_id = ` + d.ph(1) + ` ORDER BY seq`, []any{string(node)}
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
	return d.cols(sc("seq"), sc("id"), su("change_id"), sc("type"), sc("flow"), sc("process_id"), sc("execution"), sc("subject"), sc("by_whom"), sc("at"), sc("payload"), sc("labels"))
}

// sqlLog reads the entries of the log matching f, in order.
func (d dialect) sqlLog(f domain.LogFilter) (string, []any) {
	where, args := logWhere(f, d)
	q := `SELECT ` + d.logCols() + ` FROM change_log` + where + ` ORDER BY seq`
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	return q, args
}

// sqlLogCounts counts the entries of the log matching f by type (f.AfterSeq does not apply).
func (d dialect) sqlLogCounts(f domain.LogFilter) (string, []any) {
	f.AfterSeq = 0
	where, args := logWhere(f, d)
	return `SELECT type, count(*) FROM change_log` + where + ` GROUP BY type`, args
}

// sqlTags reads the tags matching f, oldest first.
func (d dialect) sqlTags(f domain.TagFilter) (string, []any) {
	q, args := `SELECT `+d.cols(su("id"), sc("name"), sc("namespace"), su("change_id"), sqlCol{name: d.idOr("COALESCE", "baseline_id")}, sc("by"), sc("created_at"))+` FROM tag`+d.where1(), []any{}
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
	if f.Change != "" {
		add("change_id", string(f.Change))
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
	return `SELECT ` + d.linkCols() + ` FROM link WHERE id = ` + d.ph(1), []any{string(id)}
}

// sqlSetNodeOrigin sets the origin of a version: arguments node, version, change, change impact, comment.
func (d dialect) sqlSetNodeOrigin() string {
	return `UPDATE node_version SET change_id = ` + d.ph(3) + `, change_impact = ` + d.ph(4) + `, comment = ` + d.ph(5) + ` WHERE node_id = ` + d.ph(1) + ` AND version = ` + d.ph(2)
}

// sqlJoinBranch makes a version join a branch: arguments node, version, branch, change.
func (d dialect) sqlJoinBranch() string {
	return `INSERT INTO node_branch (node_id, version, branch, change_id) SELECT node_id, version, ` + d.ph(3) + `, ` + d.ph(4) + ` FROM node_version
		WHERE node_id = ` + d.ph(1) + ` AND version = ` + d.ph(2) + ` AND branch <> ` + d.ph(3) + ` ON CONFLICT DO NOTHING`
}

// deleteChangeUsed are the counts that must be zero for a change to be deleted: what it wrote is used by nothing it
// did not write. The one argument is the change.
func (d dialect) deleteChangeUsed() []string {
	p := d.ph(1)
	return []string{
		`SELECT count(*) FROM baseline_entry e JOIN node_version v ON e.node_id = v.node_id AND e.version = v.version WHERE v.change_id = ` + p,
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
		`DELETE FROM link WHERE change_id = ` + p + ` OR (from_id, from_version) IN ` + mine,
		`DELETE FROM node_branch WHERE change_id = ` + p + ` OR (node_id, version) IN ` + mine,
		`DELETE FROM node_version WHERE change_id = ` + p,
		`DELETE FROM change_impact WHERE change_id = ` + p,
		`DELETE FROM change_log WHERE change_id = ` + p,
		`DELETE FROM change_object WHERE change_id = ` + p,
		`DELETE FROM tag WHERE change_id = ` + p,
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
	nodeVersionColumns  = []string{"node_id", "version", "props", "deleted", "change_id", "created_at", "branch", "parents", "reason", "state", "change_impact", "comment", "execution", "owner_id", "origins"}
	linkColumns         = []string{"id", "type", "from_id", "from_version", "to_id", "to_version", "props", "change_id"}
	baselineColumns     = []string{"id", "name", "parent_id", "merged_from", "change_id", "created_at", "branch", "namespace", "depth", "gap", "kind"}
	changeLogColumns    = []string{"id", "change_id", "type", "flow", "process_id", "execution", "subject", "by_whom", "at", "payload", "labels"}
	changeObjectColumns = []string{"change_id", "type", "key", "workspace", "version", "seq", "created_seq", "state", "value", "labels", "by_whom", "at"}
)

// labelsWhere is the condition an entry or a change object carries every label of want: the jsonb containment in
// PostgreSQL, one json_extract per label in SQLite (labels are checked by validLabels: no quote in a key).
func (d dialect) labelsWhere(want map[string]string, arg func(any) string) []string {
	if len(want) == 0 {
		return nil
	}
	if d.pg {
		raw, _ := json.Marshal(want)
		return []string{"labels @> " + arg(string(raw)) + "::jsonb"}
	}
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		out = append(out, "json_extract(labels, "+arg(`$."`+k+`"`)+") = "+arg(want[k]))
	}
	return out
}

// sqlUpsertChangeObject writes the last version of a change object in the projection (the arguments of
// changeObjectColumns); created_seq keeps the position of its first version.
func (d dialect) sqlUpsertChangeObject() string {
	return d.sqlInsert("change_object", changeObjectColumns, ` ON CONFLICT (change_id, type, key, workspace) DO UPDATE SET version = excluded.version,
		seq = excluded.seq, state = excluded.state, value = excluded.value, labels = excluded.labels, by_whom = excluded.by_whom, at = excluded.at`)
}

// changeObjectCols is the select list of a change object.
func (d dialect) changeObjectCols() string {
	return d.cols(su("change_id"), sc("type"), sc("key"), sc("workspace"), sc("version"), sc("seq"), sc("state"), sc("value"), sc("labels"), sc("by_whom"), sc("at"))
}

// sqlChangeObjects reads the change objects of a change matching f (AtSeq aside), in the order of their first version.
func (d dialect) sqlChangeObjects(change domain.ChangeID, f domain.ObjectFilter) (string, []any) {
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return d.ph(len(args))
	}
	conds := []string{"change_id = " + arg(string(change))}
	in := func(col string, vals []string) {
		ps := make([]string, len(vals))
		for i, v := range vals {
			ps[i] = arg(v)
		}
		conds = append(conds, col+" IN ("+strings.Join(ps, ", ")+")")
	}
	if len(f.Types) > 0 {
		in("type", f.Types)
	}
	if f.KeyPrefix != "" {
		conds = append(conds, "substr(key, 1, "+strconv.Itoa(len(f.KeyPrefix))+") = "+arg(f.KeyPrefix))
	}
	if f.Workspaces != nil {
		if len(f.Workspaces) == 0 {
			conds = append(conds, "1 = 0")
		} else {
			in("workspace", f.Workspaces)
		}
	}
	conds = append(conds, d.labelsWhere(f.Labels, arg)...)
	return `SELECT ` + d.changeObjectCols() + ` FROM change_object WHERE ` + strings.Join(conds, " AND ") + ` ORDER BY created_seq`, args
}
