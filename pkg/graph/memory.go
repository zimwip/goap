package graph

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"

	"github.com/zimwip/goap/pkg/domain"
)

// Memory is an in-memory Repo. Transactions are serialized and rolled back by
// restoring a snapshot.
type Memory struct {
	mu sync.Mutex
	st memState
}

type memState struct {
	versions  map[domain.NodeID][]domain.Node // index = version-1
	keys      map[string]domain.NodeID
	links     []domain.Link
	baselines map[domain.BaselineID]domain.Baseline
	changes   map[domain.ChangeID]domain.ChangeSet
}

// NewMemory returns an empty in-memory repository.
func NewMemory() *Memory {
	return &Memory{st: memState{
		versions:  map[domain.NodeID][]domain.Node{},
		keys:      map[string]domain.NodeID{},
		baselines: map[domain.BaselineID]domain.Baseline{},
		changes:   map[domain.ChangeID]domain.ChangeSet{},
	}}
}

func (s memState) clone() memState {
	c := memState{
		versions:  make(map[domain.NodeID][]domain.Node, len(s.versions)),
		keys:      maps.Clone(s.keys),
		links:     slices.Clone(s.links),
		baselines: maps.Clone(s.baselines),
		changes:   make(map[domain.ChangeID]domain.ChangeSet, len(s.changes)),
	}
	for k, v := range s.versions {
		c.versions[k] = slices.Clone(v)
	}
	for k, v := range s.changes {
		v.Items = slices.Clone(v.Items)
		c.changes[k] = v
	}
	return c
}

// InTx implements Repo.
func (m *Memory) InTx(ctx context.Context, fn func(tx Tx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := m.st.clone()
	if err := fn(&memTx{st: &m.st}); err != nil {
		m.st = snapshot
		return err
	}
	return nil
}

type memTx struct{ st *memState }

func (t *memTx) Node(_ context.Context, ref domain.NodeRef) (domain.Node, error) {
	vs, ok := t.st.versions[ref.ID]
	if !ok || len(vs) == 0 {
		return domain.Node{}, fmt.Errorf("node %s: %w", ref.ID, ErrNotFound)
	}
	if ref.Version == 0 {
		return vs[len(vs)-1], nil
	}
	if int(ref.Version) > len(vs) || ref.Version < 1 {
		return domain.Node{}, fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	return vs[ref.Version-1], nil
}

func (t *memTx) NodeByKey(ctx context.Context, key string) (domain.Node, error) {
	id, ok := t.st.keys[key]
	if !ok {
		return domain.Node{}, fmt.Errorf("node key %q: %w", key, ErrNotFound)
	}
	return t.Node(ctx, domain.NodeRef{ID: id})
}

func (t *memTx) NodesIn(ctx context.Context, baseline domain.BaselineID, nodeType string) ([]domain.Node, error) {
	b, err := t.Baseline(ctx, baseline)
	if err != nil {
		return nil, err
	}
	var out []domain.Node
	for id, v := range b.Nodes {
		n, err := t.Node(ctx, domain.NodeRef{ID: id, Version: v})
		if err != nil {
			return nil, err
		}
		if nodeType == "" || n.Type == nodeType {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (t *memTx) LatestNodes(_ context.Context) ([]domain.Node, error) {
	out := make([]domain.Node, 0, len(t.st.versions))
	for _, vs := range t.st.versions {
		out = append(out, vs[len(vs)-1])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (t *memTx) OutLinks(_ context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	var out []domain.Link
	for _, l := range t.st.links {
		if l.From == ref {
			out = append(out, l)
		}
	}
	return out, nil
}

func (t *memTx) InLinks(_ context.Context, ref domain.NodeRef) ([]domain.Link, error) {
	var out []domain.Link
	for _, l := range t.st.links {
		if l.To == ref {
			out = append(out, l)
		}
	}
	return out, nil
}

func (t *memTx) Baseline(_ context.Context, id domain.BaselineID) (domain.Baseline, error) {
	b, ok := t.st.baselines[id]
	if !ok {
		return domain.Baseline{}, fmt.Errorf("baseline %s: %w", id, ErrNotFound)
	}
	b.Nodes = maps.Clone(b.Nodes)
	return b, nil
}

func (t *memTx) Baselines(_ context.Context) ([]domain.Baseline, error) {
	out := make([]domain.Baseline, 0, len(t.st.baselines))
	for _, b := range t.st.baselines {
		b.Nodes = maps.Clone(b.Nodes)
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (t *memTx) Change(_ context.Context, id domain.ChangeID) (domain.ChangeSet, error) {
	c, ok := t.st.changes[id]
	if !ok {
		return domain.ChangeSet{}, fmt.Errorf("change %s: %w", id, ErrNotFound)
	}
	c.Items = slices.Clone(c.Items)
	return c, nil
}

func (t *memTx) Changes(ctx context.Context) ([]domain.ChangeSet, error) {
	out := make([]domain.ChangeSet, 0, len(t.st.changes))
	for id := range t.st.changes {
		c, _ := t.Change(ctx, id)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (t *memTx) PutNode(_ context.Context, n domain.Node) error {
	vs := t.st.versions[n.ID]
	if int(n.Version) != len(vs)+1 {
		return fmt.Errorf("node %s: expected version %d: %w", n.Ref(), len(vs)+1, ErrConflict)
	}
	if n.Version == 1 {
		if _, dup := t.st.keys[n.Key]; dup && n.Key != "" {
			return fmt.Errorf("node key %q already used: %w", n.Key, ErrConflict)
		}
		if n.Key != "" {
			t.st.keys[n.Key] = n.ID
		}
	}
	t.st.versions[n.ID] = append(vs, n)
	return nil
}

func (t *memTx) PutLink(_ context.Context, l domain.Link) error {
	t.st.links = append(t.st.links, l)
	return nil
}

func (t *memTx) PutBaseline(_ context.Context, b domain.Baseline) error {
	if _, dup := t.st.baselines[b.ID]; dup {
		return fmt.Errorf("baseline %s: %w", b.ID, ErrConflict)
	}
	b.Nodes = maps.Clone(b.Nodes)
	t.st.baselines[b.ID] = b
	return nil
}

func (t *memTx) PutChange(_ context.Context, c domain.ChangeSet) error {
	if old, ok := t.st.changes[c.ID]; ok {
		c.Items = old.Items
	} else {
		c.Items = nil
	}
	t.st.changes[c.ID] = c
	return nil
}

func (t *memTx) PutItem(_ context.Context, id domain.ChangeID, it domain.ChangeItem) error {
	c, ok := t.st.changes[id]
	if !ok {
		return fmt.Errorf("change %s: %w", id, ErrNotFound)
	}
	c.Items = append(c.Items, it)
	t.st.changes[id] = c
	return nil
}
