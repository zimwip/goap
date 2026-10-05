package graph

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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

func scanNode(row pgx.Row) (domain.Node, error) {
	var n domain.Node
	var id string
	var change, cnode *string
	var owner, project string
	var p []byte
	var version int
	var parents []int32
	if err := row.Scan(&id, &version, &n.Namespace, &n.Key, &n.Type, &p, &n.Deleted, &change, &n.CreatedAt, &n.Branch, &parents, &n.Reason, &n.State, &cnode, &n.Comment, &n.Execution, &owner, &project, &n.CheckedOut); err != nil {
		return n, err
	}
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
	q, args = dialectPG.sqlVersionBranches(id)
	rows, err = t.tx.Query(ctx, q, args...)
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

func (t *pgTx) Branch(ctx context.Context, namespace, name string) (domain.Branch, error) {
	var b domain.Branch
	q, args := dialectPG.sqlBranch(namespace, name)
	err := t.tx.QueryRow(ctx, q, args...).
		Scan(&b.Namespace, &b.Name, &b.Parent, (*string)(&b.ForkBaseline), (*string)(&b.Head), &b.Origin, &b.Status, &b.CreatedAt, &b.Description, (*string)(&b.Intent))
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
		err := r.Scan(&b.Namespace, &b.Name, &b.Parent, (*string)(&b.ForkBaseline), (*string)(&b.Head), &b.Origin, &b.Status, &b.CreatedAt, &b.Description, (*string)(&b.Intent))
		return b, err
	})
}

func (t *pgTx) PutBranch(ctx context.Context, b domain.Branch) error {
	namespace := domain.NamespaceOf(b.Namespace)
	_, err := t.tx.Exec(ctx, `INSERT INTO branch (namespace, name, parent, fork_baseline, head_baseline, origin, status, created_at, description, intent) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (namespace, name) DO UPDATE SET status = EXCLUDED.status, head_baseline = EXCLUDED.head_baseline, description = EXCLUDED.description`,
		namespace, b.Name, b.Parent, nullUUID(string(b.ForkBaseline)), nullUUID(string(b.Head)), b.Origin, b.Status, b.CreatedAt, b.Description, string(b.Intent))
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
	if _, err := t.Baseline(ctx, baseline); err != nil {
		return nil, err
	}
	q, args := dialectPG.sqlNodesIn(baseline, nodeType)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "baseline nodes")
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
	var b domain.Baseline
	var parent, mergedFrom, change *string
	err := t.tx.QueryRow(ctx, `SELECT id::text, name, parent_id::text, merged_from::text, change_id::text, created_at, branch, namespace, gap, kind FROM baseline WHERE id = $1`, string(id)).
		Scan((*string)(&b.ID), &b.Name, &parent, &mergedFrom, &change, &b.CreatedAt, &b.Branch, &b.Namespace, &b.Gap, &b.Kind)
	if err != nil {
		return b, mapErr(err, "baseline "+string(id))
	}
	b.ParentID, b.MergedFrom, b.ChangeID = domain.BaselineID(str(parent)), domain.BaselineID(str(mergedFrom)), domain.ChangeID(str(change))
	b.Nodes = map[domain.NodeID]domain.Version{}
	if b.Gap > 0 {
		return b, nil // only the header is stored
	}
	q, args := dialectPG.sqlBaselineEntries(id)
	rows, err := t.tx.Query(ctx, q, args...)
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
	var c domain.Change
	var result *string
	var data []byte
	err := t.tx.QueryRow(ctx, `SELECT id::text, title, intent, methodology, goal, status, COALESCE(baseline_id::text, ''), result_baseline_id::text, data, created_at, branch, namespace, COALESCE(parent_id::text, ''), owner_org, project_id, lifecycle, state
		FROM change WHERE id = $1`, string(id)).
		Scan((*string)(&c.ID), &c.Title, &c.Intent, &c.Methodology, &c.Goal, (*string)(&c.Status), (*string)(&c.BaselineID), &result, &data, &c.CreatedAt, &c.Branch, &c.Namespace, (*string)(&c.ParentID), &c.OwnerOrg, &c.ProjectID, &c.Lifecycle, &c.State)
	if err != nil {
		return c, mapErr(err, "change "+string(id))
	}
	c.ResultBaselineID, c.Data = domain.BaselineID(str(result)), props(data)
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
		string(n.ID), int(n.Version), jsonb(n.Properties), n.Deleted, nullUUID(string(n.ChangeID)), n.CreatedAt, domain.BranchOf(n.Branch), parents, n.Reason, n.State, nullUUID(string(n.ChangeImpact)), n.Comment, n.Execution, nullUUID(string(n.Owner)), n.CheckedOut)
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

