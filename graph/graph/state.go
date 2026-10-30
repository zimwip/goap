package graph

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/zimwip/goap/contracts/domain"
)

// The state of a namespace at a commit is a selection (ADR 0143): a version is complete (its properties, owner, state
// and link set, ADR 0141) and commits on a branch at a point of one global order (Node.Committed, Commit.Seq), so the
// version of a node at a commit is the last one that committed on the commit's branch up to its order, else, past the
// fork of the branch, the one selected on the branch it was forked from at the fork. Nothing is folded and no commit
// stores entries: a commit is a header, and reading the state of some nodes is one indexed read per branch of the
// chain of forks (CommittedVersions), whatever the length of the branches.

// The headers of commits are kept (Graph.headers, a lib/cache): a header never changes once written. The states are
// not: a read of some nodes is one indexed selection, cheaper than keeping whole states for the rare whole reads.

// header reads the header of a commit, cached once the transaction that wrote it is over.
func (t *guardTx) header(ctx context.Context, id domain.CommitID) (domain.Commit, error) {
	if b, ok := t.g.headers.Get(id); ok {
		return b, nil
	}
	b, err := t.Tx.CommitHeader(ctx, id)
	if err == nil && !t.newCommits[id] {
		t.g.headers.Put(b.ID, b)
	}
	return b, err
}

// stateAt reads the versions of some nodes (nil: every node) in a commit (ADR 0143), the nodes it does not hold left
// out: the selection on its branch up to its order, then, for the nodes not decided, in the commit it was written over
// (its fork), and so on.
func (t *guardTx) stateAt(ctx context.Context, id domain.CommitID, ids []domain.NodeID) (_ map[domain.NodeID]domain.Version, err error) {
	read := stateRead{start: time.Now(), whole: ids == nil, source: stateFromCommits}
	defer func() {
		if err == nil {
			read.record(ctx)
		}
	}()
	if id == "" {
		return map[domain.NodeID]domain.Version{}, nil
	}
	b, err := t.header(ctx, id)
	if err != nil {
		return nil, err
	}
	return selectState(ctx, t.Tx, b, ids, t.header, &read)
}

// branchFork returns the commit a branch is forked from now: none for main or a branch the store does not know.
func branchFork(ctx context.Context, tx Tx, namespace, branch string) (domain.CommitID, error) {
	if domain.BranchOf(branch) == domain.MainBranch {
		return "", nil
	}
	br, err := tx.Branch(ctx, domain.NamespaceOf(namespace), domain.BranchOf(branch))
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	return br.ForkCommit, err
}

// selectState is the selection of ADR 0143 from the header of a commit: for each node (nil ids: every node), its last
// commit on the branch up to the order of the commit, then the nodes not decided in the commit it was written over
// (Commit.Fork, ADR 0175); a version deleting its node leaves it out, a revert of the branch of a change (ADR 0140)
// leaves it to the fork. A whole state reads the commits of the branches forked off
// as they are (their history is theirs alone, short), and the branch at the root of the chain, whose history is the
// namespace's, as its node list then one selection per node, in batches (§4): its cost is the number of nodes, never
// the length of the history.
func selectState(ctx context.Context, tx Tx, b domain.Commit, ids []domain.NodeID,
	header func(context.Context, domain.CommitID) (domain.Commit, error), read *stateRead) (map[domain.NodeID]domain.Version, error) {
	decided := map[domain.NodeID]domain.Version{} // 0: the commit does not hold the node
	var pending map[domain.NodeID]bool            // nil: every node
	if ids != nil {
		pending = make(map[domain.NodeID]bool, len(ids))
		for _, n := range ids {
			pending[n] = true
		}
	}
	for {
		next := b.Fork
		if pending == nil && next == "" {
			// the root of the chain: the nodes of the namespace not decided yet, selected one by one
			all, err := tx.NamespaceNodeIDs(ctx, b.Namespace)
			if err != nil {
				return nil, err
			}
			pending = make(map[domain.NodeID]bool, len(all))
			for _, n := range all {
				if _, ok := decided[n]; !ok {
					pending[n] = true
				}
			}
		}
		if pending != nil && len(pending) == 0 {
			break
		}
		var want []domain.NodeID
		if pending != nil {
			want = slices.Sorted(maps.Keys(pending))
		}
		for start := 0; start == 0 || start < len(want); start += selectBatch {
			batch := want
			if want != nil {
				batch = want[start:min(start+selectBatch, len(want))]
			}
			got, err := tx.BranchCommits(ctx, b.Namespace, b.Branch, b.Seq, batch)
			if err != nil {
				return nil, err
			}
			if read != nil {
				read.chain++
				read.committed += len(got)
			}
			for n, l := range got {
				if l.Revert {
					continue // the fork decides it
				}
				v := l.Version
				if l.Deleted {
					v = 0
				}
				if pending == nil {
					if _, ok := decided[n]; !ok {
						decided[n] = v
					}
				} else if pending[n] {
					decided[n] = v
					delete(pending, n)
				}
			}
			if want == nil {
				break
			}
		}
		if next == "" {
			break
		}
		nb, err := header(ctx, next)
		if err != nil {
			return nil, err
		}
		b = nb
	}
	out := make(map[domain.NodeID]domain.Version, len(decided))
	for n, v := range decided {
		if v > 0 {
			out[n] = v
		}
	}
	return out, nil
}

