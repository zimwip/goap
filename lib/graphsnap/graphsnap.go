// Package graphsnap is the one way the services read the graph at a commit (Reader.At, ADR 0145) and the typed views
// they build of the heads of the main branches (View): access, hub, models, methodologies.
package graphsnap

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"sync"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
)

// Graph is the part of the graph a snapshot reads.
type Graph interface {
	BranchHead(ctx context.Context, namespace, name string) (domain.Baseline, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
}

// Reader is the one way the services read the graph (ADR 0145): At gives the state of a namespace at a commit (a
// baseline), Head names the commit at the head of its main branch. A commit never changes, so the state at one is a
// pure function of it: the reader keeps the last state it gave per namespace and reaches the next commit asked from it
// by the nodes committed in between (Graph.BaselineDelta), else by reading that commit whole. Nothing is kept about the
// head: a reader asks the graph for it each time.
type Reader struct {
	Graph Graph

	mu sync.Mutex
	ns map[string]*nsState
}

type nsState struct {
	mu   sync.Mutex
	head Head
}

// State is the state of a namespace at a commit as a view reads it.
type State struct {
	Namespace string
	// ID is the commit ("": the namespace holds none yet, Nodes and Links are empty).
	ID    domain.BaselineID
	Nodes []domain.Node
	Links []domain.Link
}

// NewReader is the reader of g.
func NewReader(g Graph) *Reader { return &Reader{Graph: g} }

func (r *Reader) of(namespace string) *nsState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ns == nil {
		r.ns = map[string]*nsState{}
	}
	n := r.ns[namespace]
	if n == nil {
		n = &nsState{}
		r.ns[namespace] = n
	}
	return n
}

// Head returns the commit at the head of namespace's main ("": none yet).
func (r *Reader) Head(ctx context.Context, namespace string) (domain.BaselineID, error) {
	b, err := r.Graph.BranchHead(ctx, namespace, domain.MainBranch)
	if errors.Is(err, graphapi.ErrNotFound) {
		return "", nil
	}
	return b.ID, err
}

// At returns the state of namespace at the commit id, with its suspect links when suspects.
func (r *Reader) At(ctx context.Context, namespace string, id domain.BaselineID, suspects bool) (State, error) {
	n := r.of(namespace)
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.head.Move(ctx, r.Graph, id, true); err != nil {
		return State{}, err
	}
	nodes, links := n.head.State(suspects)
	return State{Namespace: namespace, ID: id, Nodes: nodes, Links: links}, nil
}

// View is a typed view of the heads of some namespaces (ADR 0145): each Get asks the reader for the heads, and builds
// the view again from their states only when one of them is another commit than the one it was built from. When the
// graph cannot be read the last view is returned with the error.
type View[T any] struct {
	Reader     *Reader
	Namespaces []string
	// Suspects gives the views the suspect links too (a view following references: a sub-project keeps its parent once
	// the parent is edited).
	Suspects bool
	// Build makes the view of the states of the namespaces, in the order of Namespaces.
	Build func(states []State) T

	// building serializes the Gets, which read the graph; mu guards only the view built, so that Peek never waits for
	// a read of the graph: Peek is called inside a transaction of the graph (a facet of the blackboard), and the
	// local mode's single connection would otherwise deadlock it with a Get waiting for that connection
	building sync.Mutex
	mu       sync.Mutex
	have     bool
	cur      T
	ids      []domain.BaselineID
}

// Get returns the view of the heads and the commits it was built from, in the order of Namespaces.
func (v *View[T]) Get(ctx context.Context) (T, []domain.BaselineID, error) {
	v.building.Lock()
	defer v.building.Unlock()
	cur, have, built := v.last()
	ids := make([]domain.BaselineID, len(v.Namespaces))
	for i, ns := range v.Namespaces {
		id, err := v.Reader.Head(ctx, ns)
		if err != nil {
			return cur, built, err
		}
		ids[i] = id
	}
	if have && slices.Equal(ids, built) {
		return cur, built, nil
	}
	states := make([]State, len(v.Namespaces))
	for i, ns := range v.Namespaces {
		st, err := v.Reader.At(ctx, ns, ids[i], v.Suspects)
		if err != nil {
			return cur, built, err
		}
		states[i] = st
	}
	cur = v.Build(states)
	v.mu.Lock()
	v.cur, v.ids, v.have = cur, ids, true
	v.mu.Unlock()
	return cur, ids, nil
}

func (v *View[T]) last() (T, bool, []domain.BaselineID) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.cur, v.have, v.ids
}

// Peek returns the view last built, without asking the graph nor waiting for a Get: false before the first Get.
func (v *View[T]) Peek() (T, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.cur, v.have
}

// One is the view of the head of one namespace: Build takes its commit, nodes and links.
func One[T any](r *Reader, namespace string, suspects bool, build func(id domain.BaselineID, nodes []domain.Node, links []domain.Link) T) *View[T] {
	return &View[T]{Reader: r, Namespaces: []string{namespace}, Suspects: suspects,
		Build: func(s []State) T { return build(s[0].ID, s[0].Nodes, s[0].Links) }}
}

// First is the commit of a view of one namespace.
func First(ids []domain.BaselineID) domain.BaselineID {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
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
// nodes that committed since, so that a commit costs a snapshot the nodes it touched rather than the namespace. It keeps
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
