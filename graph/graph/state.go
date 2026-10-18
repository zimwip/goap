package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strconv"
	"sync"

	"github.com/zimwip/goap/contracts/domain"
)

// The state of a namespace after a change follows from what the change did (ADR 0056), so storing the entries of a
// baseline (materialising it, a snapshot) is an acceleration: a baseline with a Gap has only its header stored. Its
// state is the state of the baseline before it with the change's work put in, according to its Kind: the `landed`
// events of the change for a commit or a 3-way merge, the versions the change joined to the branch for a
// fast-forward. Every MaterializeEvery baselines along a chain one is materialised.

// DefaultMaterializeEvery is how often a baseline is materialised along a chain of baselines when the graph is given
// no MaterializeEvery: the others are computed. GOAP_MATERIALIZE_EVERY overrides it (1: every baseline is
// materialised).
var DefaultMaterializeEvery = 16

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

// stateCache keeps the states computed by replaying the log: a state never changes once its baseline exists, so
// an entry is never stale (an id written in a transaction that rolled back is never asked again). It is bounded.
type stateCache struct {
	mu    sync.Mutex
	m     map[domain.BaselineID]map[domain.NodeID]domain.Version
	order []domain.BaselineID
}

const stateCacheSize = 32

// movesCacheSize bounds the moves kept: they are small (what one change did), and every read of a node in a baseline
// kept as a header walks them (ADR 0137).
const movesCacheSize = 1024

func (c *stateCache) get(id domain.BaselineID) (map[domain.NodeID]domain.Version, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.m[id]
	if !ok {
		return nil, false
	}
	return maps.Clone(s), true
}

// lookup reads some nodes of a cached state, without copying it.
func (c *stateCache) lookup(id domain.BaselineID, ids []domain.NodeID) (map[domain.NodeID]domain.Version, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.m[id]
	if !ok {
		return nil, false
	}
	out := map[domain.NodeID]domain.Version{}
	for _, n := range ids {
		if v, ok := s[n]; ok {
			out[n] = v
		}
	}
	return out, true
}

func (c *stateCache) put(id domain.BaselineID, s map[domain.NodeID]domain.Version) {
	c.putSized(id, s, stateCacheSize)
}

