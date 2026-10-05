package graph

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

// SQLiteMigrations holds the SQLite schema of the graph (local development mode).
//
//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// SQLite is a Repo on a SQLite database (database/sql, schema migrated from
// SQLiteMigrations). The driver is registered by the caller.
type SQLite struct{ db *sql.DB }

// NewSQLite returns a repository on db.
func NewSQLite(db *sql.DB) *SQLite { return &SQLite{db: db} }

// InTx implements Repo.
func (s *SQLite) InTx(ctx context.Context, fn func(tx Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&sqliteTx{tx: tx}); err != nil {
		tx.Rollback() //nolint:errcheck
		return err
	}
	return tx.Commit()
}

type sqliteTx struct{ tx *sql.Tx }

// sqliteTime is a fixed-width UTC layout: text order is time order.
const sqliteTime = "2006-01-02T15:04:05.000000000Z"

func tsText(t time.Time) string { return t.UTC().Format(sqliteTime) }

func tsParse(s string) time.Time {
	t, _ := time.Parse(sqliteTime, s)
	return t
}

func sqliteErr(err error, what string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"), strings.Contains(msg, "PRIMARY KEY constraint failed"):
		return fmt.Errorf("%s: %s: %w", what, msg, ErrConflict)
	case strings.Contains(msg, "FOREIGN KEY constraint failed"):
		return fmt.Errorf("%s: %s: %w", what, msg, ErrInvalid)
	}
	return err
}

const sqliteNodeCols = `n.id, v.version, n.namespace, n.key, n.type, v.props, v.deleted, v.change_id, v.created_at, v.branch, v.parents, v.reason, v.state, v.change_impact, v.comment, v.execution, v.owner_id, n.project_id`

type scanner interface{ Scan(dest ...any) error }

func sqliteScanNode(row scanner) (domain.Node, error) {
	var n domain.Node
	var id, p, created, parents string
	var change, cnode sql.NullString
	var owner, project string
	var version int
	if err := row.Scan(&id, &version, &n.Namespace, &n.Key, &n.Type, &p, &n.Deleted, &change, &created, &n.Branch, &parents, &n.Reason, &n.State, &cnode, &n.Comment, &n.Execution, &owner, &project); err != nil {
		return n, err
	}
	_ = json.Unmarshal([]byte(parents), &n.Parents)
	if len(n.Parents) == 0 {
		n.Parents = nil
	}
	n.ID, n.Version, n.Properties, n.ChangeID = domain.NodeID(id), domain.Version(version), props([]byte(p)), domain.ChangeID(change.String)
	n.ChangeImpact = domain.ChangeImpactID(cnode.String)
	n.Owner, n.Project = domain.NodeID(owner), domain.NodeID(project)
	n.CreatedAt = tsParse(created)
	return n, nil
}

