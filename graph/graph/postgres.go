package graph

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/lnk"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
)

// Migrations holds the PostgreSQL schema of the graph service.
//
//go:embed migrations/*.sql
var Migrations embed.FS

// Postgres is a PostgreSQL Repo.
type Postgres struct {
	pool *pgxpool.Pool
	// read, when set, serves the transactions of a read-only context (platform.ReadOnly) on read-only connections
	// (platform.PostgresReader) with no transaction around them: no BEGIN and COMMIT round trips. They see what they
	// would see in a transaction: the graph's transactions run READ COMMITTED, each statement reading the last commit.
	read *pgxpool.Pool
	// readAll takes every context as read-only (the tests of the readers)
	readAll bool
	// roots are the roots of the trees of the changes last locked (LockChange)
	roots *changeRoots
}

// changeRoots keeps the root of the tree of each change last locked: a guess its lock checks once held (an attach or
// a detach moves it), so a lock reads the tree once instead of twice.
type changeRoots struct {
	mu    sync.Mutex
	roots map[domain.ChangeID]string
}

// maxChangeRoots is the number of change roots kept.
const maxChangeRoots = 4096

func (c *changeRoots) get(id domain.ChangeID) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.roots[id]
	return r, ok
}

func (c *changeRoots) keep(id domain.ChangeID, root string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.roots[id]; !ok && len(c.roots) >= maxChangeRoots {
		clear(c.roots)
	}
	c.roots[id] = root
}

// NewPostgres returns a repository on pool (schema already migrated).
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool, roots: &changeRoots{roots: map[domain.ChangeID]string{}}}
}

// WithReader gives the repository a pool of read-only connections (platform.PostgresReader) for the transactions of
// a read-only context.
func (p *Postgres) WithReader(read *pgxpool.Pool) *Postgres {
	p.read = read
	return p
}

// InTx implements Repo.
func (p *Postgres) InTx(ctx context.Context, fn func(tx Tx) error) error {
	if p.read != nil && (p.readAll || platform.IsReadOnly(ctx)) {
		err := p.readTx(ctx, fn)
		if !errors.Is(err, errWrites) {
			return err
		}
		slog.WarnContext(ctx, "graph: a read-only request writes; running its transaction on the writer")
	}
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	pipe := &pipeTx{conn: conn.Conn()}
	pipe.begin()
	defer func() {
		if pipe.begun {
			_ = pipe.rollback(ctx) // a panic, or an error before the commit
		}
	}()
	h := &heldQuerier{Tx: planned{pipe}, pipe: pipe}
	if err := fn(&pgTx{tx: h, held: h, roots: p.roots, pipe: pipe}); err != nil {
		return err
	}
	if err := h.flush(ctx); err != nil {
		return err
	}
	return pipe.commit(ctx)
}

// readTx runs fn on one read-only connection, outside any transaction; a write it tries ends it with errWrites, before
// anything is written (every statement of the connection is read-only).
func (p *Postgres) readTx(ctx context.Context, fn func(tx Tx) error) error {
	conn, err := p.read.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	err = fn(&pgTx{tx: planned{conn}, readOnly: true})
	if err != nil && platform.IsPostgresReadOnly(err) {
		return errWrites
	}
	return err
}

// planned runs the statements whose plan depends on the values given (customPlan) with a plan of their own each time:
// a prepared statement's generic plan does not know how many ids an array holds nor what a pattern matches, and the
// one PostgreSQL settles on after the first runs reads the tables whole for the next ones.
type planned struct{ pgQuerier }

func (q planned) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return q.pgQuerier.Exec(ctx, sql, customPlan(sql, args)...)
}

func (q planned) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return q.pgQuerier.Query(ctx, sql, customPlan(sql, args)...)
}

func (q planned) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return q.pgQuerier.QueryRow(ctx, sql, customPlan(sql, args)...)
}

// customPlan asks pgx for a plan made with the values of a statement that reads an array of ids or matches a pattern
// (an unnamed statement, planned at each run, its description cached); the others keep their prepared statement.
func customPlan(sql string, args []any) []any {
	if len(args) == 0 || !(strings.Contains(sql, "unnest(") || strings.Contains(sql, "ANY(") || strings.Contains(sql, " LIKE ")) {
		return args
	}
	if _, ok := args[0].(pgx.QueryExecMode); ok {
		return args
	}
	return append([]any{pgx.QueryExecModeCacheDescribe}, args...)
}

// pgQuerier is what a pgTx runs its statements on: a transaction, or a read-only connection (readTx).
type pgQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type pgTx struct {
	tx pgQuerier
	// held holds the change_impact rows of a write transaction (nil: a read)
	held *heldQuerier
	// readOnly is a read on a read-only connection (readTx): no transaction, so no lock to take
	readOnly bool
	// pipe is the transaction of a write: what queues the statements nothing waits on (nil: a read)
	pipe  *pipeTx
	roots *changeRoots
	// locked are the headers of the changes read with the root of their tree once its lock was held (LockChange): what
	// the transaction then reads of them, until it writes them
	locked map[domain.ChangeID]domain.Change
	// lockedReads are what the same statement read of each such change: its branch and the last position of its log
	lockedReads map[domain.ChangeID]lockedRead
}

// lockedRead is what the statement taking the header of a change once its lock is held reads with it (lockReader).
type lockedRead struct {
	branch    domain.Branch
	hasBranch bool
	// top is the last position of the log of the change, whatever the entry (0: no entry)
	top int64
	// next is a position of the log taken with them for the next entry of the change, so that its insert answers
	// nothing to wait for (0: none, or taken)
	next int64
}

