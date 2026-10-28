package graph

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zimwip/goap/contracts/domain"
)

// impactColumns are the columns of a change_impact row, in the order of PutChangeImpact's arguments.
var impactColumns = []string{"id", "change_id", "node_id", "key", "type", "intent", "rationale", "pre_version", "post_version",
	"committed_version", "review", "reviews", "via", "recheck", "produced_by", "derived_from", "items", "execution", "created_at",
	"flow", "superseded"}

// impactRowsPerInsert is the number of change_impact rows one statement writes (22 arguments each).
const impactRowsPerInsert = 500

// heldQuerier holds the rows of change_impact a write transaction writes and sends them in one statement before its
// next statement but an entry of a log, which reads none of them, and before its commit: a batched operation writes
// an impact per node, one statement each otherwise. The last write of a row replaces the one held; each row takes the
// position of its first write, the sequence moving once per write, as when each write was its own statement.
type heldQuerier struct {
	Tx pgQuerier
	// pipe is the transaction the rows are written in: their inserts wait for the next statement (nil: sent at once)
	pipe    *pipeTx
	rows    []heldImpact
	upserts int
	// versions are the node versions held, in the order written, each with its link set once set
	versions []heldVersion
	// links are the link rows held (links and drops, ADR 0141), written after the versions they leave
	links []heldLink
	// joins are the versions held joining a branch (JoinBranch: node, version, branch, change, committed), written
	// after the versions
	joins [][]any
}

// heldLink is a link row held: the version it leaves and its arguments (linkColumns).
type heldLink struct {
	from domain.NodeRef
	args []any
}

// holdLink holds a link row.
func (h *heldQuerier) holdLink(from domain.NodeRef, args []any) {
	h.links = append(h.links, heldLink{from: from, args: args})
}

// heldVersion is a node version a write transaction writes: its node created (version 1) or moved to it, its row with
// how it holds its links (ADR 0141), all written in a few statements for the versions of a commit.
type heldVersion struct {
	n       domain.Node
	version []any
	set     [4]any
}

// holdVersion holds a version; refused (false) when its node is held at another version (one statement moves a
// node once).
func (h *heldQuerier) holdVersion(n domain.Node, args []any) bool {
	for _, v := range h.versions {
		if v.n.ID == n.ID {
			return false
		}
	}
	h.versions = append(h.versions, heldVersion{n: n, version: args, set: [4]any{nil, nil, 0, 0}})
	return true
}

// holdLinkSet sets how a held version holds its links; false when the version is not held.
func (h *heldQuerier) holdLinkSet(from domain.NodeRef, set [4]any) bool {
	for i := range h.versions {
		if h.versions[i].n.Ref() == from {
			h.versions[i].set = set
			return true
		}
	}
	return false
}

type heldImpact struct {
	args []any
	at   int
}

func (h *heldQuerier) hold(args []any) {
	at := h.upserts
	h.upserts++
	for i := range h.rows {
		if h.rows[i].args[0] == args[0] {
			h.rows[i].args = args
			return
		}
	}
	h.rows = append(h.rows, heldImpact{args: args, at: at})
}

// flush writes the rows held.
func (h *heldQuerier) flush(ctx context.Context) error {
	if err := h.flushVersions(ctx); err != nil {
		return err
	}
	if err := h.flushLinks(ctx); err != nil {
		return err
	}
	if err := h.flushJoins(ctx); err != nil {
		return err
	}
	if len(h.rows) == 0 {
		return nil
	}
	rows, upserts := h.rows, h.upserts
	h.rows, h.upserts = nil, 0
	columns := impactColumns
	var seqs []int64
	if upserts > len(rows) {
		r, err := h.Tx.Query(ctx, fmt.Sprintf(`SELECT nextval(pg_get_serial_sequence('change_impact', 'seq')) FROM generate_series(1, %d)`, upserts))
		if err != nil {
			return mapErr(err, "change impacts")
		}
		if seqs, err = pgx.CollectRows(r, pgx.RowTo[int64]); err != nil {
			return mapErr(err, "change impacts")
		}
		sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		columns = append(append([]string{}, impactColumns...), "seq")
	}
	set := make([]string, 0, len(impactColumns))
	for _, c := range impactColumns {
		if c != "id" && c != "change_id" && c != "created_at" {
			set = append(set, c+" = EXCLUDED."+c)
		}
	}
	for start := 0; start < len(rows); start += impactRowsPerInsert {
		chunk := rows[start:min(start+impactRowsPerInsert, len(rows))]
		args := make([]any, 0, len(chunk)*len(columns))
		for _, r := range chunk {
			args = append(args, r.args...)
			if seqs != nil {
				args = append(args, seqs[r.at])
			}
		}
		q := dialectPG.sqlInsertRows("change_impact", columns, len(chunk)) + ` ON CONFLICT (id) DO UPDATE SET ` + strings.Join(set, ", ")
		if err := h.write(ctx, q, args, "change impacts"); err != nil {
			return err
		}
	}
	return nil
}

