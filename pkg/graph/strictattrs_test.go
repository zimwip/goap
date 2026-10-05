package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/typecat"
)

// strictGraph is a graph judged by a catalogue with a closed type, a subtype of it, and a free-form type.
func strictGraph(t *testing.T) (*Graph, domain.Baseline) {
	t.Helper()
	d, err := def.ParseDomain([]byte(`
name: shop
version: 1.0.0
nodeTypes:
  - {name: Item, attributes: [title]}
  - {name: Book, extends: Item, attributes: [isbn]}
  - {name: Bag, additionalProperties: true, attributes: [label]}
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
	b, err := g.BranchHead(context.Background(), "shop", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	return g, b
}

// A property that is no attribute of the type of a node, ancestors' included, is refused (ErrInvalid), unless the type
// is free-form; a type the catalogue does not know, and the untyped graph, stay permissive.
func TestCheckAttributesIsStrict(t *testing.T) {
	g, _ := strictGraph(t)
	ix := &typeIndex{cat: g.catalog()}
	node := func(typ string) domain.Node { return domain.Node{Key: "K", Type: typ} }
	check := func(typ string, props map[string]any) error { return ix.checkAttributes(node(typ), props) }

	if err := check("shop@Item", map[string]any{"title": "a"}); err != nil {
		t.Fatalf("a declared attribute: %v", err)
	}
	if err := check("shop@Book", map[string]any{"title": "a", "isbn": "1"}); err != nil {
		t.Fatalf("an inherited attribute: %v", err)
	}
	err := check("shop@Item", map[string]any{"title": "a", "colour": "red"})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `property "colour" is not an attribute of shop@Item`) {
		t.Fatalf("an unknown property: %v", err)
	}
	if err := check("shop@Item", map[string]any{"isbn": "1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an attribute of a subtype is no attribute of its supertype: %v", err)
	}
	if err := check("shop@Bag", map[string]any{"label": "a", "anything": 1}); err != nil {
		t.Fatalf("a free-form type: %v", err)
	}
	if err := check("shop@Nope", map[string]any{"anything": 1}); err != nil {
		t.Fatalf("a type the catalogue does not know: %v", err)
	}
	if err := (&typeIndex{}).checkAttributes(node("shop@Item"), map[string]any{"anything": 1}); err != nil {
		t.Fatalf("the untyped graph: %v", err)
	}
	// the landing check (validateProps) holds a version to it too
	if err := g.validateProps(context.Background(), ix, node("shop@Item"), map[string]any{"colour": "red"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("validateProps: %v", err)
	}
	if err := g.validateProps(context.Background(), ix, node("shop@Book"), map[string]any{"isbn": "1"}); err != nil {
		t.Fatalf("validateProps: %v", err)
	}
}

// Creating a node and editing its working version go through the same rule: through the operations of a change.
func TestImpactNodeOperationsAreStrict(t *testing.T) {
	ctx := context.Background()
	g, b := strictGraph(t)
	c, err := g.CreateChange(ctx, NewChange{Namespace: "shop", Title: "t", BaselineID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Rationale: "r", Key: "I1", Type: "shop@Item", Properties: map[string]any{"colour": "red"}}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not an attribute") {
		t.Fatalf("create with an unknown property: %v", err)
	}
	cn, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Rationale: "r", Key: "B1", Type: "shop@Book", Properties: map[string]any{"title": "t", "isbn": "1"}})
	if err != nil {
		t.Fatalf("create with attributes, inherited ones included: %v", err)
	}
	if _, err := g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"colour": "red"}}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not an attribute") {
		t.Fatalf("update with an unknown property: %v", err)
	}
	if _, err := g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "u"}}); err != nil {
		t.Fatalf("update of an attribute: %v", err)
	}
	if _, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Rationale: "r", Key: "G1", Type: "shop@Bag", Properties: map[string]any{"label": "l", "whatever": true}}); err != nil {
		t.Fatalf("a free-form type takes any property: %v", err)
	}
}
