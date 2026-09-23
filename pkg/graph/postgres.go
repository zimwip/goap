package graph

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/pkg/domain"
)

// Migrations holds the PostgreSQL schema of the graph service.
//
//go:embed migrations/*.sql
var Migrations embed.FS

// Postgres is a PostgreSQL Repo.
type Postgres struct{ pool *pgxpool.Pool }

// NewPostgres returns a repository on pool (schema already migrated).
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// InTx implements Repo.
func (p *Postgres) InTx(ctx context.Context, fn func(tx Tx) error) error {
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error { return fn(&pgTx{tx: tx}) })
}

type pgTx struct{ tx pgx.Tx }

func mapErr(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%s: %s: %w", what, pgErr.Detail, ErrConflict)
		case "23503", "22P02":
			return fmt.Errorf("%s: %s: %w", what, pgErr.Message, ErrInvalid)
		}
	}
	return err
}

func jsonb(m map[string]any) []byte {
	if m == nil {
		return []byte("{}")
	}
	b, _ := json.Marshal(m)
	return b
}

func props(b []byte) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if len(m) == 0 {
		return nil
	}
	return m
}

func nullUUID(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

const nodeCols = `n.id::text, v.version, n.key, n.type, v.props, v.deleted, v.change_id::text, v.created_at, v.branch, v.parents, v.reason`

func scanNode(row pgx.Row) (domain.Node, error) {
	var n domain.Node
	var id string
	var change *string
	var p []byte
	var version int
	var parents []int32
	if err := row.Scan(&id, &version, &n.Key, &n.Type, &p, &n.Deleted, &change, &n.CreatedAt, &n.Branch, &parents, &n.Reason); err != nil {
		return n, err
	}
	for _, pv := range parents {
		n.Parents = append(n.Parents, domain.Version(pv))
	}
	n.ID, n.Version, n.Properties, n.ChangeID = domain.NodeID(id), domain.Version(version), props(p), domain.ChangeID(str(change))
	return n, nil
}

func collectNodes(rows pgx.Rows) ([]domain.Node, error) {
	defer rows.Close()
	var out []domain.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (t *pgTx) Node(ctx context.Context, ref domain.NodeRef) (domain.Node, error) {
	if ref.Version == 0 {
		return t.LatestOn(ctx, ref.ID, domain.MainBranch)
	}
	q := `SELECT ` + nodeCols + ` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = $1 AND v.version = $2`
	n, err := scanNode(t.tx.QueryRow(ctx, q, string(ref.ID), int(ref.Version)))
	return n, mapErr(err, "node "+ref.String())
}

func (t *pgTx) LatestOn(ctx context.Context, id domain.NodeID, branch string) (domain.Node, error) {
	q := `SELECT ` + nodeCols + ` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = $1 AND v.branch = $2 ORDER BY v.version DESC LIMIT 1`
	n, err := scanNode(t.tx.QueryRow(ctx, q, string(id), domain.BranchOf(branch)))
	return n, mapErr(err, "node "+string(id)+" on "+domain.BranchOf(branch))
}

func (t *pgTx) Versions(ctx context.Context, id domain.NodeID) ([]domain.Node, error) {
	rows, err := t.tx.Query(ctx, `SELECT `+nodeCols+` FROM node n JOIN node_version v ON v.node_id = n.id WHERE n.id = $1 ORDER BY v.version`, string(id))
	if err != nil {
		return nil, err
	}
	out, err := collectNodes(rows)
	if err == nil && len(out) == 0 {
		err = fmt.Errorf("node %s: %w", id, ErrNotFound)
	}
	return out, err
}

func (t *pgTx) Branch(ctx context.Context, name string) (domain.Branch, error) {
	var b domain.Branch
	err := t.tx.QueryRow(ctx, `SELECT name, parent, coalesce(fork_baseline::text, ''), coalesce(head_baseline::text, ''), origin, status, created_at FROM branch WHERE name = $1`, name).
		Scan(&b.Name, &b.Parent, (*string)(&b.ForkBaseline), (*string)(&b.Head), &b.Origin, &b.Status, &b.CreatedAt)
	return b, mapErr(err, "branch "+name)
}

func (t *pgTx) Branches(ctx context.Context) ([]domain.Branch, error) {
	rows, err := t.tx.Query(ctx, `SELECT name, parent, coalesce(fork_baseline::text, ''), coalesce(head_baseline::text, ''), origin, status, created_at FROM branch ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Branch, error) {
		var b domain.Branch
		err := r.Scan(&b.Name, &b.Parent, (*string)(&b.ForkBaseline), (*string)(&b.Head), &b.Origin, &b.Status, &b.CreatedAt)
		return b, err
	})
}

func (t *pgTx) PutBranch(ctx context.Context, b domain.Branch) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO branch (name, parent, fork_baseline, head_baseline, origin, status, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (name) DO UPDATE SET status = EXCLUDED.status, head_baseline = EXCLUDED.head_baseline`,
		b.Name, b.Parent, nullUUID(string(b.ForkBaseline)), nullUUID(string(b.Head)), b.Origin, b.Status, b.CreatedAt)
	return mapErr(err, "branch "+b.Name)
}

func (t *pgTx) NodeByKey(ctx context.Context, key string) (domain.Node, error) {
	var id string
	if err := t.tx.QueryRow(ctx, `SELECT id::text FROM node WHERE key = $1`, key).Scan(&id); err != nil {
		return domain.Node{}, mapErr(err, "node key "+key)
	}
	return t.LatestOn(ctx, domain.NodeID(id), domain.MainBranch)
}

func (t *pgTx) NodesIn(ctx context.Context, baseline domain.BaselineID, nodeType string) ([]domain.Node, error) {
	if _, err := t.Baseline(ctx, baseline); err != nil {
		return nil, err
	}
	q := `SELECT ` + nodeCols + ` FROM baseline_entry e JOIN node n ON n.id = e.node_id
	      JOIN node_version v ON v.node_id = e.node_id AND v.version = e.version
	      WHERE e.baseline_id = $1 AND ($2 = '' OR n.type = $2) ORDER BY n.key`
	rows, err := t.tx.Query(ctx, q, string(baseline), nodeType)
	if err != nil {
		return nil, mapErr(err, "baseline nodes")
	}
	return collectNodes(rows)
}

func (t *pgTx) LatestNodes(ctx context.Context) ([]domain.Node, error) {
	q := `SELECT DISTINCT ON (n.key) ` + nodeCols + ` FROM node n JOIN node_version v ON v.node_id = n.id AND v.branch = 'main' ORDER BY n.key, v.version DESC`
	rows, err := t.tx.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	return collectNodes(rows)
}

func (t *pgTx) links(ctx context.Context, where string, ref domain.NodeRef) ([]domain.Link, error) {
	q := `SELECT id::text, type, from_id::text, from_version, to_id::text, to_version, props, change_id::text FROM link WHERE ` + where + ` ORDER BY id`
	rows, err := t.tx.Query(ctx, q, string(ref.ID), int(ref.Version))
	if err != nil {
		return nil, mapErr(err, "links")
	}
	defer rows.Close()
	var out []domain.Link
	for rows.Next() {
		var l domain.Link
		var id, from, to string
		var fv, tv int
		var p []byte
		var change *string
		if err := rows.Scan(&id, &l.Type, &from, &fv, &to, &tv, &p, &change); err != nil {
			return nil, err
		}
		l.ID, l.Properties, l.ChangeID = domain.LinkID(id), props(p), domain.ChangeID(str(change))
		l.From = domain.NodeRef{ID: domain.NodeID(from), Version: domain.Version(fv)}
		l.To = domain.NodeRef{ID: domain.NodeID(to), Version: domain.Version(tv)}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (t *pgTx) OutLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	return t.links(ctx, "from_id = $1 AND from_version = $2", ref)
}

func (t *pgTx) InLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	return t.links(ctx, "to_id = $1 AND to_version = $2", ref)
}

func (t *pgTx) Baseline(ctx context.Context, id domain.BaselineID) (domain.Baseline, error) {
	var b domain.Baseline
	var parent, change *string
	err := t.tx.QueryRow(ctx, `SELECT id::text, name, parent_id::text, change_id::text, created_at, branch FROM baseline WHERE id = $1`, string(id)).
		Scan((*string)(&b.ID), &b.Name, &parent, &change, &b.CreatedAt, &b.Branch)
	if err != nil {
		return b, mapErr(err, "baseline "+string(id))
	}
	b.ParentID, b.ChangeID = domain.BaselineID(str(parent)), domain.ChangeID(str(change))
	b.Nodes = map[domain.NodeID]domain.Version{}
	rows, err := t.tx.Query(ctx, `SELECT node_id::text, version FROM baseline_entry WHERE baseline_id = $1`, string(id))
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

func (t *pgTx) Baselines(ctx context.Context) ([]domain.Baseline, error) {
	rows, err := t.tx.Query(ctx, `SELECT id::text FROM baseline ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
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

func (t *pgTx) Change(ctx context.Context, id domain.ChangeID) (domain.ChangeSet, error) {
	var c domain.ChangeSet
	var result *string
	var data []byte
	err := t.tx.QueryRow(ctx, `SELECT id::text, title, intent, methodology, goal, status, baseline_id::text, result_baseline_id::text, data, created_at, branch
		FROM change_set WHERE id = $1`, string(id)).
		Scan((*string)(&c.ID), &c.Title, &c.Intent, &c.Methodology, &c.Goal, (*string)(&c.Status), (*string)(&c.BaselineID), &result, &data, &c.CreatedAt, &c.Branch)
	if err != nil {
		return c, mapErr(err, "change "+string(id))
	}
	c.ResultBaselineID, c.Data = domain.BaselineID(str(result)), props(data)
	rows, err := t.tx.Query(ctx, `SELECT payload FROM change_item WHERE change_id = $1 ORDER BY seq`, string(id))
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return c, err
		}
		var it domain.ChangeItem
		if err := json.Unmarshal(payload, &it); err != nil {
			return c, err
		}
		c.Items = append(c.Items, it)
	}
	return c, rows.Err()
}

func (t *pgTx) Changes(ctx context.Context) ([]domain.ChangeSet, error) {
	rows, err := t.tx.Query(ctx, `SELECT id::text FROM change_set ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
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

func (t *pgTx) PutNode(ctx context.Context, n domain.Node) error {
	if n.Version == 1 {
		if _, err := t.tx.Exec(ctx, `INSERT INTO node (id, key, type, latest) VALUES ($1, $2, $3, 1)`, string(n.ID), n.Key, n.Type); err != nil {
			return mapErr(err, "node "+n.Key)
		}
	} else {
		tag, err := t.tx.Exec(ctx, `UPDATE node SET latest = $2 WHERE id = $1 AND latest = $2 - 1`, string(n.ID), int(n.Version))
		if err != nil {
			return mapErr(err, "node "+n.Ref().String())
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("node %s: not the next version: %w", n.Ref(), ErrConflict)
		}
	}
	parents := make([]int32, len(n.Parents))
	for i, pv := range n.Parents {
		parents[i] = int32(pv)
	}
	_, err := t.tx.Exec(ctx, `INSERT INTO node_version (node_id, version, props, deleted, change_id, created_at, branch, parents, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		string(n.ID), int(n.Version), jsonb(n.Properties), n.Deleted, nullUUID(string(n.ChangeID)), n.CreatedAt, domain.BranchOf(n.Branch), parents, n.Reason)
	return mapErr(err, "node "+n.Ref().String())
}

func (t *pgTx) PutLink(ctx context.Context, l domain.Link) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO link (id, type, from_id, from_version, to_id, to_version, props, change_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		string(l.ID), l.Type, string(l.From.ID), int(l.From.Version), string(l.To.ID), int(l.To.Version), jsonb(l.Properties), nullUUID(string(l.ChangeID)))
	return mapErr(err, "link")
}

func (t *pgTx) PutBaseline(ctx context.Context, b domain.Baseline) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO baseline (id, name, parent_id, change_id, created_at, branch) VALUES ($1, $2, $3, $4, $5, $6)`,
		string(b.ID), b.Name, nullUUID(string(b.ParentID)), nullUUID(string(b.ChangeID)), b.CreatedAt, domain.BranchOf(b.Branch))
	if err != nil {
		return mapErr(err, "baseline")
	}
	rows := make([][]any, 0, len(b.Nodes))
	for id, v := range b.Nodes {
		rows = append(rows, []any{string(b.ID), string(id), int(v)})
	}
	_, err = t.tx.CopyFrom(ctx, pgx.Identifier{"baseline_entry"}, []string{"baseline_id", "node_id", "version"}, pgx.CopyFromRows(rows))
	return mapErr(err, "baseline entries")
}

func (t *pgTx) PutChange(ctx context.Context, c domain.ChangeSet) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO change_set (id, title, intent, methodology, goal, status, baseline_id, result_baseline_id, data, created_at, branch)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO UPDATE SET title = EXCLUDED.title, intent = EXCLUDED.intent, goal = EXCLUDED.goal, status = EXCLUDED.status,
		  result_baseline_id = EXCLUDED.result_baseline_id, data = EXCLUDED.data, baseline_id = EXCLUDED.baseline_id, branch = EXCLUDED.branch`,
		string(c.ID), c.Title, c.Intent, c.Methodology, c.Goal, string(c.Status), string(c.BaselineID), nullUUID(string(c.ResultBaselineID)), jsonb(c.Data), c.CreatedAt,
		domain.BranchOf(c.Branch))
	return mapErr(err, "change")
}

func (t *pgTx) PutItem(ctx context.Context, change domain.ChangeID, it domain.ChangeItem) error {
	payload, err := json.Marshal(it)
	if err != nil {
		return err
	}
	var tid *string
	var tv *int
	if it.Target != nil {
		s, v := string(it.Target.ID), int(it.Target.Version)
		tid, tv = &s, &v
	}
	_, err = t.tx.Exec(ctx, `INSERT INTO change_item (id, change_id, kind, payload, target_id, target_version, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		string(it.ID), string(change), string(it.Kind), payload, tid, tv, it.CreatedAt)
	return mapErr(err, "change item")
}
