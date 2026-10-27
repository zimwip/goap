package graph

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/lnk"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
)

// SQLiteMigrations holds the SQLite schema of the graph (local development mode).
//
//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// SQLite is a Repo on a SQLite database (database/sql, schema migrated from
// SQLiteMigrations). The driver is registered by the caller.
type SQLite struct {
	db *sql.DB
	// read, when set, serves the transactions of a read-only context (platform.ReadOnly) on read-only connections:
	// they never wait behind the writer of the single connection of db (WithReader)
	read *sql.DB
	// readAll takes every context as read-only (the tests of the readers)
	readAll bool
	// commit is the greatest order of commit allocated (ADR 0140), read from the database at the first allocation:
	// the order is global and the local mode has one process writing
	mu     sync.Mutex
	commit int64
}

// NewSQLite returns a repository on db.
func NewSQLite(db *sql.DB) *SQLite { return &SQLite{db: db} }

// WithReader gives the repository a pool of read-only connections on its file (platform.OpenSQLiteReader) for the
// transactions of a read-only context.
func (s *SQLite) WithReader(read *sql.DB) *SQLite {
	s.read = read
	return s
}

// errWrites stops a read-only transaction that writes: it runs again on the writer.
var errWrites = errors.New("graph: a read-only transaction writes")

// InTx implements Repo.
func (s *SQLite) InTx(ctx context.Context, fn func(tx Tx) error) error {
	if s.read != nil && (s.readAll || platform.IsReadOnly(ctx)) {
		err := s.readTx(ctx, fn)
		if !errors.Is(err, errWrites) {
			return err
		}
		slog.WarnContext(ctx, "graph: a read-only request writes; running its transaction on the writer")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&sqliteTx{tx: tx, repo: s}); err != nil {
		tx.Rollback() //nolint:errcheck
		return err
	}
	return tx.Commit()
}

// readTx runs fn in a read-only transaction on the readers; a write it tries ends it with errWrites.
func (s *SQLite) readTx(ctx context.Context, fn func(tx Tx) error) error {
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	err = fn(&sqliteTx{tx: tx, repo: s, readOnly: true})
	if err != nil && platform.IsSQLiteReadOnly(err) {
		return errWrites
	}
	return err
}

type sqliteTx struct {
	tx   *sql.Tx
	repo *SQLite
	// readOnly is a transaction on the readers (readTx)
	readOnly bool
}

// AppendOutbox implements Tx.
func (t *sqliteTx) AppendOutbox(ctx context.Context, msgs []outbox.Message) error {
	return outbox.SQL{}.Append(ctx, t.tx, msgs...)
}

// Pending implements outbox.Store.
func (s *SQLite) Pending(ctx context.Context, limit int) ([]outbox.Message, error) {
	return outbox.SQL{DB: s.db}.Pending(ctx, limit)
}

// MarkPublished implements outbox.Store.
func (s *SQLite) MarkPublished(ctx context.Context, seqs []int64, at time.Time) error {
	return outbox.SQL{DB: s.db}.MarkPublished(ctx, seqs, at)
}

// PurgePublished implements outbox.Store.
func (s *SQLite) PurgePublished(ctx context.Context, before time.Time) error {
	return outbox.SQL{DB: s.db}.PurgePublished(ctx, before)
}

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

type scanner interface{ Scan(dest ...any) error }

func sqliteScanNode(row scanner) (domain.Node, error) {
	var n domain.Node
	var id, p, created, parents, origins string
	var change, cnode sql.NullString
	var owner, project, creatorUnit string
	var version int
	if err := row.Scan(&id, &version, &n.Namespace, &n.Key, &n.Type, &p, &n.Deleted, &change, &created, &n.Branch, &parents, &n.Reason, &n.State, &cnode, &n.Comment, &n.Execution, &owner, &project, &origins, &n.Creator.Subject, &creatorUnit, &n.Editor, &n.Visibility); err != nil {
		return n, err
	}
	n.Origins = originsOf([]byte(origins))
	_ = json.Unmarshal([]byte(parents), &n.Parents)
	if len(n.Parents) == 0 {
		n.Parents = nil
	}
	n.ID, n.Version, n.Properties, n.ChangeID = domain.NodeID(id), domain.Version(version), props([]byte(p)), domain.ChangeID(change.String)
	n.ChangeImpact = domain.ChangeImpactID(cnode.String)
	n.Responsible, n.Project, n.Creator.Unit = lnk.Of(owner), lnk.Of(project), lnk.Of(creatorUnit)
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
	q, args := dialectSQLite.sqlNode(ref)
	n, err := sqliteScanNode(t.tx.QueryRowContext(ctx, q, args...))
	return n, sqliteErr(err, "node "+ref.String())
}

func (t *sqliteTx) LatestOn(ctx context.Context, id domain.NodeID, branch string) (domain.Node, error) {
	q, args := dialectSQLite.sqlLatestOn(id, branch)
	n, err := sqliteScanNode(t.tx.QueryRowContext(ctx, q, args...))
	return n, sqliteErr(err, "node "+string(id)+" on "+domain.BranchOf(branch))
}

func (t *sqliteTx) DerivedNodes(ctx context.Context, ref domain.NodeRef) ([]domain.Node, error) {
	q, args := dialectSQLite.sqlDerivedNodes(ref)
	return t.nodes(ctx, q, args...)
}

