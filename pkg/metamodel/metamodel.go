// Package metamodel models methodologies as versioned elements of the domain
// graph (ADR 0011): every published methodology is projected onto nodes
// (methodology, agents, actions, goals, conditions, triggers, node types)
// and links, through an ordinary change applied on main. Each publication
// creates new versions of the elements that changed, so executions (journal
// records name the methodology version, agent and action) and improvement
// proposals can reference the exact definition they are about.
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
	LinkExtends     = "extends"     // NodeType → NodeType
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
	els := []Element{{Key: root, Type: TypeMethodology, Props: props(map[string]any{"name": m.Name, "version": m.Version, "description": m.Description})}}
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

// Result reports a synchronization.
type Result struct {
	Change   domain.ChangeID   `json:"change,omitempty"`
	Baseline domain.BaselineID `json:"baseline,omitempty"`
	Created  int               `json:"created"`
	Updated  int               `json:"updated"`
	Deleted  int               `json:"deleted"`
	Links    int               `json:"links"`
}

// Changed reports whether the synchronization changed the graph.
func (r Result) Changed() bool { return r.Change != "" }

// Sync projects a methodology onto main: a change creates, updates or
// deletes the element nodes and links that differ, and is applied. Nothing
// happens when the graph already matches.
func Sync(ctx context.Context, g Graph, m *methodology.Methodology) (Result, error) {
	res, err := sync(ctx, g, m)
	if errors.Is(err, graph.ErrConflict) {
		res, err = sync(ctx, g, m) // main moved meanwhile: once more on the new head
	}
	return res, err
}

func sync(ctx context.Context, g Graph, m *methodology.Methodology) (Result, error) {
	head, err := g.BranchHead(ctx, domain.MainBranch)
	if errors.Is(err, graph.ErrNotFound) {
		head, err = g.CreateBaseline(ctx, "Référentiel", nil)
	}
	if err != nil {
		return Result{}, err
	}
	nodes, links, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		return Result{}, err
	}
	prefix := Key(m.Name, TypeMethodology, "")
	current := map[string]domain.Node{}
	byID := map[domain.NodeID]domain.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
		if n.Key == prefix || strings.HasPrefix(n.Key, prefix+"/") {
			current[n.Key] = n
		}
	}
	els, edges := Project(m)
	var res Result
	var items []domain.ChangeItem
	refs := map[string]domain.Endpoint{} // element key → endpoint
	desired := map[string]bool{}
	for _, el := range els {
		desired[el.Key] = true
		if n, ok := current[el.Key]; ok {
			ref := n.Ref()
			refs[el.Key] = domain.Endpoint{Node: &ref}
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
		if !desired[k] {
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
		if !want[e] && desired[e.From] && desired[e.To] {
			items = append(items, domain.ChangeItem{Kind: domain.KindProposal, Type: "metamodel", ProducedBy: "metamodel.sync",
				Proposal: &domain.Proposal{Op: domain.OpRemoveLink, Link: &domain.LinkDraft{LinkID: id}}})
			res.Links++
		}
	}
	if len(items) == 0 {
		return res, nil
	}
	title := fmt.Sprintf("Méthodologie %s %s", m.Name, m.Version)
	c, err := g.CreateChange(ctx, graph.NewChange{Title: title, Intent: "Publication de la méthodologie " + m.Name + " " + m.Version,
		BaselineID: head.ID, Data: map[string]any{"metamodel": map[string]any{"methodology": m.Name, "version": m.Version}}})
	if err != nil {
		return res, err
	}
	if _, err := g.AddItems(ctx, c.ID, items); err != nil {
		return res, err
	}
	b, err := g.Apply(ctx, c.ID, m.Name+"@"+m.Version)
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
