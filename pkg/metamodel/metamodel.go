// Package metamodel models methodologies as versioned elements of the domain
// graph (ADR 0011): every published methodology is projected onto nodes
// (methodology, agents, actions, goals, conditions, triggers, node types)
// and links, through an ordinary change applied on main. Each publication
// creates new versions of the elements that changed, so executions (journal
// records name the methodology version, agent and action) and improvement
// proposals can reference the exact definition they are about.
//
// NodeType is the exception to the mirror: it is the metadata layer of the
// domain graph (ADR 0012). Once a NodeType node exists in the graph, Sync
// never updates or deletes it again — the registry's declared node types are
// only its bootstrap seed and a compile-time validation schema from then on.
// Data-layer nodes reference their NodeType by a LinkInstanceOf edge.
//
// A methodology that references a shared Domain (Methodology.DomainRef) does
// not own NodeType nodes: they belong to the domain, one node per type, keyed
// "D:<domain>/nodetype/<name>" and shared by every methodology (and every data
// node) that uses the domain. The methodology's root node carries its
// domainRef, which is how graph-side lookups find the type namespace.
package metamodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

// Node types of the methodology model.
const (
	TypeMethodology = "Methodology"
	TypeAgent       = "Agent"
	TypeAction      = "Action"
	TypeGoal        = "Goal"
	TypeCondition   = "Condition"
	TypeTrigger     = "Trigger"
	TypeNodeType    = "NodeType"
)

// Link types of the methodology model.
const (
	LinkContains    = "contains"    // Methodology → element
	LinkUses        = "uses"        // Agent → Action (admissible)
	LinkPursues     = "pursues"     // Agent → Goal
	LinkHas         = "has"         // Agent → Trigger
	LinkRequires    = "requires"    // Action / Goal → Condition (pre-condition)
	LinkAchieves    = "achieves"    // Action → Condition (effect)
	LinkSpecializes = "specializes" // Action → Action
	LinkExtends     = "extends"     // NodeType → NodeType (subtyping, metadata layer)
	LinkInstanceOf  = "instanceOf"  // data node → NodeType (metadata layer)
)

// Key returns the domain key of an element: "M:<methodology>" for the
// methodology, "M:<methodology>/<kind>/<name>" for its elements (kind is the
// lower-case node type).
func Key(meth, typ, name string) string {
	if typ == TypeMethodology {
		return "M:" + meth
	}
	return "M:" + meth + "/" + strings.ToLower(typ) + "/" + name
}

// DomainKey returns the key of a NodeType owned by a shared domain.
func DomainKey(domainName, name string) string {
	return "D:" + domainName + "/" + strings.ToLower(TypeNodeType) + "/" + name
}

// domainNamespace is the type namespace of a shared domain (see typeKey).
func domainNamespace(domainName string) string { return "D:" + domainName }

// typeKey returns the key of the NodeType called name in a type namespace: a
// methodology name (own types) or "D:<domain>" (shared domain). Names cannot
// contain ':', so the two are never confused.
func typeKey(ns, name string) string {
	if d, ok := strings.CutPrefix(ns, "D:"); ok {
		return DomainKey(d, name)
	}
	return Key(ns, TypeNodeType, name)
}

// TypeName returns the name of a NodeType key of the type namespace ns.
func TypeName(key, ns string) (string, bool) {
	prefix := typeKey(ns, "")
	if name, ok := strings.CutPrefix(key, prefix); ok && name != "" {
		return name, true
	}
	return "", false
}

// TypeNamespace returns where the NodeTypes of a methodology live: the
// methodology itself, or the shared domain its root node references (props
// "domainRef", "<name>[@<version>]"). Pass the nodes of a baseline; without a
// methodology root node the methodology owns its types.
func TypeNamespace(nodes []domain.Node, meth string) string {
	root := Key(meth, TypeMethodology, "")
	for _, n := range nodes {
		if n.Key != root {
			continue
		}
		if ref, _ := n.Properties["domainRef"].(string); ref != "" {
			name, _, _ := strings.Cut(ref, "@")
			return domainNamespace(name)
		}
		break
	}
	return meth
}

