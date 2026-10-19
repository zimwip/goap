package graph

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/outbox"
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

type pgTx struct {
	tx pgx.Tx
	// seq is the order of the last baseline the transaction allocated (ADR 0140)
	seq int64
}

// AppendOutbox implements Tx.
func (t *pgTx) AppendOutbox(ctx context.Context, msgs []outbox.Message) error {
	for _, m := range msgs {
		h, err := json.Marshal(m.Headers)
		if err != nil {
			return err
		}
		if m.Headers == nil {
			h = []byte("{}")
		}
		if _, err := t.tx.Exec(ctx, `INSERT INTO event_outbox (subject, payload, headers, created_at) VALUES ($1, $2, $3, $4)`,
			m.Subject, m.Data, h, m.CreatedAt.UTC()); err != nil {
			return mapErr(err, "outbox")
		}
	}
	return nil
}

// Pending implements outbox.Store.
func (p *Postgres) Pending(ctx context.Context, limit int) ([]outbox.Message, error) {
	rows, err := p.pool.Query(ctx, `SELECT seq, subject, payload, headers, created_at FROM event_outbox
		WHERE published_at IS NULL ORDER BY seq LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []outbox.Message
	for rows.Next() {
		var m outbox.Message
		var headers []byte
		if err := rows.Scan(&m.Seq, &m.Subject, &m.Data, &headers, &m.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(headers, &m.Headers)
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkPublished implements outbox.Store.
func (p *Postgres) MarkPublished(ctx context.Context, seqs []int64, at time.Time) error {
	_, err := p.pool.Exec(ctx, `UPDATE event_outbox SET published_at = $1 WHERE seq = ANY($2)`, at.UTC(), seqs)
	return err
}

// PurgePublished implements outbox.Store.
func (p *Postgres) PurgePublished(ctx context.Context, before time.Time) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM event_outbox WHERE published_at IS NOT NULL AND published_at < $1`, before.UTC())
	return err
}

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
		case "40P01", "40001": // a deadlock or a serialization failure: the caller tries again
			return fmt.Errorf("%s: %s: %w", what, pgErr.Message, ErrConflict)
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

// originsJSON encodes the origins of a node version (both dialects store them as a JSON array).
func originsJSON(o []domain.NodeRef) []byte {
	if len(o) == 0 {
		return []byte("[]")
	}
	b, _ := json.Marshal(o)
	return b
}

func originsOf(b []byte) []domain.NodeRef {
	var o []domain.NodeRef
	_ = json.Unmarshal(b, &o)
	if len(o) == 0 {
		return nil
	}
	return o
}

// guardiansJSON encodes the guardian chain of a change (ADR 0106): a JSON array, "[]" when empty.
func guardiansJSON(gs []domain.GuardianRef) []byte {
	if len(gs) == 0 {
		return []byte("[]")
	}
	b, _ := json.Marshal(gs)
	return b
}

// dependsOnJSON encodes the dependencies of a change (ADR 0108): a JSON array, "[]" when empty.
func dependsOnJSON(ds []domain.ChangeID) []byte {
	if len(ds) == 0 {
		return []byte("[]")
	}
	b, _ := json.Marshal(ds)
	return b
}

func dependsOnOf(b []byte) ([]domain.ChangeID, error) {
	var ds []domain.ChangeID
	if len(b) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(b, &ds); err != nil {
		return nil, fmt.Errorf("dependencies of a change: %w", err)
	}
	if len(ds) == 0 {
		return nil, nil
	}
	return ds, nil
}

