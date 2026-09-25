package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestImpactPrePost(t *testing.T) { forEachRepo(t, testImpactPrePost) }

func testImpactPrePost(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	c := w.change(t, "rework REQ-1")
	pre := w.req1.Ref()

	reopen, edit, approve := moveItem(w.req1, "draft"), updateItem(w.req1, map[string]any{"title": "one v2"}), moveItem(w.req1, "approved")
	reopen.ID, edit.ID, approve.ID = "reopen", "edit", "approve"
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{reopen, edit, approve}); err != nil {
		t.Fatal(err)
	}

	// post must be a proposal acting on the pre node
	other := domain.ItemID("other")
	o := updateItem(w.req2, map[string]any{"title": "x"})
	o.ID = other
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{o}); err == nil {
		// REQ-2 is proposed (not editable): the proposal itself is refused
		t.Fatal("editing a proposed node must be refused")
	}
	bad := domain.ChangeItem{Kind: domain.KindImpact, Target: &pre, Post: &domain.Endpoint{Item: "nope"}}
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown post item: %v", err)
	}
	req2 := w.req2.Ref()
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{moveItem(w.req2, "draft")}); err != nil {
		t.Fatal(err)
	}
	other2 := updateItem(w.req2, map[string]any{"title": "y"})
	other2.ID = "other2"
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{other2}); err != nil {
		t.Fatal(err)
	}
	wrong := domain.ChangeItem{Kind: domain.KindImpact, Target: &pre, Post: &domain.Endpoint{Item: "other2"}}
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{wrong}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("post on another node: %v", err)
	}
	_ = req2

	good := domain.ChangeItem{ID: "imp", Kind: domain.KindImpact, Type: "direct", Target: &pre, Post: &domain.Endpoint{Item: "edit"}}
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{good}); err != nil {
		t.Fatal(err)
	}
	ims, err := w.g.Impacts(ctx, c.ID)
	if err != nil || len(ims) != 1 {
		t.Fatalf("impacts = %+v, %v", ims, err)
	}
	if ims[0].Pre == nil || ims[0].PreState != "approved" || ims[0].Post != nil || ims[0].Key != "REQ-1" {
		t.Fatalf("before apply: %+v", ims[0])
	}

	// finish REQ-2 so the change can apply
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{updateItem(w.req2, map[string]any{"title": "y"}), moveItem(w.req2, "approved")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	ims, err = w.g.Impacts(ctx, c.ID)
	if err != nil || len(ims) != 1 {
		t.Fatal(ims, err)
	}
	if ims[0].Post == nil || ims[0].Post.Version != 2 || ims[0].PostState != "approved" {
		t.Fatalf("after apply: %+v", ims[0])
	}
}

func TestImpactPreMustBeReleased(t *testing.T) { forEachRepo(t, testImpactPreMustBeReleased) }

func testImpactPreMustBeReleased(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	drafty, err := w.g.CreateNode(ctx, NewNode{Key: "REQ-9", Type: "Requirement", State: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	tReq, err := w.g.NodeByKey(ctx, "", "D:x/nodetype/Requirement")
	if err != nil {
		t.Fatal(err)
	}
	base, err := w.g.CreateBaseline(ctx, "B2", []domain.NodeRef{tReq.Ref(), drafty.Ref(), w.req1.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := w.g.CreateChange(ctx, NewChange{Title: "x", BaselineID: base.ID})
	if err != nil {
		t.Fatal(err)
	}
	ref := drafty.Ref()
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindImpact, Target: &ref}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("editable pre must be refused: %v", err)
	}
}

func TestImpactCreatedNode(t *testing.T) { forEachRepo(t, testImpactCreatedNode) }

func testImpactCreatedNode(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	c, err := f.g.CreateChange(ctx, NewChange{Title: "new", BaselineID: f.base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.g.AddItems(ctx, c.ID, []domain.ChangeItem{
		{ID: "mk", Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Key: "TST-9", Type: "TestCase"}}},
		{Kind: domain.KindImpact, Post: &domain.Endpoint{Item: "mk"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	ims, err := f.g.Impacts(ctx, c.ID)
	if err != nil || len(ims) != 1 || ims[0].Pre != nil || ims[0].Post == nil || ims[0].Key != "TST-9" {
		t.Fatalf("impacts = %+v, %v", ims, err)
	}
}