// ParseKey splits an element key (ok is false for other nodes).
func ParseKey(key string) (meth, kind, name string, ok bool) {
	rest, ok := strings.CutPrefix(key, "M:")
	if !ok {
		return "", "", "", false
	}
	parts := strings.SplitN(rest, "/", 3)
	switch len(parts) {
	case 1:
		return parts[0], strings.ToLower(TypeMethodology), parts[0], true
	case 3:
		return parts[0], parts[1], parts[2], true
	}
	return "", "", "", false
}

// Element is a projected node.
type Element struct {
	Key   string
	Type  string
	Props map[string]any
}

// Edge is a projected link between element keys.
type Edge struct {
	Type, From, To string
}

// props normalizes a definition into JSON-compatible properties (empty
// values dropped), so that projections compare with stored properties.
func props(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for k, x := range m {
		if empty(x) {
			delete(m, k)
		}
	}
	return m
}

func empty(x any) bool {
	switch v := x.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case float64:
		return v == 0
	case bool:
		return !v
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

// Project returns the elements and links of a methodology.
func Project(m *methodology.Methodology) ([]Element, []Edge) {
	name := m.Name
	root := Key(name, TypeMethodology, "")
	els := []Element{{Key: root, Type: TypeMethodology, Props: props(map[string]any{"name": m.Name, "version": m.Version, "description": m.Description, "domainRef": m.DomainRef})}}
	var edges []Edge
	add := func(typ, n string, v any) string {
		k := Key(name, typ, n)
		els = append(els, Element{Key: k, Type: typ, Props: props(v)})
		edges = append(edges, Edge{LinkContains, root, k})
		return k
	}
	conditions := map[string]bool{}
	for _, c := range m.Conditions {
		add(TypeCondition, c.Name, c)
		conditions[c.Name] = true
	}
	cond := func(from, typ string, keys map[string]bool) {
		for _, k := range slices.Sorted(maps.Keys(keys)) {
			if conditions[k] {
				edges = append(edges, Edge{typ, from, Key(name, TypeCondition, k)})
			}
		}
	}
	if m.DomainRef == "" { // otherwise the node types belong to the shared domain (ProjectDomain)
		types := map[string]bool{}
		for _, t := range m.Domain.NodeTypes {
			types[t.Name] = true
		}
		for _, t := range m.Domain.NodeTypes {
			k := add(TypeNodeType, t.Name, t)
			if t.Extends != "" && types[t.Extends] {
				edges = append(edges, Edge{LinkExtends, k, Key(name, TypeNodeType, t.Extends)})
			}
		}
	}
	actions := map[string]methodology.Action{}
	for _, a := range m.Actions {
		actions[a.Name] = a
	}
	for _, a := range m.Actions {
		k := add(TypeAction, a.Name, a)
		cond(k, LinkRequires, a.Pre)
		cond(k, LinkAchieves, a.Effects)
		if target, local := a.SpecializedAction(name); a.IsSpecialization() {
			if local {
				edges = append(edges, Edge{LinkSpecializes, k, Key(name, TypeAction, target)})
			} else {
				other, _, _ := strings.Cut(a.Specializes, "/")
				edges = append(edges, Edge{LinkSpecializes, k, Key(other, TypeAction, target)})
			}
		}
	}
	for _, g := range m.Goals {
		k := add(TypeGoal, g.Name, g)
		cond(k, LinkRequires, g.Pre)
	}
	for _, ag := range m.Agents {
		def := ag
		def.Triggers = nil
		k := add(TypeAgent, ag.Name, def)
		uses := ag.Actions
		if len(uses) == 0 {
			for _, a := range m.Actions {
				if !a.IsSpecialization() {
					uses = append(uses, a.Name)
				}
			}
		}
		for _, a := range uses {
			edges = append(edges, Edge{LinkUses, k, Key(name, TypeAction, a)})
		}
		goals := ag.Goals
		if len(goals) == 0 {
			for _, g := range m.Goals {
				goals = append(goals, g.Name)
			}
		}
		for _, g := range goals {
			edges = append(edges, Edge{LinkPursues, k, Key(name, TypeGoal, g)})
		}
		for _, tr := range ag.Triggers {
			tk := add(TypeTrigger, ag.Name+"."+tr.Name, tr)
			edges = append(edges, Edge{LinkHas, k, tk})
		}
	}
	return els, edges
}

// ProjectDomain returns the NodeType nodes and extends links of a shared domain.
func ProjectDomain(d *methodology.Domain) ([]Element, []Edge) {
	types := map[string]bool{}
	for _, t := range d.NodeTypes {
		types[t.Name] = true
	}
	var els []Element
	var edges []Edge
	for _, t := range d.NodeTypes {
		k := DomainKey(d.Name, t.Name)
		els = append(els, Element{Key: k, Type: TypeNodeType, Props: props(t)})
		if t.Extends != "" && types[t.Extends] {
			edges = append(edges, Edge{LinkExtends, k, DomainKey(d.Name, t.Extends)})
		}
	}
	return els, edges
}

// Graph is what the projection needs from the graph (*graph.Graph and the
// graph service client implement it).
type Graph interface {
	BranchHead(ctx context.Context, name string) (domain.Baseline, error)
	CreateBaseline(ctx context.Context, name string, nodes []domain.NodeRef) (domain.Baseline, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
	CreateChange(ctx context.Context, in graph.NewChange) (domain.ChangeSet, error)
	AddItems(ctx context.Context, id domain.ChangeID, items []domain.ChangeItem) ([]domain.ChangeItem, error)
	Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error)
}

// KeyGraph is what LinkToType additionally needs: looking an existing
// NodeType up by key. *graph.Graph implements it; engine.GraphPort does not
// need to (the engine only resolves ancestor types, never links to them).
type KeyGraph interface {
	Graph
	NodeByKey(ctx context.Context, key string) (domain.Node, error)
}

// Result reports a synchronization.
type Result struct {
	Methodology string            `json:"methodology,omitempty"`
	Change      domain.ChangeID   `json:"change,omitempty"`
	Baseline    domain.BaselineID `json:"baseline,omitempty"`
	Created     int               `json:"created"`
	Updated     int               `json:"updated"`
	Deleted     int               `json:"deleted"`
	Links       int               `json:"links"`
}

// Changed reports whether the synchronization changed the graph.
func (r Result) Changed() bool { return r.Change != "" }

// Sync projects a methodology onto main: a change creates, updates or
// deletes the element nodes and links that differ, and is applied. Nothing
// happens when the graph already matches.
//
// A methodology referencing a shared domain first synchronizes that domain
// (its Domain must be resolved, as registry.Compile does), so that the
// methodology's types exist on the graph once it is projected.
func Sync(ctx context.Context, g Graph, m *methodology.Methodology) (Result, error) {
	if m.DomainRef != "" {
		name, _, _ := strings.Cut(m.DomainRef, "@")
		if _, err := SyncDomain(ctx, g, &methodology.Domain{Name: name, Schema: m.Domain}); err != nil {
			return Result{Methodology: m.Name}, fmt.Errorf("domain %s: %w", name, err)
		}
	}
	els, edges := Project(m)
	return syncProjection(ctx, g, target{
		prefix: Key(m.Name, TypeMethodology, ""), els: els, edges: edges, name: m.Name,
		title:  fmt.Sprintf("Methodology %s %s", m.Name, m.Version),
		intent: "Publish methodology " + m.Name + " " + m.Version,
		data:   map[string]any{"metamodel": map[string]any{"methodology": m.Name, "version": m.Version}},
		branch: m.Name + "@" + m.Version,
	})
}

// SyncDomain projects the NodeTypes of a shared domain onto main. Like every
// NodeType (ADR 0012) they are only created and linked: an existing node is
// authored on the graph and never overwritten or deleted by a publication.
func SyncDomain(ctx context.Context, g Graph, d *methodology.Domain) (Result, error) {
	els, edges := ProjectDomain(d)
	return syncProjection(ctx, g, target{
		prefix: "D:" + d.Name, els: els, edges: edges, name: "domain " + d.Name,
		title:  fmt.Sprintf("Domain %s %s", d.Name, d.Version),
		intent: "Publish domain " + d.Name + " " + d.Version,
		data:   map[string]any{"metamodel": map[string]any{"domain": d.Name, "version": d.Version}},
		branch: d.Name + "@" + d.Version,
	})
}

// target is what a synchronization projects: the elements owned under a key
// prefix, and how the change that applies them is described.
type target struct {
	prefix, name, title, intent, branch string
	els                                 []Element
	edges                               []Edge
	data                                map[string]any
}

func syncProjection(ctx context.Context, g Graph, t target) (Result, error) {
	res, err := sync(ctx, g, t)
	if errors.Is(err, graph.ErrConflict) {
		res, err = sync(ctx, g, t) // main moved meanwhile: once more on the new head
	}
	return res, err
}

func sync(ctx context.Context, g Graph, t target) (Result, error) {
	head, err := g.BranchHead(ctx, domain.MainBranch)
	if errors.Is(err, graph.ErrNotFound) {
		head, err = g.CreateBaseline(ctx, "Repository", nil)
	}
	if err != nil {
		return Result{}, err
	}
	nodes, links, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		return Result{}, err
	}
	prefix := t.prefix
	current := map[string]domain.Node{}
	byID := map[domain.NodeID]domain.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
		if n.Key == prefix || strings.HasPrefix(n.Key, prefix+"/") {
			current[n.Key] = n
		}
	}
	els, edges := t.els, t.edges
	res := Result{Methodology: t.name}
	var items []domain.ChangeItem
	refs := map[string]domain.Endpoint{} // element key → endpoint
	desired := map[string]bool{}
	for _, el := range els {
		desired[el.Key] = true
		if n, ok := current[el.Key]; ok {
			ref := n.Ref()
			refs[el.Key] = domain.Endpoint{Node: &ref}
			// NodeType is the metadata layer (ADR 0012): once created, it is
			// authored on the graph, not overwritten from the registry.
			if el.Type == TypeNodeType {
				continue
			}
			if patch := diff(n.Properties, el.Props); patch != nil {
				items = append(items, domain.ChangeItem{Kind: domain.KindProposal, Type: "metamodel", ProducedBy: "metamodel.sync",
					Proposal: &domain.Proposal{Op: domain.OpUpdateNode, Node: &domain.NodeDraft{Base: &ref, Properties: patch}}})
				res.Updated++
			}
			continue
		}
		id := domain.ItemID(uuid.NewString())
		refs[el.Key] = domain.Endpoint{Item: id}
		items = append(items, domain.ChangeItem{ID: id, Kind: domain.KindProposal, Type: "metamodel", ProducedBy: "metamodel.sync",
			Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Key: el.Key, Type: el.Type, Properties: el.Props}}})
		res.Created++
	}
	for _, k := range slices.Sorted(maps.Keys(current)) {
		// NodeType nodes are never deleted by Sync (ADR 0012): a node type
		// removed from the registry's declared schema stays in the graph.
		if !desired[k] && current[k].Type != TypeNodeType {
			ref := current[k].Ref()
			items = append(items, domain.ChangeItem{Kind: domain.KindProposal, Type: "metamodel", ProducedBy: "metamodel.sync",
				Proposal: &domain.Proposal{Op: domain.OpDeleteNode, Node: &domain.NodeDraft{Base: &ref}}})
			res.Deleted++
		}
	}
	// links: outgoing links of the element nodes, keyed by (type, target key)
	have := map[Edge]domain.LinkID{}
	for _, l := range links {
		from, to := byID[l.From.ID], byID[l.To.ID]
		if _, ok := current[from.Key]; ok {
			have[Edge{l.Type, from.Key, to.Key}] = l.ID
		}
	}
	want := map[Edge]bool{}
	for _, e := range edges {
		to, ok := refs[e.To]
		if !ok {
			if n, exists := keyNode(nodes, e.To); exists { // element of another methodology
				ref := n.Ref()
				to = domain.Endpoint{Node: &ref}
			} else {
				continue
			}
		}
		want[e] = true
		if _, ok := have[e]; ok {
			continue
		}
		items = append(items, domain.ChangeItem{Kind: domain.KindProposal, Type: "metamodel", ProducedBy: "metamodel.sync",
			Proposal: &domain.Proposal{Op: domain.OpAddLink, Link: &domain.LinkDraft{Type: e.Type, From: refs[e.From], To: to}}})
		res.Links++
	}
	for e, id := range have {
		// extends edges between NodeType nodes are metadata-layer data
		// (ADR 0012): never removed by Sync, even when the registry's
		// declared "extends" target changes.
		if e.Type != LinkExtends && !want[e] && desired[e.From] && desired[e.To] {
			items = append(items, domain.ChangeItem{Kind: domain.KindProposal, Type: "metamodel", ProducedBy: "metamodel.sync",
				Proposal: &domain.Proposal{Op: domain.OpRemoveLink, Link: &domain.LinkDraft{LinkID: id}}})
			res.Links++
		}
	}
	if len(items) == 0 {
		return res, nil
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Title: t.title, Intent: t.intent, BaselineID: head.ID, Data: t.data})
	if err != nil {
		return res, err
	}
	if _, err := g.AddItems(ctx, c.ID, items); err != nil {
		return res, err
	}
	b, err := g.Apply(ctx, c.ID, t.branch)
	if err != nil {
		return res, err
	}
	res.Change, res.Baseline = c.ID, b.ID
	return res, nil
}