// versionRowsPerInsert is the number of node versions one statement writes (22 arguments each).
const versionRowsPerInsert = 500

// flushVersions writes the versions held: the nodes created, the nodes moved to their next version (each checked:
// ErrConflict when another writer moved it first), then the versions with their link sets.
func (h *heldQuerier) flushVersions(ctx context.Context) error {
	if len(h.versions) == 0 {
		return nil
	}
	vs := h.versions
	h.versions = nil
	var created []heldVersion
	var movedIDs []string
	var movedTo []int32
	for _, v := range vs {
		if v.n.Version == 1 {
			created = append(created, v)
		} else {
			movedIDs, movedTo = append(movedIDs, string(v.n.ID)), append(movedTo, int32(v.n.Version))
		}
	}
	nodeColumns := []string{"id", "namespace", "key", "type", "latest", "project", "creator", "creator_unit"}
	for chunk := range slices.Chunk(created, versionRowsPerInsert) {
		args := make([]any, 0, len(chunk)*len(nodeColumns))
		for _, v := range chunk {
			n := v.n
			args = append(args, string(n.ID), domain.NamespaceOf(n.Namespace), n.Key, n.Type, 1, n.Project.String(), n.Creator.Subject, n.Creator.Unit.String())
		}
		what := "nodes"
		if len(chunk) == 1 {
			what = "node " + chunk[0].n.Key
		}
		if err := h.write(ctx, dialectPG.sqlInsertRows("node", nodeColumns, len(chunk)), args, what); err != nil {
			return err
		}
	}
	if len(movedIDs) > 0 {
		tag, err := h.Tx.Exec(ctx, `UPDATE node SET latest = u.v FROM unnest($1::uuid[], $2::int[]) AS u(id, v) WHERE node.id = u.id AND node.latest = u.v - 1`, movedIDs, movedTo)
		if err != nil {
			return mapErr(err, "nodes")
		}
		if int(tag.RowsAffected()) != len(movedIDs) {
			// the node another writer moved first: the one whose latest is not the version before
			for _, v := range vs {
				if v.n.Version == 1 {
					continue
				}
				var latest int
				if err := h.Tx.QueryRow(ctx, `SELECT latest FROM node WHERE id = $1`, string(v.n.ID)).Scan(&latest); err != nil || latest != int(v.n.Version) {
					return fmt.Errorf("node %s: not the next version: %w", v.n.Ref(), ErrConflict)
				}
			}
			return fmt.Errorf("nodes: not the next versions: %w", ErrConflict)
		}
	}
	columns := append(append([]string{}, nodeVersionColumns...), "links", "links_base", "links_depth", "links_seq")
	for chunk := range slices.Chunk(vs, versionRowsPerInsert) {
		args := make([]any, 0, len(chunk)*len(columns))
		for _, v := range chunk {
			args = append(append(args, v.version...), v.set[:]...)
		}
		what := "node versions"
		if len(chunk) == 1 {
			what = "node " + chunk[0].n.Ref().String()
		}
		if err := h.write(ctx, dialectPG.sqlInsertRows("node_version", columns, len(chunk)), args, what); err != nil {
			return err
		}
	}
	return nil
}

// write writes rows held: an insert answering nothing but its error, which waits for the next statement of the
// transaction, or its commit, when it has one.
func (h *heldQuerier) write(ctx context.Context, sql string, args []any, what string) error {
	if h.pipe != nil {
		h.pipe.queue(sql, args, what)
		return nil
	}
	_, err := h.Tx.Exec(ctx, sql, args...)
	return mapErr(err, what)
}

func (h *heldQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := h.flush(ctx); err != nil {
		return pgconn.CommandTag{}, err
	}
	return h.Tx.Exec(ctx, sql, args...)
}

func (h *heldQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := h.flush(ctx); err != nil {
		return nil, err
	}
	return h.Tx.Query(ctx, sql, args...)
}

func (h *heldQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := h.flush(ctx); err != nil {
		return errRow{err}
	}
	return h.Tx.QueryRow(ctx, sql, args...)
}

// errRow is a row whose statement was not run: its error.
type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// impactArgs are the arguments of a change_impact row, in the order of impactColumns.
func impactArgs(change domain.ChangeID, cn domain.ChangeImpact) ([]any, error) {
	r, err := toCNRow(cn)
	if err != nil {
		return nil, err
	}
	return []any{r.ID, string(change), r.NodeID, r.Key, r.Type, r.Intent, r.Rationale, r.Pre, r.Post, r.Committed, r.Review, r.Reviews,
		nullUUID(r.Via), r.Recheck, r.ProducedBy, r.DerivedFrom, r.Items, r.Execution, cn.CreatedAt, r.Flow, r.Superseded}, nil
}

