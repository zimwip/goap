package graph

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/zimwip/goap/contracts/domain"
)

// The state of a namespace at a baseline is a selection (ADR 0143): a version is complete (its properties, owner, state
// and link set, ADR 0141) and commits on a branch at a point of one global order (Node.Committed, Baseline.Seq), so the
// version of a node at a baseline is the last one that committed on the baseline's branch up to its order, else, past the
// fork of the branch, the one selected on the branch it was forked from at the fork. Nothing is folded and no baseline
// stores entries: a baseline is a header, and reading the state of some nodes is one indexed read per branch of the
// chain of forks (CommittedVersions), whatever the length of the branches.

// The headers of baselines are kept (Graph.headers, a lib/cache): a header never changes once written. The states are
// not: a read of some nodes is one indexed selection, cheaper than keeping whole states for the rare whole reads.

// header reads the header of a baseline, cached once the transaction that wrote it is over.
func (t *guardTx) header(ctx context.Context, id domain.BaselineID) (domain.Baseline, error) {
	if b, ok := t.g.headers.Get(id); ok {
		return b, nil
	}
	b, err := t.Tx.BaselineHeader(ctx, id)
	if err == nil && !t.newBaselines[id] {
		t.g.headers.Put(b.ID, b)
	}
	return b, err
}

// stateAt reads the versions of some nodes (nil: every node) in a baseline (ADR 0143), the nodes it does not hold left
// out: the selection on its branch up to its order, then, for the nodes not decided, on the branch it was forked from
// at the fork, and so on.
func (t *guardTx) stateAt(ctx context.Context, id domain.BaselineID, ids []domain.NodeID) (_ map[domain.NodeID]domain.Version, err error) {
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
	return selectState(ctx, t.Tx, b, ids, t.header, t.forkOf, &read)
}

// forkOf returns the baseline a lasting branch was forked from (none for main), read once per transaction.
func (t *guardTx) forkOf(ctx context.Context, namespace, branch string) (domain.BaselineID, error) {
	key := domain.NamespaceOf(namespace) + "\x00" + domain.BranchOf(branch)
	if fork, ok := t.forks[key]; ok {
		return fork, nil
	}
	fork, err := branchFork(ctx, t.Tx, namespace, branch)
	if err != nil {
		return "", err
	}
	if t.forks == nil {
		t.forks = map[string]domain.BaselineID{}
	}
	t.forks[key] = fork
	return fork, nil
}

// branchFork returns the baseline a branch was forked from: none for main or a branch the store does not know.
func branchFork(ctx context.Context, tx Tx, namespace, branch string) (domain.BaselineID, error) {
	if domain.BranchOf(branch) == domain.MainBranch {
		return "", nil
	}
	br, err := tx.Branch(ctx, domain.NamespaceOf(namespace), domain.BranchOf(branch))
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	return br.ForkBaseline, err
}

