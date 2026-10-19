package graph

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/domain"
)

// The state of a namespace after a change follows from what the change did (ADR 0056), and what it did is the versions
// it landed (ADR 0140): a version written on a branch and a version joining one carry the order of the baseline they
// landed in (Node.Landed, Baseline.Seq), so a baseline is kept as a header only, over the baseline its state builds on
// (BaseID): its state is the last version of each node that landed on its branch since BaseID, put in the state of
// BaseID. A commit, a merge and a fast-forward write no entry. What is not derived from the versions landed (a
// snapshot, a rebase, a revert) stores its entries: what it changed over the baseline it is put on (BaseID), or the
// whole state at a checkpoint. Reading the state of some nodes is then one indexed read per baseline of that chain
// (StoredVersions, LandedVersions), whatever the length of the branch.

// DefaultMaterializeEvery is how often a baseline is stored with its entries along a chain of baselines kept as
// headers when the graph is given no MaterializeEvery: 0, never (ADR 0140). GOAP_MATERIALIZE_EVERY overrides it (1:
// every baseline stores its entries).
var DefaultMaterializeEvery = 0

func init() {
	if n, err := strconv.Atoi(os.Getenv("GOAP_MATERIALIZE_EVERY")); err == nil && n > 0 {
		DefaultMaterializeEvery = n
	}
}

func (g *Graph) materializeEvery() int {
	if g.MaterializeEvery > 0 {
		return g.MaterializeEvery
	}
	return DefaultMaterializeEvery
}

// checkpointEvery is the longest chain of baselines storing their entries over one another (ADR 0032): one that would
// be that deep stores the whole state.
var checkpointEvery = 50

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

// headerCache keeps the headers of baselines: what they say of the state never becomes wrong (a materialisation only
// stores what the versions landed already give; Materialize drops the header it changed).
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