// flushLinks writes the link rows held, several per statement.
func (h *heldQuerier) flushLinks(ctx context.Context) error {
	links := h.links
	h.links = nil
	for rows := range slices.Chunk(links, linkRowsPerInsert) {
		args := make([]any, 0, len(rows)*len(linkColumns))
		for _, l := range rows {
			args = append(args, l.args...)
		}
		if err := h.write(ctx, dialectPG.sqlInsertRows("link", linkColumns, len(rows)), args, "link"); err != nil {
			return err
		}
	}
	return nil
}

// flushJoins writes the joins held, several per statement: as JoinBranch, a version already on the branch or joined to
// it is left alone.
func (h *heldQuerier) flushJoins(ctx context.Context) error {
	joins := h.joins
	h.joins = nil
	for rows := range slices.Chunk(joins, joinRowsPerInsert) {
		args := make([]any, 0, len(rows)*5)
		vals := make([]string, len(rows))
		for i, j := range rows {
			args = append(args, j...)
			b := i * 5
			vals[i] = fmt.Sprintf("($%d::uuid, $%d::int, $%d::text, $%d::uuid, $%d::bigint)", b+1, b+2, b+3, b+4, b+5)
		}
		q := `INSERT INTO node_branch (node_id, version, branch, change_id, committed)
			SELECT v.node_id, v.version, j.branch, j.change_id, j.committed FROM (VALUES ` + strings.Join(vals, ", ") + `) AS j(node_id, version, branch, change_id, committed)
			JOIN node_version v ON v.node_id = j.node_id AND v.version = j.version AND v.branch <> j.branch
			ON CONFLICT DO NOTHING`
		if err := h.write(ctx, q, args, "node branch"); err != nil {
			return err
		}
	}
	return nil
}

// joinRowsPerInsert is the number of joins one statement writes (5 arguments each).
const joinRowsPerInsert = 1000

// joinsNode tells whether a join of one of the nodes is held.
func (h *heldQuerier) joinsNode(ids ...domain.NodeID) bool {
	for _, j := range h.joins {
		if slices.Contains(ids, domain.NodeID(j[0].(string))) {
			return true
		}
	}
	return false
}

// holdsNode tells whether a version of one of the nodes is held: a read of those nodes waits for them.
func (h *heldQuerier) holdsNode(ids []domain.NodeID) bool {
	for _, v := range h.versions {
		if slices.Contains(ids, v.n.ID) {
			return true
		}
	}
	for _, l := range h.links {
		if slices.Contains(ids, l.from.ID) {
			return true
		}
	}
	return h.joinsNode(ids...)
}

// holdsKey tells whether a node of the key is created by a version held.
func (h *heldQuerier) holdsKey(namespace, key string) bool {
	for _, v := range h.versions {
		if v.n.Version == 1 && v.n.Key == key && domain.NamespaceOf(v.n.Namespace) == domain.NamespaceOf(namespace) {
			return true
		}
	}
	return false
}

// onNodes is what a read of versions of the nodes runs on: a read of other nodes leaves the rows held for later (it
// reads none of them: node, node_version and link rows of other nodes, no change_impact row), so the versions of a
// commit are written together.
func (t *pgTx) onNodes(ids ...domain.NodeID) pgQuerier {
	if t.held == nil || t.held.holdsNode(ids) {
		return t.tx
	}
	return t.held.Tx
}

// onRefs is what a read of the versions refs runs on: a version held is the only one of its node the rows held
// hold, and its node's latest, so a read of another version of the node (one before it, which reaches only versions
// before it) leaves them for later too.
func (t *pgTx) onRefs(refs []domain.NodeRef) pgQuerier {
	if t.held == nil {
		return t.tx
	}
	for _, v := range t.held.versions {
		if slices.Contains(refs, v.n.Ref()) {
			return t.tx
		}
	}
	for _, l := range t.held.links {
		if slices.Contains(refs, l.from) {
			return t.tx
		}
	}
	// a join changes what a branch holds of its node, whatever the version read
	for _, j := range t.held.joins {
		if slices.ContainsFunc(refs, func(r domain.NodeRef) bool { return string(r.ID) == j[0].(string) }) {
			return t.tx
		}
	}
	return t.held.Tx
}

// unheld is what a read of no row a write holds runs on (the change and branch headers, a lock): it leaves them held.
func (t *pgTx) unheld() pgQuerier {
	if t.held == nil {
		return t.tx
	}
	return t.held.Tx
}
