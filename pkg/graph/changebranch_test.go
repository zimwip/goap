package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestChangeBranch(t *testing.T) { forEachRepo(t, testChangeBranch) }

func setProp(t *testing.T, g *Graph, f fixture, base domain.BaselineID, title string, props map[string]any) domain.ChangeSet {
	t.Helper()
	ctx := context.Background()
	c, err := g.CreateChange(ctx, NewChange{Title: title, BaselineID: base, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	ref := f.req.Ref()
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindProposal,
		Proposal: &domain.Proposal{Op: domain.OpUpdateNode, Node: &domain.NodeDraft{Base: &ref, Properties: props}}}}); err != nil {
		t.Fatal(err)
	}
	return c
}

func testChangeBranch(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g

	a := setProp(t, g, f, f.base.ID, "A", map[string]any{"title": "A title", "owner": "x"})
	b := setProp(t, g, f, f.base.ID, "B", map[string]any{"title": "B title", "owner": "x"})
	if a.Branch == "main" || a.Branch == b.Branch {
		t.Fatalf("each change needs its own branch: %q %q", a.Branch, b.Branch)
	}
	// versions live on the change branch until merged: main still sees v1
	shared, err := g.SharedNodes(ctx, a.ID)
	if err != nil || len(shared) != 1 || shared[0].Changes[0] != b.ID {
		t.Fatalf("shared nodes = %+v, %v", shared, err)
	}

	// A applies: merged into main (no conflict)
	ba, err := g.Apply(ctx, a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if ba.Branch != "main" {
		t.Fatalf("result baseline branch = %q", ba.Branch)
	}
	ac, _ := g.Change(ctx, a.ID)
	if ac.Status != domain.ChangeApplied || ac.ResultBaselineID != ba.ID {
		t.Fatalf("A = %s %s", ac.Status, ac.ResultBaselineID)
	}
	if br, _ := g.Branch(ctx, a.Branch); br.Status != domain.BranchMerged {
		t.Fatalf("A branch = %s", br.Status)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID}); n.Properties["title"] != "A title" {
		t.Fatalf("main not updated: %v", n.Properties)
	}

	// B changes the same property from the same base: merge required
	if _, err := g.Apply(ctx, b.ID, ""); err != nil {
		t.Fatal(err)
	}
	bc, _ := g.Change(ctx, b.ID)
	if bc.Status != domain.ChangeMergePending {
		t.Fatalf("B = %s, want merge_pending", bc.Status)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID}); n.Properties["title"] != "A title" {
		t.Fatalf("pending change leaked on main: %v", n.Properties)
	}
	// the change branch is visible to the change, not on main
	if n, err := g.NodeByKeyOn(ctx, "", b.Branch, "REQ-1"); err != nil || n.Properties["title"] != "B title" {
		t.Fatalf("NodeByKeyOn(branch) = %v, %v", n.Properties, err)
	}
	if n, err := g.NodeByKeyOn(ctx, "", "main", "REQ-1"); err != nil || n.Properties["title"] != "A title" {
		t.Fatalf("NodeByKeyOn(main) = %v, %v", n.Properties, err)
	}
	if _, err := g.MergeChange(ctx, b.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("merge without resolution: %v", err)
	}
	bc, err = g.MergeChange(ctx, b.ID, map[domain.NodeID]Resolution{f.req.ID: {Props: map[string]any{"title": "merged", "owner": "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if bc.Status != domain.ChangeApplied {
		t.Fatalf("B = %s", bc.Status)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID}); n.Properties["title"] != "merged" {
		t.Fatalf("resolution not applied: %v", n.Properties)
	}
}

func TestChangeBranchDisjointProps(t *testing.T) { forEachRepo(t, testChangeBranchDisjoint) }

func testChangeBranchDisjoint(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	a := setProp(t, g, f, f.base.ID, "A", map[string]any{"title": "Use PSP v1", "a": 1})
	b := setProp(t, g, f, f.base.ID, "B", map[string]any{"title": "Use PSP v1", "b": 2})
	for _, c := range []domain.ChangeSet{a, b} {
		if _, err := g.Apply(ctx, c.ID, ""); err != nil {
			t.Fatal(err)
		}
		if got, _ := g.Change(ctx, c.ID); got.Status != domain.ChangeApplied {
			t.Fatalf("%s = %s", c.Title, got.Status)
		}
	}
	n, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID})
	if n.Properties["a"] != float64(1) && n.Properties["a"] != 1 || n.Properties["b"] != float64(2) && n.Properties["b"] != 2 {
		t.Fatalf("both changes must survive the 3-way merge: %v", n.Properties)
	}
}

func TestAbandonChangeBranch(t *testing.T) { forEachRepo(t, testAbandonChangeBranch) }

func testAbandonChangeBranch(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	c := setProp(t, f.g, f, f.base.ID, "A", map[string]any{"x": 1})
	st := domain.ChangeAbandoned
	if _, err := f.g.UpdateChange(ctx, c.ID, ChangePatch{Status: &st}); err != nil {
		t.Fatal(err)
	}
	if b, _ := f.g.Branch(ctx, c.Branch); b.Status != domain.BranchAbandoned {
		t.Fatalf("branch = %s", b.Status)
	}
}
