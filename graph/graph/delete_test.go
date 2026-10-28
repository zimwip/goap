package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A change deletes a node (ADR 0172): the commit writes a tombstone and the node leaves the state; a later change
// creating a node of its key restores it, the same node, and one checking it out restores it as it was.
func TestDeleteAndRestore(t *testing.T) { forEachRepo(t, testDeleteAndRestore) }

func testDeleteAndRestore(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "drop REQ-1", BaselineID: f.base.ID}))
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeDelete(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "obsolete"}))
	if cn.Intent != domain.IntentModified || cn.Post == nil || !cn.Post.IsDraft() {
		t.Fatalf("the deletion checks the node out: %+v", cn)
	}
	d := must[*domain.Draft](t)(g.draftOf(ctx, c.ID, "", cn.ID))
	if d == nil || !d.Deleted || len(d.Links) != 0 || d.Properties["title"] != "Use PSP v1" {
		t.Fatalf("the draft is deleted, its links dropped, its properties kept: %+v", d)
	}
	if _, err := g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "x"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a deleted draft is not edited: %v", err)
	}
	if again := must[domain.ChangeImpact](t)(g.ImpactNodeDelete(ctx, c.ID, NodeCheckout{Impact: cn.ID})); again.ID != cn.ID {
		t.Fatalf("deleting twice is deleting once: %+v", again)
	}
	created := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-8", Type: "Requirement", Rationale: "new"}))
	if _, err := g.ImpactNodeDelete(ctx, c.ID, NodeCheckout{Impact: created.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a creation is cancelled, not deleted: %v", err)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeCancel(ctx, c.ID, created.ID, "", ""))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, cn.ID, "bob", "gone"))
	gone := g.withState(t, ctx, must[domain.Baseline](t)(g.apply(ctx, c.ID, "")))
	if _, ok := gone.Nodes[f.req.ID]; ok {
		t.Fatalf("a deleted node leaves the state: %v", gone.Nodes)
	}
	if sus := must[[]domain.Link](t)(g.SuspectLinks(ctx, gone.ID)); len(sus) != 1 || sus[0].To.ID != f.req.ID {
		t.Fatalf("a link to a deleted node is suspect: %+v", sus)
	}
	tomb := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: f.req.Version + 1}))
	if !tomb.Deleted || tomb.Properties["title"] != "Use PSP v1" {
		t.Fatalf("the tombstone keeps the last properties: %+v", tomb)
	}
	if out := must[[]domain.Link](t)(g.OutLinksOf(ctx, tomb.Ref())); len(out) != 0 {
		t.Fatalf("and holds no link: %+v", out)
	}

	// created again: the same node, restored
	c2 := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "REQ-1 again", BaselineID: gone.ID}))
	back := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c2.ID, NodeCreate{Key: "REQ-1", Type: "Requirement", Rationale: "needed again",
		Properties: map[string]any{"title": "Use PSP v3"}, Links: []LinkWrite{{Type: "satisfies", To: f.need.Ref()}}}))
	if back.Intent != domain.IntentModified || back.Pre == nil || *back.Pre != tomb.Ref() || back.Post == nil || back.Post.ID != f.req.ID {
		t.Fatalf("the creation of a deleted key restores the node from its tombstone: %+v", back)
	}
	if _, err := g.ImpactNodeCreate(ctx, c2.ID, NodeCreate{Key: "NEED-1", Type: "Requirement", Rationale: "x"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a live key is still taken: %v", err)
	}
	must[domain.ChangeImpact](t)(g.accept(ctx, c2.ID, back.ID, "bob", "back"))
	res := g.withState(t, ctx, must[domain.Baseline](t)(g.apply(ctx, c2.ID, "")))
	n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: res.Nodes[f.req.ID]}))
	if n.Deleted || n.Version != tomb.Version+1 || n.Properties["title"] != "Use PSP v3" || len(n.Parents) != 1 || n.Parents[0] != tomb.Version {
		t.Fatalf("restored as created, over its tombstone: %+v", n)
	}
	if out := must[[]domain.Link](t)(g.OutLinksOf(ctx, n.Ref())); len(out) != 1 || out[0].To.ID != f.need.ID {
		t.Fatalf("with the links of its creation: %+v", out)
	}

	// a checkout of the deleted node restores it as it was deleted
	c3 := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "undo", BaselineID: gone.ID}))
	undo := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c3.ID, NodeCheckout{Node: f.req.ID, Rationale: "undo"}))
	if undo.Pre == nil || *undo.Pre != tomb.Ref() {
		t.Fatalf("checked out from its tombstone: %+v", undo)
	}
	if d := must[*domain.Draft](t)(g.draftOf(ctx, c3.ID, "", undo.ID)); d == nil || d.Deleted || d.Properties["title"] != "Use PSP v1" {
		t.Fatalf("restored as it was: %+v", d)
	}
	if !must[bool](t)(g.ImpactModifies(ctx, c3.ID, undo.ID)) {
		t.Fatal("a restoration is a modification")
	}
}