func (c *stateCache) putSized(id domain.BaselineID, s map[domain.NodeID]domain.Version, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[domain.BaselineID]map[domain.NodeID]domain.Version{}
	}
	if _, ok := c.m[id]; !ok {
		c.order = append(c.order, id)
		if len(c.order) > size {
			delete(c.m, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.m[id] = maps.Clone(s)
}

// step returns the state of a baseline kept as a header only, from the state of its parent.
func (t *guardTx) step(ctx context.Context, b domain.Baseline, from map[domain.NodeID]domain.Version) (map[domain.NodeID]domain.Version, error) {
	mv, err := t.moves(ctx, b)
	if err != nil {
		return nil, err
	}
	out := maps.Clone(from)
	applyMoves(out, mv)
	return out, nil
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

// moves returns what a baseline whose state is derived did to the state of its parent (ADR 0056, 0137): the version
// each node it moved is at, 0 for a node it removed. A baseline never changes: they are cached.
func (t *guardTx) moves(ctx context.Context, b domain.Baseline) (map[domain.NodeID]domain.Version, error) {
	if mv, ok := t.g.moves.get(b.ID); ok {
		return mv, nil
	}
	mv, err := t.readMoves(ctx, b)
	if err != nil {
		return nil, err
	}
	if !t.newBaselines[b.ID] { // one this transaction wrote may not have all its landed events yet
		t.g.moves.putSized(b.ID, mv, movesCacheSize)
	}
	return mv, nil
}

func (t *guardTx) readMoves(ctx context.Context, b domain.Baseline) (map[domain.NodeID]domain.Version, error) {
	out := map[domain.NodeID]domain.Version{}
	put := func(ref domain.NodeRef) error {
		n, err := t.Tx.Node(ctx, ref)
		if err != nil {
			return err
		}
		if n.Deleted {
			out[n.ID] = 0
		} else {
			out[n.ID] = n.Version
		}
		return nil
	}
	switch b.Kind {
	case domain.BaselineCommit, domain.BaselineMerge:
		entries, err := t.Tx.Log(ctx, domain.LogFilter{Change: b.ChangeID, Types: []string{domain.LogImpact + "." + string(domain.ImpactLanded)}})
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			var ev domain.ImpactEvent
			if err := json.Unmarshal(e.Payload, &ev); err != nil {
				return nil, fmt.Errorf("log entry %d of change %s: %w", e.Seq, b.ChangeID, err)
			}
			if ev.Baseline == b.ID && ev.Landed != nil {
				if err := put(*ev.Landed); err != nil {
					return nil, err
				}
			}
		}
	case domain.BaselineFastForward:
		refs, err := t.Tx.BranchJoins(ctx, b.Namespace, b.Branch, b.ChangeID)
		if err != nil {
			return nil, err
		}
		latest := map[domain.NodeID]domain.NodeRef{} // a node joined twice: the last version counts
		for _, r := range refs {
			if r.Version > latest[r.ID].Version {
				latest[r.ID] = r
			}
		}
		for _, r := range latest {
			if err := put(r); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("baseline %s is a %q: its state is not derived, it is always stored: %w", b.ID, b.Kind, ErrInvalid)
	}
	return out, nil
}

// nodesOf returns the state of a baseline read from the store: its entries, or, for a baseline kept as a header
// only, the state computed from the nearest snapshot before it.
func (t *guardTx) nodesOf(ctx context.Context, b domain.Baseline) (map[domain.NodeID]domain.Version, error) {
	if b.Gap == 0 {
		return b.Nodes, nil
	}
	if s, ok := t.g.states.get(b.ID); ok {
		return s, nil
	}
	// walk up to a state known (a snapshot, a cached one, the empty state), then apply the moves down
	chain := []domain.Baseline{b}
	var base map[domain.NodeID]domain.Version
	for cur := b; base == nil; {
		if cur.ParentID == "" {
			base = map[domain.NodeID]domain.Version{}
			break
		}
		if s, ok := t.g.states.get(cur.ParentID); ok {
			base = s
			break
		}
		p, err := t.Tx.Baseline(ctx, cur.ParentID)
		if err != nil {
			return nil, err
		}
		if p.Gap == 0 {
			base = p.Nodes
			break
		}
		chain = append(chain, p)
		cur = p
	}
	for i := len(chain) - 1; i >= 0; i-- {
		var err error
		if base, err = t.step(ctx, chain[i], base); err != nil {
			return nil, err
		}
		t.g.states.put(chain[i].ID, base)
	}
	return maps.Clone(base), nil
}

// Baseline reads a baseline, its nodes included whether it is materialised or computed; the empty id is the empty
// state, what the first change of a namespace starts from (ADR 0056): no baseline holds it, it is nothing.
func (t *guardTx) Baseline(ctx context.Context, id domain.BaselineID) (domain.Baseline, error) {
	if id == "" {
		return domain.Baseline{Nodes: map[domain.NodeID]domain.Version{}}, nil
	}
	b, err := t.Tx.Baseline(ctx, id)
	if err != nil || b.Gap == 0 {
		return b, err
	}
	b.Nodes, err = t.nodesOf(ctx, b)
	return b, err
}

// BaselineVersions reads the versions some nodes have in a baseline (ADR 0137): in a materialised baseline, its
// entries for those nodes; in one kept as a header only, the entries of the nearest materialised baseline before it
// (or a state cached, or the empty one) with what the changes in between did to them. Nothing is read of the other
// nodes: what it costs follows the nodes asked and the baselines walked, not the size of the state.
func (t *guardTx) BaselineVersions(ctx context.Context, id domain.BaselineID, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	if id == "" || len(ids) == 0 {
		return map[domain.NodeID]domain.Version{}, nil
	}
	if s, ok := t.g.states.lookup(id, ids); ok {
		return s, nil
	}
	b, err := t.Tx.BaselineHeader(ctx, id)
	if err != nil {
		return nil, err
	}
	if b.Gap == 0 {
		return t.Tx.BaselineVersions(ctx, id, ids)
	}
	chain := []domain.Baseline{b}
	var out map[domain.NodeID]domain.Version
	for cur := b; out == nil; {
		if cur.ParentID == "" {
			out = map[domain.NodeID]domain.Version{}
			break
		}
		if s, ok := t.g.states.lookup(cur.ParentID, ids); ok {
			out = s
			break
		}
		p, err := t.Tx.BaselineHeader(ctx, cur.ParentID)
		if err != nil {
			return nil, err
		}
		if p.Gap == 0 {
			if out, err = t.Tx.BaselineVersions(ctx, p.ID, ids); err != nil {
				return nil, err
			}
			break
		}
		chain = append(chain, p)
		cur = p
	}
	want := map[domain.NodeID]bool{}
	for _, n := range ids {
		want[n] = true
	}
	for i := len(chain) - 1; i >= 0; i-- {
		mv, err := t.moves(ctx, chain[i])
		if err != nil {
			return nil, err
		}
		for n, v := range mv {
			if !want[n] {
				continue
			}
			if v == 0 {
				delete(out, n)
			} else {
				out[n] = v
			}
		}
	}
	return out, nil
}

// putBaselineOver writes a baseline whose state is the state of over with delta put in (a version, 0: the node
// left), without reading that state when the baseline is kept as a header only (ADR 0137): its state follows from
// what its change did, delta is that. A baseline to materialise (every MaterializeEvery along a chain, a state that is
// not derived, over not its parent) is written whole, the state of over read then.
func (t *guardTx) putBaselineOver(ctx context.Context, b domain.Baseline, over domain.BaselineID, delta map[domain.NodeID]domain.Version) error {
	if b.ParentID != "" && over == b.ParentID && domain.DerivedKind(b.Kind) {
		if err := t.needChange(b.ChangeID, "baseline "+b.Name); err != nil {
			return err
		}
		parent, err := t.Tx.BaselineHeader(ctx, b.ParentID)
		if err != nil {
			return err
		}
		if gap := parent.Gap + 1; gap < t.g.materializeEvery() {
			b.Gap, b.Nodes = gap, nil
			t.markNew(b.ID)
			return t.Tx.PutBaseline(ctx, b)
		}
	}
	from, err := t.Baseline(ctx, over)
	if err != nil {
		return err
	}
	b.Nodes = from.Nodes
	applyMoves(b.Nodes, delta)
	return t.PutBaseline(ctx, b)
}

func (t *guardTx) markNew(id domain.BaselineID) {
	if t.newBaselines == nil {
		t.newBaselines = map[domain.BaselineID]bool{}
	}
	t.newBaselines[id] = true
}

// putBaselineOver writes a baseline whose state is the state of over with delta put in (0: the node left), through
// the guard of the transaction when there is one (guardTx.putBaselineOver), else whole.
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

// Baselines lists the baselines of a namespace, their nodes included.
func (t *guardTx) Baselines(ctx context.Context, namespace string) ([]domain.Baseline, error) {
	bs, err := t.Tx.Baselines(ctx, namespace)
	if err != nil {
		return nil, err
	}
	for i, b := range bs {
		if b.Gap > 0 {
			if bs[i].Nodes, err = t.nodesOf(ctx, b); err != nil {
				return nil, err
			}
		}
	}
	return bs, nil
}

// materialize stores the state of a baseline kept as a header only.
func (t *guardTx) materialize(ctx context.Context, id domain.BaselineID) error {
	b, err := t.Tx.Baseline(ctx, id)
	if err != nil || b.Gap == 0 {
		return err
	}
	nodes, err := t.nodesOf(ctx, b)
	if err != nil {
		return err
	}
	return t.Tx.MaterializeBaseline(ctx, id, nodes)
}

// NodesIn lists the nodes of a baseline of a type: the store reads them from the entries, so a baseline kept as a
// header only is materialised first.
func (t *guardTx) NodesIn(ctx context.Context, id domain.BaselineID, nodeType string) ([]domain.Node, error) {
	if id == "" {
		return nil, nil
	}
	if err := t.materialize(ctx, id); err != nil {
		return nil, err
	}
	return t.Tx.NodesIn(ctx, id, nodeType)
}

// PutBaseline writes a baseline, materialised only every MaterializeEvery baselines along its chain, and always when
// its state is not derived from what its change did (a snapshot, the first baseline of a chain) (ADR 0056).
func (t *guardTx) PutBaseline(ctx context.Context, b domain.Baseline) error {
	if err := t.needChange(b.ChangeID, "baseline "+b.Name); err != nil {
		return err
	}
	b.Gap = 0
	t.markNew(b.ID)
	if b.ParentID != "" && domain.DerivedKind(b.Kind) {
		parent, err := t.Tx.BaselineHeader(ctx, b.ParentID)
		if err != nil {
			return err
		}
		if b.Gap = parent.Gap + 1; b.Gap >= t.g.materializeEvery() {
			b.Gap = 0
		}
	}
	if err := t.Tx.PutBaseline(ctx, b); err != nil {
		return err
	}
	t.g.states.put(b.ID, b.Nodes)
	return nil
}

// Materialize stores the state of a baseline kept as a header only (a snapshot to speed up reading what follows it);
// a materialised one is left as it is.
func (g *Graph) Materialize(ctx context.Context, id domain.BaselineID) error {
	return g.repo.InTx(ctx, func(tx Tx) error {
		if gt, ok := unwrapGuard(tx); ok {
			return gt.materialize(ctx, id)
		}
		return fmt.Errorf("the transaction of the graph is not guarded: %w", ErrInvalid)
	})
}

// unwrapGuard returns the guard of a transaction of the graph.
func unwrapGuard(tx Tx) (*guardTx, bool) {
	if ot, ok := tx.(*observedTx); ok {
		tx = ot.Tx
	}
	gt, ok := tx.(*guardTx)
	return gt, ok
}

// kindOf is the kind stored for a baseline: one written without a kind is a snapshot.
func kindOf(b domain.Baseline) string {
	if b.Kind == "" {
		return domain.BaselineSnapshot
	}
	return b.Kind
}
