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

// Cache holds the snapshot of the head of Namespace's main. The head is looked at most once per TTL
// (one second by default). When the graph cannot be read the last snapshot keeps serving and the error
// is returned with it.
type Cache[T any] struct {
	Graph     Graph
	Namespace string
	TTL       time.Duration
	// Build makes the view of a baseline; the graph without any baseline yields Build("", nil, nil).
	Build func(id domain.BaselineID, nodes []domain.Node, links []domain.Link) T
	// Suspects reads the suspect links too (BaselineWithSuspects): a view following references.
	Suspects bool

	mu      sync.Mutex
	head    Head
	have    bool
	cur     T
	id      domain.BaselineID
	checked time.Time
}

// Get returns the current snapshot and the baseline it was built from.
func (c *Cache[T]) Get(ctx context.Context) (T, domain.BaselineID, error) { return c.get(ctx, false) }

// Peek returns the snapshot last built, with no look at the graph: false before the first Get.
func (c *Cache[T]) Peek() (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur, c.have
}

// Fresh is Get without the TTL: the head is looked at now (before a write that builds on what it reads).
func (c *Cache[T]) Fresh(ctx context.Context) (T, domain.BaselineID, error) { return c.get(ctx, true) }

func (c *Cache[T]) get(ctx context.Context, fresh bool) (T, domain.BaselineID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := c.TTL
	if ttl == 0 {
		ttl = time.Second
	}
	if !fresh && c.have && time.Since(c.checked) < ttl {
		return c.cur, c.id, nil
	}
	head, err := c.Graph.BranchHead(ctx, c.Namespace, domain.MainBranch)
	if errors.Is(err, graphapi.ErrNotFound) {
		c.cur, c.id, c.have, c.checked = c.Build("", nil, nil), "", true, time.Now()
		return c.cur, c.id, nil
	}
	if err != nil {
		return c.cur, c.id, err
	}
	c.checked = time.Now()
	if c.have && c.id == head.ID {
		return c.cur, c.id, nil
	}
	if err := c.head.Move(ctx, c.Graph, head.ID, c.Suspects); err != nil {
		return c.cur, c.id, err
	}
	nodes, links := c.head.State()
	c.cur, c.id, c.have = c.Build(head.ID, nodes, links), head.ID, true
	return c.cur, c.id, nil
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
// the links whose target it holds, suspect ones included; State gives them as a baseline read gives them.
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
// the state holds, and the suspect ones too when the state was read with them.
func (h *Head) State() ([]domain.Node, []domain.Link) {
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
			if to, ok := h.nodes[l.To.ID]; ok && (h.suspects || to.Version == l.To.Version) {
				links = append(links, l)
			}
		}
	}
	slices.SortFunc(links, func(a, b domain.Link) int { return cmp.Compare(a.ID, b.ID) })
	return nodes, links
}