func (t *sqliteTx) Versions(ctx context.Context, id domain.NodeID) ([]domain.Node, error) {
	q, args := dialectSQLite.sqlVersions(id)
	out, err := t.nodes(ctx, q, args...)
	if err == nil && len(out) == 0 {
		err = fmt.Errorf("node %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return out, err
	}
	return out, t.joinBranches(ctx, id, out)
}

func (t *sqliteTx) VersionRange(ctx context.Context, id domain.NodeID, before domain.Version, limit int) ([]domain.Node, error) {
	q, args := dialectSQLite.sqlVersionRange(id, before, limit)
	out, err := t.nodes(ctx, q, args...)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	slices.Reverse(out)
	return out, t.joinBranches(ctx, id, out)
}

// joinBranches fills the branches the versions vs of a node (by version, consecutive) joined.
func (t *sqliteTx) joinBranches(ctx context.Context, id domain.NodeID, vs []domain.Node) error {
	q, args := dialectSQLite.sqlVersionBranches(id, vs[0].Version)
	rows, err := t.tx.QueryContext(ctx, q, args...)
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

func sqliteScanBranch(row scanner) (domain.Branch, error) {
	var b domain.Branch
	var created string
	err := row.Scan(&b.Namespace, &b.Name, &b.Parent, (*string)(&b.ForkBaseline), (*string)(&b.Head), &b.Origin, &b.Status, &created, &b.Description, (*string)(&b.Intent), (*string)(&b.RebasedFrom), &b.Ephemeral, &b.ForkSeq)
	b.CreatedAt = tsParse(created)
	return b, err
}

// LockChange implements Tx: SQLite runs one transaction at a time (one connection, internal/platform), so the writers
// of a change are serialized already.
// A lock announces a write: a read-only transaction stops there, before it reads what the write checks.
func (t *sqliteTx) LockChange(context.Context, domain.ChangeID) error { return t.writes() }

// LockBranch implements Tx (see LockChange).
func (t *sqliteTx) LockBranch(context.Context, string, string) error { return t.writes() }

func (t *sqliteTx) writes() error {
	if t.readOnly {
		return errWrites
	}
	return nil
}

func (t *sqliteTx) Branch(ctx context.Context, namespace, name string) (domain.Branch, error) {
	q, args := dialectSQLite.sqlBranch(namespace, name)
	b, err := sqliteScanBranch(t.tx.QueryRowContext(ctx, q, args...))
	return b, sqliteErr(err, "branch "+name)
}

func (t *sqliteTx) Branches(ctx context.Context, namespace string) ([]domain.Branch, error) {
	q, args := dialectSQLite.sqlBranches(namespace)
	rows, err := t.tx.QueryContext(ctx, q, args...)
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
	_, err := t.tx.ExecContext(ctx, `INSERT INTO branch (namespace, name, parent, fork_baseline, head_baseline, origin, status, created_at, description, intent, rebased_from, ephemeral, fork_seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (namespace, name) DO UPDATE SET status = excluded.status, head_baseline = excluded.head_baseline, description = excluded.description,
			fork_baseline = excluded.fork_baseline, rebased_from = excluded.rebased_from, parent = excluded.parent, fork_seq = excluded.fork_seq`,
		namespace, b.Name, b.Parent, nullUUID(string(b.ForkBaseline)), nullUUID(string(b.Head)), b.Origin, b.Status, tsText(b.CreatedAt), b.Description, string(b.Intent), nullUUID(string(b.RebasedFrom)), b.Ephemeral, b.ForkSeq)
	return sqliteErr(err, "branch "+b.Name)
}

func (t *sqliteTx) NodeIDByKey(ctx context.Context, namespace, key string) (domain.NodeID, error) {
	var id string
	q, args := dialectSQLite.sqlNodeIDByKey(namespace, key)
	if err := t.tx.QueryRowContext(ctx, q, args...).Scan(&id); err != nil {
		return "", sqliteErr(err, "node key "+key)
	}
	return domain.NodeID(id), nil
}

func (t *sqliteTx) NodeIDsByKey(ctx context.Context, namespace string, keys []string) (map[string]domain.NodeID, error) {
	out := make(map[string]domain.NodeID, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	q, args := dialectSQLite.sqlNodeIDsByKey(namespace, keys)
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, sqliteErr(err, "node keys")
	}
	defer rows.Close()
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			return nil, err
		}
		out[key] = domain.NodeID(id)
	}
	return out, rows.Err()
}

func (t *sqliteTx) NodeByKey(ctx context.Context, namespace, key string) (domain.Node, error) {
	var id string
	q, args := dialectSQLite.sqlNodeIDByKey(namespace, key)
	if err := t.tx.QueryRowContext(ctx, q, args...).Scan(&id); err != nil {
		return domain.Node{}, sqliteErr(err, "node key "+key)
	}
	return t.LatestOn(ctx, domain.NodeID(id), domain.MainBranch)
}

func (t *sqliteTx) NodesIn(ctx context.Context, baseline domain.BaselineID, nodeType string) ([]domain.Node, error) {
	return storedNodesIn(ctx, t, baseline, nodeType)
}

func (t *sqliteTx) NodesAt(ctx context.Context, refs []domain.NodeRef, nodeType string) ([]domain.Node, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	q, args := dialectSQLite.sqlNodesAt(refs, nodeType)
	return t.nodes(ctx, q, args...)
}

