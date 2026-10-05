package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestNamespaces(t *testing.T) { forEachRepo(t, testNamespaces) }

func testNamespaces(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)

	sdlc, err := importNode(ctx, g, newNode{Key: "X-1", Type: "Thing"})
	if err != nil {
		t.Fatal(err)
	}
	if sdlc.Namespace != domain.DefaultNamespace {
		t.Fatalf("default namespace = %q", sdlc.Namespace)
	}
	// the same key can live in another namespace
	org, err := importNode(ctx, g, newNode{Namespace: "organisation", Key: "X-1", Type: "OrgUnit"})
	if err != nil {
		t.Fatalf("same key in another namespace: %v", err)
	}
	if _, err := importNode(ctx, g, newNode{Key: "X-1", Type: "Thing"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate key in one namespace: %v", err)
	}
	if n, err := g.NodeByKey(ctx, "organisation", "X-1"); err != nil || n.ID != org.ID {
		t.Fatalf("NodeByKey(organisation) = %v, %v", n.ID, err)
	}
	if n, err := g.NodeByKey(ctx, "", "X-1"); err != nil || n.ID != sdlc.ID {
		t.Fatalf("NodeByKey(default) = %v, %v", n.ID, err)
	}

	base, err := g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, NewChange{Title: "t", BaselineID: base.ID})
	if err != nil {
		t.Fatal(err)
	}
	if c.Namespace != domain.DefaultNamespace {
		t.Fatalf("change namespace = %q", c.Namespace)
	}
	orgRef, sdlcRef := org.Ref(), sdlc.Ref()
	// a node of another namespace can never be in the change's own (now namespace-scoped)
	// reference baseline, so modifying it is refused even before the namespace itself is checked
	_, err = g.AddNodes(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &orgRef, Rationale: "x"}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("update across namespaces: %v", err)
	}
	// a link to a node of another namespace is allowed, and created nodes belong to the change namespace
	ns, err := g.AddNodes(ctx, c.ID, []domain.ChangeImpact{
		{Intent: domain.IntentCreated, Key: "Y", Type: "T", Rationale: "new"},
		{Intent: domain.IntentModified, Pre: &sdlcRef, Rationale: "owned by the unit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct {
		n domain.ChangeImpact
		w edit
	}{{ns[0], edit{Properties: map[string]any{"a": 1}}}, {ns[1], edit{AddLinks: []LinkWrite{{Type: "owner", To: orgRef}}}}} {
		if _, err := g.edit(ctx, c.ID, w.n.ID, w.w); err != nil {
			t.Fatalf("cross-namespace link: %v", err)
		}
		if _, err := g.accept(ctx, c.ID, w.n.ID, "u", "ok"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Apply(ctx, c.ID, "B2"); err != nil {
		t.Fatal(err)
	}
	if n, err := g.NodeByKey(ctx, "", "Y"); err != nil || n.Namespace != domain.DefaultNamespace {
		t.Fatalf("created node = %+v, %v", n, err)
	}
}
