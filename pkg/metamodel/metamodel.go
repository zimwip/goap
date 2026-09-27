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
// A data node names its NodeType by its Type attribute.
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
	// an explicit false (not change controlled) is not an empty value
	if t, ok := v.(methodology.NodeType); ok && t.ChangeControlled != nil {
		m["changeControlled"] = *t.ChangeControlled
	}
	return m
}

// nodeTypeProps are the properties of a NodeType graph node. The lifecycle the
// type names is embedded (resolved) so that the graph needs no other node to
// evaluate it; its name stays in lifecycleRef.
func nodeTypeProps(s methodology.Schema, t methodology.NodeType) map[string]any {
	m := props(t)
	delete(m, "lifecycle")
	if l := s.Lifecycle(t.Lifecycle); t.Lifecycle != "" && l != nil {
		m["lifecycleRef"] = t.Lifecycle
		m["lifecycle"] = props(s.BindLifecycle(l))
	}
	// property validators are embedded resolved (instance + algorithm code), like the lifecycle
	delete(m, "validators")
	if bound := s.OwnBoundValidators(t); len(bound) > 0 {
		b, _ := json.Marshal(bound)
		var v any
		_ = json.Unmarshal(b, &v)
		m["validators"] = v
	}
	return m
}

// lifecycleKeys are the NodeType properties that carry the lifecycle and algorithm model, the index
// declarations and the editor of the user interface.
var lifecycleKeys = []string{"lifecycle", "lifecycleRef", "document", "changeControlled", "validators", "search", "editor"}

func pick(m map[string]any, keys []string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
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
			k := Key(name, TypeNodeType, t.Name)
			els = append(els, Element{Key: k, Type: TypeNodeType, Props: nodeTypeProps(m.Domain, t)})
			edges = append(edges, Edge{LinkContains, root, k})
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
		els = append(els, Element{Key: k, Type: TypeNodeType, Props: nodeTypeProps(d.Schema, t)})
		if t.Extends != "" && types[t.Extends] {
			edges = append(edges, Edge{LinkExtends, k, DomainKey(d.Name, t.Extends)})
		}
	}
	return els, edges
}

