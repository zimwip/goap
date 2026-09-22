package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

type fixture struct {
	g               *Graph
	need, req, test domain.Node
	base            domain.Baseline
}

// need <-satisfies- req <-verifies- test
func newFixture(t *testing.T, repo Repo) fixture {
	t.Helper()
	ctx := context.Background()
	g := New(repo)
	f := fixture{g: g}
	var err error
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	f.need, err = g.CreateNode(ctx, NewNode{Key: "NEED-1", Type: "Need", Properties: map[string]any{"title": "Pay online"}})
	must(err)
	f.req, err = g.CreateNode(ctx, NewNode{Key: "REQ-1", Type: "Requirement", Properties: map[string]any{"title": "Use PSP v1"}})
	must(err)
	f.test, err = g.CreateNode(ctx, NewNode{Key: "TST-1", Type: "TestCase"})
	must(err)
	_, err = g.Link(ctx, "satisfies", f.req.Ref(), f.need.Ref(), nil)
	must(err)
	_, err = g.Link(ctx, "verifies", f.test.Ref(), f.req.Ref(), nil)
	must(err)
	f.base, err = g.CreateBaseline(ctx, "B1", []domain.NodeRef{f.need.Ref(), f.req.Ref(), f.test.Ref()})
	must(err)
	return f
}

func TestApplyUpdateCreatesSuspectLinks(t *testing.T) { forEachRepo(t, testApplyUpdateCreatesSuspectLinks) }

func testApplyUpdateCreatesSuspectLinks(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g

	c, err := g.CreateChange(ctx, NewChange{Title: "PSP v2", BaselineID: f.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	reqRef := f.req.Ref()
	items, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{
		{Kind: domain.KindImpact, Type: "direct", Target: &reqRef},
		{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpUpdateNode,
			Node: &domain.NodeDraft{Base: &reqRef, Properties: map[string]any{"title": "Use PSP v2"}}}},
		{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpCreateNode,
			Node: &domain.NodeDraft{Key: "TST-2", Type: "TestCase"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	newTest := items[2].ID
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{
		{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpAddLink,
			Link: &domain.LinkDraft{Type: "verifies", From: domain.Endpoint{Item: newTest}, To: domain.Endpoint{Node: &reqRef}}}},
	}); err != nil {
		t.Fatal(err)
	}

	bb, err := g.Blackboard(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v := bb.Nodes[reqRef]; v.Key != "REQ-1" || len(v.In) != 1 || len(v.Out) != 1 {
		t.Fatalf("unexpected hydration: %+v", v)
	}

	b2, err := g.Apply(ctx, c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if b2.Nodes[f.req.ID] != 2 {
		t.Fatalf("REQ-1 should be at v2 in the target baseline, got %v", b2.Nodes[f.req.ID])
	}
	if b2.Nodes[f.need.ID] != 1 || b2.Nodes[f.test.ID] != 1 {
		t.Fatalf("untouched nodes must keep their version: %+v", b2.Nodes)
	}
	req2, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID})
	if req2.Properties["title"] != "Use PSP v2" {
		t.Fatalf("props not updated: %v", req2.Properties)
	}

	nodes, links, err := g.BaselineGraph(ctx, b2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(nodes))
	}
	// carried: REQ-1@v2 satisfies NEED-1@v1 ; added: TST-2@v1 verifies REQ-1@v2
	if len(links) != 2 {
		t.Fatalf("expected 2 links in B2, got %+v", links)
	}
	suspects, err := g.SuspectLinks(ctx, b2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(suspects) != 1 || suspects[0].From != f.test.Ref() {
		t.Fatalf("TST-1 verifies REQ-1@v1 must be suspect, got %+v", suspects)
	}
	// The reference baseline is untouched.
	_, links1, _ := g.BaselineGraph(ctx, f.base.ID)
	if len(links1) != 2 {
		t.Fatalf("reference baseline changed: %+v", links1)
	}
	sus1, _ := g.SuspectLinks(ctx, f.base.ID)
	if len(sus1) != 0 {
		t.Fatalf("reference baseline has suspects: %+v", sus1)
	}
}

func TestApplyRemoveLinkBumpsSource(t *testing.T) { forEachRepo(t, testApplyRemoveLinkBumpsSource) }

func testApplyRemoveLinkBumpsSource(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	v, _ := g.View(ctx, f.test.Ref())
	c, _ := g.CreateChange(ctx, NewChange{Title: "drop test", BaselineID: f.base.ID})
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindProposal,
		Proposal: &domain.Proposal{Op: domain.OpRemoveLink, Link: &domain.LinkDraft{LinkID: v.Out[0].ID}}}}); err != nil {
		t.Fatal(err)
	}
	b2, err := g.Apply(ctx, c.ID, "B2")
	if err != nil {
		t.Fatal(err)
	}
	if b2.Nodes[f.test.ID] != 2 {
		t.Fatalf("TST-1 should be bumped")
	}
	_, links, _ := g.BaselineGraph(ctx, b2.ID)
	if len(links) != 1 || links[0].Type != "satisfies" {
		t.Fatalf("unexpected links %+v", links)
	}
}

func TestApplyRejectedAndConflicts(t *testing.T) { forEachRepo(t, testApplyRejectedAndConflicts) }

func testApplyRejectedAndConflicts(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	reqRef := f.req.Ref()
	c1, _ := g.CreateChange(ctx, NewChange{Title: "c1", BaselineID: f.base.ID})
	c2, _ := g.CreateChange(ctx, NewChange{Title: "c2", BaselineID: f.base.ID})
	upd := domain.ChangeItem{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpUpdateNode,
		Node: &domain.NodeDraft{Base: &reqRef, Properties: map[string]any{"x": 1}}}}
	it1, _ := g.AddItems(ctx, c1.ID, []domain.ChangeItem{upd})
	if _, err := g.AddItems(ctx, c2.ID, []domain.ChangeItem{upd}); err != nil {
		t.Fatal(err)
	}
	// reject the only proposal of c1: applying it is a no-op
	if _, err := g.AddItems(ctx, c1.ID, []domain.ChangeItem{{Kind: domain.KindDecision, Decision: &domain.Decision{Item: it1[0].ID}}}); err != nil {
		t.Fatal(err)
	}
	b, err := g.Apply(ctx, c1.ID, "")
	if err != nil || b.Nodes[f.req.ID] != 1 {
		t.Fatalf("rejected proposal applied: %v %v", err, b.Nodes)
	}
	if _, err := g.Apply(ctx, c2.ID, ""); err != nil {
		t.Fatal(err)
	}
	// a third change from B1 now conflicts on REQ-1
	c3, _ := g.CreateChange(ctx, NewChange{Title: "c3", BaselineID: f.base.ID})
	_, _ = g.AddItems(ctx, c3.ID, []domain.ChangeItem{upd})
	if _, err := g.Apply(ctx, c3.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	// the failed apply was rolled back
	c3b, _ := g.Change(ctx, c3.ID)
	if c3b.Status == domain.ChangeApplied {
		t.Fatal("failed apply must not change status")
	}
}

func TestAddItemsValidation(t *testing.T) { forEachRepo(t, testAddItemsValidation) }

func testAddItemsValidation(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	c, _ := f.g.CreateChange(ctx, NewChange{Title: "c", BaselineID: f.base.ID})
	bad := domain.NodeRef{ID: f.req.ID, Version: 9}
	if _, err := f.g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindImpact, Target: &bad}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected invalid, got %v", err)
	}
	if _, err := f.g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindDecision, Decision: &domain.Decision{Item: "nope"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected invalid, got %v", err)
	}
}
