package mcpsvc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/zimwip/goap/pkg/adapter"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
)

// Graph is the part of the graph the hub reads.
type Graph interface {
	BranchHead(ctx context.Context, namespace, name string) (domain.Baseline, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
	// Structures names the organisation (ADR 0054): the snapshot never names its types itself.
	Structures(ctx context.Context) (domain.Structures, error)
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

	org      *domain.Hierarchy                      // the unit hierarchy of the organisation structure (ADR 0054)
	units    map[string]bool                        // unit keys
	mcps     map[string]mcp.Def                     // by name
	defs     map[string]adapter.Def                 // adapter definitions by name
	adapters map[string]map[string]adapter.Instance // unit -> mcp name -> adapter
}

// Effective is an MCP a unit can use with the adapter that implements it.
type Effective struct {
	MCP     mcp.Def
	Adapter adapter.Instance // Adapter.Unit is where it is defined
	// Inherited: defined by an ancestor unit.
	Inherited bool
	// Restriction is what the instances of the unit's chain restrict (ADR 0028).
	Restriction adapter.Restriction
}

// Allowed returns the MCP with only the tools the unit may call.
func (e Effective) Allowed() mcp.Def { return e.Restriction.Apply(e.MCP) }

// Usable reports whether the unit can call at least one tool of the MCP.
func (e Effective) Usable() bool { return len(e.Allowed().Tools) > 0 }

// BuildSnapshot reads the objects of the combined organisation and platform baseline graphs; the units are the nodes
// of the organisation structure of st, their hierarchy its parent links (ADR 0054).
func BuildSnapshot(st domain.Structures, baselines Baselines, nodes []domain.Node, links []domain.Link) *Snapshot {
	org := domain.StructureOrganisation
	s := &Snapshot{Baselines: baselines, org: st.Hierarchy(org, nodes, links), units: map[string]bool{}, mcps: map[string]mcp.Def{}, defs: map[string]adapter.Def{}, adapters: map[string]map[string]adapter.Instance{}}
	byID := map[domain.NodeID]domain.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	for _, n := range nodes {
		switch {
		case n.Namespace == st.Organisation.Namespace && st.In(org, n.Type):
			s.units[n.Key] = true
		case n.Namespace == domain.NamespacePlatform && n.Type == mcp.NodeTypeMCP:
			d, err := mcp.DefFromProps(n.Properties)
			if err == nil {
				err = d.Validate()
			}
			if err != nil {
				s.Problems = append(s.Problems, fmt.Sprintf("%s: %v", n.Key, err))
				continue
			}
			s.mcps[d.Name] = d
		case n.Namespace == domain.NamespacePlatform && n.Type == domain.TypeAdapterDef:
			d, err := adapter.DefFromProps(n.Properties)
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
	// an adapter instance is the unit's that owns it (ADR 0054: the owner of the version)
	for _, n := range nodes {
		if n.Deleted || n.Namespace != domain.NamespaceOrganisation || n.Type != domain.TypeAdapter {
			continue
		}
		unit, ok := byID[n.Owner]
		if !ok || !st.In(org, unit.Type) {
			s.Problems = append(s.Problems, fmt.Sprintf("%s: its owner %s is not a unit of the organisation", n.Key, n.Owner))
			continue
		}
		a, err := adapter.FromProps(unit.Key, n.Properties)
		if err != nil {
			s.Problems = append(s.Problems, fmt.Sprintf("%s: %v", n.Key, err))
			continue
		}
		if s.adapters[unit.Key] == nil {
			s.adapters[unit.Key] = map[string]adapter.Instance{}
		}
		if _, dup := s.adapters[unit.Key][a.MCP]; dup {
			s.Problems = append(s.Problems, fmt.Sprintf("%s: unit %s has several adapters for %s", n.Key, unit.Key, a.MCP))
			continue
		}
		s.adapters[unit.Key][a.MCP] = a
	}
	return s
}

// Chain returns the unit, its ancestors (part_of, nearest first) and, last, the default
// organisation, which every unit inherits from.
func (s *Snapshot) Chain(unit string) []string {
	return s.org.Chain(unit)
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
func (s *Snapshot) AdapterDef(name string) (adapter.Def, bool) {
	d, ok := s.defs[name]
	return d, ok
}

// AdapterDefs lists the adapter definitions by name.
func (s *Snapshot) AdapterDefs() []adapter.Def {
	out := make([]adapter.Def, 0, len(s.defs))
	for _, d := range s.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Resolve returns the adapter of an MCP for a unit: the one of the nearest unit of its chain that
// names an adapter definition (instances that only restrict are skipped).
func (s *Snapshot) Resolve(unit, mcpName string) (a adapter.Instance, inherited bool, ok bool) {
	for i, u := range s.Chain(unit) {
		if a, ok := s.adapters[u][mcpName]; ok && a.Implements() {
			return a, i > 0, true
		}
	}
	return adapter.Instance{}, false, false
}

// Restriction returns what the instances of the unit's chain restrict of an MCP: they all add up,
// so that a unit cannot widen what an ancestor restricted.
func (s *Snapshot) Restriction(unit, mcpName string) adapter.Restriction {
	var r adapter.Restriction
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
	// st are the structures of the graph, asked once (ADR 0054: tagged by a frozen built-in domain)
	st *domain.Structures
}

// structures asks the graph for its structures, once it answers.
func (d *Directory) structures(ctx context.Context) (domain.Structures, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.st == nil {
		st, err := d.Graph.Structures(ctx)
		if err != nil {
			return st, err
		}
		d.st = &st
	}
	return *d.st, nil
}

// Snapshot returns the current snapshot. A graph without any baseline yields an empty one.
func (d *Directory) Snapshot(ctx context.Context) (*Snapshot, error) {
	st, err := d.structures(ctx)
	if err != nil {
		return nil, err
	}
	orgHead, err := d.Graph.BranchHead(ctx, st.Organisation.Namespace, domain.MainBranch)
	if err != nil && !errors.Is(err, graph.ErrNotFound) {
		return nil, err
	}
	platHead, err := d.Graph.BranchHead(ctx, domain.NamespacePlatform, domain.MainBranch)
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
	d.cur = BuildSnapshot(st, baselines, nodes, links)
	return d.cur, nil
}