func (t *sqliteTx) nodes(ctx context.Context, q string, args ...any) ([]domain.Node, error) {
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Node
	for rows.Next() {
		n, err := sqliteScanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (t *sqliteTx) Node(ctx context.Context, ref domain.NodeRef) (domain.Node, error) {
	if ref.Version == 0 {
		return t.LatestOn(ctx, ref.ID, domain.MainBranch)
	}
	q := `SELECT ` + sqliteNodeCols + ` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = ? AND v.version = ?`
	n, err := sqliteScanNode(t.tx.QueryRowContext(ctx, q, string(ref.ID), int(ref.Version)))
	return n, sqliteErr(err, "node "+ref.String())
}

func (t *sqliteTx) LatestOn(ctx context.Context, id domain.NodeID, branch string) (domain.Node, error) {
	q := `SELECT ` + sqliteNodeCols + ` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = ? AND ` + sqliteOnBranch + ` ORDER BY v.version DESC LIMIT 1`
	n, err := sqliteScanNode(t.tx.QueryRowContext(ctx, q, string(id), domain.BranchOf(branch), domain.BranchOf(branch)))
	return n, sqliteErr(err, "node "+string(id)+" on "+domain.BranchOf(branch))
}

func (t *sqliteTx) Versions(ctx context.Context, id domain.NodeID) ([]domain.Node, error) {
	out, err := t.nodes(ctx, `SELECT `+sqliteNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = ? ORDER BY v.version`, string(id))
	if err == nil && len(out) == 0 {
		err = fmt.Errorf("node %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return out, err
	}
	rows, err := t.tx.QueryContext(ctx, `SELECT version, branch FROM node_branch WHERE node_id = ? ORDER BY branch`, string(id))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		var b string
		if err := rows.Scan(&v, &b); err != nil {
			return out, err
		}
		if v >= 1 && v <= len(out) {
			out[v-1].Joined = append(out[v-1].Joined, b)
		}
	}
	return out, rows.Err()
}

// sqliteOnBranch selects the versions v that are part of a branch (two parameters, the branch twice): written
// there, or joined (ADR 0032).
const sqliteOnBranch = `(v.branch = ? OR EXISTS (SELECT 1 FROM node_branch j WHERE j.node_id = v.node_id AND j.version = v.version AND j.branch = ?))`

const sqliteBranchCols = `namespace, name, parent, coalesce(fork_baseline, ''), coalesce(head_baseline, ''), origin, status, created_at, description, intent`

func sqliteScanBranch(row scanner) (domain.Branch, error) {
	var b domain.Branch
	var created string
	err := row.Scan(&b.Namespace, &b.Name, &b.Parent, (*string)(&b.ForkBaseline), (*string)(&b.Head), &b.Origin, &b.Status, &created, &b.Description, (*string)(&b.Intent))
	b.CreatedAt = tsParse(created)
	return b, err
}

func (t *sqliteTx) Branch(ctx context.Context, namespace, name string) (domain.Branch, error) {
	b, err := sqliteScanBranch(t.tx.QueryRowContext(ctx, `SELECT `+sqliteBranchCols+` FROM branch WHERE namespace = ? AND name = ?`, domain.NamespaceOf(namespace), name))
	return b, sqliteErr(err, "branch "+name)
}

func (t *sqliteTx) Branches(ctx context.Context, namespace string) ([]domain.Branch, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT `+sqliteBranchCols+` FROM branch WHERE namespace = ? ORDER BY created_at, rowid`, domain.NamespaceOf(namespace))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Branch
	for rows.Next() {
		b, err := sqliteScanBranch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (t *sqliteTx) PutBranch(ctx context.Context, b domain.Branch) error {
	namespace := domain.NamespaceOf(b.Namespace)
	_, err := t.tx.ExecContext(ctx, `INSERT INTO branch (namespace, name, parent, fork_baseline, head_baseline, origin, status, created_at, description, intent) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (namespace, name) DO UPDATE SET status = excluded.status, head_baseline = excluded.head_baseline, description = excluded.description`,
		namespace, b.Name, b.Parent, nullUUID(string(b.ForkBaseline)), nullUUID(string(b.Head)), b.Origin, b.Status, tsText(b.CreatedAt), b.Description, string(b.Intent))
	return sqliteErr(err, "branch "+b.Name)
}

func (t *sqliteTx) NodeIDByKey(ctx context.Context, namespace, key string) (domain.NodeID, error) {
	var id string
	if err := t.tx.QueryRowContext(ctx, `SELECT id FROM node WHERE namespace = ? AND key = ?`, domain.NamespaceOf(namespace), key).Scan(&id); err != nil {
		return "", sqliteErr(err, "node key "+key)
	}
	return domain.NodeID(id), nil
}

func (t *sqliteTx) NodeByKey(ctx context.Context, namespace, key string) (domain.Node, error) {
	var id string
	if err := t.tx.QueryRowContext(ctx, `SELECT id FROM node WHERE namespace = ? AND key = ?`, domain.NamespaceOf(namespace), key).Scan(&id); err != nil {
		return domain.Node{}, sqliteErr(err, "node key "+key)
	}
	return t.LatestOn(ctx, domain.NodeID(id), domain.MainBranch)
}

func (t *sqliteTx) NodesIn(ctx context.Context, baseline domain.BaselineID, nodeType string) ([]domain.Node, error) {
	if _, err := t.Baseline(ctx, baseline); err != nil {
		return nil, err
	}
	return t.nodes(ctx, baselineEntriesSQL("?")+` SELECT `+sqliteNodeCols+` FROM eff e JOIN node n ON n.id = e.node_id
	      JOIN node_version v ON v.node_id = e.node_id AND v.version = e.version
	      WHERE e.rn = 1 AND NOT e.removed AND (? = '' OR n.type = ?) ORDER BY n.key`, string(baseline), nodeType, nodeType)
}

func (t *sqliteTx) LatestNodes(ctx context.Context, namespace, branch string) ([]domain.Node, error) {
	ns, br := domain.NamespaceOf(namespace), domain.BranchOf(branch)
	return t.nodes(ctx, `SELECT `+sqliteNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id
		WHERE n.namespace = ? AND v.version = (SELECT max(version) FROM (
		  SELECT version FROM node_version WHERE node_id = n.id AND branch = ?
		  UNION ALL SELECT version FROM node_branch WHERE node_id = n.id AND branch = ?))
		ORDER BY n.key`, ns, br, br)
}

func (t *sqliteTx) Namespaces(ctx context.Context) ([]string, error) {
	return t.ids(ctx, `SELECT DISTINCT namespace FROM node ORDER BY namespace`)
}

func (t *sqliteTx) links(ctx context.Context, where string, ref domain.NodeRef) ([]domain.Link, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT id, type, from_id, from_version, to_id, to_version, props, change_id FROM link WHERE `+where+` ORDER BY id`,
		string(ref.ID), int(ref.Version))
	if err != nil {
		return nil, sqliteErr(err, "links")
	}
	defer rows.Close()
	var out []domain.Link
	for rows.Next() {
		var l domain.Link
		var id, from, to, p string
		var fv, tv int
		var change sql.NullString
		if err := rows.Scan(&id, &l.Type, &from, &fv, &to, &tv, &p, &change); err != nil {
			return nil, err
		}
		l.ID, l.Properties, l.ChangeID = domain.LinkID(id), props([]byte(p)), domain.ChangeID(change.String)
		l.From = domain.NodeRef{ID: domain.NodeID(from), Version: domain.Version(fv)}
		l.To = domain.NodeRef{ID: domain.NodeID(to), Version: domain.Version(tv)}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (t *sqliteTx) OutLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	return t.links(ctx, "from_id = ? AND from_version = ?", ref)
}

func (t *sqliteTx) InLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	return t.links(ctx, "to_id = ? AND to_version = ?", ref)
}

func (t *sqliteTx) Baseline(ctx context.Context, id domain.BaselineID) (domain.Baseline, error) {
	var b domain.Baseline
	var parent, mergedFrom, change sql.NullString
	var created string
	err := t.tx.QueryRowContext(ctx, `SELECT id, name, parent_id, merged_from, change_id, created_at, branch, namespace, gap, kind FROM baseline WHERE id = ?`, string(id)).
		Scan((*string)(&b.ID), &b.Name, &parent, &mergedFrom, &change, &created, &b.Branch, &b.Namespace, &b.Gap, &b.Kind)
	if err != nil {
		return b, sqliteErr(err, "baseline "+string(id))
	}
	b.ParentID, b.MergedFrom, b.ChangeID, b.CreatedAt = domain.BaselineID(parent.String), domain.BaselineID(mergedFrom.String), domain.ChangeID(change.String), tsParse(created)
	b.Nodes = map[domain.NodeID]domain.Version{}
	if b.Gap > 0 {
		return b, nil // only the header is stored
	}
	rows, err := t.tx.QueryContext(ctx, baselineEntriesSQL("?")+` SELECT node_id, version FROM eff WHERE rn = 1 AND NOT removed`, string(id))
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var nid string
		var v int
		if err := rows.Scan(&nid, &v); err != nil {
			return b, err
		}
		b.Nodes[domain.NodeID(nid)] = domain.Version(v)
	}
	return b, rows.Err()
}

func (t *sqliteTx) ids(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (t *sqliteTx) Baselines(ctx context.Context, namespace string) ([]domain.Baseline, error) {
	ids, err := t.ids(ctx, `SELECT id FROM baseline WHERE namespace = ? ORDER BY created_at, rowid`, domain.NamespaceOf(namespace))
	if err != nil {
		return nil, err
	}
	out := make([]domain.Baseline, 0, len(ids))
	for _, id := range ids {
		b, err := t.Baseline(ctx, domain.BaselineID(id))
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (t *sqliteTx) Change(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	var c domain.Change
	var start, result sql.NullString
	var data, created string
	err := t.tx.QueryRowContext(ctx, `SELECT id, title, intent, methodology, goal, status, baseline_id, result_baseline_id, data, created_at, branch, namespace, COALESCE(parent_id, ''), owner_org, project_id, lifecycle, state
		FROM change WHERE id = ?`, string(id)).
		Scan((*string)(&c.ID), &c.Title, &c.Intent, &c.Methodology, &c.Goal, (*string)(&c.Status), &start, &result, &data, &created, &c.Branch, &c.Namespace, (*string)(&c.ParentID), &c.OwnerOrg, &c.ProjectID, &c.Lifecycle, &c.State)
	if err != nil {
		return c, sqliteErr(err, "change "+string(id))
	}
	c.BaselineID, c.ResultBaselineID, c.Data, c.CreatedAt = domain.BaselineID(start.String), domain.BaselineID(result.String), props([]byte(data)), tsParse(created)
	facts, err := t.Log(ctx, factsFilter(id))
	if err != nil {
		return c, err
	}
	if c.Items, err = itemsOf(facts); err != nil {
		return c, err
	}
	c.Nodes, err = t.ChangeImpacts(ctx, id)
	return c, err
}

func (t *sqliteTx) Changes(ctx context.Context) ([]domain.Change, error) {
	ids, err := t.ids(ctx, `SELECT id FROM change ORDER BY created_at, rowid`)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Change, 0, len(ids))
	for _, id := range ids {
		c, err := t.Change(ctx, domain.ChangeID(id))
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (t *sqliteTx) PutNode(ctx context.Context, n domain.Node) error {
	if n.Version == 1 {
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO node (id, namespace, key, type, latest, project_id) VALUES (?, ?, ?, ?, 1, ?)`, string(n.ID), domain.NamespaceOf(n.Namespace), n.Key, n.Type, nullUUID(string(n.Project))); err != nil {
			return sqliteErr(err, "node "+n.Key)
		}
	} else {
		res, err := t.tx.ExecContext(ctx, `UPDATE node SET latest = ?2 WHERE id = ?1 AND latest = ?2 - 1`, string(n.ID), int(n.Version))
		if err != nil {
			return sqliteErr(err, "node "+n.Ref().String())
		}
		if k, _ := res.RowsAffected(); k != 1 {
			return fmt.Errorf("node %s: not the next version: %w", n.Ref(), ErrConflict)
		}
	}
	parents := n.Parents
	if parents == nil {
		parents = []domain.Version{}
	}
	pj, _ := json.Marshal(parents)
	_, err := t.tx.ExecContext(ctx, `INSERT INTO node_version (node_id, version, props, deleted, change_id, created_at, branch, parents, reason, state, change_impact, comment, execution, owner_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(n.ID), int(n.Version), string(jsonb(n.Properties)), n.Deleted, nullUUID(string(n.ChangeID)), tsText(n.CreatedAt),
		domain.BranchOf(n.Branch), string(pj), n.Reason, n.State, nullUUID(string(n.ChangeImpact)), n.Comment, n.Execution, nullUUID(string(n.Owner)))
	return sqliteErr(err, "node "+n.Ref().String())
}

func (t *sqliteTx) SetNodeProps(ctx context.Context, ref domain.NodeRef, props map[string]any) error {
	res, err := t.tx.ExecContext(ctx, `UPDATE node_version SET props = ? WHERE node_id = ? AND version = ?`, string(jsonb(props)), string(ref.ID), int(ref.Version))
	if err != nil {
		return sqliteErr(err, "node "+ref.String())
	}
	if k, _ := res.RowsAffected(); k != 1 {
		return fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	return nil
}

func (t *sqliteTx) PutLink(ctx context.Context, l domain.Link) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO link (id, type, from_id, from_version, to_id, to_version, props, change_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(l.ID), l.Type, string(l.From.ID), int(l.From.Version), string(l.To.ID), int(l.To.Version), string(jsonb(l.Properties)), nullUUID(string(l.ChangeID)))
	return sqliteErr(err, "link")
}

// PutBaseline stores the baseline as a delta from its parent, or whole at a checkpoint (ADR 0032).
func (t *sqliteTx) PutBaseline(ctx context.Context, b domain.Baseline) error {
	var parent *domain.Baseline
	parentDepth := 0
	if b.ParentID != "" && b.Gap == 0 {
		var parentGap int
		if err := t.tx.QueryRowContext(ctx, `SELECT depth, gap FROM baseline WHERE id = ?`, string(b.ParentID)).Scan(&parentDepth, &parentGap); err != nil {
			return sqliteErr(err, "baseline "+string(b.ParentID))
		}
		// a delta is only stored over a parent whose entries are stored: else the baseline is stored whole
		if parentGap == 0 {
			p, err := t.Baseline(ctx, b.ParentID)
			if err != nil {
				return err
			}
			parent = &p
		}
	}
	depth, entries := storedEntries(parent, parentDepth, b.Nodes)
	if b.Gap > 0 {
		depth, entries = 0, nil
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO baseline (id, name, parent_id, merged_from, change_id, created_at, branch, namespace, depth, gap, kind) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(b.ID), b.Name, nullUUID(string(b.ParentID)), nullUUID(string(b.MergedFrom)), nullUUID(string(b.ChangeID)), tsText(b.CreatedAt), domain.BranchOf(b.Branch), domain.NamespaceOf(b.Namespace), depth, b.Gap, kindOf(b))
	if err != nil {
		return sqliteErr(err, "baseline")
	}
	stmt, err := t.tx.PrepareContext(ctx, `INSERT INTO baseline_entry (baseline_id, node_id, version, removed) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, string(b.ID), string(e.node), int(e.version), e.removed); err != nil {
			return sqliteErr(err, "baseline entries")
		}
	}
	return nil
}

func (t *sqliteTx) BranchJoins(ctx context.Context, namespace, branch string, change domain.ChangeID) ([]domain.NodeRef, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT j.node_id, j.version FROM node_branch j JOIN node n ON n.id = j.node_id WHERE n.namespace = ?1 AND j.branch = ?2 AND j.change_id = ?3`,
		domain.NamespaceOf(namespace), domain.BranchOf(branch), string(change))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.NodeRef
	for rows.Next() {
		var id string
		var v int
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out = append(out, domain.NodeRef{ID: domain.NodeID(id), Version: domain.Version(v)})
	}
	return out, rows.Err()
}

func (t *sqliteTx) MaterializeBaseline(ctx context.Context, id domain.BaselineID, nodes map[domain.NodeID]domain.Version) error {
	if _, err := t.tx.ExecContext(ctx, `DELETE FROM baseline_entry WHERE baseline_id = ?`, string(id)); err != nil {
		return err
	}
	if res, err := t.tx.ExecContext(ctx, `UPDATE baseline SET depth = 0, gap = 0 WHERE id = ?`, string(id)); err != nil {
		return sqliteErr(err, "baseline "+string(id))
	} else if k, _ := res.RowsAffected(); k != 1 {
		return fmt.Errorf("baseline %s: %w", id, ErrNotFound)
	}
	stmt, err := t.tx.PrepareContext(ctx, `INSERT INTO baseline_entry (baseline_id, node_id, version, removed) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for nid, v := range nodes {
		if _, err := stmt.ExecContext(ctx, string(id), string(nid), int(v), false); err != nil {
			return sqliteErr(err, "baseline entries")
		}
	}
	return nil
}

func (t *sqliteTx) PutChange(ctx context.Context, c domain.Change) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO change (id, title, intent, methodology, goal, status, baseline_id, result_baseline_id, data, created_at, branch, namespace, parent_id, owner_org, project_id, lifecycle, state)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET title = excluded.title, intent = excluded.intent, goal = excluded.goal, status = excluded.status,
		  result_baseline_id = excluded.result_baseline_id, data = excluded.data, baseline_id = excluded.baseline_id, branch = excluded.branch,
		  lifecycle = excluded.lifecycle, state = excluded.state`,
		string(c.ID), c.Title, c.Intent, c.Methodology, c.Goal, string(c.Status), nullUUID(string(c.BaselineID)), nullUUID(string(c.ResultBaselineID)),
		string(jsonb(c.Data)), tsText(c.CreatedAt), domain.BranchOf(c.Branch), domain.NamespaceOf(c.Namespace), nullUUID(string(c.ParentID)), c.OwnerOrg, c.ProjectID, c.Lifecycle, c.State)
	return sqliteErr(err, "change")
}

func (t *sqliteTx) AppendLog(ctx context.Context, e domain.LogEntry) (domain.LogEntry, error) {
	res, err := t.tx.ExecContext(ctx, `INSERT INTO change_log (id, change_id, type, flow, process_id, execution, subject, by_whom, at, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, string(e.Change), e.Type, e.Flow, e.Process, e.Execution, e.Subject, e.By, tsText(e.At), string(e.Payload))
	if err != nil {
		return e, sqliteErr(err, "log entry "+e.Type)
	}
	e.Seq, err = res.LastInsertId()
	return e, err
}

func (t *sqliteTx) LogCounts(ctx context.Context, f domain.LogFilter) (map[string]int, error) {
	f.AfterSeq = 0
	where, args := logWhere(f, func(int) string { return "?" })
	rows, err := t.tx.QueryContext(ctx, `SELECT type, count(*) FROM change_log`+where+` GROUP BY type`, args...)
	if err != nil {
		return nil, sqliteErr(err, "log counts")
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var typ string
		var n int
		if err := rows.Scan(&typ, &n); err != nil {
			return nil, err
		}
		out[typ] = n
	}
	return out, rows.Err()
}

func (t *sqliteTx) Log(ctx context.Context, f domain.LogFilter) ([]domain.LogEntry, error) {
	where, args := logWhere(f, func(int) string { return "?" })
	q := `SELECT seq, id, change_id, type, flow, process_id, execution, subject, by_whom, at, payload FROM change_log` + where + ` ORDER BY seq`
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, sqliteErr(err, "log")
	}
	defer rows.Close()
	var out []domain.LogEntry
	for rows.Next() {
		var e domain.LogEntry
		var change, at, payload string
		if err := rows.Scan(&e.Seq, &e.ID, &change, &e.Type, &e.Flow, &e.Process, &e.Execution, &e.Subject, &e.By, &at, &payload); err != nil {
			return nil, err
		}
		e.Change, e.At, e.Payload = domain.ChangeID(change), tsParse(at), json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (t *sqliteTx) OpenChangeIDs(ctx context.Context) ([]domain.ChangeID, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT id FROM change WHERE status NOT IN ('applied', 'abandoned') ORDER BY created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ChangeID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, domain.ChangeID(id))
	}
	return out, rows.Err()
}

func (t *sqliteTx) PutChangeImpact(ctx context.Context, change domain.ChangeID, cn domain.ChangeImpact) error {
	r, err := toCNRow(cn)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO change_impact (id, seq, change_id, node_id, key, type, intent, rationale, pre_version, post_version, landed_version, review, reviews, via, recheck, produced_by, derived_from, items, execution, created_at, flow, superseded)
		VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM change_impact), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET node_id = excluded.node_id, key = excluded.key, type = excluded.type, intent = excluded.intent, rationale = excluded.rationale,
			pre_version = excluded.pre_version, post_version = excluded.post_version, landed_version = excluded.landed_version, review = excluded.review,
			reviews = excluded.reviews, via = excluded.via, recheck = excluded.recheck, produced_by = excluded.produced_by, derived_from = excluded.derived_from, items = excluded.items, execution = excluded.execution, flow = excluded.flow, superseded = excluded.superseded`,
		r.ID, string(change), r.NodeID, r.Key, r.Type, r.Intent, r.Rationale, r.Pre, r.Post, r.Landed, r.Review, string(r.Reviews), nullUUID(r.Via), r.Recheck, r.ProducedBy, string(r.DerivedFrom), string(r.Items), r.Execution, tsText(cn.CreatedAt), r.Flow, r.Superseded)
	return sqliteErr(err, "change impact "+cn.Key)
}

func (t *sqliteTx) ChangeImpacts(ctx context.Context, change domain.ChangeID) ([]domain.ChangeImpact, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT id, node_id, key, type, intent, rationale, pre_version, post_version, landed_version, review, reviews, COALESCE(via, ''), recheck, produced_by, derived_from, items, execution, created_at, flow, superseded
		FROM change_impact WHERE change_id = ? ORDER BY seq`, string(change))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ChangeImpact
	for rows.Next() {
		var r cnRow
		var reviews, derived, items, created string
		var node sql.NullString
		var pre, post, landed sql.NullInt64
		if err := rows.Scan(&r.ID, &node, &r.Key, &r.Type, &r.Intent, &r.Rationale, &pre, &post, &landed, &r.Review, &reviews, &r.Via, &r.Recheck, &r.ProducedBy, &derived, &items, &r.Execution, &created, &r.Flow, &r.Superseded); err != nil {
			return nil, err
		}
		if node.Valid {
			r.NodeID = &node.String
		}
		for _, x := range []struct {
			src sql.NullInt64
			dst **int
		}{{pre, &r.Pre}, {post, &r.Post}, {landed, &r.Landed}} {
			if x.src.Valid {
				v := int(x.src.Int64)
				*x.dst = &v
			}
		}
		r.Reviews, r.DerivedFrom, r.Items = []byte(reviews), []byte(derived), []byte(items)
		cn, err := r.node()
		if err != nil {
			return nil, err
		}
		cn.CreatedAt = tsParse(created)
		out = append(out, cn)
	}
	return out, rows.Err()
}

func (t *sqliteTx) NodeChangeImpacts(ctx context.Context, node domain.NodeID) ([]domain.ChangeID, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT change_id FROM change_impact WHERE node_id = ? ORDER BY seq`, string(node))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ChangeID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, domain.ChangeID(id))
	}
	return out, rows.Err()
}

func (t *sqliteTx) SetNodeOrigin(ctx context.Context, ref domain.NodeRef, change domain.ChangeID, cn domain.ChangeImpactID, comment string) error {
	res, err := t.tx.ExecContext(ctx, `UPDATE node_version SET change_id = ?, change_impact = ?, comment = ? WHERE node_id = ? AND version = ?`, nullUUID(string(change)), nullUUID(string(cn)), comment, string(ref.ID), int(ref.Version))
	if err != nil {
		return sqliteErr(err, "node "+ref.String())
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	return nil
}

func (t *sqliteTx) JoinBranch(ctx context.Context, ref domain.NodeRef, branch string, change domain.ChangeID) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO node_branch (node_id, version, branch, change_id) SELECT node_id, version, ?, ? FROM node_version
		WHERE node_id = ? AND version = ? AND branch <> ? ON CONFLICT DO NOTHING`, domain.BranchOf(branch), nullUUID(string(change)), string(ref.ID), int(ref.Version), domain.BranchOf(branch))
	return sqliteErr(err, "node "+ref.String())
}

func (t *sqliteTx) baselineHeaders(ctx context.Context) ([]baselineHeader, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT id, coalesce(parent_id, ''), depth FROM baseline WHERE gap = 0 ORDER BY created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []baselineHeader
	for rows.Next() {
		var h baselineHeader
		if err := rows.Scan((*string)(&h.id), (*string)(&h.parent), &h.depth); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (t *sqliteTx) rewriteBaseline(ctx context.Context, id domain.BaselineID, depth int, entries []baselineEntry) error {
	if _, err := t.tx.ExecContext(ctx, `DELETE FROM baseline_entry WHERE baseline_id = ?`, string(id)); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE baseline SET depth = ? WHERE id = ?`, depth, string(id)); err != nil {
		return err
	}
	stmt, err := t.tx.PrepareContext(ctx, `INSERT INTO baseline_entry (baseline_id, node_id, version, removed) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, string(id), string(e.node), int(e.version), e.removed); err != nil {
			return sqliteErr(err, "baseline entries")
		}
	}
	return nil
}

func (t *sqliteTx) PutTag(ctx context.Context, tag domain.Tag) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO tag (id, name, namespace, change_id, baseline_id, by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, baseline_id = excluded.baseline_id`,
		string(tag.ID), tag.Name, domain.NamespaceOf(tag.Namespace), string(tag.ChangeID), nullUUID(string(tag.BaselineID)), tag.By, tsText(tag.CreatedAt))
	return sqliteErr(err, "tag "+tag.Name)
}

func (t *sqliteTx) DeleteTag(ctx context.Context, id domain.TagID) error {
	res, err := t.tx.ExecContext(ctx, `DELETE FROM tag WHERE id = ?`, string(id))
	if err != nil {
		return sqliteErr(err, "tag "+string(id))
	}
	if k, _ := res.RowsAffected(); k != 1 {
		return fmt.Errorf("tag %s: %w", id, ErrNotFound)
	}
	return nil
}

func (t *sqliteTx) Tags(ctx context.Context, f domain.TagFilter) ([]domain.Tag, error) {
	q, args := `SELECT id, name, namespace, change_id, COALESCE(baseline_id, ''), by, created_at FROM tag WHERE 1 = 1`, []any{}
	if f.Namespace != "" {
		q, args = q+` AND namespace = ?`, append(args, domain.NamespaceOf(f.Namespace))
	}
	if f.Name != "" {
		q, args = q+` AND name = ?`, append(args, f.Name)
	}
	if f.Change != "" {
		q, args = q+` AND change_id = ?`, append(args, string(f.Change))
	}
	rows, err := t.tx.QueryContext(ctx, q+` ORDER BY created_at, rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Tag
	for rows.Next() {
		var tag domain.Tag
		var created string
		if err := rows.Scan((*string)(&tag.ID), &tag.Name, &tag.Namespace, (*string)(&tag.ChangeID), (*string)(&tag.BaselineID), &tag.By, &created); err != nil {
			return nil, err
		}
		tag.CreatedAt = tsParse(created)
		out = append(out, tag)
	}
	return out, rows.Err()
}

func (t *sqliteTx) DeleteChange(ctx context.Context, id domain.ChangeID, namespace, branch string) error {
	for _, q := range []string{
		`SELECT count(*) FROM baseline_entry e JOIN node_version v ON e.node_id = v.node_id AND e.version = v.version WHERE v.change_id = ?1`,
		`SELECT count(*) FROM baseline WHERE change_id = ?1`,
		`SELECT count(*) FROM link l JOIN node_version v ON l.to_id = v.node_id AND l.to_version = v.version WHERE v.change_id = ?1 AND (l.change_id IS NULL OR l.change_id != ?1)`,
		`SELECT count(*) FROM node_version o JOIN node_version v ON o.node_id = v.node_id AND o.version > v.version WHERE v.change_id = ?1 AND (o.change_id IS NULL OR o.change_id != ?1)`,
	} {
		var used int
		if err := t.tx.QueryRowContext(ctx, q, string(id)).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			return fmt.Errorf("change %s: what it wrote is used by the graph: %w", id, ErrConflict)
		}
	}
	rows, err := t.tx.QueryContext(ctx, `SELECT DISTINCT node_id FROM node_version WHERE change_id = ?`, string(id))
	if err != nil {
		return err
	}
	var nodes []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		nodes = append(nodes, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	const mine = `(SELECT node_id, version FROM node_version WHERE change_id = ?1)`
	for _, q := range []string{
		`DELETE FROM link WHERE change_id = ?1 OR (from_id, from_version) IN ` + mine,
		`DELETE FROM node_branch WHERE change_id = ?1 OR (node_id, version) IN ` + mine,
		`DELETE FROM node_version WHERE change_id = ?1`,
		`DELETE FROM change_impact WHERE change_id = ?1`,
		`DELETE FROM change_log WHERE change_id = ?1`,
		`DELETE FROM tag WHERE change_id = ?1`,
	} {
		if _, err := t.tx.ExecContext(ctx, q, string(id)); err != nil {
			return sqliteErr(err, "change "+string(id))
		}
	}
	for _, n := range nodes {
		if _, err := t.tx.ExecContext(ctx, `DELETE FROM node WHERE id = ?1 AND NOT EXISTS (SELECT 1 FROM node_version WHERE node_id = ?1)`, n); err != nil {
			return sqliteErr(err, "node "+n)
		}
		if _, err := t.tx.ExecContext(ctx, `UPDATE node SET latest = (SELECT max(version) FROM node_version WHERE node_id = ?1) WHERE id = ?1`, n); err != nil {
			return sqliteErr(err, "node "+n)
		}
	}
	if branch != "" {
		if _, err := t.tx.ExecContext(ctx, `DELETE FROM branch WHERE namespace = ? AND name = ?`, namespace, branch); err != nil {
			return sqliteErr(err, "branch "+branch)
		}
	}
	res, err := t.tx.ExecContext(ctx, `DELETE FROM change WHERE id = ?`, string(id))
	if err != nil {
		return sqliteErr(err, "change "+string(id))
	}
	if k, _ := res.RowsAffected(); k != 1 {
		return fmt.Errorf("change %s: %w", id, ErrNotFound)
	}
	return nil
}