func guardiansOf(b []byte) ([]domain.GuardianRef, error) {
	var gs []domain.GuardianRef
	if len(b) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(b, &gs); err != nil {
		return nil, fmt.Errorf("guardians of a change: %w", err)
	}
	if len(gs) == 0 {
		return nil, nil
	}
	return gs, nil
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

func scanNode(row pgx.Row) (domain.Node, error) {
	var n domain.Node
	var id string
	var change, cnode *string
	var owner, project string
	var p []byte
	var version int
	var parents []int32
	var origins []byte
	if err := row.Scan(&id, &version, &n.Namespace, &n.Key, &n.Type, &p, &n.Deleted, &change, &n.CreatedAt, &n.Branch, &parents, &n.Reason, &n.State, &cnode, &n.Comment, &n.Execution, &owner, &project, &origins); err != nil {
		return n, err
	}
	n.Origins = originsOf(origins)
	for _, pv := range parents {
		n.Parents = append(n.Parents, domain.Version(pv))
	}
	n.ID, n.Version, n.Properties, n.ChangeID = domain.NodeID(id), domain.Version(version), props(p), domain.ChangeID(str(change))
	n.ChangeImpact = domain.ChangeImpactID(str(cnode))
	n.Owner, n.Project = domain.NodeID(owner), domain.NodeID(project)
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
	q, args := dialectPG.sqlNode(ref)
	n, err := scanNode(t.tx.QueryRow(ctx, q, args...))
	return n, mapErr(err, "node "+ref.String())
}

func (t *pgTx) LatestOn(ctx context.Context, id domain.NodeID, branch string) (domain.Node, error) {
	q, args := dialectPG.sqlLatestOn(id, branch)
	n, err := scanNode(t.tx.QueryRow(ctx, q, args...))
	return n, mapErr(err, "node "+string(id)+" on "+domain.BranchOf(branch))
}

func (t *pgTx) DerivedNodes(ctx context.Context, ref domain.NodeRef) ([]domain.Node, error) {
	q, args := dialectPG.sqlDerivedNodes(ref)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return collectNodes(rows)
}

func (t *pgTx) Versions(ctx context.Context, id domain.NodeID) ([]domain.Node, error) {
	q, args := dialectPG.sqlVersions(id)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out, err := collectNodes(rows)
	if err == nil && len(out) == 0 {
		err = fmt.Errorf("node %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return out, err
	}
	return out, t.joinBranches(ctx, id, out)
}

func (t *pgTx) VersionRange(ctx context.Context, id domain.NodeID, before domain.Version, limit int) ([]domain.Node, error) {
	q, args := dialectPG.sqlVersionRange(id, before, limit)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out, err := collectNodes(rows)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	slices.Reverse(out)
	return out, t.joinBranches(ctx, id, out)
}

// joinBranches fills the branches the versions vs of a node (by version, consecutive) joined.
func (t *pgTx) joinBranches(ctx context.Context, id domain.NodeID, vs []domain.Node) error {
	q, args := dialectPG.sqlVersionBranches(id, vs[0].Version)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		var b string
		if err := rows.Scan(&v, &b); err != nil {
			return err
		}
		if i := v - int(vs[0].Version); i >= 0 && i < len(vs) {
			vs[i].Joined = append(vs[i].Joined, b)
		}
	}
	return rows.Err()
}

// LockChange implements Tx: a transaction-level advisory lock on the root of the change's tree. The root is read again
// once the lock is held: an attach or a detach that committed meanwhile moved the change to another tree, whose lock
// is taken in turn.
func (t *pgTx) LockChange(ctx context.Context, id domain.ChangeID) error {
	root, err := t.changeRoot(ctx, id)
	for err == nil {
		if err = t.advisoryLock(ctx, "goap.change:"+root); err != nil {
			break
		}
		var again string
		if again, err = t.changeRoot(ctx, id); err == nil && again == root {
			return nil
		}
		root = again
	}
	return mapErr(err, "lock change "+string(id))
}

// LockBranch implements Tx: a transaction-level advisory lock on the branch.
func (t *pgTx) LockBranch(ctx context.Context, namespace, name string) error {
	return mapErr(t.advisoryLock(ctx, "goap.branch:"+domain.NamespaceOf(namespace)+"/"+name), "lock branch "+name)
}

func (t *pgTx) advisoryLock(ctx context.Context, key string) error {
	_, err := t.tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key)
	return err
}

// changeRoot is the root change of the tree id belongs to, id itself when it is not stored yet.
func (t *pgTx) changeRoot(ctx context.Context, id domain.ChangeID) (string, error) {
	var root string
	err := t.tx.QueryRow(ctx, `WITH RECURSIVE up (id, parent_id) AS (
			SELECT id, parent_id FROM change WHERE id = $1
			UNION ALL SELECT c.id, c.parent_id FROM change c JOIN up ON c.id = up.parent_id)
		SELECT id::text FROM up WHERE parent_id IS NULL`, string(id)).Scan(&root)
	if errors.Is(err, pgx.ErrNoRows) {
		return string(id), nil
	}
	return root, err
}

func (t *pgTx) Branch(ctx context.Context, namespace, name string) (domain.Branch, error) {
	var b domain.Branch
	q, args := dialectPG.sqlBranch(namespace, name)
	err := t.tx.QueryRow(ctx, q, args...).
		Scan(&b.Namespace, &b.Name, &b.Parent, (*string)(&b.ForkBaseline), (*string)(&b.Head), &b.Origin, &b.Status, &b.CreatedAt, &b.Description, (*string)(&b.Intent), (*string)(&b.RebasedFrom), &b.Ephemeral)
	return b, mapErr(err, "branch "+name)
}

func (t *pgTx) Branches(ctx context.Context, namespace string) ([]domain.Branch, error) {
	q, args := dialectPG.sqlBranches(namespace)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Branch, error) {
		var b domain.Branch
		err := r.Scan(&b.Namespace, &b.Name, &b.Parent, (*string)(&b.ForkBaseline), (*string)(&b.Head), &b.Origin, &b.Status, &b.CreatedAt, &b.Description, (*string)(&b.Intent), (*string)(&b.RebasedFrom), &b.Ephemeral)
		return b, err
	})
}

