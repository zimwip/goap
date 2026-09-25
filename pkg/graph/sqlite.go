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

const sqliteNodeCols = `n.id, v.version, n.key, n.type, v.props, v.deleted, v.change_id, v.created_at, v.branch, v.parents, v.reason, v.state`

type scanner interface{ Scan(dest ...any) error }

func sqliteScanNode(row scanner) (domain.Node, error) {
	var n domain.Node
	var id, p, created, parents string
	var change sql.NullString
	var version int
	if err := row.Scan(&id, &version, &n.Key, &n.Type, &p, &n.Deleted, &change, &created, &n.Branch, &parents, &n.Reason, &n.State); err != nil {
		return n, err
	}
	_ = json.Unmarshal([]byte(parents), &n.Parents)
	if len(n.Parents) == 0 {
		n.Parents = nil
	}
	n.ID, n.Version, n.Properties, n.ChangeID = domain.NodeID(id), domain.Version(version), props([]byte(p)), domain.ChangeID(change.String)
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
	q := `SELECT ` + sqliteNodeCols + ` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = ? AND v.branch = ? ORDER BY v.version DESC LIMIT 1`
	n, err := sqliteScanNode(t.tx.QueryRowContext(ctx, q, string(id), domain.BranchOf(branch)))
	return n, sqliteErr(err, "node "+string(id)+" on "+domain.BranchOf(branch))
}

func (t *sqliteTx) Versions(ctx context.Context, id domain.NodeID) ([]domain.Node, error) {
	out, err := t.nodes(ctx, `SELECT `+sqliteNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = ? ORDER BY v.version`, string(id))
	if err == nil && len(out) == 0 {
		err = fmt.Errorf("node %s: %w", id, ErrNotFound)
	}
	return out, err
}

const sqliteBranchCols = `name, parent, coalesce(fork_baseline, ''), coalesce(head_baseline, ''), origin, status, created_at`

func sqliteScanBranch(row scanner) (domain.Branch, error) {
	var b domain.Branch
	var created string
	err := row.Scan(&b.Name, &b.Parent, (*string)(&b.ForkBaseline), (*string)(&b.Head), &b.Origin, &b.Status, &created)
	b.CreatedAt = tsParse(created)
	return b, err
}

func (t *sqliteTx) Branch(ctx context.Context, name string) (domain.Branch, error) {
	b, err := sqliteScanBranch(t.tx.QueryRowContext(ctx, `SELECT `+sqliteBranchCols+` FROM branch WHERE name = ?`, name))
	return b, sqliteErr(err, "branch "+name)
}

func (t *sqliteTx) Branches(ctx context.Context) ([]domain.Branch, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT `+sqliteBranchCols+` FROM branch ORDER BY created_at, rowid`)
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
	_, err := t.tx.ExecContext(ctx, `INSERT INTO branch (name, parent, fork_baseline, head_baseline, origin, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET status = excluded.status, head_baseline = excluded.head_baseline`,
		b.Name, b.Parent, nullUUID(string(b.ForkBaseline)), nullUUID(string(b.Head)), b.Origin, b.Status, tsText(b.CreatedAt))
	return sqliteErr(err, "branch "+b.Name)
}

func (t *sqliteTx) NodeByKey(ctx context.Context, key string) (domain.Node, error) {
	var id string
	if err := t.tx.QueryRowContext(ctx, `SELECT id FROM node WHERE key = ?`, key).Scan(&id); err != nil {
		return domain.Node{}, sqliteErr(err, "node key "+key)
	}
	return t.LatestOn(ctx, domain.NodeID(id), domain.MainBranch)
}

func (t *sqliteTx) NodesIn(ctx context.Context, baseline domain.BaselineID, nodeType string) ([]domain.Node, error) {
	if _, err := t.Baseline(ctx, baseline); err != nil {
		return nil, err
	}
	return t.nodes(ctx, `SELECT `+sqliteNodeCols+` FROM baseline_entry e JOIN node n ON n.id = e.node_id
	      JOIN node_version v ON v.node_id = e.node_id AND v.version = e.version
	      WHERE e.baseline_id = ? AND (? = '' OR n.type = ?) ORDER BY n.key`, string(baseline), nodeType, nodeType)
}

