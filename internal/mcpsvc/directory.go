package mcpsvc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
)

// Graph is the part of the graph the hub reads.
type Graph interface {
	BranchHead(ctx context.Context, name string) (domain.Baseline, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
}

// Snapshot is the organisation and the tool layer as of one baseline: the unit hierarchy
// (part_of), the MCPs of the platform namespace and the adapters of the organisation namespace.
type Snapshot struct {
	Baseline domain.BaselineID
	// Problems lists the nodes that could not be read (malformed adapter, adapter without unit, ...).
	Problems []string

	parent   map[string]string                 // unit -> parent unit
	units    map[string]bool                   // unit keys
	mcps     map[string]mcp.Def                // by name
	adapters map[string]map[string]mcp.Adapter // unit -> mcp name -> adapter
}

// Effective is an MCP a unit can use with the adapter that implements it.
type Effective struct {
	MCP     mcp.Def
	Adapter mcp.Adapter // Adapter.Unit is where it is defined
	// Inherited: defined by an ancestor unit.
	Inherited bool
}

// BuildSnapshot reads the objects of a baseline graph.
func BuildSnapshot(id domain.BaselineID, nodes []domain.Node, links []domain.Link) *Snapshot {
	s := &Snapshot{Baseline: id, parent: map[string]string{}, units: map[string]bool{}, mcps: map[string]mcp.Def{}, adapters: map[string]map[string]mcp.Adapter{}}
	byID := map[domain.NodeID]domain.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	for _, n := range nodes {
		switch {
		case n.Namespace == mcp.NamespaceOrganisation && n.Type == mcp.NodeTypeOrgUnit:
			s.units[n.Key] = true
		case n.Namespace == mcp.NamespacePlatform && n.Type == mcp.NodeTypeMCP:
			d, err := mcp.DefFromProps(n.Properties)
			if err == nil {
				err = d.Validate()
			}
			if err != nil {
				s.Problems = append(s.Problems, fmt.Sprintf("%s: %v", n.Key, err))
				continue
			}
			s.mcps[d.Name] = d
		}
	}
	for _, l := range links {
		from, to := byID[l.From.ID], byID[l.To.ID]
		switch {
		case l.Type == mcp.LinkPartOf && from.Type == mcp.NodeTypeOrgUnit && to.Type == mcp.NodeTypeOrgUnit:
			s.parent[from.Key] = to.Key
		case l.Type == mcp.LinkOwner && from.Namespace == mcp.NamespaceOrganisation && from.Type == mcp.NodeTypeAdapter && to.Type == mcp.NodeTypeOrgUnit:
			a, err := mcp.AdapterFromProps(to.Key, from.Properties)
			if err != nil {
				s.Problems = append(s.Problems, fmt.Sprintf("%s: %v", from.Key, err))
				continue
			}
			if s.adapters[to.Key] == nil {
				s.adapters[to.Key] = map[string]mcp.Adapter{}
			}
			if _, dup := s.adapters[to.Key][a.MCP]; dup {
				s.Problems = append(s.Problems, fmt.Sprintf("%s: unit %s has several adapters for %s", from.Key, to.Key, a.MCP))
				continue
			}
			s.adapters[to.Key][a.MCP] = a
		}
	}
	return s
}

// Chain returns the unit, its ancestors (part_of, nearest first) and, last, the default
// organisation, which every unit inherits from.
func (s *Snapshot) Chain(unit string) []string {
	unit = domain.OrgOf(unit)
	var out []string
	seen := map[string]bool{}
	for cur := unit; cur != "" && !seen[cur]; cur = s.parent[cur] {
		seen[cur] = true
		out = append(out, cur)
	}
	if !seen[domain.DefaultOrg] {
		out = append(out, domain.DefaultOrg)
	}
	return out
}

// Def returns an MCP definition.
func (s *Snapshot) Def(name string) (mcp.Def, bool) {
	d, ok := s.mcps[name]
	return d, ok
}

// Defs lists the MCPs by name.
func (s *Snapshot) Defs() []mcp.Def {
	out := make([]mcp.Def, 0, len(s.mcps))
	for _, d := range s.mcps {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Resolve returns the adapter of an MCP for a unit: the one of the nearest unit of its chain.
func (s *Snapshot) Resolve(unit, mcpName string) (a mcp.Adapter, inherited bool, ok bool) {
	for i, u := range s.Chain(unit) {
		if a, ok := s.adapters[u][mcpName]; ok {
			return a, i > 0, true
		}
	}
	return mcp.Adapter{}, false, false
}

// Effective lists the MCPs a unit can use (an adapter exists in its chain for an MCP that
// exists), each with its resolved adapter, by MCP name.
func (s *Snapshot) Effective(unit string) []Effective {
	var out []Effective
	for _, d := range s.Defs() {
		if a, inherited, ok := s.Resolve(unit, d.Name); ok {
			out = append(out, Effective{MCP: d, Adapter: a, Inherited: inherited})
		}
	}
	return out
}

// Directory reads the snapshot of the head of the main branch, rebuilding it only when
// the head moved.
type Directory struct {
	Graph Graph

	mu  sync.Mutex
	cur *Snapshot
}

// Snapshot returns the current snapshot. A graph without any baseline yields an empty one.
func (d *Directory) Snapshot(ctx context.Context) (*Snapshot, error) {
	head, err := d.Graph.BranchHead(ctx, domain.MainBranch)
	if errors.Is(err, graph.ErrNotFound) {
		return BuildSnapshot("", nil, nil), nil
	}
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cur != nil && d.cur.Baseline == head.ID {
		return d.cur, nil
	}
	nodes, links, err := d.Graph.BaselineGraph(ctx, head.ID)
	if err != nil {
		return nil, err
	}
	d.cur = BuildSnapshot(head.ID, nodes, links)
	return d.cur, nil
}