func keyNode(nodes []domain.Node, key string) (domain.Node, bool) {
	for _, n := range nodes {
		if n.Key == key {
			return n, true
		}
	}
	return domain.Node{}, false
}

// diff returns the update of stored properties to reach want (removed keys
// are set to null), or nil when they match.
func diff(have, want map[string]any) map[string]any {
	patch := map[string]any{}
	for k, v := range want {
		if !jsonEqual(have[k], v) {
			patch[k] = v
		}
	}
	for k, v := range have {
		if _, ok := want[k]; !ok && v != nil {
			patch[k] = nil
		}
	}
	if len(patch) == 0 {
		return nil
	}
	return patch
}

func jsonEqual(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// Published lists methodologies to synchronize.
type Published interface {
	List(ctx context.Context) ([]*methodology.Compiled, error)
}

// SyncAll synchronizes every published methodology (startup).
func SyncAll(ctx context.Context, g Graph, reg Published) ([]Result, error) {
	ms, err := reg.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Result
	for _, m := range ms {
		r, err := Sync(ctx, g, m.Methodology)
		if err != nil {
			return out, fmt.Errorf("%s: %w", m.Name, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// Supertypes maps each NodeType of a methodology to its ancestors, nearest
// first, computed from the metadata layer of the graph's main branch (ADR
// 0012) rather than from the declared schema. It returns a nil map, not an
// error, when the methodology has no NodeType nodes in the graph yet (never
// synced): callers fall back to Methodology.Supertypes() in that case.
func Supertypes(ctx context.Context, g Graph, methodology string) (map[string][]string, error) {
	head, err := g.BranchHead(ctx, domain.MainBranch)
	if errors.Is(err, graph.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	nodes, links, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		return nil, err
	}
	ns := TypeNamespace(nodes, methodology)
	names := map[domain.NodeID]string{} // NodeType node id → name
	for _, n := range nodes {
		if name, ok := TypeName(n.Key, ns); ok {
			names[n.ID] = name
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	parents := map[string]string{} // name → parent name
	for _, l := range links {
		if l.Type != LinkExtends {
			continue
		}
		fname, fok := names[l.From.ID]
		tname, tok := names[l.To.ID]
		if fok && tok {
			parents[fname] = tname
		}
	}
	out := map[string][]string{}
	for _, name := range names {
		seen := map[string]bool{name: true}
		for t := parents[name]; t != "" && !seen[t]; t = parents[t] {
			seen[t] = true
			out[name] = append(out[name], t)
		}
	}
	return out, nil
}

// NodeTypeOp authors one NodeType of the metadata layer: Extends targets
// another NodeType of the same methodology by name ("" clears it), Delete
// removes the NodeType and its extends edges.
type NodeTypeOp struct {
	Name        string
	Description string
	Properties  []string
	Extends     string
	Delete      bool
}

// ApplyNodeTypes authors NodeType nodes and their extends edges directly on
// the graph's metadata layer (ADR 0012), independently of the registry: once
// NodeType is graph-native, this is how it is created and edited going
// forward (Sync only ever seeds it once).
func ApplyNodeTypes(ctx context.Context, g Graph, meth string, ops []NodeTypeOp) (Result, error) {
	head, err := g.BranchHead(ctx, domain.MainBranch)
	if errors.Is(err, graph.ErrNotFound) {
		head, err = g.CreateBaseline(ctx, "Repository", nil)
	}
	if err != nil {
		return Result{}, err
	}
	nodes, links, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		return Result{}, err
	}
	byKey := map[string]domain.Node{}
	for _, n := range nodes {
		byKey[n.Key] = n
	}
	ns := TypeNamespace(nodes, meth)
	res := Result{Methodology: meth}
	var items []domain.ChangeItem
	refs := map[string]domain.Endpoint{} // NodeType name → endpoint, for extends targets created in this batch
	for _, op := range ops {
		key := typeKey(ns, op.Name)
		n, exists := byKey[key]
		if op.Delete {
			if exists {
				ref := n.Ref()
				items = append(items, domain.ChangeItem{Kind: domain.KindProposal, Type: "metamodel", ProducedBy: "metamodel.apply_node_types",
					Proposal: &domain.Proposal{Op: domain.OpDeleteNode, Node: &domain.NodeDraft{Base: &ref}}})
				res.Deleted++
			}
			continue
		}
		props := map[string]any{"name": op.Name, "description": op.Description, "properties": op.Properties, "extends": op.Extends}
		if exists {
			ref := n.Ref()
			refs[op.Name] = domain.Endpoint{Node: &ref}
			continue // description/properties are seeded once; only new extends edges are added below.
		}
		id := domain.ItemID(uuid.NewString())
		refs[op.Name] = domain.Endpoint{Item: id}
		items = append(items, domain.ChangeItem{ID: id, Kind: domain.KindProposal, Type: "metamodel", ProducedBy: "metamodel.apply_node_types",
			Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Key: key, Type: TypeNodeType, Properties: props}}})
		res.Created++
	}
	have := map[string]bool{} // "from|to" of existing extends edges
	for _, l := range links {
		if l.Type == LinkExtends {
			have[string(l.From.ID)+"|"+string(l.To.ID)] = true
		}
	}
	for _, op := range ops {
		if op.Delete || op.Extends == "" {
			continue
		}
		from, ok := refs[op.Name]
		if !ok {
			continue
		}
		to, ok := refs[op.Extends]
		if !ok {
			if n, exists := byKey[typeKey(ns, op.Extends)]; exists {
				ref := n.Ref()
				to = domain.Endpoint{Node: &ref}
			} else {
				return Result{}, fmt.Errorf("node type %q extends unknown %q", op.Name, op.Extends)
			}
		}
		if from.Node != nil && to.Node != nil && have[string(from.Node.ID)+"|"+string(to.Node.ID)] {
			continue
		}
		items = append(items, domain.ChangeItem{Kind: domain.KindProposal, Type: "metamodel", ProducedBy: "metamodel.apply_node_types",
			Proposal: &domain.Proposal{Op: domain.OpAddLink, Link: &domain.LinkDraft{Type: LinkExtends, From: from, To: to}}})
		res.Links++
	}
	if len(items) == 0 {
		return res, nil
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "Node types of " + meth, Intent: "Define node types of " + meth,
		BaselineID: head.ID, Methodology: meth})
	if err != nil {
		return res, err
	}
	if _, err := g.AddItems(ctx, c.ID, items); err != nil {
		return res, err
	}
	b, err := g.Apply(ctx, c.ID, domain.MainBranch)
	if err != nil {
		return res, err
	}
	res.Change, res.Baseline = c.ID, b.ID
	return res, nil
}

// LinkToType adds an instanceOf edge from an existing data node to the
// NodeType named typeName of methodology meth. It is a no-op (not an error)
// when that NodeType does not exist in the graph yet.
func LinkToType(ctx context.Context, g KeyGraph, meth string, ref domain.NodeRef, typeName string) error {
	head, err := g.BranchHead(ctx, domain.MainBranch)
	if err != nil {
		return err
	}
	ns := meth
	if root, err := g.NodeByKey(ctx, Key(meth, TypeMethodology, "")); err == nil {
		ns = TypeNamespace([]domain.Node{root}, meth)
	}
	nt, err := g.NodeByKey(ctx, typeKey(ns, typeName))
	if errors.Is(err, graph.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	ntRef := nt.Ref()
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "Link " + string(ref.ID) + " to " + typeName,
		Intent: "instanceOf " + typeName, BaselineID: head.ID, Methodology: meth})
	if err != nil {
		return err
	}
	item := domain.ChangeItem{Kind: domain.KindProposal, Type: "metamodel", ProducedBy: "metamodel.link_to_type",
		Proposal: &domain.Proposal{Op: domain.OpAddLink, Link: &domain.LinkDraft{Type: LinkInstanceOf, From: domain.Endpoint{Node: &ref}, To: domain.Endpoint{Node: &ntRef}}}}
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{item}); err != nil {
		return err
	}
	_, err = g.Apply(ctx, c.ID, domain.MainBranch)
	return err
}

// BackfillInstanceOf adds an instanceOf edge for every data node of the main
// branch whose Type matches a NodeType of methodology and that has none yet.
// It is the catch-up for nodes created before the metadata layer existed for
// that methodology (ADR 0012 phase 3), and is idempotent: nodes already
// linked, and methodologies with no NodeType nodes yet, are left untouched.
func BackfillInstanceOf(ctx context.Context, g Graph, meth string) (Result, error) {
	res := Result{Methodology: meth}
	head, err := g.BranchHead(ctx, domain.MainBranch)
	if errors.Is(err, graph.ErrNotFound) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	nodes, links, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		return res, err
	}
	ns := TypeNamespace(nodes, meth)
	types := map[string]domain.NodeRef{} // NodeType name → node
	for _, n := range nodes {
		if name, ok := TypeName(n.Key, ns); ok {
			types[name] = n.Ref()
		}
	}
	if len(types) == 0 {
		return res, nil
	}
	linked := map[domain.NodeID]bool{}
	for _, l := range links {
		if l.Type == LinkInstanceOf {
			linked[l.From.ID] = true
		}
	}
	var items []domain.ChangeItem
	for _, n := range nodes {
		if linked[n.ID] {
			continue
		}
		to, ok := types[n.Type]
		if !ok {
			continue
		}
		from := n.Ref()
		items = append(items, domain.ChangeItem{Kind: domain.KindProposal, Type: "metamodel", ProducedBy: "metamodel.backfill_instance_of",
			Proposal: &domain.Proposal{Op: domain.OpAddLink, Link: &domain.LinkDraft{Type: LinkInstanceOf, From: domain.Endpoint{Node: &from}, To: domain.Endpoint{Node: &to}}}})
		res.Links++
	}
	if len(items) == 0 {
		return res, nil
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "Backfill instanceOf for " + meth,
		Intent: "Link existing nodes to their node type", BaselineID: head.ID, Methodology: meth})
	if err != nil {
		return res, err
	}
	if _, err := g.AddItems(ctx, c.ID, items); err != nil {
		return res, err
	}
	b, err := g.Apply(ctx, c.ID, domain.MainBranch)
	if err != nil {
		return res, err
	}
	res.Change, res.Baseline = c.ID, b.ID
	return res, nil
}