func (t *sqliteTx) LatestNodes(ctx context.Context) ([]domain.Node, error) {
	return t.nodes(ctx, `SELECT `+sqliteNodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id
		WHERE v.branch = 'main' AND v.version = (SELECT max(version) FROM node_version WHERE node_id = n.id AND branch = 'main')
		ORDER BY n.key`)
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
	var parent, change sql.NullString
	var created string
	err := t.tx.QueryRowContext(ctx, `SELECT id, name, parent_id, change_id, created_at, branch FROM baseline WHERE id = ?`, string(id)).
		Scan((*string)(&b.ID), &b.Name, &parent, &change, &created, &b.Branch)
	if err != nil {
		return b, sqliteErr(err, "baseline "+string(id))
	}
	b.ParentID, b.ChangeID, b.CreatedAt = domain.BaselineID(parent.String), domain.ChangeID(change.String), tsParse(created)
	b.Nodes = map[domain.NodeID]domain.Version{}
	rows, err := t.tx.QueryContext(ctx, `SELECT node_id, version FROM baseline_entry WHERE baseline_id = ?`, string(id))
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

func (t *sqliteTx) ids(ctx context.Context, q string) ([]string, error) {
	rows, err := t.tx.QueryContext(ctx, q)
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

func (t *sqliteTx) Baselines(ctx context.Context) ([]domain.Baseline, error) {
	ids, err := t.ids(ctx, `SELECT id FROM baseline ORDER BY created_at, rowid`)
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

func (t *sqliteTx) Change(ctx context.Context, id domain.ChangeID) (domain.ChangeSet, error) {
	var c domain.ChangeSet
	var result sql.NullString
	var data, created string
	err := t.tx.QueryRowContext(ctx, `SELECT id, title, intent, methodology, goal, status, baseline_id, result_baseline_id, data, created_at, branch
		FROM change_set WHERE id = ?`, string(id)).
		Scan((*string)(&c.ID), &c.Title, &c.Intent, &c.Methodology, &c.Goal, (*string)(&c.Status), (*string)(&c.BaselineID), &result, &data, &created, &c.Branch)
	if err != nil {
		return c, sqliteErr(err, "change "+string(id))
	}
	c.ResultBaselineID, c.Data, c.CreatedAt = domain.BaselineID(result.String), props([]byte(data)), tsParse(created)
	rows, err := t.tx.QueryContext(ctx, `SELECT payload FROM change_item WHERE change_id = ? ORDER BY seq`, string(id))
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return c, err
		}
		var it domain.ChangeItem
		if err := json.Unmarshal([]byte(payload), &it); err != nil {
			return c, err
		}
		c.Items = append(c.Items, it)
	}
	return c, rows.Err()
}

func (t *sqliteTx) Changes(ctx context.Context) ([]domain.ChangeSet, error) {
	ids, err := t.ids(ctx, `SELECT id FROM change_set ORDER BY created_at, rowid`)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ChangeSet, 0, len(ids))
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
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO node (id, key, type, latest) VALUES (?, ?, ?, 1)`, string(n.ID), n.Key, n.Type); err != nil {
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
	_, err := t.tx.ExecContext(ctx, `INSERT INTO node_version (node_id, version, props, deleted, change_id, created_at, branch, parents, reason, state)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(n.ID), int(n.Version), string(jsonb(n.Properties)), n.Deleted, nullUUID(string(n.ChangeID)), tsText(n.CreatedAt),
		domain.BranchOf(n.Branch), string(pj), n.Reason, n.State)
	return sqliteErr(err, "node "+n.Ref().String())
}

func (t *sqliteTx) PutLink(ctx context.Context, l domain.Link) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO link (id, type, from_id, from_version, to_id, to_version, props, change_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(l.ID), l.Type, string(l.From.ID), int(l.From.Version), string(l.To.ID), int(l.To.Version), string(jsonb(l.Properties)), nullUUID(string(l.ChangeID)))
	return sqliteErr(err, "link")
}

func (t *sqliteTx) PutBaseline(ctx context.Context, b domain.Baseline) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO baseline (id, name, parent_id, change_id, created_at, branch) VALUES (?, ?, ?, ?, ?, ?)`,
		string(b.ID), b.Name, nullUUID(string(b.ParentID)), nullUUID(string(b.ChangeID)), tsText(b.CreatedAt), domain.BranchOf(b.Branch))
	if err != nil {
		return sqliteErr(err, "baseline")
	}
	stmt, err := t.tx.PrepareContext(ctx, `INSERT INTO baseline_entry (baseline_id, node_id, version) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for id, v := range b.Nodes {
		if _, err := stmt.ExecContext(ctx, string(b.ID), string(id), int(v)); err != nil {
			return sqliteErr(err, "baseline entries")
		}
	}
	return nil
}

func (t *sqliteTx) PutChange(ctx context.Context, c domain.ChangeSet) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO change_set (id, title, intent, methodology, goal, status, baseline_id, result_baseline_id, data, created_at, branch)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET title = excluded.title, intent = excluded.intent, goal = excluded.goal, status = excluded.status,
		  result_baseline_id = excluded.result_baseline_id, data = excluded.data, baseline_id = excluded.baseline_id, branch = excluded.branch`,
		string(c.ID), c.Title, c.Intent, c.Methodology, c.Goal, string(c.Status), string(c.BaselineID), nullUUID(string(c.ResultBaselineID)),
		string(jsonb(c.Data)), tsText(c.CreatedAt), domain.BranchOf(c.Branch))
	return sqliteErr(err, "change")
}

func (t *sqliteTx) PutItem(ctx context.Context, change domain.ChangeID, it domain.ChangeItem) error {
	payload, err := json.Marshal(it)
	if err != nil {
		return err
	}
	var tid sql.NullString
	var tv sql.NullInt64
	if it.Target != nil {
		tid = sql.NullString{String: string(it.Target.ID), Valid: true}
		tv = sql.NullInt64{Int64: int64(it.Target.Version), Valid: true}
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO change_item (id, change_id, kind, payload, target_id, target_version, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(it.ID), string(change), string(it.Kind), string(payload), tid, tv, tsText(it.CreatedAt))
	return sqliteErr(err, "change item")
}

func (t *sqliteTx) PutAttachment(ctx context.Context, change domain.ChangeID, ref domain.NodeRef) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO change_node (change_id, node_id, base_version) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
		string(change), string(ref.ID), int(ref.Version))
	return sqliteErr(err, "change attachment")
}