// keySelector is a store reading the node of a key and its last commit on a branch up to an order in one statement
// (the PostgreSQL one): the node "" when no node has the key, decided false when it committed nothing in the range.
type keySelector interface {
	CommittedAtKey(ctx context.Context, namespace, branch string, upTo int64, key string) (id domain.NodeID, v CommittedVersion, decided bool, err error)
}

// selectKey reads the node of a key and its version at a commit in one statement, when the store reads them so and
// the commit's branch is forked from none (main: its commits decide every node): decided then, version 0 for a node
// the commit does not hold, the node "" for a key no node has.
func selectKey(ctx context.Context, tx Tx, at domain.CommitID, key string) (domain.NodeID, domain.Version, bool, error) {
	gt, ok := unwrapGuard(tx)
	if !ok || at == "" || gt.newCommits[at] {
		return "", 0, false, nil
	}
	ks, ok := gt.Tx.(keySelector)
	if !ok {
		return "", 0, false, nil
	}
	b, err := gt.header(ctx, at)
	if err != nil {
		return "", 0, false, err
	}
	if b.Fork != "" {
		return "", 0, false, nil
	}
	// the head kept in memory holds it (heads.go), else the store answers
	id, v, kept := gt.g.keptSelection(b, key)
	if !kept {
		var cv CommittedVersion
		var decided bool
		if id, cv, decided, err = ks.CommittedAtKey(ctx, b.Namespace, b.Branch, b.Seq, key); err != nil || id == "" {
			return "", 0, err == nil, err
		}
		v = cv.Version
		if !decided || cv.Deleted {
			v = 0
		}
		gt.g.keepSelection(b, key, id, v)
	}
	// what CommitVersions answers of it next
	if gt.commitReads == nil {
		gt.commitReads = map[domain.CommitID]map[domain.NodeID]domain.Version{}
	}
	if gt.commitReads[at] == nil {
		gt.commitReads[at] = map[domain.NodeID]domain.Version{}
	}
	gt.commitReads[at][id] = v
	return id, v, true, nil
}

// selectBatch is the number of nodes one statement selects the version of.
const selectBatch = 2000

// nodesOf returns the whole state of a commit.
func (t *guardTx) nodesOf(ctx context.Context, id domain.CommitID) (map[domain.NodeID]domain.Version, error) {
	return t.stateAt(ctx, id, nil)
}

