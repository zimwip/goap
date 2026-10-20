// Package graphsnap reads a typed view of the graph at the head of the main branch, rebuilding it only
// when the head moved. Services that keep configuration as graph nodes (access, models, ...) build their
// snapshot with it.
package graphsnap

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
)

// Graph is the part of the graph a snapshot reads.
type Graph interface {
	BranchHead(ctx context.Context, namespace, name string) (domain.Baseline, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
}

// Heads is the one reader of the heads of the main branches in a process (ADR 0145): one Head per namespace, read
// whole once then moved by the nodes landed since, looked at most once per TTL, whatever the number of views built on
// it. Every view of the head of a process (access, hub, models, methodologies) reads through the process's Heads, so a
// process holds and moves each namespace once. A head is read with its suspect links; a view asks for them or not.
type Heads struct {
	Graph Graph
	// TTL is how often a head is looked at: one second when zero, at every read when negative.
	TTL time.Duration

	mu sync.Mutex
	ns map[string]*nsHead
}

type nsHead struct {
	mu      sync.Mutex
	head    Head
	looked  bool
	checked time.Time
}

// State is the state of the head of a namespace's main as a view reads it.
type State struct {
	Namespace string
	// ID is the baseline of the head ("": the namespace holds none yet, Nodes and Links are empty).
	ID    domain.BaselineID
	Nodes []domain.Node
	Links []domain.Link
}

// NewHeads is the reader of the heads of g.
func NewHeads(g Graph) *Heads { return &Heads{Graph: g} }

func (h *Heads) of(namespace string) *nsHead {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ns == nil {
		h.ns = map[string]*nsHead{}
	}
	n := h.ns[namespace]
	if n == nil {
		n = &nsHead{}
		h.ns[namespace] = n
	}
	return n
}

// Moved tells the reader the head of a namespace moved (an event of the graph): the next read looks at it, whatever
// the TTL.
func (h *Heads) Moved(namespace string) {
	n := h.of(namespace)
	n.mu.Lock()
	n.checked = time.Time{}
	n.mu.Unlock()
}

// look brings the head of namespace to the graph's (now when fresh, else once per TTL) and returns its baseline. When
// the graph cannot be read the head stays where it was and the error is returned with its baseline.
func (h *Heads) look(ctx context.Context, n *nsHead, namespace string, fresh bool) (domain.BaselineID, error) {
	ttl := h.TTL
	if ttl == 0 {
		ttl = time.Second
	}
	if !fresh && n.looked && ttl > 0 && time.Since(n.checked) < ttl {
		return n.head.ID, nil
	}
	b, err := h.Graph.BranchHead(ctx, namespace, domain.MainBranch)
	if errors.Is(err, graphapi.ErrNotFound) {
		b, err = domain.Baseline{}, nil
	}
	if err != nil {
		return n.head.ID, err
	}
	if err := n.head.Move(ctx, h.Graph, b.ID, true); err != nil {
		return n.head.ID, err
	}
	n.looked, n.checked = true, time.Now()
	return n.head.ID, nil
}

// Head returns the baseline of the head of namespace's main, looking at the graph now when fresh, else once per TTL.
func (h *Heads) Head(ctx context.Context, namespace string, fresh bool) (domain.BaselineID, error) {
	n := h.of(namespace)
	n.mu.Lock()
	defer n.mu.Unlock()
	return h.look(ctx, n, namespace, fresh)
}

// State returns the state of the head of namespace's main, with its suspect links when suspects.
func (h *Heads) State(ctx context.Context, namespace string, suspects, fresh bool) (State, error) {
	n := h.of(namespace)
	n.mu.Lock()
	defer n.mu.Unlock()
	_, err := h.look(ctx, n, namespace, fresh)
	nodes, links := n.head.State(suspects)
	return State{Namespace: namespace, ID: n.head.ID, Nodes: nodes, Links: links}, err
}

// View is a typed view of the heads of some namespaces, built again only when one of them moved (ADR 0145). When the
// graph cannot be read the last view keeps serving and the error is returned with it.
type View[T any] struct {
	Heads      *Heads
	Namespaces []string
	// Suspects gives the views the suspect links too (a view following references: a sub-project keeps its parent once
	// the parent is edited).
	Suspects bool
	// Build makes the view of the states of the namespaces, in the order of Namespaces.
	Build func(states []State) T

	mu   sync.Mutex
	have bool
	cur  T
	ids  []domain.BaselineID
}

// Get returns the current view and the baselines it was built from, in the order of Namespaces.
func (v *View[T]) Get(ctx context.Context) (T, []domain.BaselineID, error) { return v.get(ctx, false) }

// Fresh is Get looking at the heads now, not once per TTL (before a write that builds on what it reads).
func (v *View[T]) Fresh(ctx context.Context) (T, []domain.BaselineID, error) { return v.get(ctx, true) }

// Peek returns the view last built, with no look at the graph: false before the first Get.
func (v *View[T]) Peek() (T, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.cur, v.have
}

func (v *View[T]) get(ctx context.Context, fresh bool) (T, []domain.BaselineID, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	ids := make([]domain.BaselineID, len(v.Namespaces))
	for i, ns := range v.Namespaces {
		id, err := v.Heads.Head(ctx, ns, fresh)
		if err != nil {
			return v.cur, v.ids, err
		}
		ids[i] = id
	}
	if v.have && slices.Equal(ids, v.ids) {
		return v.cur, v.ids, nil
	}
	states := make([]State, len(v.Namespaces))
	for i, ns := range v.Namespaces {
		// no look again: the state of the head just looked at, or of a later one another view moved it to
		st, err := v.Heads.State(ctx, ns, v.Suspects, false)
		if err != nil {
			return v.cur, v.ids, err
		}
		states[i], ids[i] = st, st.ID
	}
	v.cur, v.ids, v.have = v.Build(states), ids, true
	return v.cur, v.ids, nil
}

// Cache is a View of the head of one namespace (Heads: the process's reader; nil, a reader of its own over Graph
// looking once per TTL).
type Cache[T any] struct {
	Graph     Graph
	Namespace string
	TTL       time.Duration
	// Heads is the reader of the heads the cache shares (nil: its own over Graph).
	Heads *Heads
	// Build makes the view of a baseline; the graph without any baseline yields Build("", nil, nil).
	Build func(id domain.BaselineID, nodes []domain.Node, links []domain.Link) T
	// Suspects reads the suspect links too: a view following references.
	Suspects bool

	once sync.Once
	view View[T]
}

func (c *Cache[T]) init() *View[T] {
	c.once.Do(func() {
		heads := c.Heads
		if heads == nil {
			heads = &Heads{Graph: c.Graph, TTL: c.TTL}
		}
		c.view = View[T]{Heads: heads, Namespaces: []string{c.Namespace}, Suspects: c.Suspects,
			Build: func(s []State) T { return c.Build(s[0].ID, s[0].Nodes, s[0].Links) }}
	})
	return &c.view
}

// Get returns the current snapshot and the baseline it was built from.
func (c *Cache[T]) Get(ctx context.Context) (T, domain.BaselineID, error) {
	return first(c.init().Get(ctx))
}

// Fresh is Get without the TTL: the head is looked at now (before a write that builds on what it reads).
func (c *Cache[T]) Fresh(ctx context.Context) (T, domain.BaselineID, error) {
	return first(c.init().Fresh(ctx))
}

// Peek returns the snapshot last built, with no look at the graph: false before the first Get.
func (c *Cache[T]) Peek() (T, bool) { return c.init().Peek() }

func first[T any](v T, ids []domain.BaselineID, err error) (T, domain.BaselineID, error) {
	if len(ids) == 0 {
		return v, "", err
	}
	return v, ids[0], err
}

// SuspectReader is a graph that gives the suspect links of a baseline (*graph.Graph, the graph service client): the
// links whose source is in the baseline and whose target is there at another version.
type SuspectReader interface {
	SuspectLinks(ctx context.Context, id domain.BaselineID) ([]domain.Link, error)
}

// suspectsAtOnce reads a baseline and its suspect links in one call (the graph service client).
type suspectsAtOnce interface {
	BaselineGraphWithSuspects(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, []domain.Link, error)
}

// BaselineWithSuspects reads a baseline with its suspect links too. A link names the version its target had when it
// was written, and editing the target leaves it in place, suspect: it is a reference all the same (a sub-project's
// parent, the project of an Assignment, ADR 0120). A graph that gives no suspect links gives the baseline alone.
func BaselineWithSuspects(ctx context.Context, g Graph, id domain.BaselineID) ([]domain.Node, []domain.Link, error) {
	if r, ok := g.(suspectsAtOnce); ok {
		nodes, links, sus, err := r.BaselineGraphWithSuspects(ctx, id)
		return nodes, append(links, sus...), err
	}
	nodes, links, err := g.BaselineGraph(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if sr, ok := g.(SuspectReader); ok {
		sus, err := sr.SuspectLinks(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		links = append(links, sus...)
	}
	return nodes, links, nil
}

// DeltaReader is a graph that reads what moved between two baselines of a branch (ADR 0143 §4: *graph.Graph, the graph
// service client); refused with graphapi.ErrInvalid when it cannot, and the head is then read whole.
type DeltaReader interface {
	BaselineDelta(ctx context.Context, from, to domain.BaselineID) (graphapi.BaselineDelta, error)
}

// Head is the state of the head of a branch as a snapshot follows it (ADR 0143 §4): read whole once, then moved by the
// nodes that landed since, so that a commit costs a snapshot the nodes it touched rather than the namespace. It keeps
// the links whose target it holds, suspect ones included when read with them; State gives them as a baseline read gives
// them.
type Head struct {
	// ID is the baseline the state is of ("": none read yet, or the empty state).
	ID       domain.BaselineID
	suspects bool
	nodes    map[domain.NodeID]domain.Node
	out      map[domain.NodeID][]domain.Link
}

// Move brings the state to the baseline to: from the nodes moved since the state's baseline when g reads deltas,
// else (or when the delta is refused) by reading to whole, with its suspect links when suspects.
func (h *Head) Move(ctx context.Context, g Graph, to domain.BaselineID, suspects bool) error {
	if to == h.ID && h.nodes != nil && h.suspects == suspects {
		return nil
	}
	if to == "" {
		h.ID, h.suspects, h.nodes, h.out = "", suspects, map[domain.NodeID]domain.Node{}, map[domain.NodeID][]domain.Link{}
		return nil
	}
	if dr, ok := g.(DeltaReader); ok && h.ID != "" && h.nodes != nil && h.suspects == suspects {
		d, err := dr.BaselineDelta(ctx, h.ID, to)
		if err == nil {
			h.apply(d)
			h.ID = to
			return nil
		}
		if !errors.Is(err, graphapi.ErrInvalid) {
			return err
		}
	}
	read := g.BaselineGraph
	if suspects {
		read = func(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error) {
			return BaselineWithSuspects(ctx, g, id)
		}
	}
	nodes, links, err := read(ctx, to)
	if err != nil {
		return err
	}
	h.nodes, h.out = make(map[domain.NodeID]domain.Node, len(nodes)), map[domain.NodeID][]domain.Link{}
	for _, n := range nodes {
		h.nodes[n.ID] = n
	}
	for _, l := range links {
		h.out[l.From.ID] = append(h.out[l.From.ID], l)
	}
	h.ID, h.suspects = to, suspects
	return nil
}

// apply lays a delta over the state: the moved nodes take their new version and its outgoing links, the removed ones go.
func (h *Head) apply(d graphapi.BaselineDelta) {
	for _, id := range d.Removed {
		delete(h.nodes, id)
		delete(h.out, id)
	}
	for _, n := range d.Nodes {
		h.nodes[n.ID] = n
		delete(h.out, n.ID)
	}
	for _, l := range d.Links {
		if _, ok := h.nodes[l.To.ID]; ok {
			h.out[l.From.ID] = append(h.out[l.From.ID], l)
		}
	}
}

// State returns the nodes of the state by key and the links between them by id: those whose target is at the version
// the state holds, and the suspect ones too when suspects and the state was read with them.
func (h *Head) State(suspects bool) ([]domain.Node, []domain.Link) {
	if len(h.nodes) == 0 {
		return nil, nil
	}
	nodes := slices.SortedFunc(maps.Values(h.nodes), func(a, b domain.Node) int { return cmp.Compare(a.Key, b.Key) })
	var links []domain.Link
	for from, out := range h.out {
		if _, ok := h.nodes[from]; !ok {
			continue
		}
		for _, l := range out {
			if to, ok := h.nodes[l.To.ID]; ok && (suspects && h.suspects || to.Version == l.To.Version) {
				links = append(links, l)
			}
		}
	}
	slices.SortFunc(links, func(a, b domain.Link) int { return cmp.Compare(a.ID, b.ID) })
	return nodes, links
}