// Reader is what reading the projection needs from the graph.
type Reader interface {
	BranchHead(ctx context.Context, name string) (domain.Baseline, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
}

// Graph is what the projection needs from the graph (*graph.Graph and the
// graph service client implement it).
type Graph interface {
	Reader
	CreateBaseline(ctx context.Context, name string, nodes []domain.NodeRef) (domain.Baseline, error)
	// Commit runs a change of node edits (ADR 0024).
	Commit(ctx context.Context, in graph.Commit) (graph.CommitResult, error)
}

// KeyGraph is what CreateObject additionally needs: looking a node up by key.
// *graph.Graph implements it; engine.GraphPort does not need to.
type KeyGraph interface {
	Graph
	NodeByKey(ctx context.Context, namespace, key string) (domain.Node, error)
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
		name, version, _ := strings.Cut(m.DomainRef, "@")
		if _, err := SyncDomain(ctx, g, &methodology.Domain{Name: name, Version: version, Schema: m.Domain}); err != nil {
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
	var edits []graph.NodeEdit
	editOf := map[string]int{}                  // element key → its edit
	refs := map[string]*domain.NodeRef{}        // element key → existing version (nil when created by this commit)
	editFor := func(k string) *graph.NodeEdit { // the edit of an existing element, made on first use
		if i, ok := editOf[k]; ok {
			return &edits[i]
		}
		editOf[k] = len(edits)
		edits = append(edits, graph.NodeEdit{Pre: refs[k], Rationale: t.intent})
		return &edits[len(edits)-1]
	}
	desired := map[string]bool{}
	for _, el := range els {
		desired[el.Key] = true
		if n, ok := current[el.Key]; ok {
			ref := n.Ref()
			refs[el.Key] = &ref
			// NodeType is the metadata layer (ADR 0012): once created, it is
			// authored on the graph, not overwritten from the registry.
			// The lifecycle, document, change-control, index and editor
			// declarations are the exception (ADR 0014): they are governed by
			// the domain and follow its published version.
			if el.Type == TypeNodeType {
				if patch := diff(pick(n.Properties, lifecycleKeys), pick(el.Props, lifecycleKeys)); patch != nil {
					editFor(el.Key).Props = patch
					res.Updated++
				}
				continue
			}
			if patch := diff(n.Properties, el.Props); patch != nil {
				editFor(el.Key).Props = patch
				res.Updated++
			}
			continue
		}
		editOf[el.Key] = len(edits)
		edits = append(edits, graph.NodeEdit{Key: el.Key, Type: el.Type, Props: el.Props, Rationale: t.intent})
		res.Created++
	}
	for _, k := range slices.Sorted(maps.Keys(current)) {
		// NodeType nodes are never deleted by Sync (ADR 0012): a node type
		// removed from the registry's declared schema stays in the graph.
		if !desired[k] && current[k].Type != TypeNodeType {
			ref := current[k].Ref()
			edits = append(edits, graph.NodeEdit{Pre: &ref, Retire: true, Rationale: t.intent})
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
		var to graph.LinkEdit
		if _, ok := editOf[e.To]; ok && refs[e.To] == nil {
			to = graph.LinkEdit{Type: e.Type, ToKey: e.To} // created by this commit
		} else if r, ok := refs[e.To]; ok {
			to = graph.LinkEdit{Type: e.Type, To: r}
		} else if n, exists := keyNode(nodes, e.To); exists { // element of another methodology
			ref := n.Ref()
			to = graph.LinkEdit{Type: e.Type, To: &ref}
		} else {
			continue
		}
		want[e] = true
		if _, ok := have[e]; ok {
			continue
		}
		src := editFor(e.From)
		src.Links = append(src.Links, to)
		res.Links++
	}
	for _, e := range slices.SortedFunc(maps.Keys(have), func(a, b Edge) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) }) {
		// extends edges between NodeType nodes are metadata-layer data
		// (ADR 0012): never removed by Sync, even when the registry's
		// declared "extends" target changes.
		if e.Type != LinkExtends && !want[e] && desired[e.From] && desired[e.To] {
			src := editFor(e.From)
			src.RemoveLinks = append(src.RemoveLinks, have[e])
			res.Links++
		}
	}
	if len(edits) == 0 {
		return res, nil
	}
	out, err := g.Commit(ctx, graph.Commit{Namespace: domain.NamespacePlatform, Title: t.title, Intent: t.intent, Baseline: head.ID, Data: t.data,
		By: "metamodel.sync", BaselineName: t.branch, Edits: edits})
	if err != nil {
		return res, err
	}
	res.Change, res.Baseline = out.Change, out.Baseline.ID
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

// PublishedDomains lists the latest published version of every shared domain. A registry
// that implements it has SyncAll project the domains no methodology references too.
type PublishedDomains interface {
	Domains(ctx context.Context) ([]*methodology.Domain, error)
}

// SyncAll synchronizes every published methodology (startup), then, when reg lists the
// published domains, the node types of the domains no methodology references (their
// nodes, like the organisation's units and policies, are still typed on the graph).
func SyncAll(ctx context.Context, g Graph, reg Published) ([]Result, error) {
	ms, err := reg.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Result
	referenced := map[string]bool{}
	for _, m := range ms {
		r, err := Sync(ctx, g, m.Methodology)
		if err != nil {
			return out, fmt.Errorf("%s: %w", m.Name, err)
		}
		out = append(out, r)
		if name, _, _ := strings.Cut(m.DomainRef, "@"); name != "" {
			referenced[name] = true
		}
	}
	pd, ok := reg.(PublishedDomains)
	if !ok {
		return out, nil
	}
	ds, err := pd.Domains(ctx)
	if err != nil {
		return out, err
	}
	for _, d := range ds {
		if referenced[d.Name] {
			continue
		}
		r, err := SyncDomain(ctx, g, d)
		if err != nil {
			return out, fmt.Errorf("domain %s: %w", d.Name, err)
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
func Supertypes(ctx context.Context, g Reader, methodology string) (map[string][]string, error) {
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
	var edits []graph.NodeEdit
	editOf := map[string]int{}           // NodeType name → its edit
	refs := map[string]*domain.NodeRef{} // NodeType name → existing version (nil when created by this commit)
	created := map[string]string{}       // NodeType name → key, for extends targets created in this batch
	for _, op := range ops {
		key := typeKey(ns, op.Name)
		n, exists := byKey[key]
		if op.Delete {
			if exists {
				ref := n.Ref()
				edits = append(edits, graph.NodeEdit{Pre: &ref, Retire: true, Rationale: "Delete node type " + op.Name})
				res.Deleted++
			}
			continue
		}
		props := map[string]any{"name": op.Name, "description": op.Description, "properties": op.Properties, "extends": op.Extends}
		if exists {
			ref := n.Ref()
			refs[op.Name] = &ref
			continue // description/properties are seeded once; only new extends edges are added below.
		}
		created[op.Name] = key
		editOf[op.Name] = len(edits)
		edits = append(edits, graph.NodeEdit{Key: key, Type: TypeNodeType, Props: props, Rationale: "Create node type " + op.Name})
		res.Created++
	}
	editFor := func(name string) *graph.NodeEdit { // the edit of a node type, made on first use for an existing one
		if i, ok := editOf[name]; ok {
			return &edits[i]
		}
		editOf[name] = len(edits)
		edits = append(edits, graph.NodeEdit{Pre: refs[name], Rationale: "Extend node type " + name})
		return &edits[len(edits)-1]
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
		_, isCreated := created[op.Name]
		from, isExisting := refs[op.Name]
		if !isCreated && !isExisting {
			continue
		}
		var link graph.LinkEdit
		if key, ok := created[op.Extends]; ok {
			link = graph.LinkEdit{Type: LinkExtends, ToKey: key}
		} else if to, ok := refs[op.Extends]; ok {
			if isExisting && have[string(from.ID)+"|"+string(to.ID)] {
				continue
			}
			link = graph.LinkEdit{Type: LinkExtends, To: to}
		} else if n, exists := byKey[typeKey(ns, op.Extends)]; exists {
			ref := n.Ref()
			if isExisting && have[string(from.ID)+"|"+string(ref.ID)] {
				continue
			}
			link = graph.LinkEdit{Type: LinkExtends, To: &ref}
		} else {
			return Result{}, fmt.Errorf("node type %q extends unknown %q", op.Name, op.Extends)
		}
		src := editFor(op.Name)
		src.Links = append(src.Links, link)
		res.Links++
	}
	if len(edits) == 0 {
		return res, nil
	}
	out, err := g.Commit(ctx, graph.Commit{Namespace: domain.NamespacePlatform, Title: "Node types of " + meth, Intent: "Define node types of " + meth,
		Baseline: head.ID, Methodology: meth, By: "metamodel.apply_node_types", BaselineName: domain.MainBranch, Edits: edits})
	if err != nil {
		return res, err
	}
	res.Change, res.Baseline = out.Change, out.Baseline.ID
	return res, nil
}

// CreateObject creates, in namespace, a data node typed by the NodeType typeName of
// methodology meth: the node goes through one change applied on main. The type
// is the attribute of the node; the NodeType must be on the graph. It fails with graph.ErrNotFound when the NodeType is not on
// the graph yet (methodology not published), graph.ErrConflict when the key is
// taken, and graph.ErrInvalid without a key.
func CreateObject(ctx context.Context, g KeyGraph, meth, namespace, typeName, key string, props map[string]any) (domain.Node, domain.Baseline, error) {
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
	found := false
	typeKeyWanted := typeKey(TypeNamespace(nodes, meth), typeName)
	for _, n := range nodes {
		if n.Key == key && domain.NamespaceOf(n.Namespace) == domain.NamespaceOf(namespace) {
			return domain.Node{}, domain.Baseline{}, fmt.Errorf("key %q is already used: %w", key, graph.ErrConflict)
		}
		found = found || n.Key == typeKeyWanted
	}
	if !found {
		return domain.Node{}, domain.Baseline{}, fmt.Errorf("node type %s of %s is not on the graph (publish the methodology): %w", typeName, meth, graph.ErrNotFound)
	}
	out, err := g.Commit(ctx, graph.Commit{Namespace: namespace, Title: "Create " + key, Intent: "Create " + typeName + " " + key,
		Baseline: head.ID, Methodology: meth, By: "metamodel.create_object", BaselineName: domain.MainBranch,
		Edits: []graph.NodeEdit{{Key: key, Type: typeName, Props: props, Rationale: "Create " + typeName + " " + key}}})
	if err != nil {
		return domain.Node{}, domain.Baseline{}, err
	}
	n, err := g.NodeByKey(ctx, namespace, key)
	return n, out.Baseline, err
}