// stateAt reads the versions of some nodes (nil: every node) in a baseline (ADR 0140), the nodes it does not hold left
// out: down the chain of the baselines its state is stored over, each one gives the nodes not given yet, a stored
// one its entries, one kept as a header the versions landed on its branch since the baseline it is stored over.
func (t *guardTx) stateAt(ctx context.Context, id domain.BaselineID, ids []domain.NodeID) (_ map[domain.NodeID]domain.Version, err error) {
	read := stateRead{start: time.Now(), whole: ids == nil, source: stateFromLandings}
	defer func() {
		if err == nil {
			read.record(ctx)
		}
	}()
	decided := map[domain.NodeID]domain.Version{} // 0: the baseline does not hold the node
	var pending map[domain.NodeID]bool            // nil: every node
	if ids != nil {
		pending = make(map[domain.NodeID]bool, len(ids))
		for _, n := range ids {
			pending[n] = true
		}
	}
	take := func(got map[domain.NodeID]domain.Version) {
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
	}
	for id != "" && (pending == nil || len(pending) > 0) {
		var want []domain.NodeID
		if pending != nil {
			want = slices.Sorted(maps.Keys(pending))
			if s, ok := t.g.states.lookup(id, want); ok {
				take(s)
				read.source = stateFromCache
				break
			}
		} else if s, ok := t.g.states.get(id); ok {
			take(s)
			read.source = stateFromCache
			break
		}
		read.chain++
		b, err := t.header(ctx, id)
		if err != nil {
			return nil, err
		}
		var got map[domain.NodeID]domain.Version
		if b.Gap == 0 {
			if got, id, err = t.Tx.StoredVersions(ctx, b.ID, want); err != nil {
				return nil, err
			}
			read.source, read.stored = stateFromStored, read.stored+len(got)
		} else {
			var after int64
			if b.BaseID != "" {
				base, err := t.header(ctx, b.BaseID)
				if err != nil {
					return nil, err
				}
				after = base.Seq
			}
			if got, err = t.Tx.LandedVersions(ctx, b.Namespace, b.Branch, after, b.Seq, want); err != nil {
				return nil, err
			}
			read.landed += len(got)
			id = b.BaseID
		}
		take(got)
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
	return t.stateAt(ctx, id, ids)
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

// asHeader makes a baseline whose state follows from the versions its change landed a header over the baseline the
// state of its parent is stored over (ADR 0140), unless it is a MaterializeEvery-th header of a chain: false then.
func (t *guardTx) asHeader(ctx context.Context, b *domain.Baseline) (bool, error) {
	if b.ParentID == "" || !domain.DerivedKind(b.Kind) {
		return false, nil
	}
	parent, err := t.header(ctx, b.ParentID)
	if err != nil {
		return false, err
	}
	sameRun := domain.BranchOf(parent.Branch) == domain.BranchOf(b.Branch) && parent.Gap > 0
	gap := 1
	if sameRun {
		gap = parent.Gap + 1
	}
	if every := t.g.materializeEvery(); every > 0 && gap >= every {
		return false, nil
	}
	b.Gap, b.Depth, b.Nodes, b.BaseID = gap, 0, nil, parent.ID
	if sameRun {
		b.BaseID = parent.BaseID
		// a checkpoint stored since along the branch is nearer (CheckpointEvery): one of its baselines, so an ancestor
		var after int64
		if b.BaseID != "" {
			base, err := t.header(ctx, b.BaseID)
			if err != nil {
				return false, err
			}
			if domain.BranchOf(base.Branch) == domain.BranchOf(b.Branch) {
				after = base.Seq
			}
		}
		cp, err := t.Tx.LatestStored(ctx, b.Namespace, b.Branch, after, parent.Seq)
		if err != nil {
			return false, err
		}
		if cp != "" {
			b.BaseID = cp
		}
	}
	if every := t.g.CheckpointEvery; every > 0 && gap%every == 0 {
		t.checkpoints = append(t.checkpoints, b.ID)
	}
	return true, nil
}

// putOver stores the entries of a baseline over the state of over (ADR 0140): delta is what it changed there (a
// version, 0 for a node it removed); a baseline that would be checkpointEvery deep, or over nothing, stores its whole
// state.
func (t *guardTx) putOver(ctx context.Context, b domain.Baseline, over domain.BaselineID, delta map[domain.NodeID]domain.Version) error {
	b.Gap, b.Depth, b.BaseID = 0, 1, over
	if over != "" {
		base, err := t.header(ctx, over)
		if err != nil {
			return err
		}
		if base.Gap == 0 {
			b.Depth = base.Depth + 1
		}
	}
	if over == "" || b.Depth >= checkpointEvery {
		from, err := t.nodesOf(ctx, over)
		if err != nil {
			return err
		}
		applyMoves(from, delta)
		b.Nodes, b.Depth, b.BaseID = from, 0, ""
		if err := t.Tx.PutBaseline(ctx, b); err != nil {
			return err
		}
		t.g.states.put(b.ID, from)
		return nil
	}
	var removed []domain.NodeID
	for n, v := range delta {
		if v == 0 {
			removed = append(removed, n)
		}
	}
	had, err := t.BaselineVersions(ctx, over, removed)
	if err != nil {
		return err
	}
	b.Nodes = map[domain.NodeID]domain.Version{}
	for n, v := range delta {
		switch {
		case v > 0:
			b.Nodes[n] = v
		case had[n] > 0:
			b.Nodes[n] = -had[n] // a removal records the version the node had
		}
	}
	return t.Tx.PutBaseline(ctx, b)
}

// putBaselineOver writes a baseline whose state is the state of over with delta put in (a version, 0: the node left),
// without reading that state (ADR 0137, 0140): a baseline whose state follows from the versions its change landed is a
// header, any other stores delta over over.
func (t *guardTx) putBaselineOver(ctx context.Context, b domain.Baseline, over domain.BaselineID, delta map[domain.NodeID]domain.Version) error {
	if err := t.landed(ctx, &b); err != nil {
		return err
	}
	if over == b.ParentID {
		if ok, err := t.asHeader(ctx, &b); err != nil {
			return err
		} else if ok {
			return t.Tx.PutBaseline(ctx, b)
		}
	}
	return t.putOver(ctx, b, over, delta)
}

// PutBaseline writes a baseline given its whole state: a header when its state follows from the versions its change
// landed, else its entries over its parent (what differs from the state of the parent, ADR 0032), or whole without a
// parent.
func (t *guardTx) PutBaseline(ctx context.Context, b domain.Baseline) error {
	if err := t.landed(ctx, &b); err != nil {
		return err
	}
	if ok, err := t.asHeader(ctx, &b); err != nil {
		return err
	} else if ok {
		return t.Tx.PutBaseline(ctx, b)
	}
	if b.ParentID == "" {
		b.Gap, b.Depth, b.BaseID = 0, 0, ""
		if err := t.Tx.PutBaseline(ctx, b); err != nil {
			return err
		}
		t.g.states.put(b.ID, b.Nodes)
		return nil
	}
	parent, err := t.nodesOf(ctx, b.ParentID)
	if err != nil {
		return err
	}
	delta := map[domain.NodeID]domain.Version{}
	for n, v := range b.Nodes {
		if parent[n] != v {
			delta[n] = v
		}
	}
	for n := range parent {
		if _, ok := b.Nodes[n]; !ok {
			delta[n] = 0
		}
	}
	whole := b.Nodes
	if err := t.putOver(ctx, b, b.ParentID, delta); err != nil {
		return err
	}
	t.g.states.put(b.ID, whole)
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

// putBaselineOver writes a baseline whose state is the state of over with delta put in (0: the node left), through
// the guard of the transaction (guardTx.putBaselineOver), else whole.
func putBaselineOver(ctx context.Context, tx Tx, b domain.Baseline, over domain.BaselineID, delta map[domain.NodeID]domain.Version) error {
	if p, ok := tx.(interface {
		putBaselineOver(context.Context, domain.Baseline, domain.BaselineID, map[domain.NodeID]domain.Version) error
	}); ok {
		return p.putBaselineOver(ctx, b, over, delta)
	}
	from, err := tx.Baseline(ctx, over)
	if err != nil {
		return err
	}
	b.Nodes = from.Nodes
	applyMoves(b.Nodes, delta)
	return tx.PutBaseline(ctx, b)
}

// materialize stores the whole state of a baseline kept as a header or storing its entries over another.
func (t *guardTx) materialize(ctx context.Context, id domain.BaselineID) error {
	b, err := t.Tx.BaselineHeader(ctx, id)
	if err != nil || b.Gap == 0 && b.BaseID == "" {
		return err
	}
	start := time.Now()
	nodes, err := t.nodesOf(ctx, id)
	if err == nil {
		err = t.Tx.MaterializeBaseline(ctx, id, nodes)
	}
	recordCheckpoint(ctx, start, len(nodes), err)
	return err
}

// DefaultCheckpointEvery is the CheckpointEvery the compositions give the graph: a branch stores its whole state every
// 50 baselines, in the background.
const DefaultCheckpointEvery = 50

// checkpointer stores checkpoints one after the other, outside the transactions of the commits (ADR 0140).
type checkpointer struct {
	mu      sync.Mutex
	pending []domain.BaselineID
	running bool
	done    sync.WaitGroup
}

// store queues checkpoints and stores them in the background; a failure is logged, the state stays computed.
func (c *checkpointer) store(g *Graph, ids []domain.BaselineID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = append(c.pending, ids...)
	stateMeter().cpPending.Add(context.Background(), int64(len(ids)))
	if c.running {
		return
	}
	c.running = true
	c.done.Add(1)
	go func() {
		defer c.done.Done()
		for {
			c.mu.Lock()
			if len(c.pending) == 0 {
				c.running = false
				c.mu.Unlock()
				return
			}
			id := c.pending[0]
			c.pending = c.pending[1:]
			c.mu.Unlock()
			stateMeter().cpPending.Add(context.Background(), -1)
			if err := g.Materialize(context.Background(), id); err != nil {
				slog.Warn("checkpoint of a baseline", "baseline", id, "err", err)
			}
		}
	}()
}

// WaitCheckpoints waits until the checkpoints queued are stored.
func (g *Graph) WaitCheckpoints() { g.checkpoints.done.Wait() }

// Materialize stores the whole state of a baseline (a checkpoint to speed up reading the states built on it); a whole
// one is left as it is.
func (g *Graph) Materialize(ctx context.Context, id domain.BaselineID) error {
	err := g.repo.InTx(ctx, func(tx Tx) error {
		if gt, ok := unwrapGuard(tx); ok {
			return gt.materialize(ctx, id)
		}
		return fmt.Errorf("the transaction of the graph is not guarded: %w", ErrInvalid)
	})
	if err == nil {
		g.headers.drop(id)
	}
	return err
}

// unwrapGuard returns the guard of a transaction of the graph.
func unwrapGuard(tx Tx) (*guardTx, bool) {
	if ot, ok := tx.(*observedTx); ok {
		tx = ot.Tx
	}
	gt, ok := tx.(*guardTx)
	return gt, ok
}

// storedNodes is the state a store holds of a baseline without the graph: the entries in effect along its chain of
// stored baselines (none for one kept as a header).
func storedNodes(ctx context.Context, tx Tx, b domain.Baseline) (map[domain.NodeID]domain.Version, error) {
	out := map[domain.NodeID]domain.Version{}
	if b.Gap > 0 {
		return out, nil
	}
	vs, _, err := tx.StoredVersions(ctx, b.ID, nil)
	for n, v := range vs {
		if v > 0 {
			out[n] = v
		}
	}
	return out, err
}

// storedNodesIn lists the nodes of the entries a store holds of a baseline (storedNodes), of a type, by key.
func storedNodesIn(ctx context.Context, tx Tx, id domain.BaselineID, nodeType string) ([]domain.Node, error) {
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
