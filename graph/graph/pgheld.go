package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zimwip/goap/contracts/domain"
)

// heldQuerier holds the rows a write transaction writes (the versions of a commit, their links, joins and change
// impacts) and sends them in a few statements before its next statement that may read them, and before its commit.
type heldQuerier struct {
	Tx pgQuerier
	// pipe is the transaction the rows are written in: their inserts wait for the next statement (nil: sent at once)
	pipe *pipeTx
	// impacts are the change_impact rows held (ADR 0174), each statement's arguments with its number of rows
	impacts []heldRows
	// versions are the node versions held, in the order written, each with its link set once set
	versions []heldVersion
	// links are the link rows held (links and drops, ADR 0141), written after the versions they leave
	links []heldLink
	// joins are the versions held joining a branch (JoinBranch: node, version, branch, change, committed), written
	// after the versions
	joins [][]any
	// the rows held indexed, so that holding or asking for one costs the same whatever the number held (a commit holds
	// a version, a link set and an impact per node): verAt the position of a version by its node, refs the versions written or left by a link, joined the nodes of the joins, keys the namespace and
	// key of the nodes created, nodes the nodes of refs
	verAt  map[domain.NodeID]int
	refs   map[domain.NodeRef]bool
	nodes  map[domain.NodeID]bool
	joined map[domain.NodeID]bool
	keys   map[[2]string]bool
}

// heldLink is a link row held: the version it leaves and its arguments (linkColumns).
type heldLink struct {
	from domain.NodeRef
	args []any
}

// holdLink holds a link row.
func (h *heldQuerier) holdLink(from domain.NodeRef, args []any) {
	h.links = append(h.links, heldLink{from: from, args: args})
	h.noteRef(from)
}

func (h *heldQuerier) noteRef(r domain.NodeRef) {
	if h.refs == nil {
		h.refs, h.nodes = map[domain.NodeRef]bool{}, map[domain.NodeID]bool{}
	}
	h.refs[r], h.nodes[r.ID] = true, true
}

// forgetRefs indexes again the versions and links still held, once some were written.
func (h *heldQuerier) forgetRefs() {
	h.refs, h.nodes = nil, nil
	for _, v := range h.versions {
		h.noteRef(v.n.Ref())
	}
	for _, l := range h.links {
		h.noteRef(l.from)
	}
}

// holdJoin holds a join of a version to a branch (JoinBranch's arguments).
func (h *heldQuerier) holdJoin(args []any) {
	h.joins = append(h.joins, args)
	if h.joined == nil {
		h.joined = map[domain.NodeID]bool{}
	}
	h.joined[domain.NodeID(args[0].(string))] = true
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
	if _, ok := h.verAt[n.ID]; ok {
		return false
	}
	if h.verAt == nil {
		h.verAt, h.keys = map[domain.NodeID]int{}, map[[2]string]bool{}
	}
	h.verAt[n.ID] = len(h.versions)
	h.versions = append(h.versions, heldVersion{n: n, version: args, set: [4]any{nil, nil, 0, 0}})
	h.noteRef(n.Ref())
	if n.Version == 1 {
		h.keys[[2]string{domain.NamespaceOf(n.Namespace), n.Key}] = true
	}
	return true
}

// holdLinkSet sets how a held version holds its links; false when the version is not held.
func (h *heldQuerier) holdLinkSet(from domain.NodeRef, set [4]any) bool {
	i, ok := h.verAt[from.ID]
	if !ok || h.versions[i].n.Ref() != from {
		return false
	}
	h.versions[i].set = set
	return true
}

// heldRows are the arguments of a statement inserting n rows.
type heldRows struct {
	args []any
	n    int
}

// holdImpacts holds change_impact rows (commitImpactColumns), written after the versions they name.
func (h *heldQuerier) holdImpacts(args []any, n int) {
	h.impacts = append(h.impacts, heldRows{args: args, n: n})
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
	impacts := h.impacts
	h.impacts = nil
	for _, r := range impacts {
		if err := h.write(ctx, dialectPG.sqlInsertRows("change_impact", commitImpactColumns, r.n), r.args, "change impacts"); err != nil {
			return err
		}
	}
	return nil
}

// versionRowsPerInsert is the number of node versions one statement writes (23 arguments each).
const versionRowsPerInsert = 500

// flushVersions writes the versions held: the nodes created, the nodes moved to their next version (each checked:
// ErrConflict when another writer moved it first), then the versions with their link sets.
func (h *heldQuerier) flushVersions(ctx context.Context) error {
	if len(h.versions) == 0 {
		return nil
	}
	vs := h.versions
	h.versions, h.verAt, h.keys = nil, nil, nil
	h.forgetRefs()
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

// flushLinks writes the link rows held, several per statement.
func (h *heldQuerier) flushLinks(ctx context.Context) error {
	links := h.links
	h.links = nil
	h.forgetRefs()
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
	h.joins, h.joined = nil, nil
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
	for _, id := range ids {
		if h.joined[id] {
			return true
		}
	}
	return false
}

// holdsNode tells whether a version of one of the nodes is held: a read of those nodes waits for them.
func (h *heldQuerier) holdsNode(ids []domain.NodeID) bool {
	for _, id := range ids {
		if h.nodes[id] {
			return true
		}
	}
	return h.joinsNode(ids...)
}

// holdsKey tells whether a node of the key is created by a version held.
func (h *heldQuerier) holdsKey(namespace, key string) bool {
	return h.keys[[2]string{domain.NamespaceOf(namespace), key}]
}

// onNodes is what a read of versions of the nodes runs on: a read of other nodes leaves the rows held for later (it
// reads none of them: node, node_version and link rows of other nodes), so the versions of a
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
	for _, r := range refs {
		// a join changes what a branch holds of its node, whatever the version read
		if t.held.refs[r] || t.held.joined[r.ID] {
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