func (t *sqliteTx) LatestNodes(ctx context.Context, namespace, branch string) ([]domain.Node, error) {
	ns, br := domain.NamespaceOf(namespace), domain.BranchOf(branch)
	return t.nodes(ctx, `SELECT `+dialectSQLite.nodeCols()+nodeFrom+`
		WHERE n.namespace = ? AND v.version = (SELECT max(version) FROM (
		  SELECT version FROM node_version WHERE node_id = n.id AND branch = ?
		  UNION ALL SELECT version FROM node_branch WHERE node_id = n.id AND branch = ?))
		ORDER BY n.key`, ns, br, br)
}

func (t *sqliteTx) Namespaces(ctx context.Context) ([]string, error) {
	return t.ids(ctx, `SELECT DISTINCT namespace FROM node ORDER BY namespace`)
}

func (t *sqliteTx) queryLinks(ctx context.Context, q string, args ...any) ([]domain.Link, error) {
	rows, err := t.tx.QueryContext(ctx, q, args...)
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
		var url string
		if err := rows.Scan(&id, &l.Type, &from, &fv, &to, &tv, &p, &change, &url); err != nil {
			return nil, err
		}
		l.ID, l.Properties, l.ChangeID = domain.LinkID(id), props([]byte(p)), domain.ChangeID(change.String)
		l.From = domain.NodeRef{ID: domain.NodeID(from), Version: domain.Version(fv)}
		if to != "" {
			l.To = domain.NodeRef{ID: domain.NodeID(to), Version: domain.Version(tv)}
		} else if l.Target, err = lnk.Parse(url); err != nil {
			return nil, fmt.Errorf("link %s: %w", id, err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (t *sqliteTx) OutLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	return t.LinksFrom(ctx, []domain.NodeRef{ref})
}

func (t *sqliteTx) LinksInto(ctx context.Context, refs []domain.NodeRef) ([]domain.Link, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	q, args := dialectSQLite.sqlLinksInto(refs)
	return t.queryLinks(ctx, q, args...)
}

func (t *sqliteTx) LatestVersionsOn(ctx context.Context, ids []domain.NodeID, branch string) (map[domain.NodeID]domain.Version, error) {
	out := make(map[domain.NodeID]domain.Version, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	q, args := dialectSQLite.sqlLatestVersionsOn(ids, branch)
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, sqliteErr(err, "nodes")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id string
			v  int
		)
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[domain.NodeID(id)] = domain.Version(v)
	}
	return out, rows.Err()
}

func (t *sqliteTx) LatestVersions(ctx context.Context, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	out := make(map[domain.NodeID]domain.Version, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	q, args := dialectSQLite.sqlLatestVersions(ids)
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, sqliteErr(err, "nodes")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id string
			v  int
		)
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[domain.NodeID(id)] = domain.Version(v)
	}
	return out, rows.Err()
}

func (t *sqliteTx) InLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	q, args := dialectSQLite.sqlLinksTo(ref)
	return t.queryLinks(ctx, q, args...)
}

func (t *sqliteTx) Baseline(ctx context.Context, id domain.BaselineID) (domain.Baseline, error) {
	b, err := t.BaselineHeader(ctx, id)
	if err != nil {
		return b, err
	}
	b.Nodes, err = storedNodes(ctx, t, b)
	return b, err
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

func (t *sqliteTx) BaselineHeader(ctx context.Context, id domain.BaselineID) (domain.Baseline, error) {
	var b domain.Baseline
	var created string
	q, args := dialectSQLite.sqlBaselineHeader(id)
	err := t.tx.QueryRowContext(ctx, q, args...).Scan((*string)(&b.ID), &b.Name, (*string)(&b.ParentID), (*string)(&b.MergedFrom), (*string)(&b.ChangeID), &created,
		&b.Branch, &b.Namespace, &b.Kind, (*string)(&b.RebasedFrom), &b.Seq)
	if err != nil {
		return b, sqliteErr(err, "baseline "+string(id))
	}
	b.CreatedAt = tsParse(created)
	return b, nil
}

func (t *sqliteTx) CommittedVersions(ctx context.Context, namespace, branch string, after, upTo int64, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	ls, err := t.commits(ctx, namespace, branch, after, upTo, ids)
	return committedVersions(ls), err
}

func (t *sqliteTx) BranchCommits(ctx context.Context, namespace, branch string, upTo int64, ids []domain.NodeID) (map[domain.NodeID]CommittedVersion, error) {
	return t.commits(ctx, namespace, branch, 0, upTo, ids)
}

func (t *sqliteTx) commits(ctx context.Context, namespace, branch string, after, upTo int64, ids []domain.NodeID) (map[domain.NodeID]CommittedVersion, error) {
	out := map[domain.NodeID]CommittedVersion{}
	if ids != nil && len(ids) == 0 {
		return out, nil
	}
	q, args := dialectSQLite.sqlCommittedVersions(namespace, branch, after, upTo, ids)
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var nid, reason string
		var v int
		var l CommittedVersion
		if err := rows.Scan(&nid, &v, &l.Deleted, &reason); err != nil {
			return nil, err
		}
		l.Version, l.Revert = domain.Version(v), reason == domain.ReasonRevert
		out[domain.NodeID(nid)] = l
	}
	return out, rows.Err()
}