// lockReads implements lockReader.
func (t *pgTx) lockReads(id domain.ChangeID) (domain.Branch, bool, int64, bool) {
	r, ok := t.lockedReads[id]
	return r.branch, r.hasBranch, r.top, ok
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

// statsJSON encodes the counters a change committed (domain.ChangeStats.Committed): a JSON object, "{}" when none.
func statsJSON(s domain.ChangeStats) []byte {
	b, _ := json.Marshal(s.Committed())
	return b
}

func statsOf(b []byte) (domain.ChangeStats, error) {
	var s domain.ChangeStats
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("counters of a change: %w", err)
	}
	return s, nil
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
	var owner, project, creatorUnit string
	var p []byte
	var version int
	var parents []int32
	var origins []byte
	if err := row.Scan(&id, &version, &n.Namespace, &n.Key, &n.Type, &p, &n.Deleted, &change, &n.CreatedAt, &n.Branch, &parents, &n.Reason, &n.State, &cnode, &n.Comment, &n.Execution, &owner, &project, &origins, &n.Creator.Subject, &creatorUnit, &n.Editor, &n.Visibility); err != nil {
		return n, err
	}
	n.Origins = originsOf(origins)
	for _, pv := range parents {
		n.Parents = append(n.Parents, domain.Version(pv))
	}
	n.ID, n.Version, n.Properties, n.ChangeID = domain.NodeID(id), domain.Version(version), props(p), domain.ChangeID(str(change))
	n.ChangeImpact = domain.ChangeImpactID(str(cnode))
	n.Responsible, n.Project, n.Creator.Unit = lnk.Of(owner), lnk.Of(project), lnk.Of(creatorUnit)
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
	n, err := scanNode(t.onRefs([]domain.NodeRef{ref}).QueryRow(ctx, q, args...))
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
	rows, err := t.onNodes(id).Query(ctx, q, args...)
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
	if t.readOnly {
		return errWrites // a lock announces a write: stop before reading what it checks
	}
	// the root last locked is a guess the read once the lock is held checks
	root, ok := t.roots.get(id)
	var err error
	if !ok {
		root, err = t.changeRoot(ctx, id)
	}
	for err == nil {
		if err = t.advisoryLock(ctx, "goap.change:"+root); err != nil {
			break
		}
		var again string
		if again, err = t.lockedHead(ctx, id); err == nil && again == root {
			t.roots.keep(id, root)
			return nil
		}
		root = again
	}
	return mapErr(err, "lock change "+string(id))
}

