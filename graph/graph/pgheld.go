package graph

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zimwip/goap/contracts/domain"
)

// heldQuerier holds the rows a write transaction writes (the versions of a commit and their links) and sends them in a few statements before its next statement that may read them, and before its commit.
type heldQuerier struct {
	Tx pgQuerier
	// pipe is the transaction the rows are written in: their inserts wait for the next statement (nil: sent at once)
	pipe *pipeTx
	// versions are the node versions held, in the order written, each with its link set (ADR 0141)
	versions []heldVersion
	// links are the link rows held (links and drops, ADR 0141), written after the versions they leave
	links []heldLink
	// the rows held indexed, so that holding or asking for one costs the same whatever the number held (a commit holds
	// a version per node): refs the versions written or left by a link, keys the namespace and key of the nodes
	// created, nodes the nodes of refs
	refs  map[domain.NodeRef]bool
	nodes map[domain.NodeID]bool
	keys  map[[2]string]bool
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

// heldVersion is a node version a write transaction writes: its node created (version 1), its row with how it holds
// its links (ADR 0141), all written in a few statements for the versions of a commit.
type heldVersion struct {
	n       domain.Node
	version []any
}

// holdVersion holds a version.
func (h *heldQuerier) holdVersion(n domain.Node, args []any) {
	if h.keys == nil {
		h.keys = map[[2]string]bool{}
	}
	h.versions = append(h.versions, heldVersion{n: n, version: args})
	h.noteRef(n.Ref())
	if n.Version == 1 {
		h.keys[[2]string{domain.NamespaceOf(n.Namespace), n.Key}] = true
	}
}

// flush writes the rows held.
func (h *heldQuerier) flush(ctx context.Context) error {
	if err := h.flushVersions(ctx); err != nil {
		return err
	}
	return h.flushLinks(ctx)
}

// versionRowsPerInsert is the number of node versions one statement writes (23 arguments each).
const versionRowsPerInsert = 500

// flushVersions writes the versions held: the nodes created, then the versions with their link sets. Nothing is
// updated (ADR 0174 addendum): two writers giving a node the same next version meet on the key of node_version
// (ErrConflict).
func (h *heldQuerier) flushVersions(ctx context.Context) error {
	if len(h.versions) == 0 {
		return nil
	}
	vs := h.versions
	h.versions, h.keys = nil, nil
	h.forgetRefs()
	var created []heldVersion
	for _, v := range vs {
		if v.n.Version == 1 {
			created = append(created, v)
		}
	}
	for chunk := range slices.Chunk(created, versionRowsPerInsert) {
		args := make([]any, 0, len(chunk)*len(nodeColumns))
		for _, v := range chunk {
			args = append(args, nodeArgs(v.n)...)
		}
		what := "nodes"
		if len(chunk) == 1 {
			what = "node " + chunk[0].n.Key
		}
		if err := h.write(ctx, dialectPG.sqlInsertRows("node", nodeColumns, len(chunk)), args, what); err != nil {
			return err
		}
	}
	for chunk := range slices.Chunk(vs, versionRowsPerInsert) {
		args := make([]any, 0, len(chunk)*len(nodeVersionColumns))
		for _, v := range chunk {
			args = append(args, v.version...)
		}
		what := "node versions"
		if len(chunk) == 1 {
			what = "node " + chunk[0].n.Ref().String()
		}
		if err := h.write(ctx, dialectPG.sqlInsertRows("node_version", nodeVersionColumns, len(chunk)), args, what); err != nil {
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

// holdsNode tells whether a version of one of the nodes is held: a read of those nodes waits for them.
func (h *heldQuerier) holdsNode(ids []domain.NodeID) bool {
	for _, id := range ids {
		if h.nodes[id] {
			return true
		}
	}
	return false
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
		if t.held.refs[r] {
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
