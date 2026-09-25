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

	sdlc, err := g.CreateNode(ctx, NewNode{Key: "X-1", Type: "Thing"})
	if err != nil {
		t.Fatal(err)
	}
	if sdlc.Namespace != domain.DefaultNamespace {
		t.Fatalf("default namespace = %q", sdlc.Namespace)
	}
	// the same key can live in another namespace
	org, err := g.CreateNode(ctx, NewNode{Namespace: "organisation", Key: "X-1", Type: "OrgUnit"})
	if err != nil {
		t.Fatalf("same key in another namespace: %v", err)
	}
	if _, err := g.CreateNode(ctx, NewNode{Key: "X-1", Type: "Thing"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate key in one namespace: %v", err)
	}
	if n, err := g.NodeByKey(ctx, "organisation", "X-1"); err != nil || n.ID != org.ID {
		t.Fatalf("NodeByKey(organisation) = %v, %v", n.ID, err)
	}
	if n, err := g.NodeByKey(ctx, "", "X-1"); err != nil || n.ID != sdlc.ID {
		t.Fatalf("NodeByKey(default) = %v, %v", n.ID, err)
	}

	base, err := g.CreateBaseline(ctx, "B", []domain.NodeRef{sdlc.Ref(), org.Ref()})
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
	// modifying a node of another namespace is refused
	_, err = g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindProposal, Type: "x",
		Proposal: &domain.Proposal{Op: domain.OpUpdateNode, Node: &domain.NodeDraft{Base: &orgRef, Properties: map[string]any{"a": 1}}}}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("update across namespaces: %v", err)
	}
	// creating in another namespace is refused
	_, err = g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindProposal, Type: "x",
		Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Namespace: "organisation", Key: "Y", Type: "T"}}}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("create across namespaces: %v", err)
	}
	// an owner link to another namespace is allowed, and created nodes inherit the change namespace
	id := domain.ItemID("n1")
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{
		{ID: id, Kind: domain.KindProposal, Type: "x", Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Key: "Y", Type: "T"}}},
		{Kind: domain.KindProposal, Type: "x", Proposal: &domain.Proposal{Op: domain.OpAddLink, Link: &domain.LinkDraft{Type: "owner",
			From: domain.Endpoint{Node: &sdlcRef}, To: domain.Endpoint{Node: &orgRef}}}},
	}); err != nil {
		t.Fatalf("cross-namespace link: %v", err)
	}
	if _, err := g.Apply(ctx, c.ID, "B2"); err != nil {
		t.Fatal(err)
	}
	if n, err := g.NodeByKey(ctx, "", "Y"); err != nil || n.Namespace != domain.DefaultNamespace {
		t.Fatalf("created node = %+v, %v", n, err)
	}
}
