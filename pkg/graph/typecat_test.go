package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

// catalogGraph is a graph judged by a type catalogue (ADR 0012) instead of NodeType nodes.
func catalogGraph(t *testing.T) (*Graph, domain.Baseline) {
	t.Helper()
	d, err := methodology.ParseDomain([]byte(`
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
    properties: [title]
    validators: [{property: title, instance: title-required}]
    search: [{property: title, text: true}]
  - {name: SubReq, extends: Req}
  - {name: Test, properties: [title]}
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
	b, err := g.CreateBaseline(context.Background(), "B0", nil)
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
	if _, err := g.CreateNode(ctx, NewNode{Namespace: "docs", Key: "N", Type: "docs@Nope"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("direct write of an unknown type: %v", err)
	}
	// the meta-domains are always known
	if _, err := g.CreateNode(ctx, NewNode{Namespace: "methodology", Key: "MV:m@1", Type: "methodology@MethodologyVersion"}); err != nil {
		t.Fatal(err)
	}
}