// CreateObject creates a data node typed by the NodeType typeName of
// methodology meth: the node and its instanceOf edge go through one change
// applied on main. It fails with graph.ErrNotFound when the NodeType is not on
// the graph yet (methodology not published), graph.ErrConflict when the key is
// taken, and graph.ErrInvalid without a key.
func CreateObject(ctx context.Context, g KeyGraph, meth, typeName, key string, props map[string]any) (domain.Node, domain.Baseline, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return domain.Node{}, domain.Baseline{}, fmt.Errorf("object key is required: %w", graph.ErrInvalid)
	}
	head, err := g.BranchHead(ctx, domain.MainBranch)
	if err != nil {
		return domain.Node{}, domain.Baseline{}, err
	}
	nodes, _, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		return domain.Node{}, domain.Baseline{}, err
	}
	var typeRef *domain.NodeRef
	typeKeyWanted := typeKey(TypeNamespace(nodes, meth), typeName)
	for _, n := range nodes {
		if n.Key == key {
			return domain.Node{}, domain.Baseline{}, fmt.Errorf("key %q is already used: %w", key, graph.ErrConflict)
		}
		if n.Key == typeKeyWanted {
			ref := n.Ref()
			typeRef = &ref
		}
	}
	if typeRef == nil {
		return domain.Node{}, domain.Baseline{}, fmt.Errorf("node type %s of %s is not on the graph (publish the methodology): %w", typeName, meth, graph.ErrNotFound)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "Create " + key, Intent: "Create " + typeName + " " + key,
		BaselineID: head.ID, Methodology: meth})
	if err != nil {
		return domain.Node{}, domain.Baseline{}, err
	}
	id := domain.ItemID(uuid.NewString())
	items := []domain.ChangeItem{
		{ID: id, Kind: domain.KindProposal, Type: "object", ProducedBy: "metamodel.create_object",
			Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Key: key, Type: typeName, Properties: props}}},
		{Kind: domain.KindProposal, Type: "object", ProducedBy: "metamodel.create_object", DerivedFrom: []domain.ItemID{id},
			Proposal: &domain.Proposal{Op: domain.OpAddLink, Link: &domain.LinkDraft{Type: LinkInstanceOf, From: domain.Endpoint{Item: id}, To: domain.Endpoint{Node: typeRef}}}},
	}
	if _, err := g.AddItems(ctx, c.ID, items); err != nil {
		return domain.Node{}, domain.Baseline{}, err
	}
	b, err := g.Apply(ctx, c.ID, domain.MainBranch)
	if err != nil {
		return domain.Node{}, domain.Baseline{}, err
	}
	n, err := g.NodeByKey(ctx, key)
	return n, b, err
}
