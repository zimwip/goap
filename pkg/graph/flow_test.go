package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func upd(id string, n domain.Node, title string, derived ...domain.ItemID) domain.ChangeItem {
	ref := n.Ref()
	return domain.ChangeItem{ID: domain.ItemID(id), Kind: domain.KindProposal, DerivedFrom: derived,
		Proposal: &domain.Proposal{Op: domain.OpUpdateNode, Node: &domain.NodeDraft{Base: &ref, Properties: map[string]any{"title": title}}}}
}

func statusOf(t *testing.T, g *Graph, c domain.ChangeID, item string) domain.ItemStatus {
	t.Helper()
	full, err := g.Change(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return full.EffectiveStatus(domain.ItemID(item))
}

func TestFlowRelaunchAdopt(t *testing.T) { forEachRepo(t, testFlowRelaunchAdopt) }

func testFlowRelaunchAdopt(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, _ := g.CreateChange(ctx, NewChange{Title: "flow", BaselineID: f.base.ID})
	// step 1 produced p1; step 2 produced p2 from p1; p3 came from elsewhere
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{upd("p1", f.req, "old"), upd("p2", f.need, "derived from p1", "p1"), upd("p3", f.test, "independent")}); err != nil {
		t.Fatal(err)
	}
	fl, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}, FromStep: 1, Reason: "the PSP answer changed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fl.Stale) != 2 || fl.Status != domain.FlowOpen {
		t.Fatalf("flow = %+v", fl)
	}
	for item, want := range map[string]domain.ItemStatus{"p1": domain.ItemStale, "p2": domain.ItemStale, "p3": domain.ItemProposed} {
		if got := statusOf(t, g, c.ID, item); got != want {
			t.Fatalf("%s = %s, want %s", item, got, want)
		}
	}
	// only one open flow, and no apply while it is open
	if _, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p3"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second open flow: %v", err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("apply with an open flow: %v", err)
	}

	// the replanned run appends candidates on the flow
	cand := upd("c1", f.req, "new")
	cand.Flow = fl.ID
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{cand}); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, g, c.ID, "c1"); got != domain.ItemCandidate {
		t.Fatalf("c1 = %s", got)
	}
	main, _ := g.Blackboard(ctx, c.ID)
	for _, it := range main.Change.Items {
		if it.ID == "c1" {
			t.Fatal("main blackboard must not show candidates")
		}
	}
	fb, err := g.BlackboardIn(ctx, c.ID, fl.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[domain.ItemID]bool{}
	for _, it := range fb.Change.Items {
		seen[it.ID] = true
	}
	if seen["p1"] || seen["p2"] || !seen["p3"] || !seen["c1"] {
		t.Fatalf("flow view = %v", seen)
	}
	// a batch cannot mix flows, nor target a closed flow
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{cand, upd("x", f.test, "y")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mixed batch: %v", err)
	}

	if _, err := g.AdoptFlow(ctx, c.ID, fl.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	for item, want := range map[string]domain.ItemStatus{"p1": domain.ItemSuperseded, "p2": domain.ItemSuperseded, "p3": domain.ItemProposed, "c1": domain.ItemProposed} {
		if got := statusOf(t, g, c.ID, item); got != want {
			t.Fatalf("after adopt %s = %s, want %s", item, got, want)
		}
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID}); n.Properties["title"] != "new" {
		t.Fatalf("REQ-1 = %v", n.Properties)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.need.ID}); n.Version != 1 {
		t.Fatalf("the superseded derived update must not apply: %v", n.Version)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.test.ID}); n.Properties["title"] != "independent" {
		t.Fatalf("independent item lost: %v", n.Properties)
	}
	fs, _ := g.Flows(ctx, c.ID)
	if len(fs) != 1 || fs[0].Status != domain.FlowAdopted || fs[0].DecidedBy != "alice" {
		t.Fatalf("flows = %+v", fs)
	}
}

func TestFlowDiscard(t *testing.T) { forEachRepo(t, testFlowDiscard) }

func testFlowDiscard(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, _ := g.CreateChange(ctx, NewChange{Title: "flow", BaselineID: f.base.ID})
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{upd("p1", f.req, "old")}); err != nil {
		t.Fatal(err)
	}
	fl, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}})
	if err != nil {
		t.Fatal(err)
	}
	cand := upd("c1", f.req, "new")
	cand.Flow = fl.ID
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{cand}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.DiscardFlow(ctx, c.ID, fl.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, g, c.ID, "p1"); got != domain.ItemProposed {
		t.Fatalf("p1 = %s: the old run counts again", got)
	}
	if got := statusOf(t, g, c.ID, "c1"); got != domain.ItemRejected {
		t.Fatalf("c1 = %s", got)
	}
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{cand}); !errors.Is(err, ErrConflict) {
		t.Fatalf("adding to a discarded flow: %v", err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID}); n.Properties["title"] != "old" {
		t.Fatalf("REQ-1 = %v", n.Properties)
	}
	// a new relaunch is possible once the flow is decided
	c2, _ := g.CreateChange(ctx, NewChange{Title: "flow2", BaselineID: f.base.ID})
	if _, err := g.AddItems(ctx, c2.ID, []domain.ChangeItem{upd("q1", f.test, "t")}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.OpenFlow(ctx, c2.ID, OpenFlowRequest{Seeds: []domain.ItemID{"nope"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown seed: %v", err)
	}
}