// Commit reads a commit with its state; the empty id is the empty state, what the first change of a namespace
// starts from (ADR 0056): no commit holds it, it is nothing.
func (t *guardTx) Commit(ctx context.Context, id domain.CommitID) (domain.Commit, error) {
	if id == "" {
		return domain.Commit{Nodes: map[domain.NodeID]domain.Version{}}, nil
	}
	b, err := t.header(ctx, id)
	if err != nil {
		return b, err
	}
	b.Nodes, err = t.nodesOf(ctx, id)
	return b, err
}

// Commits lists the commits of a namespace with their state, oldest first.
func (t *guardTx) Commits(ctx context.Context, namespace string) ([]domain.Commit, error) {
	hs, err := t.Tx.CommitHeaders(ctx, namespace)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Commit, 0, len(hs))
	for _, h := range hs {
		b, err := t.Commit(ctx, h.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// CommitVersions reads the versions some nodes have in a commit (ADR 0137, 0140): what it costs follows the nodes
// asked and the chain of the commit, not the size of the state.
func (t *guardTx) CommitVersions(ctx context.Context, id domain.CommitID, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	if id == "" || len(ids) == 0 {
		return map[domain.NodeID]domain.Version{}, nil
	}
	if t.newCommits[id] {
		return t.stateAt(ctx, id, ids)
	}
	// a commit never changes: what the transaction read of it is not read again (a merge reads the fork and the
	// branch of the change several times, for its rebase, its guardian and its fast-forward)
	known := t.commitReads[id]
	var missing []domain.NodeID
	for _, n := range ids {
		if _, ok := known[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		got, err := t.stateAt(ctx, id, missing)
		if err != nil {
			return nil, err
		}
		if known == nil {
			if t.commitReads == nil {
				t.commitReads = map[domain.CommitID]map[domain.NodeID]domain.Version{}
			}
			known = map[domain.NodeID]domain.Version{}
			t.commitReads[id] = known
		}
		for _, n := range missing {
			known[n] = got[n] // 0: the commit does not hold it
		}
	}
	out := make(map[domain.NodeID]domain.Version, len(ids))
	for _, n := range ids {
		if v := known[n]; v > 0 {
			out[n] = v
		}
	}
	return out, nil
}

// NodesIn lists the nodes of a commit of a type, by key.
func (t *guardTx) NodesIn(ctx context.Context, id domain.CommitID, nodeType string) ([]domain.Node, error) {
	if id == "" {
		return nil, nil
	}
	nodes, err := t.nodesOf(ctx, id)
	if err != nil {
		return nil, err
	}
	return t.Tx.NodesAt(ctx, refsOf(nodes), nodeType)
}

// refsOf lists the versions of a state, by node.
func refsOf(nodes map[domain.NodeID]domain.Version) []domain.NodeRef {
	refs := make([]domain.NodeRef, 0, len(nodes))
	for _, n := range slices.Sorted(maps.Keys(nodes)) {
		refs = append(refs, domain.NodeRef{ID: n, Version: nodes[n]})
	}
	return refs
}

// commitOn returns the order of the commit the transaction is commit versions in on a branch, allocated by the
// first version, join or commit of that branch: the versions of a commit and its commit share it (ADR 0140). The
// branch holds that commit from its order (branch_commit, ADR 0176), marked when it is allocated, so the versions the
// transaction writes are on the branch at once.
func (t *guardTx) commitOn(ctx context.Context, branch string, change domain.ChangeID) (int64, error) {
	branch = domain.BranchOf(branch)
	if s, ok := t.commit[branch]; ok {
		return s, nil
	}
	s, err := t.Tx.NextCommit(ctx)
	if err != nil {
		return 0, err
	}
	// the commit of the branch's own: the entry committing its versions when one is reserved (a commit of the change)
	var logSeq int64
	if t.reserved.change == change {
		logSeq = t.reserved.seq
	}
	t.positions = nil
	if err := t.Tx.JoinCommits(ctx, branch, change, s, logSeq, []int64{s}); err != nil {
		return 0, err
	}
	if t.commit == nil {
		t.commit = map[string]int64{}
	}
	t.commit[branch] = s
	return s, nil
}

// committed gives a commit about to be written the order of its commit, which ends.
func (t *guardTx) committed(ctx context.Context, b *domain.Commit) error {
	if err := t.needChange(b.ChangeID, "commit "+b.Name); err != nil {
		return err
	}
	seq, err := t.commitOn(ctx, b.Branch, b.ChangeID)
	if err != nil {
		return err
	}
	delete(t.commit, domain.BranchOf(b.Branch))
	if b.Fork == "" {
		// the commit the branch stands on now (ADR 0175): a rebase later moves the branch, never this commit
		if b.Fork, err = branchFork(ctx, t, b.Namespace, b.Branch); err != nil {
			return err
		}
	}
	b.Seq, b.Nodes = seq, maps.Clone(b.Nodes)
	t.markNew(b.ID)
	return nil
}

// putCommitOver writes a commit whose state is the state of over with delta put in: a header (ADR 0143), the
// versions of its commit selecting its state.
func (t *guardTx) putCommitOver(ctx context.Context, b domain.Commit, _ domain.CommitID, _ map[domain.NodeID]domain.Version) error {
	return t.PutCommit(ctx, b)
}

// PutCommit writes the header of a commit (ADR 0143): its state is the selection at its order, the versions of its
// commit taking it.
func (t *guardTx) PutCommit(ctx context.Context, b domain.Commit) error {
	if err := t.committed(ctx, &b); err != nil {
		return err
	}
	b.Nodes = nil
	if err := t.Tx.PutCommit(ctx, b); err != nil {
		return err
	}
	if t.wroteHeads == nil {
		t.wroteHeads = map[domain.CommitID]domain.Commit{}
	}
	t.wroteHeads[b.ID] = b
	return nil
}

func (t *guardTx) markNew(id domain.CommitID) {
	if t.newCommits == nil {
		t.newCommits = map[domain.CommitID]bool{}
	}
	t.newCommits[id] = true
}

// applyMoves puts in a state what a commit did to it (moves: a version, 0 for a node it removed).
func applyMoves(state, mv map[domain.NodeID]domain.Version) {
	for id, v := range mv {
		if v == 0 {
			delete(state, id)
		} else {
			state[id] = v
		}
	}
}

// putCommitOver writes a commit over another (guardTx.putCommitOver): a header.
func putCommitOver(ctx context.Context, tx Tx, b domain.Commit, over domain.CommitID, delta map[domain.NodeID]domain.Version) error {
	if p, ok := tx.(interface {
		putCommitOver(context.Context, domain.Commit, domain.CommitID, map[domain.NodeID]domain.Version) error
	}); ok {
		return p.putCommitOver(ctx, b, over, delta)
	}
	return tx.PutCommit(ctx, b)
}

// unwrapGuard returns the guard of a transaction of the graph.
func unwrapGuard(tx Tx) (*guardTx, bool) {
	if ot, ok := tx.(*observedTx); ok {
		tx = ot.Tx
	}
	gt, ok := tx.(*guardTx)
	return gt, ok
}

// storedNodes is the state of a commit read by a store without the graph: the selection (ADR 0143).
func storedNodes(ctx context.Context, tx Tx, b domain.Commit) (map[domain.NodeID]domain.Version, error) {
	return selectState(ctx, tx, b, nil, tx.CommitHeader, nil)
}

// storedNodesIn lists the nodes of a commit read by a store (storedNodes), of a type, by key.
func storedNodesIn(ctx context.Context, tx Tx, id domain.CommitID, nodeType string) ([]domain.Node, error) {
	if id == "" {
		return nil, nil
	}
	b, err := tx.CommitHeader(ctx, id)
	if err != nil {
		return nil, err
	}
	nodes, err := storedNodes(ctx, tx, b)
	if err != nil {
		return nil, err
	}
	return tx.NodesAt(ctx, refsOf(nodes), nodeType)
}

// kindOf is the kind stored for a commit: one written without a kind is a snapshot.
func kindOf(b domain.Commit) string {
	if b.Kind == "" {
		return domain.CommitKindSnapshot
	}
	return b.Kind
}