func (t *pgTx) SetNodeOwner(ctx context.Context, ref domain.NodeRef, owner domain.NodeID) error {
	return t.exec1(ctx, "node "+ref.String(), dialectPG.sqlSetNodeOwner(), string(ref.ID), int(ref.Version), nullUUID(string(owner)))
}

func (t *pgTx) CheckinVersion(ctx context.Context, ref domain.NodeRef) error {
	return t.exec1(ctx, "checked-out version "+ref.String(), dialectPG.sqlCheckin(), string(ref.ID), int(ref.Version), false)
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

func (t *pgTx) DeleteLink(ctx context.Context, id domain.LinkID) error {
	return t.exec1(ctx, "link "+string(id), dialectPG.sqlDeleteLink(), string(id))
}

func (t *pgTx) SetLinkProps(ctx context.Context, id domain.LinkID, props map[string]any) error {
	return t.exec1(ctx, "link "+string(id), dialectPG.sqlSetLinkProps(), string(id), jsonb(props))
}

func (t *pgTx) SetNodeProps(ctx context.Context, ref domain.NodeRef, props map[string]any) error {
	tag, err := t.tx.Exec(ctx, dialectPG.sqlSetNodeProps(), string(ref.ID), int(ref.Version), jsonb(props))
	if err != nil {
		return mapErr(err, "node "+ref.String())
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	return nil
}

func (t *pgTx) PutLink(ctx context.Context, l domain.Link) error {
	_, err := t.tx.Exec(ctx, dialectPG.sqlInsert("link", linkColumns, ""),
		string(l.ID), l.Type, string(l.From.ID), int(l.From.Version), string(l.To.ID), int(l.To.Version), jsonb(l.Properties), nullUUID(string(l.ChangeID)))
	return mapErr(err, "link")
}

// PutBaseline stores the baseline as a delta from its parent, or whole at a checkpoint (ADR 0032).
func (t *pgTx) PutBaseline(ctx context.Context, b domain.Baseline) error {
	var parent *domain.Baseline
	parentDepth := 0
	if b.ParentID != "" && b.Gap == 0 {
		var parentGap int
		if err := t.tx.QueryRow(ctx, `SELECT depth, gap FROM baseline WHERE id = $1`, string(b.ParentID)).Scan(&parentDepth, &parentGap); err != nil {
			return mapErr(err, "baseline "+string(b.ParentID))
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
	_, err := t.tx.Exec(ctx, dialectPG.sqlInsert("baseline", baselineColumns, ""),
		string(b.ID), b.Name, nullUUID(string(b.ParentID)), nullUUID(string(b.MergedFrom)), nullUUID(string(b.ChangeID)), b.CreatedAt, domain.BranchOf(b.Branch), domain.NamespaceOf(b.Namespace), depth, b.Gap, kindOf(b))
	if err != nil {
		return mapErr(err, "baseline")
	}
	rows := make([][]any, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, []any{string(b.ID), string(e.node), int(e.version), e.removed})
	}
	_, err = t.tx.CopyFrom(ctx, pgx.Identifier{"baseline_entry"}, []string{"baseline_id", "node_id", "version", "removed"}, pgx.CopyFromRows(rows))
	return mapErr(err, "baseline entries")
}

func (t *pgTx) BranchJoins(ctx context.Context, namespace, branch string, change domain.ChangeID) ([]domain.NodeRef, error) {
	q, args := dialectPG.sqlBranchJoins(namespace, branch, change)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.NodeRef, error) {
		var id string
		var v int
		err := r.Scan(&id, &v)
		return domain.NodeRef{ID: domain.NodeID(id), Version: domain.Version(v)}, err
	})
}

func (t *pgTx) MaterializeBaseline(ctx context.Context, id domain.BaselineID, nodes map[domain.NodeID]domain.Version) error {
	if _, err := t.tx.Exec(ctx, `DELETE FROM baseline_entry WHERE baseline_id = $1`, string(id)); err != nil {
		return mapErr(err, "baseline "+string(id))
	}
	if res, err := t.tx.Exec(ctx, `UPDATE baseline SET depth = 0, gap = 0 WHERE id = $1`, string(id)); err != nil {
		return mapErr(err, "baseline "+string(id))
	} else if res.RowsAffected() != 1 {
		return fmt.Errorf("baseline %s: %w", id, ErrNotFound)
	}
	rows := make([][]any, 0, len(nodes))
	for nid, v := range nodes {
		rows = append(rows, []any{string(id), string(nid), int(v), false})
	}
	_, err := t.tx.CopyFrom(ctx, pgx.Identifier{"baseline_entry"}, []string{"baseline_id", "node_id", "version", "removed"}, pgx.CopyFromRows(rows))
	return mapErr(err, "baseline entries")
}

func (t *pgTx) PutChange(ctx context.Context, c domain.Change) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO change (id, title, intent, methodology, goal, status, baseline_id, result_baseline_id, data, created_at, branch, namespace, parent_id, owner_org, project_id, lifecycle, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (id) DO UPDATE SET title = EXCLUDED.title, intent = EXCLUDED.intent, goal = EXCLUDED.goal, status = EXCLUDED.status,
		  result_baseline_id = EXCLUDED.result_baseline_id, data = EXCLUDED.data, baseline_id = EXCLUDED.baseline_id, branch = EXCLUDED.branch,
		  lifecycle = EXCLUDED.lifecycle, state = EXCLUDED.state`,
		string(c.ID), c.Title, c.Intent, c.Methodology, c.Goal, string(c.Status), nullUUID(string(c.BaselineID)), nullUUID(string(c.ResultBaselineID)), jsonb(c.Data), c.CreatedAt,
		domain.BranchOf(c.Branch), domain.NamespaceOf(c.Namespace), nullUUID(string(c.ParentID)), c.OwnerOrg, c.ProjectID, c.Lifecycle, c.State)
	return mapErr(err, "change")
}

func (t *pgTx) AppendLog(ctx context.Context, e domain.LogEntry) (domain.LogEntry, error) {
	err := t.tx.QueryRow(ctx, dialectPG.sqlInsert("change_log", changeLogColumns, " RETURNING seq"),
		e.ID, string(e.Change), e.Type, e.Flow, e.Process, e.Execution, e.Subject, e.By, e.At, []byte(e.Payload)).Scan(&e.Seq)
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
		if err := rows.Scan(&e.Seq, &e.ID, &change, &e.Type, &e.Flow, &e.Process, &e.Execution, &e.Subject, &e.By, &e.At, &payload); err != nil {
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

func (t *pgTx) NodeChangeImpacts(ctx context.Context, node domain.NodeID) ([]domain.ChangeID, error) {
	q, args := dialectPG.sqlNodeChangeImpacts(node)
	rows, err := t.tx.Query(ctx, q, args...)
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

func (t *pgTx) JoinBranch(ctx context.Context, ref domain.NodeRef, branch string, change domain.ChangeID) error {
	_, err := t.tx.Exec(ctx, dialectPG.sqlJoinBranch(), string(ref.ID), int(ref.Version), domain.BranchOf(branch), nullUUID(string(change)))
	return mapErr(err, "node "+ref.String())
}

func (t *pgTx) baselineHeaders(ctx context.Context) ([]baselineHeader, error) {
	rows, err := t.tx.Query(ctx, dialectPG.sqlBaselineHeaders())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (baselineHeader, error) {
		var h baselineHeader
		err := r.Scan((*string)(&h.id), (*string)(&h.parent), &h.depth)
		return h, err
	})
}

func (t *pgTx) rewriteBaseline(ctx context.Context, id domain.BaselineID, depth int, entries []baselineEntry) error {
	if _, err := t.tx.Exec(ctx, `DELETE FROM baseline_entry WHERE baseline_id = $1`, string(id)); err != nil {
		return err
	}
	if _, err := t.tx.Exec(ctx, `UPDATE baseline SET depth = $2 WHERE id = $1`, string(id), depth); err != nil {
		return err
	}
	rows := make([][]any, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, []any{string(id), string(e.node), int(e.version), e.removed})
	}
	_, err := t.tx.CopyFrom(ctx, pgx.Identifier{"baseline_entry"}, []string{"baseline_id", "node_id", "version", "removed"}, pgx.CopyFromRows(rows))
	return mapErr(err, "baseline entries")
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