func (t *sqliteTx) NextCommit(ctx context.Context) (int64, error) {
	r := t.repo
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.commit == 0 {
		if err := t.tx.QueryRowContext(ctx, `SELECT max((SELECT coalesce(max(seq), 0) FROM baseline), (SELECT coalesce(max(committed), 0) FROM node_version),
			(SELECT coalesce(max(committed), 0) FROM node_branch))`).Scan(&r.commit); err != nil {
			return 0, err
		}
	}
	r.commit++
	return r.commit, nil
}

func (t *sqliteTx) BranchNodeIDs(ctx context.Context, branch string) ([]domain.NodeID, error) {
	q, args := dialectSQLite.sqlBranchNodeIDs(branch)
	ids, err := t.ids(ctx, q, args...)
	out := make([]domain.NodeID, len(ids))
	for i, id := range ids {
		out[i] = domain.NodeID(id)
	}
	return out, err
}

func (t *sqliteTx) NamespaceNodeIDs(ctx context.Context, namespace string) ([]domain.NodeID, error) {
	q, args := dialectSQLite.sqlNamespaceNodeIDs(namespace)
	ids, err := t.ids(ctx, q, args...)
	out := make([]domain.NodeID, len(ids))
	for i, id := range ids {
		out[i] = domain.NodeID(id)
	}
	return out, err
}

func (t *sqliteTx) BaselineHeaders(ctx context.Context, namespace string) ([]domain.Baseline, error) {
	q, args := dialectSQLite.sqlNamespaceBaselineHeaders(namespace)
	return t.baselineHeaders(ctx, namespace, q, args)
}

func (t *sqliteTx) BranchBaselineHeaders(ctx context.Context, namespace, branch string) ([]domain.Baseline, error) {
	q, args := dialectSQLite.sqlBranchBaselineHeaders(namespace, branch)
	return t.baselineHeaders(ctx, namespace, q, args)
}

