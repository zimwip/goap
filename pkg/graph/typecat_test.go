package graph

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/typecat"
)

// catalogGraph is a graph judged by a type catalogue built from a domain (ADR 0012).
func catalogGraph(t *testing.T) (*Graph, domain.Baseline) {
	t.Helper()
	d, err := def.ParseDomain([]byte(`
name: docs
version: 1.0.0
lifecycles:
  - name: req
    initial: proposed
    states: [{name: proposed}, {name: draft, editable: true}, {name: approved}]
    transitions: [{name: start, from: proposed, to: draft}, {name: approve, from: draft, to: approved}]
algorithms:
  - {name: not-blank, type: property_validator, language: javascript, code: 'return String(ctx.value() || "").trim() !== ""'}
algorithmInstances:
  - {name: title-required, algorithm: not-blank}
nodeTypes:
  - name: Req
    lifecycle: req
    attributes: [{name: title, validators: [title-required]}]
    search: [{property: title, text: true}]
  - {name: SubReq, extends: Req}
  - {name: Test, attributes: [title]}
linkTypes:
  - {name: verifies, from: Test, to: Req}
`))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(d)
	if err != nil {
		t.Fatal(err)
	}
	g := New(NewMemory())
	g.Types = func() TypeCatalog { return cat }
	b, err := g.BranchHead(context.Background(), "docs", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	return g, b
}

func TestCatalogJudgesTheNodes(t *testing.T) {
	ctx := context.Background()
	g, b := catalogGraph(t)
	commit := func(edits ...NodeEdit) error {
		_, err := g.Commit(ctx, Commit{Namespace: "docs", Title: "t", Baseline: b.ID, By: "test", Edits: edits})
		return err
	}
	// the existence rule
	if err := commit(NodeEdit{Key: "X", Type: "docs@Nope"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown type: %v", err)
	}
	if err := commit(NodeEdit{Key: "X", Type: "Req", Props: map[string]any{"title": "x"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bare type is unknown: %v", err)
	}
	if _, err := g.Commit(ctx, Commit{Namespace: "other", Title: "t", Baseline: b.ID, By: "test", Edits: []NodeEdit{{Key: "X", Type: "docs@Req", Props: map[string]any{"title": "x"}}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a node lives in the namespace of its type: %v", err)
	}
	// validators come from the catalogue, inherited by the subtype
	if err := commit(NodeEdit{Key: "S1", Type: "docs@SubReq", Props: map[string]any{"title": " "}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the inherited validator rejects a blank title: %v", err)
	}
	// the lifecycle comes from the catalogue: a created node starts in the initial state
	if err := commit(NodeEdit{Key: "S1", Type: "docs@SubReq", Props: map[string]any{"title": "one"}},
		NodeEdit{Key: "T1", Type: "docs@Test", Props: map[string]any{"title": "t"}, Links: []LinkEdit{{Type: "docs@verifies", ToKey: "S1"}}}); err != nil {
		t.Fatal(err)
	}
	s1, err := g.NodeByKey(ctx, "docs", "S1")
	if err != nil || s1.State != "proposed" {
		t.Fatalf("initial state of the inherited lifecycle: %+v %v", s1, err)
	}
	if ctl, _ := g.LifecycleControlled(ctx, "docs@SubReq"); !ctl {
		t.Fatal("a type with a lifecycle is controlled")
	}
	// link types are checked, ends included
	if err := commit(NodeEdit{Key: "T2", Type: "docs@Test", Props: map[string]any{"title": "t"}, Links: []LinkEdit{{Type: "docs@nope", ToKey: "S1"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown link type: %v", err)
	}
	if err := commit(NodeEdit{Key: "S2", Type: "docs@Req", Props: map[string]any{"title": "t"}, Links: []LinkEdit{{Type: "docs@verifies", ToKey: "S1"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the source of verifies must be a Test: %v", err)
	}
	// direct writes too
	if _, err := importNode(ctx, g, newNode{Namespace: "docs", Key: "N", Type: "docs@Nope"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("direct write of an unknown type: %v", err)
	}
	// the built-in domains are always known
	if _, err := importNode(ctx, g, newNode{Namespace: "methodology", Key: "MV:m@1", Type: "methodology@MethodologyVersion"}); err != nil {
		t.Fatal(err)
	}
}

// testType is a node type of testTypes.
type testType struct {
	Extends    string
	Lifecycle  *domain.Lifecycle
	Validators []algo.Bound
	Search     []domain.SearchProperty
}

// testTypes is a TypeCatalog for the tests that need types without a domain: names are taken as they are, a known
// type passes the existence rule, links are not checked.
type testTypes map[string]testType

func (ts testTypes) chain(typ string) []testType { // the type, then its ancestors
	var out []testType
	for seen := map[string]bool{}; typ != "" && !seen[typ]; {
		seen[typ] = true
		t, ok := ts[typ]
		if !ok {
			break
		}
		out = append(out, t)
		typ = t.Extends
	}
	return out
}

func (ts testTypes) Lifecycle(typ string) *domain.Lifecycle {
	for _, t := range ts.chain(typ) {
		if t.Lifecycle != nil {
			return t.Lifecycle
		}
	}
	return nil
}

func (ts testTypes) Validators(typ string) []algo.Bound {
	var out []algo.Bound
	for _, t := range slices.Backward(ts.chain(typ)) {
		out = append(out, t.Validators...)
	}
	return out
}

func (ts testTypes) Search(typ string) []domain.SearchProperty {
	var out []domain.SearchProperty
	at := map[string]int{}
	for _, t := range slices.Backward(ts.chain(typ)) {
		for _, s := range t.Search {
			if i, ok := at[s.Property]; ok {
				out[i] = s
				continue
			}
			at[s.Property] = len(out)
			out = append(out, s)
		}
	}
	return out
}

func (ts testTypes) AttributeChecks(string) []domain.AttributeCheck { return nil }

func (ts testTypes) CheckNode(_, typ string) error {
	if _, ok := ts[typ]; !ok {
		return errors.New("unknown type " + typ)
	}
	return nil
}

func (ts testTypes) CheckLink(string, string, string) error { return nil }

// Structure resolves none: the graph falls back to the built-in structures (the built-in domains).
func (ts testTypes) Structure(string) (domain.Structure, bool) { return domain.Structure{}, false }

func (ts testTypes) IsA(typ, base string) bool {
	for seen := map[string]bool{}; typ != "" && !seen[typ]; typ = ts[typ].Extends {
		if typ == base {
			return true
		}
		seen[typ] = true
	}
	return false
}

func (ts testTypes) Structures() domain.Structures { return typecat.Builtin().Structures() }

// Requires resolves none: the graph falls back to the built-in ones (the built-in domains).
func (ts testTypes) Requires(string) []domain.RequiredLink { return nil }

// AdminOnly resolves none: the graph falls back to the built-in ones (the built-in domains).
func (ts testTypes) AdminOnly(string) bool { return false }

// A node type flagged `adminOnly:` in a domain is restricted, and its subtypes with it, without any code naming it
// (ADR 0068); a type the domain does not flag is not.
func TestAdminOnlyFollowsTheDomain(t *testing.T) {
	d, err := def.ParseDomain([]byte(`
name: vault
version: 1.0.0
nodeTypes:
  - {name: Secret, adminOnly: true}
  - {name: SubSecret, extends: Secret}
  - {name: Note}
`))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(d)
	if err != nil {
		t.Fatal(err)
	}
	g := New(NewMemory())
	g.Types = func() TypeCatalog { return cat }
	for typ, want := range map[string]bool{"vault@Secret": true, "vault@SubSecret": true, "vault@Note": false, NodeTypeUser: true} {
		if got, err := g.AdminOnlyType(context.Background(), typ); err != nil || got != want {
			t.Errorf("AdminOnlyType(%s) = %v, %v; want %v", typ, got, err, want)
		}
	}
}