func (t *pgTx) PutBranch(ctx context.Context, b domain.Branch) error {
	namespace := domain.NamespaceOf(b.Namespace)
	_, err := t.tx.Exec(ctx, `INSERT INTO branch (namespace, name, parent, fork_baseline, head_baseline, origin, status, created_at, description, intent, rebased_from, ephemeral) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (namespace, name) DO UPDATE SET status = EXCLUDED.status, head_baseline = EXCLUDED.head_baseline, description = EXCLUDED.description,
			fork_baseline = EXCLUDED.fork_baseline, rebased_from = EXCLUDED.rebased_from, parent = EXCLUDED.parent`,
		namespace, b.Name, b.Parent, nullUUID(string(b.ForkBaseline)), nullUUID(string(b.Head)), b.Origin, b.Status, b.CreatedAt, b.Description, string(b.Intent), nullUUID(string(b.RebasedFrom)), b.Ephemeral)
	return mapErr(err, "branch "+b.Name)
}

func (t *pgTx) NodeIDByKey(ctx context.Context, namespace, key string) (domain.NodeID, error) {
	var id string
	q, args := dialectPG.sqlNodeIDByKey(namespace, key)
	if err := t.tx.QueryRow(ctx, q, args...).Scan(&id); err != nil {
		return "", mapErr(err, "node key "+key)
	}
	return domain.NodeID(id), nil
}

func (t *pgTx) NodeByKey(ctx context.Context, namespace, key string) (domain.Node, error) {
	var id string
	q, args := dialectPG.sqlNodeIDByKey(namespace, key)
	if err := t.tx.QueryRow(ctx, q, args...).Scan(&id); err != nil {
		return domain.Node{}, mapErr(err, "node key "+key)
	}
	return t.LatestOn(ctx, domain.NodeID(id), domain.MainBranch)
}

func (t *pgTx) NodesIn(ctx context.Context, baseline domain.BaselineID, nodeType string) ([]domain.Node, error) {
	return storedNodesIn(ctx, t, baseline, nodeType)
}

func (t *pgTx) NodesAt(ctx context.Context, refs []domain.NodeRef, nodeType string) ([]domain.Node, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	q, args := dialectPG.sqlNodesAt(refs, nodeType)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "nodes")
	}
	return collectNodes(rows)
}

func (t *pgTx) LatestNodes(ctx context.Context, namespace, branch string) ([]domain.Node, error) {
	q := `SELECT DISTINCT ON (n.key) ` + dialectPG.nodeCols() + nodeFrom + `
	      WHERE n.namespace = $1 AND ` + onBranchSQL("$2") + ` ORDER BY n.key, v.version DESC`
	rows, err := t.tx.Query(ctx, q, domain.NamespaceOf(namespace), domain.BranchOf(branch))
	if err != nil {
		return nil, err
	}
	return collectNodes(rows)
}