func (t *sqliteTx) baselineHeaders(ctx context.Context, namespace, q string, args []any) ([]domain.Baseline, error) {
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Baseline
	for rows.Next() {
		b := domain.Baseline{Namespace: domain.NamespaceOf(namespace)}
		var created string
		if err := rows.Scan((*string)(&b.ID), &b.Name, (*string)(&b.ParentID), (*string)(&b.MergedFrom), (*string)(&b.ChangeID), &created, &b.Branch, &b.Kind, (*string)(&b.RebasedFrom), &b.Seq); err != nil {
			return nil, err
		}
		b.CreatedAt = tsParse(created)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (t *sqliteTx) VersionJoins(ctx context.Context, id domain.NodeID) ([]domain.VersionJoin, error) {
	q, args := dialectSQLite.sqlVersionJoins(id)
	rows, err := t.tx.QueryContext(ctx, q, args...)
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

func (t *sqliteTx) Baselines(ctx context.Context, namespace string) ([]domain.Baseline, error) {
	q, args := dialectSQLite.sqlBaselineIDs(namespace)
	ids, err := t.ids(ctx, q, args...)
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
	c, err := t.changeHeader(ctx, id)
	if err != nil {
		return c, err
	}
	c.Nodes, err = t.ChangeImpacts(ctx, id)
	return c, err
}

// changeHeader reads a change without its change impacts (the guard takes them from the fold of its log).
func (t *sqliteTx) changeHeader(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
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

// sqliteChangeHead is the select list of a change's head, in the order scanSQLiteChangeHead reads it.
const sqliteChangeHead = `id, title, intent, methodology, goal, status, baseline_id, result_baseline_id, data, created_at, branch, namespace, COALESCE(parent_id, ''), owner_org, project_id, lifecycle, state, guardians, depends_on, methodology_version`

func scanSQLiteChangeHead(scan func(...any) error) (domain.Change, error) {
	var c domain.Change
	var start, result sql.NullString
	var data, created, guardians, dependsOn string
	if err := scan((*string)(&c.ID), &c.Title, &c.Intent, &c.Methodology, &c.Goal, (*string)(&c.Status), &start, &result, &data, &created, &c.Branch, &c.Namespace, (*string)(&c.ParentID), &c.OwnerOrg, &c.ProjectID, &c.Lifecycle, &c.State, &guardians, &dependsOn, &c.MethodologyVersion); err != nil {
		return c, err
	}
	c.BaselineID, c.ResultBaselineID, c.Data, c.CreatedAt = domain.BaselineID(start.String), domain.BaselineID(result.String), props([]byte(data)), tsParse(created)
	var err error
	if c.Guardians, err = guardiansOf([]byte(guardians)); err != nil {
		return c, err
	}
	c.DependsOn, err = dependsOnOf([]byte(dependsOn))
	return c, err
}

// ChangeHead implements Tx.
func (t *sqliteTx) ChangeHead(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	c, err := scanSQLiteChangeHead(t.tx.QueryRowContext(ctx, `SELECT `+sqliteChangeHead+` FROM change WHERE id = ?`, string(id)).Scan)
	if err != nil {
		return c, sqliteErr(err, "change "+string(id))
	}
	return c, nil
}

// ChangeHeads implements Tx.
func (t *sqliteTx) ChangeHeads(ctx context.Context, f ChangesFilter) ([]domain.Change, error) {
	q, args := dialectSQLite.sqlChangeHeads(sqliteChangeHead, f)
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Change
	for rows.Next() {
		c, err := scanSQLiteChangeHead(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (t *sqliteTx) Changes(ctx context.Context) ([]domain.Change, error) {
	ids, err := t.ids(ctx, dialectSQLite.sqlChangeIDs())
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

func (t *sqliteTx) SubChanges(ctx context.Context, parent domain.ChangeID) ([]domain.Change, error) {
	q, args := dialectSQLite.sqlSubChangeIDs(parent)
	ids, err := t.ids(ctx, q, args...)
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
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO node (id, namespace, key, type, latest, project, creator, creator_unit) VALUES (?, ?, ?, ?, 1, ?, ?, ?)`, string(n.ID), domain.NamespaceOf(n.Namespace), n.Key, n.Type, n.Project.String(), n.Creator.Subject, n.Creator.Unit.String()); err != nil {
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
	_, err := t.tx.ExecContext(ctx, dialectSQLite.sqlInsert("node_version", nodeVersionColumns, ""),
		string(n.ID), int(n.Version), string(jsonb(n.Properties)), n.Deleted, nullUUID(string(n.ChangeID)), tsText(n.CreatedAt),
		domain.BranchOf(n.Branch), string(pj), n.Reason, n.State, nullUUID(string(n.ChangeImpact)), n.Comment, n.Execution, n.Responsible.String(), string(originsJSON(n.Origins)), n.Committed, n.Editor, n.Visibility)
	return sqliteErr(err, "node "+n.Ref().String())
}

// exec1 runs a statement that must touch exactly one row (ErrNotFound otherwise).
func (t *sqliteTx) exec1(ctx context.Context, what, q string, args ...any) error {
	res, err := t.tx.ExecContext(ctx, q, args...)
	if err != nil {
		return sqliteErr(err, what)
	}
	if k, _ := res.RowsAffected(); k != 1 {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return nil
}

func (t *sqliteTx) DeleteChangeImpact(ctx context.Context, change domain.ChangeID, id domain.ChangeImpactID) error {
	return t.exec1(ctx, "change impact "+string(id), dialectSQLite.sqlDeleteChangeImpact(), string(change), string(id))
}

func (t *sqliteTx) Link(ctx context.Context, id domain.LinkID) (domain.Link, error) {
	q, args := dialectSQLite.sqlLinkByID(id)
	ls, err := t.queryLinks(ctx, q, args...)
	if err != nil {
		return domain.Link{}, err
	}
	if len(ls) == 0 {
		return domain.Link{}, fmt.Errorf("link %s: %w", id, ErrNotFound)
	}
	return ls[0], nil
}

func (t *sqliteTx) PutLinks(ctx context.Context, ls []domain.Link) error {
	for rows := range slices.Chunk(ls, linkRowsPerInsert) {
		args := make([]any, 0, len(rows)*len(linkColumns))
		for _, l := range rows {
			to, tv, url := linkTarget(l)
			args = append(args, string(l.ID), l.Type, string(l.From.ID), int(l.From.Version), to, tv, string(jsonb(l.Properties)), nullUUID(string(l.ChangeID)), nil, url)
		}
		if _, err := t.tx.ExecContext(ctx, dialectSQLite.sqlInsertRows("link", linkColumns, len(rows)), args...); err != nil {
			return sqliteErr(err, "link")
		}
	}
	return nil
}

func (t *sqliteTx) DropLink(ctx context.Context, from domain.NodeRef, row domain.LinkID, l domain.Link) error {
	to, tv, url := linkTarget(l)
	_, err := t.tx.ExecContext(ctx, dialectSQLite.sqlInsert("link", linkColumns, ""),
		string(row), l.Type, string(from.ID), int(from.Version), to, tv, string(jsonb(l.Properties)), nullUUID(string(l.ChangeID)), string(l.ID), url)
	return sqliteErr(err, "link")
}

func (t *sqliteTx) PutLinkSet(ctx context.Context, from domain.NodeRef, set LinkSet) error {
	var of, base any
	if set.Of != 0 && set.Of != from.Version {
		of = int(set.Of)
	}
	if set.Base != 0 {
		base = int(set.Base)
	}
	_, err := t.tx.ExecContext(ctx, dialectSQLite.sqlPutLinkSet(), string(from.ID), int(from.Version), of, base, set.Depth, set.Seq)
	return sqliteErr(err, "link set")
}

func (t *sqliteTx) LinkSetOf(ctx context.Context, ref domain.NodeRef) (LinkSet, error) {
	var s LinkSet
	q, args := dialectSQLite.sqlLinkSetOf(ref)
	err := t.tx.QueryRowContext(ctx, q, args...).Scan(&s.Of, &s.Base, &s.Depth, &s.Seq)
	return s, sqliteErr(err, "link set of "+ref.String())
}

func (t *sqliteTx) LinkSetsOf(ctx context.Context, refs []domain.NodeRef) (map[domain.NodeRef]LinkSet, error) {
	out := make(map[domain.NodeRef]LinkSet, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	q, args := dialectSQLite.sqlLinkSetsOf(refs)
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id string
			v  int
			s  LinkSet
		)
		if err := rows.Scan(&id, &v, &s.Of, &s.Base, &s.Depth, &s.Seq); err != nil {
			return nil, err
		}
		out[domain.NodeRef{ID: domain.NodeID(id), Version: domain.Version(v)}] = s
	}
	return out, rows.Err()
}

// PutBaseline stores the header of a baseline (ADR 0143).
func (t *sqliteTx) PutBaseline(ctx context.Context, b domain.Baseline) error {
	_, err := t.tx.ExecContext(ctx, dialectSQLite.sqlInsert("baseline", baselineColumns, ""),
		string(b.ID), b.Name, nullUUID(string(b.ParentID)), nullUUID(string(b.MergedFrom)), nullUUID(string(b.ChangeID)), tsText(b.CreatedAt), domain.BranchOf(b.Branch), domain.NamespaceOf(b.Namespace), kindOf(b), nullUUID(string(b.RebasedFrom)), b.Seq)
	return sqliteErr(err, "baseline")
}

func (t *sqliteTx) PutChange(ctx context.Context, c domain.Change) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO change (id, title, intent, methodology, goal, status, baseline_id, result_baseline_id, data, created_at, branch, namespace, parent_id, owner_org, project_id, lifecycle, state, guardians, depends_on, methodology_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET title = excluded.title, intent = excluded.intent, goal = excluded.goal, status = excluded.status,
		  result_baseline_id = excluded.result_baseline_id, data = excluded.data, baseline_id = excluded.baseline_id, branch = excluded.branch,
		  lifecycle = excluded.lifecycle, state = excluded.state, project_id = excluded.project_id,
		  parent_id = excluded.parent_id, guardians = excluded.guardians, depends_on = excluded.depends_on,
		  methodology = excluded.methodology, methodology_version = excluded.methodology_version`,
		string(c.ID), c.Title, c.Intent, c.Methodology, c.Goal, string(c.Status), nullUUID(string(c.BaselineID)), nullUUID(string(c.ResultBaselineID)),
		string(jsonb(c.Data)), tsText(c.CreatedAt), domain.BranchOf(c.Branch), domain.NamespaceOf(c.Namespace), nullUUID(string(c.ParentID)), c.OwnerOrg, c.ProjectID, c.Lifecycle, c.State, string(guardiansJSON(c.Guardians)), string(dependsOnJSON(c.DependsOn)), c.MethodologyVersion)
	return sqliteErr(err, "change")
}

func (t *sqliteTx) AppendLog(ctx context.Context, e domain.LogEntry) (domain.LogEntry, error) {
	res, err := t.tx.ExecContext(ctx, dialectSQLite.sqlInsert("change_log", changeLogColumns, ""),
		e.ID, string(e.Change), e.Type, e.Flow, e.Process, e.Execution, e.Subject, e.By, e.Causation, tsText(e.At), string(e.Payload))
	if err != nil {
		return e, sqliteErr(err, "log entry "+e.Type)
	}
	e.Seq, err = res.LastInsertId()
	return e, err
}

func (t *sqliteTx) LogCounts(ctx context.Context, f domain.LogFilter) (map[string]int, error) {
	q, args := dialectSQLite.sqlLogCounts(f)
	rows, err := t.tx.QueryContext(ctx, q, args...)
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
	q, args := dialectSQLite.sqlLog(f)
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, sqliteErr(err, "log")
	}
	defer rows.Close()
	var out []domain.LogEntry
	for rows.Next() {
		var e domain.LogEntry
		var change, at, payload string
		if err := rows.Scan(&e.Seq, &e.ID, &change, &e.Type, &e.Flow, &e.Process, &e.Execution, &e.Subject, &e.By, &e.Causation, &at, &payload); err != nil {
			return nil, err
		}
		e.Change, e.At, e.Payload = domain.ChangeID(change), tsParse(at), json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (t *sqliteTx) OpenChangeIDs(ctx context.Context) ([]domain.ChangeID, error) {
	rows, err := t.tx.QueryContext(ctx, dialectSQLite.sqlOpenChangeIDs())
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
	_, err = t.tx.ExecContext(ctx, `INSERT INTO change_impact (id, seq, change_id, node_id, key, type, intent, rationale, pre_version, post_version, committed_version, review, reviews, via, recheck, produced_by, derived_from, items, execution, created_at, flow, superseded)
		VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM change_impact), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET node_id = excluded.node_id, key = excluded.key, type = excluded.type, intent = excluded.intent, rationale = excluded.rationale,
			pre_version = excluded.pre_version, post_version = excluded.post_version, committed_version = excluded.committed_version, review = excluded.review,
			reviews = excluded.reviews, via = excluded.via, recheck = excluded.recheck, produced_by = excluded.produced_by, derived_from = excluded.derived_from, items = excluded.items, execution = excluded.execution, flow = excluded.flow, superseded = excluded.superseded`,
		r.ID, string(change), r.NodeID, r.Key, r.Type, r.Intent, r.Rationale, r.Pre, r.Post, r.Committed, r.Review, string(r.Reviews), nullUUID(r.Via), r.Recheck, r.ProducedBy, string(r.DerivedFrom), string(r.Items), r.Execution, tsText(cn.CreatedAt), r.Flow, r.Superseded)
	return sqliteErr(err, "change impact "+cn.Key)
}

func (t *sqliteTx) ChangeImpactKeys(ctx context.Context, change domain.ChangeID, limit int) ([]domain.ChangeDocRef, error) {
	q, args := dialectSQLite.sqlChangeImpactKeys(change, limit)
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ChangeDocRef
	for rows.Next() {
		var r domain.ChangeDocRef
		if err := rows.Scan(&r.Key, &r.Type); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (t *sqliteTx) ChangeImpacts(ctx context.Context, change domain.ChangeID) ([]domain.ChangeImpact, error) {
	q, args := dialectSQLite.sqlChangeImpacts(change)
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ChangeImpact
	for rows.Next() {
		var r cnRow
		var reviews, derived, items, created string
		var node sql.NullString
		var pre, post, committed sql.NullInt64
		if err := rows.Scan(&r.ID, &node, &r.Key, &r.Type, &r.Intent, &r.Rationale, &pre, &post, &committed, &r.Review, &reviews, &r.Via, &r.Recheck, &r.ProducedBy, &derived, &items, &r.Execution, &created, &r.Flow, &r.Superseded); err != nil {
			return nil, err
		}
		if node.Valid {
			r.NodeID = &node.String
		}
		for _, x := range []struct {
			src sql.NullInt64
			dst **int
		}{{pre, &r.Pre}, {post, &r.Post}, {committed, &r.Committed}} {
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

func (t *sqliteTx) NodeChangePage(ctx context.Context, node domain.NodeID, before int64, limit int) ([]NodeChange, error) {
	q, args := dialectSQLite.sqlNodeChangePage(node, before, limit)
	rows, err := t.tx.QueryContext(ctx, q, args...)
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

func (t *sqliteTx) SetNodeOrigin(ctx context.Context, ref domain.NodeRef, change domain.ChangeID, cn domain.ChangeImpactID, comment string) error {
	res, err := t.tx.ExecContext(ctx, dialectSQLite.sqlSetNodeOrigin(), string(ref.ID), int(ref.Version), nullUUID(string(change)), nullUUID(string(cn)), comment)
	if err != nil {
		return sqliteErr(err, "node "+ref.String())
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	return nil
}

func (t *sqliteTx) JoinBranch(ctx context.Context, ref domain.NodeRef, branch string, change domain.ChangeID, committed int64) error {
	_, err := t.tx.ExecContext(ctx, dialectSQLite.sqlJoinBranch(), string(ref.ID), int(ref.Version), domain.BranchOf(branch), nullUUID(string(change)), committed)
	return sqliteErr(err, "node "+ref.String())
}

func (t *sqliteTx) PutTag(ctx context.Context, tag domain.Tag) error {
	_, err := t.tx.ExecContext(ctx, dialectSQLite.sqlPutTag(), string(tag.ID), tag.Name, domain.NamespaceOf(tag.Namespace), string(tag.Node), int(tag.Version), tag.By, tsText(tag.CreatedAt))
	return sqliteErr(err, "tag "+tag.Name)
}

func (t *sqliteTx) DeleteTag(ctx context.Context, id domain.TagID) error {
	q, args := dialectSQLite.sqlDeleteTag(id)
	res, err := t.tx.ExecContext(ctx, q, args...)
	if err != nil {
		return sqliteErr(err, "tag "+string(id))
	}
	if k, _ := res.RowsAffected(); k != 1 {
		return fmt.Errorf("tag %s: %w", id, ErrNotFound)
	}
	return nil
}

func (t *sqliteTx) Tags(ctx context.Context, f domain.TagFilter) ([]domain.Tag, error) {
	q, args := dialectSQLite.sqlTags(f)
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Tag
	for rows.Next() {
		var tag domain.Tag
		var created string
		if err := rows.Scan((*string)(&tag.ID), &tag.Name, &tag.Namespace, (*string)(&tag.Node), (*int)(&tag.Version), &tag.By, &created); err != nil {
			return nil, err
		}
		tag.CreatedAt = tsParse(created)
		out = append(out, tag)
	}
	return out, rows.Err()
}

func (t *sqliteTx) DeleteChange(ctx context.Context, id domain.ChangeID, namespace, branch string) error {
	d := dialectSQLite
	for _, q := range d.deleteChangeUsed() {
		var used int
		if err := t.tx.QueryRowContext(ctx, q, string(id)).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			return fmt.Errorf("change %s: what it wrote is used by the graph: %w", id, ErrConflict)
		}
	}
	rows, err := t.tx.QueryContext(ctx, d.sqlChangeNodes(), string(id))
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
		if _, err := t.tx.ExecContext(ctx, q, string(id)); err != nil {
			return sqliteErr(err, "change "+string(id))
		}
	}
	for _, n := range nodes {
		if _, err := t.tx.ExecContext(ctx, d.sqlDeleteOrphanNode(), n); err != nil {
			return sqliteErr(err, "node "+n)
		}
		if _, err := t.tx.ExecContext(ctx, d.sqlRefreshLatest(), n); err != nil {
			return sqliteErr(err, "node "+n)
		}
	}
	if branch != "" {
		if _, err := t.tx.ExecContext(ctx, d.sqlDeleteBranch(), namespace, branch); err != nil {
			return sqliteErr(err, "branch "+branch)
		}
	}
	res, err := t.tx.ExecContext(ctx, d.sqlDeleteChange(), string(id))
	if err != nil {
		return sqliteErr(err, "change "+string(id))
	}
	if k, _ := res.RowsAffected(); k != 1 {
		return fmt.Errorf("change %s: %w", id, ErrNotFound)
	}
	return nil
}

func (t *sqliteTx) LinksFrom(ctx context.Context, refs []domain.NodeRef) ([]domain.Link, error) {
	q, args := dialectSQLite.sqlLinksFrom(refs)
	return t.queryLinks(ctx, q, args...)
}

func (t *sqliteTx) LinksFromOfType(ctx context.Context, refs []domain.NodeRef, types []string) ([]domain.Link, error) {
	q, args := dialectSQLite.sqlLinksFromOfType(refs, types)
	return t.queryLinks(ctx, q, args...)
}

func (t *sqliteTx) LinksToOfType(ctx context.Context, ref domain.NodeRef, types []string) ([]domain.Link, error) {
	if types == nil {
		types = []string{}
	}
	q, args := dialectSQLite.sqlLinksToOfType(ref, types)
	return t.queryLinks(ctx, q, args...)
}

func (t *sqliteTx) LinkSources(ctx context.Context, id domain.NodeID) ([]domain.NodeID, error) {
	q, args := dialectSQLite.sqlLinkSources(id)
	ids, err := t.ids(ctx, q, args...)
	out := make([]domain.NodeID, len(ids))
	for i, id := range ids {
		out[i] = domain.NodeID(id)
	}
	return out, err
}

func (t *sqliteTx) LinksFromInto(ctx context.Context, refs []domain.NodeRef, to domain.NodeID) ([]domain.Link, error) {
	q, args := dialectSQLite.sqlLinksFromInto(refs, to)
	return t.queryLinks(ctx, q, args...)
}

func (t *sqliteTx) LinksTo(ctx context.Context, id domain.NodeID) ([]domain.Link, error) {
	q, args := dialectSQLite.sqlLinksTo(domain.NodeRef{ID: id})
	return t.queryLinks(ctx, q, args...)
}

func (t *sqliteTx) NodePage(ctx context.Context, refs []domain.NodeRef, q NodeQuery) (page NodePage, err error) {
	cq, cargs := dialectSQLite.sqlNodeTypeCounts(refs, q)
	counts, err := t.typeCounts(ctx, cq, cargs)
	if err != nil {
		return page, err
	}
	page.Types, page.Total = typeCounts(counts, q.Type)
	pq, pargs := dialectSQLite.sqlNodePage(refs, q)
	page.Nodes, err = t.nodes(ctx, pq, pargs...)
	return page, err
}

func (t *sqliteTx) LinkPage(ctx context.Context, refs []domain.NodeRef, q LinkQuery) (page LinkPage, err error) {
	cq, cargs := dialectSQLite.sqlLinkTypeCounts(refs, q)
	counts, err := t.typeCounts(ctx, cq, cargs)
	if err != nil {
		return page, err
	}
	page.Types, page.Total = typeCounts(counts, q.Type)
	pq, pargs := dialectSQLite.sqlLinkPage(refs, q)
	page.Links, err = t.queryLinks(ctx, pq, pargs...)
	return page, err
}

// typeCounts reads the (type, count) rows of a query.
func (t *sqliteTx) typeCounts(ctx context.Context, q string, args []any) (map[string]int, error) {
	rows, err := t.tx.QueryContext(ctx, q, args...)
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

func (t *sqliteTx) BaselinePage(ctx context.Context, namespace, branch string, before int64, limit int) ([]domain.Baseline, error) {
	q, args := dialectSQLite.sqlBaselinePage(namespace, branch, before, limit)
	return t.baselineHeaders(ctx, namespace, q, args)
}

func (t *sqliteTx) BaselineHeadersOf(ctx context.Context, namespace string, ids []domain.BaselineID) ([]domain.Baseline, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q, args := dialectSQLite.sqlBaselineHeadersOf(namespace, ids)
	return t.baselineHeaders(ctx, namespace, q, args)
}

func (t *sqliteTx) ChangeBaselineHeaders(ctx context.Context, namespace string, changes []domain.ChangeID) ([]domain.Baseline, error) {
	if len(changes) == 0 {
		return nil, nil
	}
	q, args := dialectSQLite.sqlChangeBaselineHeaders(namespace, changes)
	return t.baselineHeaders(ctx, namespace, q, args)
}

func (t *sqliteTx) BaselineCounts(ctx context.Context, namespace string) (map[string]int, error) {
	q, args := dialectSQLite.sqlBaselineCounts(namespace)
	return t.typeCounts(ctx, q, args)
}

func (t *sqliteTx) StoreCounts(ctx context.Context, namespace string) (map[string]map[string]int, error) {
	namespace = domain.NamespaceOf(namespace)
	out := map[string]map[string]int{}
	for measure, q := range dialectSQLite.sqlStoreCounts(namespace) {
		counts, err := t.typeCounts(ctx, q, []any{namespace})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", measure, err)
		}
		out[measure] = counts
	}
	return out, nil
}

func (t *sqliteTx) LastCommit(ctx context.Context, branch string) (int64, error) {
	q, args := dialectSQLite.sqlLastCommit(branch)
	var seq int64
	err := t.tx.QueryRowContext(ctx, q, args...).Scan(&seq)
	return seq, err
}
