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
	BranchHead(ctx context.Context, namespace, name string) (domain.Baseline, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
}

// Baselines identifies the two namespace heads a Snapshot was built from.
type Baselines struct {
	Organisation domain.BaselineID
	Platform     domain.BaselineID
}

// Snapshot is the organisation and the tool layer as of one baseline of each of the organisation and
// platform namespaces: the unit hierarchy (part_of), the MCPs and adapter definitions of the platform
// namespace and the adapters of the organisation namespace.
type Snapshot struct {
	Baselines Baselines
	// Problems lists the nodes that could not be read (malformed adapter, adapter without unit, ...).
	Problems []string

	parent   map[string]string                 // unit -> parent unit
	units    map[string]bool                   // unit keys
	mcps     map[string]mcp.Def                // by name
	defs     map[string]mcp.AdapterDef         // adapter definitions by name
	adapters map[string]map[string]mcp.Adapter // unit -> mcp name -> adapter
}

// Effective is an MCP a unit can use with the adapter that implements it.
type Effective struct {
	MCP     mcp.Def
	Adapter mcp.Adapter // Adapter.Unit is where it is defined
	// Inherited: defined by an ancestor unit.
	Inherited bool
	// Restriction is what the instances of the unit's chain restrict (ADR 0028).
	Restriction mcp.Restriction
}

// Allowed returns the MCP with only the tools the unit may call.
func (e Effective) Allowed() mcp.Def { return e.Restriction.Apply(e.MCP) }

// Usable reports whether the unit can call at least one tool of the MCP.
func (e Effective) Usable() bool { return len(e.Allowed().Tools) > 0 }

// BuildSnapshot reads the objects of the combined organisation and platform baseline graphs.
func BuildSnapshot(baselines Baselines, nodes []domain.Node, links []domain.Link) *Snapshot {
	s := &Snapshot{Baselines: baselines, parent: map[string]string{}, units: map[string]bool{}, mcps: map[string]mcp.Def{}, defs: map[string]mcp.AdapterDef{}, adapters: map[string]map[string]mcp.Adapter{}}
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
		case n.Namespace == mcp.NamespacePlatform && n.Type == mcp.NodeTypeAdapterDef:
			d, err := mcp.AdapterDefFromProps(n.Properties)
			if err == nil {
				err = d.Validate()
			}
			if err != nil {
				s.Problems = append(s.Problems, fmt.Sprintf("%s: %v", n.Key, err))
				continue
			}
			s.defs[d.Name] = d
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

// AdapterDef returns an adapter definition.
func (s *Snapshot) AdapterDef(name string) (mcp.AdapterDef, bool) {
	d, ok := s.defs[name]
	return d, ok
}

// AdapterDefs lists the adapter definitions by name.
func (s *Snapshot) AdapterDefs() []mcp.AdapterDef {
	out := make([]mcp.AdapterDef, 0, len(s.defs))
	for _, d := range s.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Resolve returns the adapter of an MCP for a unit: the one of the nearest unit of its chain that
// names an adapter definition (instances that only restrict are skipped).
func (s *Snapshot) Resolve(unit, mcpName string) (a mcp.Adapter, inherited bool, ok bool) {
	for i, u := range s.Chain(unit) {
		if a, ok := s.adapters[u][mcpName]; ok && a.Implements() {
			return a, i > 0, true
		}
	}
	return mcp.Adapter{}, false, false
}

// Restriction returns what the instances of the unit's chain restrict of an MCP: they all add up,
// so that a unit cannot widen what an ancestor restricted.
func (s *Snapshot) Restriction(unit, mcpName string) mcp.Restriction {
	var r mcp.Restriction
	for _, u := range s.Chain(unit) {
		if a, ok := s.adapters[u][mcpName]; ok {
			r.Add(a)
		}
	}
	return r
}

// Effective lists the MCPs implemented for a unit (an adapter exists in its chain for an MCP that
// exists), each with its resolved adapter and its restriction, by MCP name. A restricted MCP is
// listed even when no tool is left (see Effective.Usable).
func (s *Snapshot) Effective(unit string) []Effective {
	var out []Effective
	for _, d := range s.Defs() {
		if a, inherited, ok := s.Resolve(unit, d.Name); ok {
			out = append(out, Effective{MCP: d, Adapter: a, Inherited: inherited, Restriction: s.Restriction(unit, d.Name)})
		}
	}
	return out
}

// Scopes returns the scope of each MCP of the head of the platform namespace (ADR 0028).
func (d *Directory) Scopes(ctx context.Context) (map[string]string, error) {
	snap, err := d.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, def := range snap.Defs() {
		out[def.Name] = mcp.ScopeOf(def.Scope)
	}
	return out, nil
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
	orgHead, err := d.Graph.BranchHead(ctx, mcp.NamespaceOrganisation, domain.MainBranch)
	if err != nil && !errors.Is(err, graph.ErrNotFound) {
		return nil, err
	}
	platHead, err := d.Graph.BranchHead(ctx, mcp.NamespacePlatform, domain.MainBranch)
	if err != nil && !errors.Is(err, graph.ErrNotFound) {
		return nil, err
	}
	baselines := Baselines{Organisation: orgHead.ID, Platform: platHead.ID}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cur != nil && d.cur.Baselines == baselines {
		return d.cur, nil
	}
	var nodes []domain.Node
	var links []domain.Link
	if orgHead.ID != "" {
		n, l, err := d.Graph.BaselineGraph(ctx, orgHead.ID)
		if err != nil {
			return nil, err
		}
		nodes, links = append(nodes, n...), append(links, l...)
	}
	if platHead.ID != "" {
		n, l, err := d.Graph.BaselineGraph(ctx, platHead.ID)
		if err != nil {
			return nil, err
		}
		nodes, links = append(nodes, n...), append(links, l...)
	}
	d.cur = BuildSnapshot(baselines, nodes, links)
	return d.cur, nil
}