// selectState is the selection of ADR 0143 from the header of a baseline: for each node (nil ids: every node), its last
// commit on the branch up to the order of the baseline, then the nodes not decided on the branch it was forked from
// at the fork; a version deleting its node leaves it out. A whole state reads the commits of the branches forked off
// as they are (their history is theirs alone, short), and the branch at the root of the chain, whose history is the
// namespace's, as its node list then one selection per node, in batches (§4): its cost is the number of nodes, never
// the length of the history.
func selectState(ctx context.Context, tx Tx, b domain.Baseline, ids []domain.NodeID,
	header func(context.Context, domain.BaselineID) (domain.Baseline, error),
	fork func(context.Context, string, string) (domain.BaselineID, error), read *stateRead) (map[domain.NodeID]domain.Version, error) {
	decided := map[domain.NodeID]domain.Version{} // 0: the baseline does not hold the node
	var pending map[domain.NodeID]bool            // nil: every node
	if ids != nil {
		pending = make(map[domain.NodeID]bool, len(ids))
		for _, n := range ids {
			pending[n] = true
		}
	}
	for {
		next, err := fork(ctx, b.Namespace, b.Branch)
		if err != nil {
			return nil, err
		}
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
			got, err := tx.CommittedVersions(ctx, b.Namespace, b.Branch, 0, b.Seq, batch)
			if err != nil {
				return nil, err
			}
			if read != nil {
				read.chain++
				read.committed += len(got)
			}
			for n, v := range got {
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
		if b, err = header(ctx, next); err != nil {
			return nil, err
		}
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

// selectKey reads the node of a key and its version at a baseline in one statement, when the store reads them so and
// the baseline's branch is forked from none (main: its commits decide every node): decided then, version 0 for a node
// the baseline does not hold, the node "" for a key no node has.
func selectKey(ctx context.Context, tx Tx, at domain.BaselineID, key string) (domain.NodeID, domain.Version, bool, error) {
	gt, ok := unwrapGuard(tx)
	if !ok || at == "" || gt.newBaselines[at] {
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
	if fork, err := gt.forkOf(ctx, b.Namespace, b.Branch); err != nil || fork != "" {
		return "", 0, false, err
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
	// what BaselineVersions answers of it next
	if gt.baselineReads == nil {
		gt.baselineReads = map[domain.BaselineID]map[domain.NodeID]domain.Version{}
	}
	if gt.baselineReads[at] == nil {
		gt.baselineReads[at] = map[domain.NodeID]domain.Version{}
	}
	gt.baselineReads[at][id] = v
	return id, v, true, nil
}

// selectBatch is the number of nodes one statement selects the version of.
const selectBatch = 2000

// nodesOf returns the whole state of a baseline.
func (t *guardTx) nodesOf(ctx context.Context, id domain.BaselineID) (map[domain.NodeID]domain.Version, error) {
	return t.stateAt(ctx, id, nil)
}

// Baseline reads a baseline with its state; the empty id is the empty state, what the first change of a namespace
// starts from (ADR 0056): no baseline holds it, it is nothing.
func (t *guardTx) Baseline(ctx context.Context, id domain.BaselineID) (domain.Baseline, error) {
	if id == "" {
		return domain.Baseline{Nodes: map[domain.NodeID]domain.Version{}}, nil
	}
	b, err := t.header(ctx, id)
	if err != nil {
		return b, err
	}
	b.Nodes, err = t.nodesOf(ctx, id)
	return b, err
}

// Baselines lists the baselines of a namespace with their state, oldest first.
func (t *guardTx) Baselines(ctx context.Context, namespace string) ([]domain.Baseline, error) {
	hs, err := t.Tx.BaselineHeaders(ctx, namespace)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Baseline, 0, len(hs))
	for _, h := range hs {
		b, err := t.Baseline(ctx, h.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// BaselineVersions reads the versions some nodes have in a baseline (ADR 0137, 0140): what it costs follows the nodes
// asked and the chain of the baseline, not the size of the state.
func (t *guardTx) BaselineVersions(ctx context.Context, id domain.BaselineID, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	if id == "" || len(ids) == 0 {
		return map[domain.NodeID]domain.Version{}, nil
	}
	if t.newBaselines[id] {
		return t.stateAt(ctx, id, ids)
	}
	// a baseline never changes: what the transaction read of it is not read again (a merge reads the fork and the
	// branch of the change several times, for its rebase, its guardian and its fast-forward)
	known := t.baselineReads[id]
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
			if t.baselineReads == nil {
				t.baselineReads = map[domain.BaselineID]map[domain.NodeID]domain.Version{}
			}
			known = map[domain.NodeID]domain.Version{}
			t.baselineReads[id] = known
		}
		for _, n := range missing {
			known[n] = got[n] // 0: the baseline does not hold it
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

// NodesIn lists the nodes of a baseline of a type, by key.
func (t *guardTx) NodesIn(ctx context.Context, id domain.BaselineID, nodeType string) ([]domain.Node, error) {
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

// commitOn returns the order of the baseline the transaction is commit versions in on a branch, allocated by the
// first version, join or baseline of that branch: the versions of a commit and its baseline share it (ADR 0140).
func (t *guardTx) commitOn(ctx context.Context, branch string) (int64, error) {
	branch = domain.BranchOf(branch)
	if s, ok := t.commit[branch]; ok {
		return s, nil
	}
	s, err := t.Tx.NextCommit(ctx)
	if err != nil {
		return 0, err
	}
	if t.commit == nil {
		t.commit = map[string]int64{}
	}
	t.commit[branch] = s
	return s, nil
}

// committed gives a baseline about to be written the order of its commit, which ends.
func (t *guardTx) committed(ctx context.Context, b *domain.Baseline) error {
	if err := t.needChange(b.ChangeID, "baseline "+b.Name); err != nil {
		return err
	}
	seq, err := t.commitOn(ctx, b.Branch)
	if err != nil {
		return err
	}
	delete(t.commit, domain.BranchOf(b.Branch))
	b.Seq, b.Nodes = seq, maps.Clone(b.Nodes)
	t.markNew(b.ID)
	return nil
}

// putBaselineOver writes a baseline whose state is the state of over with delta put in: a header (ADR 0143), the
// versions of its commit selecting its state.
func (t *guardTx) putBaselineOver(ctx context.Context, b domain.Baseline, _ domain.BaselineID, _ map[domain.NodeID]domain.Version) error {
	return t.PutBaseline(ctx, b)
}

// PutBaseline writes the header of a baseline (ADR 0143): its state is the selection at its order, the versions of its
// commit taking it.
func (t *guardTx) PutBaseline(ctx context.Context, b domain.Baseline) error {
	if err := t.committed(ctx, &b); err != nil {
		return err
	}
	b.Nodes = nil
	if err := t.Tx.PutBaseline(ctx, b); err != nil {
		return err
	}
	if t.wroteHeads == nil {
		t.wroteHeads = map[domain.BaselineID]domain.Baseline{}
	}
	t.wroteHeads[b.ID] = b
	return nil
}

func (t *guardTx) markNew(id domain.BaselineID) {
	if t.newBaselines == nil {
		t.newBaselines = map[domain.BaselineID]bool{}
	}
	t.newBaselines[id] = true
}

// applyMoves puts in a state what a baseline did to it (moves: a version, 0 for a node it removed).
func applyMoves(state, mv map[domain.NodeID]domain.Version) {
	for id, v := range mv {
		if v == 0 {
			delete(state, id)
		} else {
			state[id] = v
		}
	}
}

// putBaselineOver writes a baseline over another (guardTx.putBaselineOver): a header.
func putBaselineOver(ctx context.Context, tx Tx, b domain.Baseline, over domain.BaselineID, delta map[domain.NodeID]domain.Version) error {
	if p, ok := tx.(interface {
		putBaselineOver(context.Context, domain.Baseline, domain.BaselineID, map[domain.NodeID]domain.Version) error
	}); ok {
		return p.putBaselineOver(ctx, b, over, delta)
	}
	return tx.PutBaseline(ctx, b)
}

// unwrapGuard returns the guard of a transaction of the graph.
func unwrapGuard(tx Tx) (*guardTx, bool) {
	if ot, ok := tx.(*observedTx); ok {
		tx = ot.Tx
	}
	gt, ok := tx.(*guardTx)
	return gt, ok
}

// storedNodes is the state of a baseline read by a store without the graph: the selection (ADR 0143).
func storedNodes(ctx context.Context, tx Tx, b domain.Baseline) (map[domain.NodeID]domain.Version, error) {
	return selectState(ctx, tx, b, nil, tx.BaselineHeader, func(ctx context.Context, ns, branch string) (domain.BaselineID, error) {
		return branchFork(ctx, tx, ns, branch)
	}, nil)
}

// storedNodesIn lists the nodes of a baseline read by a store (storedNodes), of a type, by key.
func storedNodesIn(ctx context.Context, tx Tx, id domain.BaselineID, nodeType string) ([]domain.Node, error) {
	if id == "" {
		return nil, nil
	}
	b, err := tx.BaselineHeader(ctx, id)
	if err != nil {
		return nil, err
	}
	nodes, err := storedNodes(ctx, tx, b)
	if err != nil {
		return nil, err
	}
	return tx.NodesAt(ctx, refsOf(nodes), nodeType)
}

// kindOf is the kind stored for a baseline: one written without a kind is a snapshot.
func kindOf(b domain.Baseline) string {
	if b.Kind == "" {
		return domain.BaselineSnapshot
	}
	return b.Kind
}