// lockedHead reads, once the lock of its tree is held, the root of the tree of a change, its header, the branch it
// names and the last position of its log in one statement: what the transaction reads of it next (ChangeHead, Branch,
// the fold of its log), which no other writer changes while the lock is held. A change not stored yet is its own root.
func (t *pgTx) lockedHead(ctx context.Context, id domain.ChangeID) (string, error) {
	var root, bNamespace, bName, bParent, bFork, bHead, bOrigin, bStatus, bDescription, bIntent, bRebased *string
	var top, next, bForkSeq *int64
	var bCreated *time.Time
	var bEphemeral *bool
	c, err := scanPGChangeHead(func(dest ...any) error {
		return t.unheld().QueryRow(ctx, `WITH RECURSIVE up (id, parent_id) AS (
				SELECT id, parent_id FROM change WHERE id = $1
				UNION ALL SELECT c.id, c.parent_id FROM change c JOIN up ON c.id = up.parent_id)
			SELECT (SELECT id::text FROM up WHERE parent_id IS NULL), `+pgChangeHead+`,
				(SELECT max(seq) FROM change_log WHERE change_id = $1), nextval(pg_get_serial_sequence('change_log', 'seq')),
				ob.b_namespace, ob.b_name, ob.b_parent, ob.b_fork, ob.b_head, ob.b_origin, ob.b_status, ob.b_created,
				ob.b_description, ob.b_intent, ob.b_rebased, ob.b_ephemeral, ob.b_fork_seq
			FROM change LEFT JOIN LATERAL (SELECT b.namespace AS b_namespace, b.name AS b_name, b.parent AS b_parent,
				coalesce(b.fork_commit::text, '') AS b_fork, coalesce(b.head_commit::text, '') AS b_head,
				b.origin AS b_origin, b.status AS b_status, b.created_at AS b_created, b.description AS b_description,
				b.intent AS b_intent, coalesce(b.rebased_from::text, '') AS b_rebased, b.ephemeral AS b_ephemeral,
				b.fork_seq AS b_fork_seq
				FROM branch b WHERE b.namespace = change.namespace AND b.name = change.branch) ob ON true
			WHERE change.id = $1`, string(id)).
			Scan(append(append([]any{&root}, dest...), &top, &next, &bNamespace, &bName, &bParent, &bFork, &bHead, &bOrigin, &bStatus, &bCreated, &bDescription, &bIntent, &bRebased, &bEphemeral, &bForkSeq)...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		delete(t.locked, id)
		delete(t.lockedReads, id)
		return string(id), nil
	}
	if err != nil {
		return "", err
	}
	if t.locked == nil {
		t.locked = map[domain.ChangeID]domain.Change{}
		t.lockedReads = map[domain.ChangeID]lockedRead{}
	}
	t.locked[id] = c
	r := lockedRead{}
	if top != nil {
		r.top = *top
	}
	if next != nil {
		r.next = *next
	}
	if bName != nil {
		r.hasBranch = true
		r.branch = domain.Branch{Namespace: *bNamespace, Name: *bName, Parent: *bParent, ForkCommit: domain.CommitID(*bFork), Head: domain.CommitID(*bHead), Origin: *bOrigin, Status: *bStatus, CreatedAt: *bCreated, Description: *bDescription, Intent: domain.OptionIntent(*bIntent), RebasedFrom: domain.CommitID(*bRebased), Ephemeral: *bEphemeral, ForkSeq: *bForkSeq}
	}
	t.lockedReads[id] = r
	return str(root), nil
}

// LockBranch implements Tx: a transaction-level advisory lock on the branch.
func (t *pgTx) LockBranch(ctx context.Context, namespace, name string) error {
	if t.readOnly {
		return errWrites
	}
	return mapErr(t.advisoryLock(ctx, "goap.branch:"+domain.NamespaceOf(namespace)+"/"+name), "lock branch "+name)
}

func (t *pgTx) advisoryLock(ctx context.Context, key string) error {
	const lock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
	if t.pipe != nil {
		// sent with the read that follows it: it reads what the lock holds
		t.pipe.queue(lock, []any{key}, "lock "+key)
		return nil
	}
	_, err := t.unheld().Exec(ctx, lock, key)
	return err
}

// changeRoot is the root change of the tree id belongs to, id itself when it is not stored yet.
func (t *pgTx) changeRoot(ctx context.Context, id domain.ChangeID) (string, error) {
	var root string
	err := t.unheld().QueryRow(ctx, `WITH RECURSIVE up (id, parent_id) AS (
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
	err := t.unheld().QueryRow(ctx, q, args...).
		Scan(&b.Namespace, &b.Name, &b.Parent, (*string)(&b.ForkCommit), (*string)(&b.Head), &b.Origin, &b.Status, &b.CreatedAt, &b.Description, (*string)(&b.Intent), (*string)(&b.RebasedFrom), &b.Ephemeral, &b.ForkSeq)
	return b, mapErr(err, "branch "+name)
}

func (t *pgTx) Branches(ctx context.Context, namespace string) ([]domain.Branch, error) {
	q, args := dialectPG.sqlBranches(namespace)
	rows, err := t.unheld().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Branch, error) {
		var b domain.Branch
		err := r.Scan(&b.Namespace, &b.Name, &b.Parent, (*string)(&b.ForkCommit), (*string)(&b.Head), &b.Origin, &b.Status, &b.CreatedAt, &b.Description, (*string)(&b.Intent), (*string)(&b.RebasedFrom), &b.Ephemeral, &b.ForkSeq)
		return b, err
	})
}

func (t *pgTx) PutBranch(ctx context.Context, b domain.Branch) error {
	namespace := domain.NamespaceOf(b.Namespace)
	_, err := t.unheld().Exec(ctx, `INSERT INTO branch (namespace, name, parent, fork_commit, head_commit, origin, status, created_at, description, intent, rebased_from, ephemeral, fork_seq) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (namespace, name) DO UPDATE SET status = EXCLUDED.status, head_commit = EXCLUDED.head_commit, description = EXCLUDED.description,
			fork_commit = EXCLUDED.fork_commit, rebased_from = EXCLUDED.rebased_from, parent = EXCLUDED.parent, fork_seq = EXCLUDED.fork_seq`,
		namespace, b.Name, b.Parent, nullUUID(string(b.ForkCommit)), nullUUID(string(b.Head)), b.Origin, b.Status, b.CreatedAt, b.Description, string(b.Intent), nullUUID(string(b.RebasedFrom)), b.Ephemeral, b.ForkSeq)
	return mapErr(err, "branch "+b.Name)
}

func (t *pgTx) NodeIDByKey(ctx context.Context, namespace, key string) (domain.NodeID, error) {
	var id string
	q, args := dialectPG.sqlNodeIDByKey(namespace, key)
	on := t.tx
	if t.held != nil && !t.held.holdsKey(namespace, key) {
		on = t.held.Tx
	}
	if err := on.QueryRow(ctx, q, args...).Scan(&id); err != nil {
		return "", mapErr(err, "node key "+key)
	}
	return domain.NodeID(id), nil
}

func (t *pgTx) NodeIDsByKey(ctx context.Context, namespace string, keys []string) (map[string]domain.NodeID, error) {
	out := make(map[string]domain.NodeID, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	q, args := dialectPG.sqlNodeIDsByKey(namespace, keys)
	on := pgQuerier(t.tx)
	if t.held != nil && !slices.ContainsFunc(keys, func(k string) bool { return t.held.holdsKey(namespace, k) }) {
		on = t.held.Tx
	}
	rows, err := on.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "node keys")
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

func (t *pgTx) NodeByKey(ctx context.Context, namespace, key string) (domain.Node, error) {
	var id string
	q, args := dialectPG.sqlNodeIDByKey(namespace, key)
	if err := t.tx.QueryRow(ctx, q, args...).Scan(&id); err != nil {
		return domain.Node{}, mapErr(err, "node key "+key)
	}
	return t.LatestOn(ctx, domain.NodeID(id), domain.MainBranch)
}

func (t *pgTx) NodesIn(ctx context.Context, commit domain.CommitID, nodeType string) ([]domain.Node, error) {
	return storedNodesIn(ctx, t, commit, nodeType)
}

func (t *pgTx) NodesAt(ctx context.Context, refs []domain.NodeRef, nodeType string) ([]domain.Node, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	q, args := dialectPG.sqlNodesAt(refs, nodeType)
	rows, err := t.onRefs(refs).Query(ctx, q, args...)
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

func (t *pgTx) queryLinks(ctx context.Context, q string, args ...any) ([]domain.Link, error) {
	return t.queryLinksOn(ctx, t.tx, q, args...)
}

func (t *pgTx) queryLinksOn(ctx context.Context, on pgQuerier, q string, args ...any) ([]domain.Link, error) {
	rows, err := on.Query(ctx, q, args...)
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
		var url string
		if err := rows.Scan(&id, &l.Type, &from, &fv, &to, &tv, &p, &change, &url); err != nil {
			return nil, err
		}
		l.ID, l.Properties, l.ChangeID = domain.LinkID(id), props(p), domain.ChangeID(str(change))
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

func (t *pgTx) OutLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	return t.LinksFrom(ctx, []domain.NodeRef{ref})
}

func (t *pgTx) LinksInto(ctx context.Context, refs []domain.NodeRef) ([]domain.Link, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	q, args := dialectPG.sqlLinksInto(refs)
	return t.queryLinks(ctx, q, args...)
}

func (t *pgTx) LatestVersionsOn(ctx context.Context, ids []domain.NodeID, branch string) (map[domain.NodeID]domain.Version, error) {
	out := make(map[domain.NodeID]domain.Version, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	q, args := dialectPG.sqlLatestVersionsOn(ids, branch)
	rows, err := t.onNodes(ids...).Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "nodes")
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

func (t *pgTx) LatestVersions(ctx context.Context, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	out := make(map[domain.NodeID]domain.Version, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	q, args := dialectPG.sqlLatestVersions(ids)
	rows, err := t.onNodes(ids...).Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "nodes")
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

func (t *pgTx) InLinks(ctx context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	q, args := dialectPG.sqlLinksTo(ref)
	return t.queryLinks(ctx, q, args...)
}

func (t *pgTx) Commit(ctx context.Context, id domain.CommitID) (domain.Commit, error) {
	b, err := t.CommitHeader(ctx, id)
	if err != nil {
		return b, err
	}
	b.Nodes, err = storedNodes(ctx, t, b)
	return b, err
}

func (t *pgTx) CommitHeader(ctx context.Context, id domain.CommitID) (domain.Commit, error) {
	var b domain.Commit
	q, args := dialectPG.sqlCommitHeader(id)
	err := t.unheld().QueryRow(ctx, q, args...).Scan((*string)(&b.ID), &b.Name, (*string)(&b.ParentID), (*string)(&b.MergedFrom), (*string)(&b.ChangeID), &b.CreatedAt,
		&b.Branch, &b.Namespace, &b.Kind, (*string)(&b.RebasedFrom), &b.Seq)
	return b, mapErr(err, "commit "+string(id))
}

func (t *pgTx) CommittedVersions(ctx context.Context, namespace, branch string, after, upTo int64, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	ls, err := t.commits(ctx, namespace, branch, after, upTo, ids)
	return committedVersions(ls), err
}

func (t *pgTx) BranchCommits(ctx context.Context, namespace, branch string, upTo int64, ids []domain.NodeID) (map[domain.NodeID]CommittedVersion, error) {
	return t.commits(ctx, namespace, branch, 0, upTo, ids)
}

// uuidIDs are the ids that can name a node here, whose node ids are uuids: an id that is none names no node, and
// leaving it out keeps the cast of the list from failing.
func uuidIDs(ids []domain.NodeID) []domain.NodeID {
	out := make([]domain.NodeID, 0, len(ids))
	for _, id := range ids {
		if uuid.Validate(string(id)) == nil {
			out = append(out, id)
		}
	}
	return out
}

func (t *pgTx) commits(ctx context.Context, namespace, branch string, after, upTo int64, ids []domain.NodeID) (map[domain.NodeID]CommittedVersion, error) {
	out := map[domain.NodeID]CommittedVersion{}
	if ids != nil {
		ids = uuidIDs(ids)
		if len(ids) == 0 {
			return out, nil
		}
	}
	q, args := dialectPG.sqlCommittedVersions(namespace, branch, after, upTo, ids)
	rows, err := t.tx.Query(ctx, q, args...)
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

// CommittedAtKey implements keySelector: sqlFoldVersions for the node of a key, found by its key in the same statement.
func (t *pgTx) CommittedAtKey(ctx context.Context, namespace, branch string, upTo int64, key string) (domain.NodeID, CommittedVersion, bool, error) {
	var nid string
	var v *int
	var deleted *bool
	var reason *string
	err := t.tx.QueryRow(ctx, `WITH x AS MATERIALIZED (
			SELECT n.id, (
				SELECT u.version FROM (
					(SELECT v.version, v.committed FROM node_version v
					WHERE v.node_id = n.id AND v.branch = (SELECT $1::text) AND v.committed + 0 > 0 AND v.committed + 0 <= $2 ORDER BY v.committed DESC, v.version DESC LIMIT 1)
					UNION ALL
					(SELECT j.version, j.committed FROM node_branch j
					WHERE j.node_id = n.id AND j.branch = (SELECT $1::text) AND j.committed + 0 > 0 AND j.committed + 0 <= $2 ORDER BY j.committed DESC, j.version DESC LIMIT 1)
				) u ORDER BY u.committed DESC, u.version DESC LIMIT 1
			) AS version
			FROM node n WHERE n.namespace = $3 AND n.key = $4
		)
		SELECT x.id::text, x.version, w.deleted, w.reason FROM x LEFT JOIN node_version w ON w.node_id = x.id AND w.version = x.version`,
		domain.BranchOf(branch), upTo, domain.NamespaceOf(namespace), key).Scan(&nid, &v, &deleted, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", CommittedVersion{}, false, nil
	}
	if err != nil || v == nil {
		return domain.NodeID(nid), CommittedVersion{}, false, mapErr(err, "node key "+key)
	}
	return domain.NodeID(nid), CommittedVersion{Version: domain.Version(*v), Deleted: *deleted, Revert: str(reason) == domain.ReasonRevert}, true, nil
}

// NextCommit allocates the greatest order stored or allocated plus one: two transactions commit on one branch from
// the same head may take the same, one of them fails to move the head (advanceBranch); a commit built on another is
// written once that one is committed, and sees it.
func (t *pgTx) NextCommit(ctx context.Context) (int64, error) {
	var seq int64
	err := t.unheld().QueryRow(ctx, `SELECT nextval('commit_seq')`).Scan(&seq)
	return seq, err
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

func (t *pgTx) NamespaceNodeIDs(ctx context.Context, namespace string) ([]domain.NodeID, error) {
	q, args := dialectPG.sqlNamespaceNodeIDs(namespace)
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

func (t *pgTx) NodeIDsOfType(ctx context.Context, namespace, typ string, after domain.NodeID, limit int) ([]domain.NodeID, error) {
	q, args := dialectPG.sqlNodeIDsOfType(namespace, typ, after, limit)
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

func (t *pgTx) CommitHeaders(ctx context.Context, namespace string) ([]domain.Commit, error) {
	q, args := dialectPG.sqlNamespaceCommitHeaders(namespace)
	return t.commitHeaders(ctx, namespace, q, args)
}

func (t *pgTx) BranchCommitHeaders(ctx context.Context, namespace, branch string) ([]domain.Commit, error) {
	q, args := dialectPG.sqlBranchCommitHeaders(namespace, branch)
	return t.commitHeaders(ctx, namespace, q, args)
}

func (t *pgTx) commitHeaders(ctx context.Context, namespace, q string, args []any) ([]domain.Commit, error) {
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Commit
	for rows.Next() {
		b := domain.Commit{Namespace: domain.NamespaceOf(namespace)}
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

func (t *pgTx) Commits(ctx context.Context, namespace string) ([]domain.Commit, error) {
	q, args := dialectPG.sqlCommitIDs(namespace)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := make([]domain.Commit, 0, len(ids))
	for _, id := range ids {
		b, err := t.Commit(ctx, domain.CommitID(id))
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// Change reads the header of a change: its impacts and items are the fold of its log, which the guard of the graph
// reads (ADR 0153, 0174).
func (t *pgTx) Change(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	return t.changeHeader(ctx, id)
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
const pgChangeHead = `id::text, title, intent, methodology, goal, status, COALESCE(fork_commit::text, ''), landed_commit::text, data, created_at, branch, namespace, COALESCE(parent_id::text, ''), owner_org, project_id, lifecycle, state, guardians, depends_on, methodology_version, stats`

func scanPGChangeHead(scan func(...any) error) (domain.Change, error) {
	var c domain.Change
	var result *string
	var data, guardians, dependsOn, stats []byte
	if err := scan((*string)(&c.ID), &c.Title, &c.Intent, &c.Methodology, &c.Goal, (*string)(&c.Status), (*string)(&c.ForkCommit), &result, &data, &c.CreatedAt, &c.Branch, &c.Namespace, (*string)(&c.ParentID), &c.OwnerOrg, &c.ProjectID, &c.Lifecycle, &c.State, &guardians, &dependsOn, &c.MethodologyVersion, &stats); err != nil {
		return c, err
	}
	c.LandedCommit, c.Data = domain.CommitID(str(result)), props(data)
	var err error
	if c.Guardians, err = guardiansOf(guardians); err != nil {
		return c, err
	}
	if c.DependsOn, err = dependsOnOf(dependsOn); err != nil {
		return c, err
	}
	c.Stats, err = statsOf(stats)
	return c, err
}

// ChangeHead implements Tx.
func (t *pgTx) ChangeHead(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	if c, ok := t.locked[id]; ok {
		c.Data, c.Guardians, c.DependsOn = maps.Clone(c.Data), slices.Clone(c.Guardians), slices.Clone(c.DependsOn)
		return c, nil
	}
	c, err := scanPGChangeHead(t.unheld().QueryRow(ctx, `SELECT `+pgChangeHead+` FROM change WHERE id = $1`, string(id)).Scan)
	if err != nil {
		return c, mapErr(err, "change "+string(id))
	}
	return c, nil
}

// ChangeHeads implements Tx.
func (t *pgTx) ChangeHeads(ctx context.Context, f ChangesFilter) ([]domain.Change, error) {
	q, args := dialectPG.sqlChangeHeads(pgChangeHead, f)
	rows, err := t.unheld().Query(ctx, q, args...)
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
	if t.held != nil {
		if t.held.holdVersion(n, versionArgs(n)) {
			return nil
		}
		if err := t.held.flush(ctx); err != nil {
			return err
		}
		t.held.holdVersion(n, versionArgs(n))
		return nil
	}
	if n.Version == 1 {
		if _, err := t.tx.Exec(ctx, `INSERT INTO node (id, namespace, key, type, latest, project, creator, creator_unit) VALUES ($1, $2, $3, $4, 1, $5, $6, $7)`, string(n.ID), domain.NamespaceOf(n.Namespace), n.Key, n.Type, n.Project.String(), n.Creator.Subject, n.Creator.Unit.String()); err != nil {
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
	_, err := t.tx.Exec(ctx, dialectPG.sqlInsert("node_version", nodeVersionColumns, ""), versionArgs(n)...)
	return mapErr(err, "node "+n.Ref().String())
}

// versionArgs are the arguments of a node_version row, in the order of nodeVersionColumns.
func versionArgs(n domain.Node) []any {
	parents := make([]int32, len(n.Parents))
	for i, pv := range n.Parents {
		parents[i] = int32(pv)
	}
	return []any{string(n.ID), int(n.Version), jsonb(n.Properties), n.Deleted, nullUUID(string(n.ChangeID)), n.CreatedAt, domain.BranchOf(n.Branch), parents, n.Reason, n.State, nullUUID(string(n.ChangeImpact)), n.Comment, n.Execution, n.Responsible.String(), originsJSON(n.Origins), n.Committed, n.Editor, n.Visibility}
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

func (t *pgTx) PutLinks(ctx context.Context, ls []domain.Link) error {
	if t.held != nil {
		for _, l := range ls {
			to, tv, url := linkTarget(l)
			t.held.holdLink(l.From, []any{string(l.ID), l.Type, string(l.From.ID), int(l.From.Version), to, tv, jsonb(l.Properties), nullUUID(string(l.ChangeID)), nil, url})
		}
		return nil
	}
	for rows := range slices.Chunk(ls, linkRowsPerInsert) {
		args := make([]any, 0, len(rows)*len(linkColumns))
		for _, l := range rows {
			to, tv, url := linkTarget(l)
			args = append(args, string(l.ID), l.Type, string(l.From.ID), int(l.From.Version), to, tv, jsonb(l.Properties), nullUUID(string(l.ChangeID)), nil, url)
		}
		if _, err := t.tx.Exec(ctx, dialectPG.sqlInsertRows("link", linkColumns, len(rows)), args...); err != nil {
			return mapErr(err, "link")
		}
	}
	return nil
}

func (t *pgTx) DropLink(ctx context.Context, from domain.NodeRef, row domain.LinkID, l domain.Link) error {
	to, tv, url := linkTarget(l)
	if t.held != nil {
		t.held.holdLink(from, []any{string(row), l.Type, string(from.ID), int(from.Version), to, tv, jsonb(l.Properties), nullUUID(string(l.ChangeID)), string(l.ID), url})
		return nil
	}
	_, err := t.tx.Exec(ctx, dialectPG.sqlInsert("link", linkColumns, ""),
		string(row), l.Type, string(from.ID), int(from.Version), to, tv, jsonb(l.Properties), nullUUID(string(l.ChangeID)), string(l.ID), url)
	return mapErr(err, "link")
}

func (t *pgTx) PutLinkSet(ctx context.Context, from domain.NodeRef, set LinkSet) error {
	var of, base any
	if set.Of != 0 && set.Of != from.Version {
		of = int(set.Of)
	}
	if set.Base != 0 {
		base = int(set.Base)
	}
	if t.held != nil && t.held.holdLinkSet(from, [4]any{of, base, set.Depth, set.Seq}) {
		return nil
	}
	_, err := t.tx.Exec(ctx, dialectPG.sqlPutLinkSet(), string(from.ID), int(from.Version), of, base, set.Depth, set.Seq)
	return mapErr(err, "link set")
}

func (t *pgTx) LinkSetOf(ctx context.Context, ref domain.NodeRef) (LinkSet, error) {
	var s LinkSet
	q, args := dialectPG.sqlLinkSetOf(ref)
	err := t.onRefs([]domain.NodeRef{ref}).QueryRow(ctx, q, args...).Scan(&s.Of, &s.Base, &s.Depth, &s.Seq)
	return s, mapErr(err, "link set of "+ref.String())
}

func (t *pgTx) LinkSetsOf(ctx context.Context, refs []domain.NodeRef) (map[domain.NodeRef]LinkSet, error) {
	out := make(map[domain.NodeRef]LinkSet, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	q, args := dialectPG.sqlLinkSetsOf(refs)
	rows, err := t.onRefs(refs).Query(ctx, q, args...)
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

// PutCommit stores the header of a commit (ADR 0143).
func (t *pgTx) PutCommit(ctx context.Context, b domain.Commit) error {
	_, err := t.tx.Exec(ctx, dialectPG.sqlInsert("commits", commitColumns, ""),
		string(b.ID), b.Name, nullUUID(string(b.ParentID)), nullUUID(string(b.MergedFrom)), nullUUID(string(b.ChangeID)), b.CreatedAt, domain.BranchOf(b.Branch), domain.NamespaceOf(b.Namespace), kindOf(b), nullUUID(string(b.RebasedFrom)), b.Seq)
	return mapErr(err, "commit")
}

func (t *pgTx) PutChange(ctx context.Context, c domain.Change) error {
	delete(t.locked, c.ID)
	// the header stored is the change's now, the transaction holding its lock (guardTx.PutChange): what it reads of it next
	h, err := scanPGChangeHead(t.unheld().QueryRow(ctx, `INSERT INTO change (id, title, intent, methodology, goal, status, fork_commit, landed_commit, data, created_at, branch, namespace, parent_id, owner_org, project_id, lifecycle, state, guardians, depends_on, methodology_version, stats)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		ON CONFLICT (id) DO UPDATE SET title = EXCLUDED.title, intent = EXCLUDED.intent, goal = EXCLUDED.goal, status = EXCLUDED.status,
		  landed_commit = EXCLUDED.landed_commit, data = EXCLUDED.data, fork_commit = EXCLUDED.fork_commit, branch = EXCLUDED.branch,
		  lifecycle = EXCLUDED.lifecycle, state = EXCLUDED.state, project_id = EXCLUDED.project_id,
		  parent_id = EXCLUDED.parent_id, guardians = EXCLUDED.guardians, depends_on = EXCLUDED.depends_on,
		  methodology = EXCLUDED.methodology, methodology_version = EXCLUDED.methodology_version, stats = EXCLUDED.stats
		RETURNING `+pgChangeHead,
		string(c.ID), c.Title, c.Intent, c.Methodology, c.Goal, string(c.Status), nullUUID(string(c.ForkCommit)), nullUUID(string(c.LandedCommit)), jsonb(c.Data), c.CreatedAt,
		domain.BranchOf(c.Branch), domain.NamespaceOf(c.Namespace), nullUUID(string(c.ParentID)), c.OwnerOrg, c.ProjectID, c.Lifecycle, c.State, guardiansJSON(c.Guardians), dependsOnJSON(c.DependsOn), c.MethodologyVersion, statsJSON(c.Stats)).Scan)
	if err != nil {
		return mapErr(err, "change")
	}
	if t.locked == nil {
		t.locked = map[domain.ChangeID]domain.Change{}
	}
	t.locked[c.ID] = h
	// the root of its tree, the guess the next lock of it checks: no read of it before that lock
	if _, ok := t.roots.get(c.ID); !ok {
		if c.ParentID == "" {
			t.roots.keep(c.ID, string(c.ID))
		} else if root, ok := t.roots.get(c.ParentID); ok {
			t.roots.keep(c.ID, root)
		}
	}
	return nil
}

func (t *pgTx) AppendLog(ctx context.Context, e domain.LogEntry) (domain.LogEntry, error) {
	payload := []byte(e.Payload)
	if len(payload) == 0 {
		payload = []byte("null") // an entry with no payload: the column holds JSON, never NULL
	}
	if seq := t.takeNext(e.Change); seq != 0 {
		// its position is known: the insert waits for the next statement, or the commit
		e.Seq = seq
		t.pipe.queue(dialectPG.sqlInsert("change_log", append([]string{"seq"}, changeLogColumns...), ""),
			[]any{seq, e.ID, string(e.Change), e.Type, e.Flow, e.Process, e.Execution, e.Subject, e.By, e.Causation, e.At, payload, string(e.Node)}, "log entry "+e.Type)
		return e, nil
	}
	q := t.tx
	if t.held != nil {
		q = t.held.Tx // an entry of a log reads no change_impact row: the rows held wait
	}
	err := q.QueryRow(ctx, dialectPG.sqlInsert("change_log", changeLogColumns, " RETURNING seq"),
		e.ID, string(e.Change), e.Type, e.Flow, e.Process, e.Execution, e.Subject, e.By, e.Causation, e.At, payload, string(e.Node)).Scan(&e.Seq)
	return e, mapErr(err, "log entry "+e.Type)
}

// takeNext is the position taken for the next entry of a change once its lock was held (lockedHead), 0 when there
// is none: a position is taken once, and only by the first entry the transaction appends after it, so the entries
// keep the order they were appended in.
func (t *pgTx) takeNext(id domain.ChangeID) int64 {
	if t.pipe == nil {
		return 0
	}
	r, ok := t.lockedReads[id]
	seq := r.next
	t.dropNext()
	if !ok || seq == 0 {
		return 0
	}
	return seq
}

// dropNext drops the positions taken: an entry appended takes a later one.
func (t *pgTx) dropNext() {
	for id, r := range t.lockedReads {
		if r.next != 0 {
			r.next = 0
			t.lockedReads[id] = r
		}
	}
}

// AppendLogs implements logBatcher: the entries in one statement, their positions in their order.
func (t *pgTx) AppendLogs(ctx context.Context, entries []domain.LogEntry) ([]domain.LogEntry, error) {
	if len(entries) == 1 {
		e, err := t.AppendLog(ctx, entries[0])
		return []domain.LogEntry{e}, err
	}
	t.dropNext() // the positions it takes come after any taken before
	n := len(entries)
	ids, changes, types, flows, procs, execs, subjects, bys, causes, payloads, nodes := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	ats := make([]time.Time, n)
	for i, e := range entries {
		ids[i], changes[i], types[i], flows[i], procs[i], execs[i], subjects[i], bys[i], causes[i], ats[i] = e.ID, string(e.Change), e.Type, e.Flow, e.Process, e.Execution, e.Subject, e.By, e.Causation, e.At
		payloads[i], nodes[i] = string(e.Payload), string(e.Node)
		if len(e.Payload) == 0 {
			payloads[i] = "null"
		}
	}
	q := t.tx
	if t.held != nil {
		q = t.held.Tx
	}
	// the rows are inserted, and take their positions, in the order of the array
	rows, err := q.Query(ctx, `INSERT INTO change_log (`+strings.Join(changeLogColumns, ", ")+`)
		SELECT id, change_id::uuid, type, flow, process_id, execution, subject, by_whom, causation, at, payload::jsonb, node
		FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[], $10::timestamptz[], $11::text[], $12::text[])
			WITH ORDINALITY AS e (id, change_id, type, flow, process_id, execution, subject, by_whom, causation, at, payload, node, o)
		ORDER BY o RETURNING id, seq`, ids, changes, types, flows, procs, execs, subjects, bys, causes, ats, payloads, nodes)
	if err != nil {
		return nil, mapErr(err, "log entries")
	}
	seqs := make(map[string]int64, n)
	for rows.Next() {
		var id string
		var seq int64
		if err := rows.Scan(&id, &seq); err != nil {
			rows.Close()
			return nil, err
		}
		seqs[id] = seq
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err, "log entries")
	}
	out := slices.Clone(entries)
	for i := range out {
		out[i].Seq = seqs[out[i].ID]
	}
	return out, nil
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

// PutCommitImpacts implements Tx: held with the versions of the commit when the transaction holds its writes.
func (t *pgTx) PutCommitImpacts(ctx context.Context, rows []CommitImpact) error {
	for chunk := range slices.Chunk(rows, commitImpactRowsPerInsert) {
		args := make([]any, 0, len(chunk)*len(commitImpactColumns))
		for _, r := range chunk {
			a, err := commitImpactArgs(r)
			if err != nil {
				return err
			}
			args = append(append(args, a...), r.CreatedAt)
		}
		if t.held != nil {
			t.held.holdImpacts(args, len(chunk))
			continue
		}
		if _, err := t.tx.Exec(ctx, dialectPG.sqlInsertRows("change_impact", commitImpactColumns, len(chunk)), args...); err != nil {
			return mapErr(err, "change impacts")
		}
	}
	return nil
}

// commitImpactRowsPerInsert is the number of change_impact rows one statement writes (18 arguments each).
const commitImpactRowsPerInsert = 1000

func (t *pgTx) CommitImpacts(ctx context.Context, change domain.ChangeID) ([]CommitImpact, error) {
	q, args := dialectPG.sqlCommitImpacts(change)
	rows, err := t.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err, "change impacts")
	}
	defer rows.Close()
	var out []CommitImpact
	for rows.Next() {
		var r commitImpactRow
		var at time.Time
		if err := rows.Scan(append(r.dest(), &at)...); err != nil {
			return nil, err
		}
		ci, err := r.impactOf()
		if err != nil {
			return nil, err
		}
		ci.CreatedAt = at
		out = append(out, ci)
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

func (t *pgTx) JoinBranch(ctx context.Context, ref domain.NodeRef, branch string, change domain.ChangeID, committed int64) error {
	if t.held != nil {
		t.held.holdJoin([]any{string(ref.ID), int(ref.Version), domain.BranchOf(branch), nullUUID(string(change)), committed})
		return nil
	}
	_, err := t.tx.Exec(ctx, dialectPG.sqlJoinBranch(), string(ref.ID), int(ref.Version), domain.BranchOf(branch), nullUUID(string(change)), committed)
	return mapErr(err, "node "+ref.String())
}

func (t *pgTx) PutTag(ctx context.Context, tag domain.Tag) error {
	_, err := t.tx.Exec(ctx, dialectPG.sqlPutTag(), string(tag.ID), tag.Name, domain.NamespaceOf(tag.Namespace), string(tag.Node), int(tag.Version), tag.By, tag.CreatedAt)
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
		if err := rows.Scan((*string)(&tag.ID), &tag.Name, &tag.Namespace, (*string)(&tag.Node), (*int)(&tag.Version), &tag.By, &tag.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

func (t *pgTx) DeleteChange(ctx context.Context, id domain.ChangeID, namespace, branch string) error {
	delete(t.locked, id)
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
	return t.queryLinksOn(ctx, t.onRefs(refs), q, args...)
}

func (t *pgTx) LinksFromOfType(ctx context.Context, refs []domain.NodeRef, types []string) ([]domain.Link, error) {
	q, args := dialectPG.sqlLinksFromOfType(refs, types)
	return t.queryLinks(ctx, q, args...)
}

func (t *pgTx) LinksToOfType(ctx context.Context, ref domain.NodeRef, types []string) ([]domain.Link, error) {
	if types == nil {
		types = []string{}
	}
	q, args := dialectPG.sqlLinksToOfType(ref, types)
	return t.queryLinks(ctx, q, args...)
}

func (t *pgTx) LinkSources(ctx context.Context, id domain.NodeID) ([]domain.NodeID, error) {
	q, args := dialectPG.sqlLinkSources(id)
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

func (t *pgTx) LinksFromInto(ctx context.Context, refs []domain.NodeRef, to domain.NodeID) ([]domain.Link, error) {
	q, args := dialectPG.sqlLinksFromInto(refs, to)
	return t.queryLinks(ctx, q, args...)
}

func (t *pgTx) LinksTo(ctx context.Context, id domain.NodeID) ([]domain.Link, error) {
	q, args := dialectPG.sqlLinksTo(domain.NodeRef{ID: id})
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

func (t *pgTx) CommitPage(ctx context.Context, namespace, branch string, before int64, limit int) ([]domain.Commit, error) {
	q, args := dialectPG.sqlCommitPage(namespace, branch, before, limit)
	return t.commitHeaders(ctx, namespace, q, args)
}

func (t *pgTx) CommitHeadersOf(ctx context.Context, namespace string, ids []domain.CommitID) ([]domain.Commit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q, args := dialectPG.sqlCommitHeadersOf(namespace, ids)
	return t.commitHeaders(ctx, namespace, q, args)
}

func (t *pgTx) ChangeCommitHeaders(ctx context.Context, namespace string, changes []domain.ChangeID) ([]domain.Commit, error) {
	if len(changes) == 0 {
		return nil, nil
	}
	q, args := dialectPG.sqlChangeCommitHeaders(namespace, changes)
	return t.commitHeaders(ctx, namespace, q, args)
}

func (t *pgTx) CommitCounts(ctx context.Context, namespace string) (map[string]int, error) {
	q, args := dialectPG.sqlCommitCounts(namespace)
	return t.typeCounts(ctx, q, args)
}

func (t *pgTx) StoreCounts(ctx context.Context, namespace string) (map[string]map[string]int, error) {
	namespace = domain.NamespaceOf(namespace)
	out := map[string]map[string]int{}
	for measure, q := range dialectPG.sqlStoreCounts(namespace) {
		counts, err := t.typeCounts(ctx, q, []any{namespace})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", measure, err)
		}
		out[measure] = counts
	}
	return out, nil
}

func (t *pgTx) LastCommit(ctx context.Context, branch string) (int64, error) {
	q, args := dialectPG.sqlLastCommit(branch)
	var seq int64
	err := t.tx.QueryRow(ctx, q, args...).Scan(&seq)
	return seq, err
}
