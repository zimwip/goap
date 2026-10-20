package graph

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/domain"
)

// The state of a namespace at a baseline is a selection (ADR 0143): a version is complete (its properties, owner, state
// and link set, ADR 0141) and lands on a branch at a point of one global order (Node.Landed, Baseline.Seq), so the
// version of a node at a baseline is the last one that landed on the baseline's branch up to its order, else, past the
// fork of the branch, the one selected on the branch it was forked from at the fork. Nothing is folded and no baseline
// stores entries: a baseline is a header, and reading the state of some nodes is one indexed read per branch of the
// chain of forks (LandedVersions), whatever the length of the branches.

// stateCache keeps the whole states computed for baselines: a state never changes once its baseline exists, so an
// entry is never stale (an id written in a transaction that rolled back is never asked again). It is bounded.
type stateCache struct {
	mu    sync.Mutex
	m     map[domain.BaselineID]map[domain.NodeID]domain.Version
	order []domain.BaselineID
}

const stateCacheSize = 32

func (c *stateCache) get(id domain.BaselineID) (map[domain.NodeID]domain.Version, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.m[id]
	if !ok {
		return nil, false
	}
	return maps.Clone(s), true
}

// lookup reads some nodes of a cached state, without copying it: absent ones are 0.
func (c *stateCache) lookup(id domain.BaselineID, ids []domain.NodeID) (map[domain.NodeID]domain.Version, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.m[id]
	if !ok {
		return nil, false
	}
	out := make(map[domain.NodeID]domain.Version, len(ids))
	for _, n := range ids {
		out[n] = s[n]
	}
	return out, true
}

func (c *stateCache) put(id domain.BaselineID, s map[domain.NodeID]domain.Version) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[domain.BaselineID]map[domain.NodeID]domain.Version{}
	}
	if _, ok := c.m[id]; !ok {
		c.order = append(c.order, id)
		if len(c.order) > stateCacheSize {
			delete(c.m, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.m[id] = maps.Clone(s)
}

// headerCache keeps the headers of baselines: a header never changes once written.
type headerCache struct {
	mu    sync.Mutex
	m     map[domain.BaselineID]domain.Baseline
	order []domain.BaselineID
}

const headerCacheSize = 4096

func (c *headerCache) get(id domain.BaselineID) (domain.Baseline, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.m[id]
	return b, ok
}

func (c *headerCache) drop(id domain.BaselineID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, id) // its place in order goes with the next eviction
}

func (c *headerCache) put(b domain.Baseline) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[domain.BaselineID]domain.Baseline{}
	}
	if _, ok := c.m[b.ID]; !ok {
		c.order = append(c.order, b.ID)
		if len(c.order) > headerCacheSize {
			delete(c.m, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.m[b.ID] = b
}

// header reads the header of a baseline, cached once the transaction that wrote it is over.
func (t *guardTx) header(ctx context.Context, id domain.BaselineID) (domain.Baseline, error) {
	if b, ok := t.g.headers.get(id); ok {
		return b, nil
	}
	b, err := t.Tx.BaselineHeader(ctx, id)
	if err == nil && !t.newBaselines[id] {
		t.g.headers.put(b)
	}
	return b, err
}

// stateAt reads the versions of some nodes (nil: every node) in a baseline (ADR 0143), the nodes it does not hold left
// out: the selection on its branch up to its order, then, for the nodes not decided, on the branch it was forked from
// at the fork, and so on.
func (t *guardTx) stateAt(ctx context.Context, id domain.BaselineID, ids []domain.NodeID) (_ map[domain.NodeID]domain.Version, err error) {
	read := stateRead{start: time.Now(), whole: ids == nil, source: stateFromLandings}
	defer func() {
		if err == nil {
			read.record(ctx)
		}
	}()
	if id == "" {
		return map[domain.NodeID]domain.Version{}, nil
	}
	if ids == nil {
		if s, ok := t.g.states.get(id); ok {
			read.source = stateFromCache
			return s, nil
		}
	} else if s, ok := t.g.states.lookup(id, ids); ok {
		read.source = stateFromCache
		for n, v := range s {
			if v == 0 {
				delete(s, n)
			}
		}
		return s, nil
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
// landing on the branch up to the order of the baseline, then the nodes not decided on the branch it was forked from
// at the fork; a version deleting its node leaves it out.
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
		var want []domain.NodeID
		if pending != nil {
			if len(pending) == 0 {
				break
			}
			want = slices.Sorted(maps.Keys(pending))
		}
		got, err := tx.LandedVersions(ctx, b.Namespace, b.Branch, 0, b.Seq, want)
		if err != nil {
			return nil, err
		}
		if read != nil {
			read.chain++
			read.landed += len(got)
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
		next, err := fork(ctx, b.Namespace, b.Branch)
		if err != nil || next == "" {
			if err != nil {
				return nil, err
			}
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

// nodesOf returns the whole state of a baseline.
func (t *guardTx) nodesOf(ctx context.Context, id domain.BaselineID) (map[domain.NodeID]domain.Version, error) {
	if s, ok := t.g.states.get(id); ok {
		return s, nil
	}
	s, err := t.stateAt(ctx, id, nil)
	if err == nil {
		t.g.states.put(id, s)
	}
	return s, err
}

// Baseline reads a baseline with its state; the empty id is the empty state, what the first change of a namespace
// starts from (ADR 0056): no baseline holds it, it is nothing.
func (t *guardTx) Baseline(ctx context.Context, id domain.BaselineID) (domain.Baseline, error) {
	if id == "" {
		return domain.Baseline{Nodes: map[domain.NodeID]domain.Version{}}, nil
	}
	b, err := t.Tx.BaselineHeader(ctx, id)
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

// landingOn returns the order of the baseline the transaction is landing versions in on a branch, allocated by the
// first version, join or baseline of that branch: the versions of a landing and its baseline share it (ADR 0140).
func (t *guardTx) landingOn(ctx context.Context, branch string) (int64, error) {
	branch = domain.BranchOf(branch)
	if s, ok := t.landing[branch]; ok {
		return s, nil
	}
	s, err := t.Tx.NextLanding(ctx)
	if err != nil {
		return 0, err
	}
	if t.landing == nil {
		t.landing = map[string]int64{}
	}
	t.landing[branch] = s
	return s, nil
}

// landed gives a baseline about to be written the order of its landing, which ends.
func (t *guardTx) landed(ctx context.Context, b *domain.Baseline) error {
	if err := t.needChange(b.ChangeID, "baseline "+b.Name); err != nil {
		return err
	}
	seq, err := t.landingOn(ctx, b.Branch)
	if err != nil {
		return err
	}
	delete(t.landing, domain.BranchOf(b.Branch))
	b.Seq, b.Nodes = seq, maps.Clone(b.Nodes)
	t.markNew(b.ID)
	return nil
}

// putBaselineOver writes a baseline whose state is the state of over with delta put in: a header (ADR 0143), the
// versions of its landing selecting its state.
func (t *guardTx) putBaselineOver(ctx context.Context, b domain.Baseline, _ domain.BaselineID, _ map[domain.NodeID]domain.Version) error {
	return t.PutBaseline(ctx, b)
}

// PutBaseline writes the header of a baseline (ADR 0143): its state is the selection at its order, the versions of its
// landing taking it.
func (t *guardTx) PutBaseline(ctx context.Context, b domain.Baseline) error {
	if err := t.landed(ctx, &b); err != nil {
		return err
	}
	b.Nodes = nil
	return t.Tx.PutBaseline(ctx, b)
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
