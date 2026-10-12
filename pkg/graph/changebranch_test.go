package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestChangeBranch(t *testing.T) { forEachRepo(t, testChangeBranch) }

func setProp(t *testing.T, g *Graph, f fixture, base domain.BaselineID, title string, props map[string]any) domain.Change {
	t.Helper()
	ctx := context.Background()
	c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: title, BaselineID: base, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	ref := f.req.Ref()
	ns, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &ref, Rationale: title}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.edit(ctx, c.ID, ns[0].ID, edit{Properties: props}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.accept(ctx, c.ID, ns[0].ID, "u", "ok"); err != nil {
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
	if br, _ := g.Branch(ctx, "", a.Branch); br.Status != domain.BranchMerged {
		t.Fatalf("A branch = %s", br.Status)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID}); n.Properties["title"] != "A title" {
		t.Fatalf("main not updated: %v", n.Properties)
	}

	// B changes the same property from the same base: the apply commits it, rebases its branch onto A and stops on the
	// conflict (ADR 0102)
	if _, err := g.Apply(ctx, b.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("apply with a conflict: %v", err)
	}
	bc, _ := g.Change(ctx, b.ID)
	if bc.Status != domain.ChangeActive || bc.BaselineID != ba.ID {
		t.Fatalf("B = %s on %s, want active, rebased onto %s", bc.Status, bc.BaselineID, ba.ID)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID}); n.Properties["title"] != "A title" {
		t.Fatalf("pending change leaked on main: %v", n.Properties)
	}
	// the author settles it (B keeps its values) and applies: its merge version (B's commit and A's version as parents)
	// is committed, then a fast-forward on top of A
	settle(t, g, b.ID)
	bb, err := g.Apply(ctx, b.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if bb.Kind != domain.BaselineFastForward || bb.ParentID != ba.ID {
		t.Fatalf("B's baseline = %s on %s, want a fast-forward of %s", bb.Kind, bb.ParentID, ba.ID)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID}); n.Properties["title"] != "B title" || len(n.Parents) != 2 {
		t.Fatalf("B not landed on A: %v %v", n.Properties, n.Parents)
	}
}

// settle keeps the values of a change in its conflicts (ImpactNodeResolve) and accepts what is proposed again.
func settle(t *testing.T, g *Graph, id domain.ChangeID) {
	t.Helper()
	ctx := context.Background()
	st, err := g.RebaseState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range st.Conflicts {
		if _, err := g.ImpactNodeResolve(ctx, id, c.Impact, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.acceptAll(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestChangeBranchDisjointProps(t *testing.T) { forEachRepo(t, testChangeBranchDisjoint) }

func testChangeBranchDisjoint(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	a := setProp(t, g, f, f.base.ID, "A", map[string]any{"title": "Use PSP v1", "a": 1})
	b := setProp(t, g, f, f.base.ID, "B", map[string]any{"title": "Use PSP v1", "b": 2})
	for _, c := range []domain.Change{a, b} {
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
	if b, _ := f.g.Branch(ctx, "", c.Branch); b.Status != domain.BranchAbandoned {
		t.Fatalf("branch = %s", b.Status)
	}
}
