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
	changes   map[domain.ChangeID]domain.Change
	branches  map[string]domain.Branch
	nodes     map[domain.ChangeID][]domain.ChangeImpact
	// log holds the entries of every change's log, in their order (ADR 0030)
	log []domain.LogEntry
}

// NewMemory returns an empty in-memory repository.
func NewMemory() *Memory {
	return &Memory{st: memState{
		versions:  map[domain.NodeID][]domain.Node{},
		keys:      map[string]domain.NodeID{},
		baselines: map[domain.BaselineID]domain.Baseline{},
		changes:   map[domain.ChangeID]domain.Change{},
		branches:  map[string]domain.Branch{},
		nodes:     map[domain.ChangeID][]domain.ChangeImpact{},
	}}
}

func (s memState) clone() memState {
	c := memState{
		versions:  make(map[domain.NodeID][]domain.Node, len(s.versions)),
		keys:      maps.Clone(s.keys),
		links:     slices.Clone(s.links),
		baselines: maps.Clone(s.baselines),
		changes:   make(map[domain.ChangeID]domain.Change, len(s.changes)),
		branches:  maps.Clone(s.branches),
		log:       slices.Clone(s.log),
		nodes:     make(map[domain.ChangeID][]domain.ChangeImpact, len(s.nodes)),
	}
	for k, v := range s.nodes {
		c.nodes[k] = slices.Clone(v)
	}
	for k, v := range s.versions {
		c.versions[k] = slices.Clone(v)
	}
	for k, v := range s.changes {
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
		for i := len(vs) - 1; i >= 0; i-- {
			if domain.BranchOf(vs[i].Branch) == domain.MainBranch {
				return vs[i], nil
			}
		}
		return domain.Node{}, fmt.Errorf("node %s has no version on main: %w", ref.ID, ErrNotFound)
	}
	if int(ref.Version) > len(vs) || ref.Version < 1 {
		return domain.Node{}, fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	return vs[ref.Version-1], nil
}

func (t *memTx) LatestOn(_ context.Context, id domain.NodeID, branch string) (domain.Node, error) {
	vs := t.st.versions[id]
	for i := len(vs) - 1; i >= 0; i-- {
		if domain.BranchOf(vs[i].Branch) == domain.BranchOf(branch) {
			return vs[i], nil
		}
	}
	return domain.Node{}, fmt.Errorf("node %s has no version on %s: %w", id, domain.BranchOf(branch), ErrNotFound)
}

func (t *memTx) Versions(_ context.Context, id domain.NodeID) ([]domain.Node, error) {
	vs, ok := t.st.versions[id]
	if !ok {
		return nil, fmt.Errorf("node %s: %w", id, ErrNotFound)
	}
	return slices.Clone(vs), nil
}

func (t *memTx) Branch(_ context.Context, namespace, name string) (domain.Branch, error) {
	b, ok := t.st.branches[branchKey(namespace, name)]
	if !ok {
		return domain.Branch{}, fmt.Errorf("branch %s/%s: %w", domain.NamespaceOf(namespace), name, ErrNotFound)
	}
	return b, nil
}

func (t *memTx) Branches(_ context.Context, namespace string) ([]domain.Branch, error) {
	namespace = domain.NamespaceOf(namespace)
	out := make([]domain.Branch, 0, len(t.st.branches))
	for _, b := range t.st.branches {
		if b.Namespace == namespace {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (t *memTx) PutBranch(_ context.Context, b domain.Branch) error {
	b.Namespace = domain.NamespaceOf(b.Namespace)
	t.st.branches[branchKey(b.Namespace, b.Name)] = b
	return nil
}

// nsKey scopes a node key to its namespace.
func nsKey(namespace, key string) string { return domain.NamespaceOf(namespace) + "\x00" + key }

// branchKey scopes a branch name to its namespace.
func branchKey(namespace, name string) string {
	return domain.NamespaceOf(namespace) + "\x00" + domain.BranchOf(name)
}

func (t *memTx) NodeIDByKey(_ context.Context, namespace, key string) (domain.NodeID, error) {
	id, ok := t.st.keys[nsKey(namespace, key)]
	if !ok {
		return "", fmt.Errorf("node key %q: %w", key, ErrNotFound)
	}
	return id, nil
}

func (t *memTx) NodeByKey(ctx context.Context, namespace, key string) (domain.Node, error) {
	id, ok := t.st.keys[nsKey(namespace, key)]
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

func (t *memTx) LatestNodes(ctx context.Context, namespace, branch string) ([]domain.Node, error) {
	namespace = domain.NamespaceOf(namespace)
	out := make([]domain.Node, 0, len(t.st.versions))
	for id := range t.st.versions {
		n, err := t.LatestOn(ctx, id, branch)
		if err != nil {
			continue
		}
		if n.Namespace == namespace {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (t *memTx) Namespaces(_ context.Context) ([]string, error) {
	seen := map[string]bool{}
	for _, vs := range t.st.versions {
		if len(vs) > 0 {
			seen[vs[0].Namespace] = true
		}
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
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

func (t *memTx) Baselines(_ context.Context, namespace string) ([]domain.Baseline, error) {
	namespace = domain.NamespaceOf(namespace)
	out := make([]domain.Baseline, 0, len(t.st.baselines))
	for _, b := range t.st.baselines {
		if b.Namespace != namespace {
			continue
		}
		b.Nodes = maps.Clone(b.Nodes)
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (t *memTx) Change(_ context.Context, id domain.ChangeID) (domain.Change, error) {
	c, ok := t.st.changes[id]
	if !ok {
		return domain.Change{}, fmt.Errorf("change %s: %w", id, ErrNotFound)
	}
	facts, _ := t.Log(context.Background(), factsFilter(id))
	items, err := itemsOf(facts)
	if err != nil {
		return c, err
	}
	c.Items = items
	c.Nodes = slices.Clone(t.st.nodes[id])
	return c, nil
}

func (t *memTx) Changes(ctx context.Context) ([]domain.Change, error) {
	out := make([]domain.Change, 0, len(t.st.changes))
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
		if _, dup := t.st.keys[nsKey(n.Namespace, n.Key)]; dup && n.Key != "" {
			return fmt.Errorf("node key %q already used: %w", n.Key, ErrConflict)
		}
		if n.Key != "" {
			t.st.keys[nsKey(n.Namespace, n.Key)] = n.ID
		}
	}
	t.st.versions[n.ID] = append(vs, n)
	return nil
}

func (t *memTx) SetNodeProps(_ context.Context, ref domain.NodeRef, props map[string]any) error {
	vs := t.st.versions[ref.ID]
	if ref.Version < 1 || int(ref.Version) > len(vs) {
		return fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	vs[ref.Version-1].Properties = props
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
	b.Namespace = domain.NamespaceOf(b.Namespace)
	for id := range b.Nodes {
		vs := t.st.versions[id]
		if len(vs) == 0 {
			return fmt.Errorf("baseline %s: node %s: %w", b.ID, id, ErrNotFound)
		}
		if vs[0].Namespace != b.Namespace {
			return fmt.Errorf("baseline %s is of namespace %s, node %s is of namespace %s: %w",
				b.ID, b.Namespace, id, vs[0].Namespace, ErrInvalid)
		}
	}
	b.Nodes = maps.Clone(b.Nodes)
	t.st.baselines[b.ID] = b
	return nil
}

func (t *memTx) PutChange(_ context.Context, c domain.Change) error {
	c.Items, c.Nodes = nil, nil // the facts are in the log
	t.st.changes[c.ID] = c
	return nil
}

func (t *memTx) AppendLog(_ context.Context, e domain.LogEntry) (domain.LogEntry, error) {
	if _, ok := t.st.changes[e.Change]; !ok { // a reference to an unknown change, as the SQL foreign key
		return e, fmt.Errorf("change %s: %w", e.Change, ErrInvalid)
	}
	for _, x := range t.st.log {
		if x.ID == e.ID {
			return e, fmt.Errorf("log entry %s: %w", e.ID, ErrConflict)
		}
	}
	e.Payload = slices.Clone(e.Payload)
	e.Seq = int64(len(t.st.log) + 1)
	t.st.log = append(t.st.log, e)
	return e, nil
}

func (t *memTx) LogCounts(_ context.Context, f domain.LogFilter) (map[string]int, error) {
	f.AfterSeq = 0
	out := map[string]int{}
	for _, e := range t.st.log {
		if f.Match(e) {
			out[e.Type]++
		}
	}
	return out, nil
}

func (t *memTx) Log(_ context.Context, f domain.LogFilter) ([]domain.LogEntry, error) {
	var out []domain.LogEntry
	for _, e := range t.st.log {
		if f.Match(e) {
			out = append(out, e)
			if f.Limit > 0 && len(out) == f.Limit {
				break
			}
		}
	}
	return out, nil
}

func (t *memTx) OpenChangeIDs(_ context.Context) ([]domain.ChangeID, error) {
	var out []domain.ChangeID
	for id, c := range t.st.changes {
		if c.Status != domain.ChangeApplied && c.Status != domain.ChangeAbandoned {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return t.st.changes[out[i]].CreatedAt.Before(t.st.changes[out[j]].CreatedAt) })
	return out, nil
}

func (t *memTx) PutChangeImpact(_ context.Context, change domain.ChangeID, cn domain.ChangeImpact) error {
	if _, ok := t.st.changes[change]; !ok {
		return fmt.Errorf("change %s: %w", change, ErrNotFound)
	}
	list := t.st.nodes[change]
	for i := range list {
		if list[i].ID == cn.ID {
			list[i] = cn
			return nil
		}
		if cn.Pre != nil && list[i].Pre != nil && list[i].Pre.ID == cn.Pre.ID && list[i].Flow == cn.Flow && !list[i].Superseded && !cn.Superseded {
			return fmt.Errorf("change impact %s: node already in the change: %w", cn.Key, ErrConflict)
		}
	}
	t.st.nodes[change] = append(list, cn)
	return nil
}

func (t *memTx) ChangeImpacts(_ context.Context, change domain.ChangeID) ([]domain.ChangeImpact, error) {
	return slices.Clone(t.st.nodes[change]), nil
}

func (t *memTx) NodeChangeImpacts(_ context.Context, node domain.NodeID) ([]domain.ChangeID, error) {
	var out []domain.ChangeID
	for id, list := range t.st.nodes {
		for _, cn := range list {
			if (cn.Pre != nil && cn.Pre.ID == node) || (cn.Post != nil && cn.Post.ID == node) {
				out = append(out, id)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return t.st.changes[out[i]].CreatedAt.Before(t.st.changes[out[j]].CreatedAt) })
	return out, nil
}

func (t *memTx) SetNodeOrigin(_ context.Context, ref domain.NodeRef, change domain.ChangeID, cn domain.ChangeImpactID, comment string) error {
	vs := t.st.versions[ref.ID]
	if ref.Version < 1 || int(ref.Version) > len(vs) {
		return fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	vs[ref.Version-1].ChangeID, vs[ref.Version-1].ChangeImpact, vs[ref.Version-1].Comment = change, cn, comment
	return nil
}

func (t *memTx) MoveVersion(_ context.Context, ref domain.NodeRef, to string) error {
	vs := t.st.versions[ref.ID]
	if ref.Version < 1 || int(ref.Version) > len(vs) {
		return fmt.Errorf("node %s: %w", ref, ErrNotFound)
	}
	vs[ref.Version-1].Branch = to
	return nil
}