func (t *pgTx) Namespaces(ctx context.Context) ([]string, error) {
	rows, err := t.tx.Query(ctx, `SELECT DISTINCT namespace FROM node ORDER BY namespace`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (t *pgTx) links(ctx context.Context, leaving bool, ref domain.NodeRef) ([]domain.Link, error) {
	q, args := dialectPG.sqlLinks(leaving, ref)
	return t.queryLinks(ctx, q, args...)
}

func (t *pgTx) queryLinks(ctx context.Context, q string, args ...any) ([]domain.Link, error) {
	rows, err := t.tx.Query(ctx, q, args...)
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
	return t.links(ctx, true, ref)
}

func (t *pgTx) InLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	return t.links(ctx, false, ref)
}

func (t *pgTx) Baseline(ctx context.Context, id domain.BaselineID) (domain.Baseline, error) {
	b, err := t.BaselineHeader(ctx, id)
	if err != nil {
		return b, err
	}
	b.Nodes, err = storedNodes(ctx, t, b)
	return b, err
}

func (t *pgTx) BaselineHeader(ctx context.Context, id domain.BaselineID) (domain.Baseline, error) {
	var b domain.Baseline
	q, args := dialectPG.sqlBaselineHeader(id)
	err := t.tx.QueryRow(ctx, q, args...).Scan((*string)(&b.ID), &b.Name, (*string)(&b.ParentID), (*string)(&b.MergedFrom), (*string)(&b.ChangeID), &b.CreatedAt,
		&b.Branch, &b.Namespace, &b.Gap, &b.Kind, (*string)(&b.RebasedFrom), &b.Seq, (*string)(&b.BaseID), &b.Depth)
	return b, mapErr(err, "baseline "+string(id))
}

func (t *pgTx) StoredVersions(ctx context.Context, id domain.BaselineID, ids []domain.NodeID) (map[domain.NodeID]domain.Version, domain.BaselineID, error) {
	out := map[domain.NodeID]domain.Version{}
	if ids != nil && len(ids) == 0 {
		return out, "", nil
	}
	q, args := dialectPG.sqlStoredVersions(id, ids)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	for rows.Next() {
		var nid string
		var v int
		var removed bool
		if err := rows.Scan(&nid, &v, &removed); err != nil {
			rows.Close()
			return nil, "", err
		}
		if removed {
			v = 0
		}
		out[domain.NodeID(nid)] = domain.Version(v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var tail string
	q, args = dialectPG.sqlStoredTail(id)
	if err := t.tx.QueryRow(ctx, q, args...).Scan(&tail); err != nil {
		return nil, "", mapErr(err, "baseline "+string(id))
	}
	return out, domain.BaselineID(tail), nil
}

func (t *pgTx) LandedVersions(ctx context.Context, namespace, branch string, after, upTo int64, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	out := map[domain.NodeID]domain.Version{}
	if ids != nil && len(ids) == 0 {
		return out, nil
	}
	q, args := dialectPG.sqlLandedVersions(namespace, branch, after, upTo, ids)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var nid string
		var v int
		var deleted bool
		if err := rows.Scan(&nid, &v, &deleted); err != nil {
			return nil, err
		}
		if deleted {
			v = 0
		}
		out[domain.NodeID(nid)] = domain.Version(v)
	}
	return out, rows.Err()
}

// NextLanding allocates the greatest order stored or allocated plus one: two transactions landing on one branch from
// the same head may take the same, one of them fails to move the head (advanceBranch); a baseline built on another is
// written once that one is committed, and sees it.
func (t *pgTx) LatestStored(ctx context.Context, namespace, branch string, after, upTo int64) (domain.BaselineID, error) {
	q, args := dialectPG.sqlLatestStored(namespace, branch, after, upTo)
	var id string
	if err := t.tx.QueryRow(ctx, q, args...).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return domain.BaselineID(id), nil
}

func (t *pgTx) NextLanding(ctx context.Context) (int64, error) {
	var stored int64
	if err := t.tx.QueryRow(ctx, `SELECT coalesce(max(seq), 0) FROM baseline`).Scan(&stored); err != nil {
		return 0, err
	}
	t.seq = max(t.seq, stored) + 1
	return t.seq, nil
}

func (t *pgTx) BranchNodeIDs(ctx context.Context, branch string) ([]domain.NodeID, error) {
	q, args := dialectPG.sqlBranchNodeIDs(branch)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.NodeID, error) {
		var id string
		err := r.Scan(&id)
		return domain.NodeID(id), err
	})
}

func (t *pgTx) BaselineHeaders(ctx context.Context, namespace string) ([]domain.Baseline, error) {
	q, args := dialectPG.sqlNamespaceBaselineHeaders(namespace)
	return t.baselineHeaders(ctx, namespace, q, args)
}

func (t *pgTx) BranchBaselineHeaders(ctx context.Context, namespace, branch string) ([]domain.Baseline, error) {
	q, args := dialectPG.sqlBranchBaselineHeaders(namespace, branch)
	return t.baselineHeaders(ctx, namespace, q, args)
}

