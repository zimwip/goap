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
	f.need, err = importNode(ctx, g, newNode{Key: "NEED-1", Type: "Need", Properties: map[string]any{"title": "Pay online"}})
	must(err)
	f.req, err = importNode(ctx, g, newNode{Key: "REQ-1", Type: "Requirement", Properties: map[string]any{"title": "Use PSP v1"},
		Links: []LinkWrite{{Type: "satisfies", To: f.need.Ref()}}})
	must(err)
	f.test, err = importNode(ctx, g, newNode{Key: "TST-1", Type: "TestCase", Links: []LinkWrite{{Type: "verifies", To: f.req.Ref()}}})
	must(err)
	f.base, err = g.BranchHead(ctx, "", domain.MainBranch)
	must(err)
	return f
}

func TestApplyUpdateCreatesSuspectLinks(t *testing.T) {
	forEachRepo(t, testApplyUpdateCreatesSuspectLinks)
}

func testApplyUpdateCreatesSuspectLinks(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g

	c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "PSP v2", BaselineID: f.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	reqRef := f.req.Ref()
	cns, err := g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{
		{Intent: domain.IntentModified, Pre: &reqRef, Rationale: "PSP v2"},
		{Intent: domain.IntentCreated, Key: "TST-2", Type: "TestCase", Rationale: "cover REQ-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.edit(ctx, c.ID, cns[0].ID, edit{Properties: map[string]any{"title": "Use PSP v2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.edit(ctx, c.ID, cns[1].ID, edit{AddLinks: []LinkWrite{{Type: "verifies", To: reqRef}}}); err != nil {
		t.Fatal(err)
	}
	for _, n := range cns {
		if _, err := g.accept(ctx, c.ID, n.ID, "u", "ok"); err != nil {
			t.Fatal(err)
		}
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
	c, _ := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "drop test", BaselineID: f.base.ID})
	pre := f.test.Ref()
	ns, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "the test no longer verifies"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.edit(ctx, c.ID, ns[0].ID, edit{RemoveLinks: []domain.LinkID{v.Out[0].ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.accept(ctx, c.ID, ns[0].ID, "u", "ok"); err != nil {
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
	// each change writes the same property with its own value, and accepts (or rejects) it
	change := func(title string, value int, accept bool) domain.Change {
		c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: title, BaselineID: f.base.ID})
		if err != nil {
			t.Fatal(err)
		}
		ns, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &reqRef, Rationale: title}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.edit(ctx, c.ID, ns[0].ID, edit{Properties: map[string]any{"x": value}}); err != nil {
			t.Fatal(err)
		}
		status := domain.ReviewAccepted
		if !accept {
			status = domain.ReviewRejected
		}
		if _, err := g.ImpactNodeReview(ctx, c.ID, ns[0].ID, status, "u", "decided"); err != nil {
			t.Fatal(err)
		}
		return c
	}
	c1, c2 := change("c1", 1, false), change("c2", 1, true)
	// the only change impact of c1 is rejected: applying it is a no-op
	b, err := g.Apply(ctx, c1.ID, "")
	if err != nil || b.Nodes[f.req.ID] != 1 {
		t.Fatalf("rejected change impact applied: %v %v", err, b.Nodes)
	}
	if _, err := g.Apply(ctx, c2.ID, ""); err != nil {
		t.Fatal(err)
	}
	// a third change from B1 writes another value of the same property: a merge is needed
	c3 := change("c3", 2, true)
	if _, err := g.Apply(ctx, c3.ID, ""); err != nil {
		t.Fatal(err)
	}
	if c3b, _ := g.Change(ctx, c3.ID); c3b.Status != domain.ChangeCommitted {
		t.Fatalf("expected committed, got %s", c3b.Status)
	}
}

func TestAddItemsValidation(t *testing.T) { forEachRepo(t, testAddItemsValidation) }

func testAddItemsValidation(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	c, _ := f.g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "c", BaselineID: f.base.ID})
	bad := domain.NodeRef{ID: f.req.ID, Version: 9}
	if _, err := f.g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &bad, Rationale: "x"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected a conflict (the version is not in the baseline), got %v", err)
	}
	if _, err := f.g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: "impact"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the impact kind is gone, got %v", err)
	}
	if _, err := f.g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindDecision, Decision: &domain.Decision{Item: "nope"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected invalid, got %v", err)
	}
}