func (t *sqliteTx) Attachments(ctx context.Context, change domain.ChangeID) ([]domain.NodeRef, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT node_id, base_version FROM change_node WHERE change_id = ? ORDER BY seq`, string(change))
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

func (t *sqliteTx) NodeAttachments(ctx context.Context, node domain.NodeID) ([]domain.ChangeID, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT change_id FROM change_node WHERE node_id = ? ORDER BY seq`, string(node))
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

func (t *sqliteTx) PutExecution(ctx context.Context, r domain.ExecutionRecord) error {
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx, `INSERT INTO execution (id, change_id, process_id, seq, kind, action, started_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, string(r.ChangeID), r.ProcessID, r.Seq, r.Kind, r.Action, tsText(r.StartedAt), string(payload))
	return sqliteErr(err, "execution")
}

func (t *sqliteTx) Executions(ctx context.Context, f domain.ExecutionFilter) ([]domain.ExecutionRecord, error) {
	q := `SELECT payload FROM execution WHERE 1 = 1`
	var args []any
	if f.ChangeID != "" {
		q += ` AND change_id = ?`
		args = append(args, string(f.ChangeID))
	}
	if len(f.ProcessIDs) > 0 {
		q += ` AND process_id IN (?` + strings.Repeat(", ?", len(f.ProcessIDs)-1) + `)`
		for _, p := range f.ProcessIDs {
			args = append(args, p)
		}
	}
	rows, err := t.tx.QueryContext(ctx, q+` ORDER BY rowid`, args...)
	if err != nil {
		return nil, sqliteErr(err, "executions")
	}
	defer rows.Close()
	var out []domain.ExecutionRecord
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var r domain.ExecutionRecord
		if err := json.Unmarshal([]byte(payload), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
