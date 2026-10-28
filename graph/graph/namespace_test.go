package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
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
	// a creation of a key a namespace holds is the modification of that node (ADR 0172)
	again, err := importNode(ctx, g, newNode{Key: "X-1", Type: "Thing", Properties: map[string]any{"title": "again"}})
	if err != nil || again.ID != sdlc.ID {
		t.Fatalf("a key is one node in one namespace: %v, %v", again.ID, err)
	}
	sdlc = again
	if _, err := importNode(ctx, g, newNode{Key: "X-1", Type: "OtherThing"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a key of another type: %v", err)
	}
	if n, err := g.NodeByKey(ctx, "organisation", "X-1"); err != nil || n.ID != org.ID {
		t.Fatalf("NodeByKey(organisation) = %v, %v", n.ID, err)
	}
	if n, err := g.NodeByKey(ctx, "", "X-1"); err != nil || n.ID != sdlc.ID || n.Properties["title"] != "again" {
		t.Fatalf("NodeByKey(default) = %v, %v", n.ID, err)
	}

	base, err := g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "t", ForkCommit: base.ID})
	if err != nil {
		t.Fatal(err)
	}
	if c.Namespace != domain.DefaultNamespace {
		t.Fatalf("change namespace = %q", c.Namespace)
	}
	orgRef, sdlcRef := org.Ref(), sdlc.Ref()
	// a node of another namespace can never be in the change's own (now namespace-scoped)
	// reference commit, so modifying it is refused even before the namespace itself is checked
	_, err = g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &orgRef, Rationale: "x"}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("update across namespaces: %v", err)
	}
	// a link to a node of another namespace is allowed, and created nodes belong to the change namespace
	ns, err := g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{
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
	if _, err := g.apply(ctx, c.ID, "B2"); err != nil {
		t.Fatal(err)
	}
	if n, err := g.NodeByKey(ctx, "", "Y"); err != nil || n.Namespace != domain.DefaultNamespace {
		t.Fatalf("created node = %+v, %v", n, err)
	}
}