func (t *pgTx) baselineHeaders(ctx context.Context, namespace, q string, args []any) ([]domain.Baseline, error) {
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Baseline
	for rows.Next() {
		b := domain.Baseline{Namespace: domain.NamespaceOf(namespace)}
		if err := rows.Scan((*string)(&b.ID), &b.Name, (*string)(&b.ParentID), (*string)(&b.MergedFrom), (*string)(&b.ChangeID), &b.CreatedAt, &b.Branch, &b.Kind, (*string)(&b.RebasedFrom), &b.Seq); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (t *pgTx) VersionJoins(ctx context.Context, id domain.NodeID) ([]domain.VersionJoin, error) {
	q, args := dialectPG.sqlVersionJoins(id)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.VersionJoin
	for rows.Next() {
		var v int
		var j domain.VersionJoin
		if err := rows.Scan(&v, &j.Branch, (*string)(&j.Change)); err != nil {
			return nil, err
		}
		j.Version = domain.Version(v)
		out = append(out, j)
	}
	return out, rows.Err()
}

func (t *pgTx) Baselines(ctx context.Context, namespace string) ([]domain.Baseline, error) {
	q, args := dialectPG.sqlBaselineIDs(namespace)
	rows, err := t.tx.Query(ctx, q, args...)
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

func (t *pgTx) Change(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	c, err := t.changeHeader(ctx, id)
	if err != nil {
		return c, err
	}
	c.Nodes, err = t.ChangeImpacts(ctx, id)
	return c, err
}

// changeHeader reads a change without its change impacts (the guard takes them from the fold of its log).
func (t *pgTx) changeHeader(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	c, err := t.ChangeHead(ctx, id)
	if err != nil {
		return c, err
	}
	facts, err := t.Log(ctx, factsFilter(id))
	if err != nil {
		return c, err
	}
	if c.Items, err = itemsOf(facts); err != nil {
		return c, err
	}
	return c, nil
}

// pgChangeHead is the select list of a change's head, in the order scanPGChangeHead reads it.
const pgChangeHead = `id::text, title, intent, methodology, goal, status, COALESCE(baseline_id::text, ''), result_baseline_id::text, data, created_at, branch, namespace, COALESCE(parent_id::text, ''), owner_org, project_id, lifecycle, state, guardians, depends_on, methodology_version`

func scanPGChangeHead(scan func(...any) error) (domain.Change, error) {
	var c domain.Change
	var result *string
	var data, guardians, dependsOn []byte
	if err := scan((*string)(&c.ID), &c.Title, &c.Intent, &c.Methodology, &c.Goal, (*string)(&c.Status), (*string)(&c.BaselineID), &result, &data, &c.CreatedAt, &c.Branch, &c.Namespace, (*string)(&c.ParentID), &c.OwnerOrg, &c.ProjectID, &c.Lifecycle, &c.State, &guardians, &dependsOn, &c.MethodologyVersion); err != nil {
		return c, err
	}
	c.ResultBaselineID, c.Data = domain.BaselineID(str(result)), props(data)
	var err error
	if c.Guardians, err = guardiansOf(guardians); err != nil {
		return c, err
	}
	c.DependsOn, err = dependsOnOf(dependsOn)
	return c, err
}

// ChangeHead implements Tx.
func (t *pgTx) ChangeHead(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	c, err := scanPGChangeHead(t.tx.QueryRow(ctx, `SELECT `+pgChangeHead+` FROM change WHERE id = $1`, string(id)).Scan)
	if err != nil {
		return c, mapErr(err, "change "+string(id))
	}
	return c, nil
}

// ChangeHeads implements Tx.
func (t *pgTx) ChangeHeads(ctx context.Context) ([]domain.Change, error) {
	rows, err := t.tx.Query(ctx, `SELECT `+pgChangeHead+` FROM change`+dialectPG.orderBy("created_at", ""))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Change
	for rows.Next() {
		c, err := scanPGChangeHead(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (t *pgTx) Changes(ctx context.Context) ([]domain.Change, error) {
	rows, err := t.tx.Query(ctx, dialectPG.sqlChangeIDs())
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
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

func (t *pgTx) SubChanges(ctx context.Context, parent domain.ChangeID) ([]domain.Change, error) {
	q, args := dialectPG.sqlSubChangeIDs(parent)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
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

func (t *pgTx) PutNode(ctx context.Context, n domain.Node) error {
	if n.Version == 1 {
		if _, err := t.tx.Exec(ctx, `INSERT INTO node (id, namespace, key, type, latest, project_id) VALUES ($1, $2, $3, $4, 1, $5)`, string(n.ID), domain.NamespaceOf(n.Namespace), n.Key, n.Type, nullUUID(string(n.Project))); err != nil {
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
	_, err := t.tx.Exec(ctx, dialectPG.sqlInsert("node_version", nodeVersionColumns, ""),
		string(n.ID), int(n.Version), jsonb(n.Properties), n.Deleted, nullUUID(string(n.ChangeID)), n.CreatedAt, domain.BranchOf(n.Branch), parents, n.Reason, n.State, nullUUID(string(n.ChangeImpact)), n.Comment, n.Execution, nullUUID(string(n.Owner)), originsJSON(n.Origins), n.Landed)
	return mapErr(err, "node "+n.Ref().String())
}

// exec1 runs a statement that must touch exactly one row (ErrNotFound otherwise).
func (t *pgTx) exec1(ctx context.Context, what, q string, args ...any) error {
	tag, err := t.tx.Exec(ctx, q, args...)
	if err != nil {
		return mapErr(err, what)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return nil
}

func (t *pgTx) DeleteChangeImpact(ctx context.Context, change domain.ChangeID, id domain.ChangeImpactID) error {
	return t.exec1(ctx, "change impact "+string(id), dialectPG.sqlDeleteChangeImpact(), string(change), string(id))
}

func (t *pgTx) Link(ctx context.Context, id domain.LinkID) (domain.Link, error) {
	q, args := dialectPG.sqlLinkByID(id)
	ls, err := t.queryLinks(ctx, q, args...)
	if err != nil {
		return domain.Link{}, err
	}
	if len(ls) == 0 {
		return domain.Link{}, fmt.Errorf("link %s: %w", id, ErrNotFound)
	}
	return ls[0], nil
}

func (t *pgTx) PutLink(ctx context.Context, l domain.Link) error {
	_, err := t.tx.Exec(ctx, dialectPG.sqlInsert("link", linkColumns, ""),
		string(l.ID), l.Type, string(l.From.ID), int(l.From.Version), string(l.To.ID), int(l.To.Version), jsonb(l.Properties), nullUUID(string(l.ChangeID)))
	return mapErr(err, "link")
}

// PutBaseline stores the baseline as the graph says (ADR 0140): its header, and its entries when it has a Gap of 0.
func (t *pgTx) PutBaseline(ctx context.Context, b domain.Baseline) error {
	_, err := t.tx.Exec(ctx, dialectPG.sqlInsert("baseline", baselineColumns, ""),
		string(b.ID), b.Name, nullUUID(string(b.ParentID)), nullUUID(string(b.MergedFrom)), nullUUID(string(b.ChangeID)), b.CreatedAt, domain.BranchOf(b.Branch), domain.NamespaceOf(b.Namespace), b.Depth, b.Gap, kindOf(b), nullUUID(string(b.RebasedFrom)), b.Seq, nullUUID(string(b.BaseID)))
	if err != nil {
		return mapErr(err, "baseline")
	}
	if b.Gap > 0 {
		return nil
	}
	return t.putEntries(ctx, b.ID, b.Nodes)
}

// putEntries writes the entries of a baseline: a version, minus the version for a node it removes.
func (t *pgTx) putEntries(ctx context.Context, id domain.BaselineID, nodes map[domain.NodeID]domain.Version) error {
	rows := make([][]any, 0, len(nodes))
	for nid, v := range nodes {
		rows = append(rows, []any{string(id), string(nid), int(max(v, -v)), v < 0})
	}
	_, err := t.tx.CopyFrom(ctx, pgx.Identifier{"baseline_entry"}, []string{"baseline_id", "node_id", "version", "removed"}, pgx.CopyFromRows(rows))
	return mapErr(err, "baseline entries")
}

func (t *pgTx) MaterializeBaseline(ctx context.Context, id domain.BaselineID, nodes map[domain.NodeID]domain.Version) error {
	if _, err := t.tx.Exec(ctx, `DELETE FROM baseline_entry WHERE baseline_id = $1`, string(id)); err != nil {
		return mapErr(err, "baseline "+string(id))
	}
	if res, err := t.tx.Exec(ctx, `UPDATE baseline SET depth = 0, gap = 0, base_id = NULL WHERE id = $1`, string(id)); err != nil {
		return mapErr(err, "baseline "+string(id))
	} else if res.RowsAffected() != 1 {
		return fmt.Errorf("baseline %s: %w", id, ErrNotFound)
	}
	return t.putEntries(ctx, id, nodes)
}

func (t *pgTx) PutChange(ctx context.Context, c domain.Change) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO change (id, title, intent, methodology, goal, status, baseline_id, result_baseline_id, data, created_at, branch, namespace, parent_id, owner_org, project_id, lifecycle, state, guardians, depends_on, methodology_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
		ON CONFLICT (id) DO UPDATE SET title = EXCLUDED.title, intent = EXCLUDED.intent, goal = EXCLUDED.goal, status = EXCLUDED.status,
		  result_baseline_id = EXCLUDED.result_baseline_id, data = EXCLUDED.data, baseline_id = EXCLUDED.baseline_id, branch = EXCLUDED.branch,
		  lifecycle = EXCLUDED.lifecycle, state = EXCLUDED.state, project_id = EXCLUDED.project_id,
		  parent_id = EXCLUDED.parent_id, guardians = EXCLUDED.guardians, depends_on = EXCLUDED.depends_on,
		  methodology = EXCLUDED.methodology, methodology_version = EXCLUDED.methodology_version`,
		string(c.ID), c.Title, c.Intent, c.Methodology, c.Goal, string(c.Status), nullUUID(string(c.BaselineID)), nullUUID(string(c.ResultBaselineID)), jsonb(c.Data), c.CreatedAt,
		domain.BranchOf(c.Branch), domain.NamespaceOf(c.Namespace), nullUUID(string(c.ParentID)), c.OwnerOrg, c.ProjectID, c.Lifecycle, c.State, guardiansJSON(c.Guardians), dependsOnJSON(c.DependsOn), c.MethodologyVersion)
	return mapErr(err, "change")
}

func (t *pgTx) AppendLog(ctx context.Context, e domain.LogEntry) (domain.LogEntry, error) {
	err := t.tx.QueryRow(ctx, dialectPG.sqlInsert("change_log", changeLogColumns, " RETURNING seq"),
		e.ID, string(e.Change), e.Type, e.Flow, e.Process, e.Execution, e.Subject, e.By, e.Causation, e.At, []byte(e.Payload)).Scan(&e.Seq)
	return e, mapErr(err, "log entry "+e.Type)
}

func (t *pgTx) LogCounts(ctx context.Context, f domain.LogFilter) (map[string]int, error) {
	q, args := dialectPG.sqlLogCounts(f)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "log counts")
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

func (t *pgTx) Log(ctx context.Context, f domain.LogFilter) ([]domain.LogEntry, error) {
	q, args := dialectPG.sqlLog(f)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "log")
	}
	defer rows.Close()
	var out []domain.LogEntry
	for rows.Next() {
		var e domain.LogEntry
		var change string
		var payload []byte
		if err := rows.Scan(&e.Seq, &e.ID, &change, &e.Type, &e.Flow, &e.Process, &e.Execution, &e.Subject, &e.By, &e.Causation, &e.At, &payload); err != nil {
			return nil, err
		}
		e.Change, e.Payload = domain.ChangeID(change), json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (t *pgTx) OpenChangeIDs(ctx context.Context) ([]domain.ChangeID, error) {
	rows, err := t.tx.Query(ctx, dialectPG.sqlOpenChangeIDs())
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := make([]domain.ChangeID, len(ids))
	for i, id := range ids {
		out[i] = domain.ChangeID(id)
	}
	return out, nil
}

func (t *pgTx) PutChangeImpact(ctx context.Context, change domain.ChangeID, cn domain.ChangeImpact) error {
	r, err := toCNRow(cn)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, `INSERT INTO change_impact (id, change_id, node_id, key, type, intent, rationale, pre_version, post_version, landed_version, review, reviews, via, recheck, produced_by, derived_from, items, execution, created_at, flow, superseded)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		ON CONFLICT (id) DO UPDATE SET node_id = $3, key = $4, type = $5, intent = $6, rationale = $7, pre_version = $8, post_version = $9, landed_version = $10,
			review = $11, reviews = $12, via = $13, recheck = $14, produced_by = $15, derived_from = $16, items = $17, execution = $18, flow = $20, superseded = $21`,
		r.ID, string(change), r.NodeID, r.Key, r.Type, r.Intent, r.Rationale, r.Pre, r.Post, r.Landed, r.Review, r.Reviews, nullUUID(r.Via), r.Recheck, r.ProducedBy, r.DerivedFrom, r.Items, r.Execution, cn.CreatedAt, r.Flow, r.Superseded)
	return mapErr(err, "change impact "+cn.Key)
}

func (t *pgTx) ChangeImpacts(ctx context.Context, change domain.ChangeID) ([]domain.ChangeImpact, error) {
	q, args := dialectPG.sqlChangeImpacts(change)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ChangeImpact
	for rows.Next() {
		var r cnRow
		var created time.Time
		if err := rows.Scan(&r.ID, &r.NodeID, &r.Key, &r.Type, &r.Intent, &r.Rationale, &r.Pre, &r.Post, &r.Landed, &r.Review, &r.Reviews, &r.Via, &r.Recheck, &r.ProducedBy, &r.DerivedFrom, &r.Items, &r.Execution, &created, &r.Flow, &r.Superseded); err != nil {
			return nil, err
		}
		cn, err := r.node()
		if err != nil {
			return nil, err
		}
		cn.CreatedAt = created
		out = append(out, cn)
	}
	return out, rows.Err()
}

func (t *pgTx) NodeChangePage(ctx context.Context, node domain.NodeID, before int64, limit int) ([]NodeChange, error) {
	q, args := dialectPG.sqlNodeChangePage(node, before, limit)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeChange
	for rows.Next() {
		var c NodeChange
		if err := rows.Scan((*string)(&c.Change), &c.Seq); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (t *pgTx) SetNodeOrigin(ctx context.Context, ref domain.NodeRef, change domain.ChangeID, cn domain.ChangeImpactID, comment string) error {
	tag, err := t.tx.Exec(ctx, dialectPG.sqlSetNodeOrigin(), string(ref.ID), int(ref.Version), nullUUID(string(change)), nullUUID(string(cn)), comment)
	if err != nil {
		return mapErr(err, "node "+ref.String())
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	return nil
}

func (t *pgTx) JoinBranch(ctx context.Context, ref domain.NodeRef, branch string, change domain.ChangeID, landed int64) error {
	_, err := t.tx.Exec(ctx, dialectPG.sqlJoinBranch(), string(ref.ID), int(ref.Version), domain.BranchOf(branch), nullUUID(string(change)), landed)
	return mapErr(err, "node "+ref.String())
}

func (t *pgTx) PutTag(ctx context.Context, tag domain.Tag) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO tag (id, name, namespace, change_id, baseline_id, by, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, baseline_id = excluded.baseline_id`,
		string(tag.ID), tag.Name, domain.NamespaceOf(tag.Namespace), string(tag.ChangeID), nullUUID(string(tag.BaselineID)), tag.By, tag.CreatedAt)
	return mapErr(err, "tag "+tag.Name)
}

func (t *pgTx) DeleteTag(ctx context.Context, id domain.TagID) error {
	q, args := dialectPG.sqlDeleteTag(id)
	res, err := t.tx.Exec(ctx, q, args...)
	if err != nil {
		return mapErr(err, "tag "+string(id))
	}
	if res.RowsAffected() != 1 {
		return fmt.Errorf("tag %s: %w", id, ErrNotFound)
	}
	return nil
}

func (t *pgTx) Tags(ctx context.Context, f domain.TagFilter) ([]domain.Tag, error) {
	q, args := dialectPG.sqlTags(f)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Tag
	for rows.Next() {
		var tag domain.Tag
		if err := rows.Scan((*string)(&tag.ID), &tag.Name, &tag.Namespace, (*string)(&tag.ChangeID), (*string)(&tag.BaselineID), &tag.By, &tag.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

func (t *pgTx) DeleteChange(ctx context.Context, id domain.ChangeID, namespace, branch string) error {
	var used int
	d := dialectPG
	for _, q := range d.deleteChangeUsed() {
		if err := t.tx.QueryRow(ctx, q, string(id)).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			return fmt.Errorf("change %s: what it wrote is used by the graph: %w", id, ErrConflict)
		}
	}
	rows, err := t.tx.Query(ctx, d.sqlChangeNodes(), string(id))
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
	for _, q := range d.deleteChangeRows() {
		if _, err := t.tx.Exec(ctx, q, string(id)); err != nil {
			return mapErr(err, "change "+string(id))
		}
	}
	for _, n := range nodes {
		if _, err := t.tx.Exec(ctx, d.sqlDeleteOrphanNode(), n); err != nil {
			return mapErr(err, "node "+n)
		}
		if _, err := t.tx.Exec(ctx, d.sqlRefreshLatest(), n); err != nil {
			return mapErr(err, "node "+n)
		}
	}
	if branch != "" {
		if _, err := t.tx.Exec(ctx, d.sqlDeleteBranch(), namespace, branch); err != nil {
			return mapErr(err, "branch "+branch)
		}
	}
	tag, err := t.tx.Exec(ctx, d.sqlDeleteChange(), string(id))
	if err != nil {
		return mapErr(err, "change "+string(id))
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("change %s: %w", id, ErrNotFound)
	}
	return nil
}

func (t *pgTx) LinksFrom(ctx context.Context, refs []domain.NodeRef) ([]domain.Link, error) {
	q, args := dialectPG.sqlLinksFrom(refs)
	return t.queryLinks(ctx, q, args...)
}

func (t *pgTx) LinksTo(ctx context.Context, id domain.NodeID) ([]domain.Link, error) {
	q, args := dialectPG.sqlLinksTo(id)
	return t.queryLinks(ctx, q, args...)
}

func (t *pgTx) NodePage(ctx context.Context, refs []domain.NodeRef, q NodeQuery) (page NodePage, err error) {
	cq, cargs := dialectPG.sqlNodeTypeCounts(refs, q)
	counts, err := t.typeCounts(ctx, cq, cargs)
	if err != nil {
		return page, err
	}
	page.Types, page.Total = typeCounts(counts, q.Type)
	pq, pargs := dialectPG.sqlNodePage(refs, q)
	rows, err := t.tx.Query(ctx, pq, pargs...)
	if err != nil {
		return page, mapErr(err, "nodes")
	}
	page.Nodes, err = collectNodes(rows)
	return page, err
}

func (t *pgTx) LinkPage(ctx context.Context, refs []domain.NodeRef, q LinkQuery) (page LinkPage, err error) {
	cq, cargs := dialectPG.sqlLinkTypeCounts(refs, q)
	counts, err := t.typeCounts(ctx, cq, cargs)
	if err != nil {
		return page, err
	}
	page.Types, page.Total = typeCounts(counts, q.Type)
	pq, pargs := dialectPG.sqlLinkPage(refs, q)
	page.Links, err = t.queryLinks(ctx, pq, pargs...)
	return page, err
}

// typeCounts reads the (type, count) rows of a query.
func (t *pgTx) typeCounts(ctx context.Context, q string, args []any) (map[string]int, error) {
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
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

func (t *pgTx) BaselinePage(ctx context.Context, namespace, branch string, before int64, limit int) ([]domain.Baseline, error) {
	q, args := dialectPG.sqlBaselinePage(namespace, branch, before, limit)
	return t.baselineHeaders(ctx, namespace, q, args)
}

func (t *pgTx) BaselineHeadersOf(ctx context.Context, namespace string, ids []domain.BaselineID) ([]domain.Baseline, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q, args := dialectPG.sqlBaselineHeadersOf(namespace, ids)
	return t.baselineHeaders(ctx, namespace, q, args)
}

func (t *pgTx) ChangeBaselineHeaders(ctx context.Context, namespace string, changes []domain.ChangeID) ([]domain.Baseline, error) {
	if len(changes) == 0 {
		return nil, nil
	}
	q, args := dialectPG.sqlChangeBaselineHeaders(namespace, changes)
	return t.baselineHeaders(ctx, namespace, q, args)
}

func (t *pgTx) BaselineCounts(ctx context.Context, namespace string) (map[string]int, error) {
	q, args := dialectPG.sqlBaselineCounts(namespace)
	return t.typeCounts(ctx, q, args)
}
