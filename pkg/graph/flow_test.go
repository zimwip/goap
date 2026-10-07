package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// upd is a fact of the blackboard (an artifact) that stands for what a step produced.
func upd(id string, _ domain.Node, title string, derived ...domain.ItemID) domain.ChangeItem {
	return domain.ChangeItem{ID: domain.ItemID(id), Kind: domain.KindArtifact, Type: "result", DerivedFrom: derived, Data: map[string]any{"title": title}}
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
	c, _ := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "flow", BaselineID: f.base.ID})
	// step 1 produced p1; step 2 produced p2 from p1; p3 came from elsewhere
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{upd("p1", f.req, "old"), upd("p2", f.need, "derived from p1", "p1"), upd("p3", f.test, "independent")}); err != nil {
		t.Fatal(err)
	}
	fl, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}, Origin: map[string]any{"step": 1, "reason": "the PSP answer changed"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fl.Stale) != 2 || fl.Status != domain.FlowOpen {
		t.Fatalf("flow = %+v", fl)
	}
	// the origin is stored and returned as is (the graph never reads it)
	if full, _ := g.Change(ctx, c.ID); full.Flows()[0].Origin["reason"] != "the PSP answer changed" {
		t.Fatalf("origin = %+v", full.Flows()[0].Origin)
	}
	for item, want := range map[string]domain.ItemStatus{"p1": domain.ItemStale, "p2": domain.ItemStale, "p3": domain.ItemProposed} {
		if got := statusOf(t, g, c.ID, item); got != want {
			t.Fatalf("%s = %s, want %s", item, got, want)
		}
	}
	// no apply while a flow is open
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
	c, _ := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "flow", BaselineID: f.base.ID})
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
	// a new relaunch is possible once the flow is decided
	c2, _ := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "flow2", BaselineID: f.base.ID})
	if _, err := g.AddItems(ctx, c2.ID, []domain.ChangeItem{upd("q1", f.test, "t")}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.OpenFlow(ctx, c2.ID, OpenFlowRequest{Seeds: []domain.ItemID{"nope"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown seed: %v", err)
	}
}

func addFlowItem(t *testing.T, g *Graph, c domain.ChangeID, flow string, it domain.ChangeItem) {
	t.Helper()
	it.Flow = flow
	if _, err := g.AddItems(context.Background(), c, []domain.ChangeItem{it}); err != nil {
		t.Fatal(err)
	}
}

func TestParallelFlowsCompete(t *testing.T) { forEachRepo(t, testParallelFlowsCompete) }

func testParallelFlowsCompete(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, _ := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "parallel", BaselineID: f.base.ID})
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{upd("p1", f.req, "old"), upd("p2", f.need, "derived", "p1"), upd("p3", f.test, "independent")}); err != nil {
		t.Fatal(err)
	}
	// three alternatives open at the same time: two replace p1, one replaces p3
	f1, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}})
	if err != nil {
		t.Fatal(err)
	}
	f2, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}})
	if err != nil {
		t.Fatalf("a second open flow must be allowed: %v", err)
	}
	f3, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p3"}})
	if err != nil {
		t.Fatal(err)
	}
	addFlowItem(t, g, c.ID, f1.ID, upd("c1", f.req, "one"))
	addFlowItem(t, g, c.ID, f2.ID, upd("c2", f.req, "two"))
	addFlowItem(t, g, c.ID, f3.ID, upd("c3", f.test, "three"))

	// each flow reads its own view: the other alternatives are invisible to it
	seen := func(flow string) map[domain.ItemID]bool {
		bb, err := g.BlackboardIn(ctx, c.ID, flow)
		if err != nil {
			t.Fatal(err)
		}
		out := map[domain.ItemID]bool{}
		for _, it := range bb.Change.Items {
			out[it.ID] = true
		}
		return out
	}
	if v := seen(f1.ID); !v["c1"] || v["c2"] || v["p1"] || !v["p3"] {
		t.Fatalf("view of flow 1 = %v", v)
	}
	if v := seen(f2.ID); v["c1"] || !v["c2"] {
		t.Fatalf("view of flow 2 = %v", v)
	}

	if _, err := g.AdoptFlow(ctx, c.ID, f1.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	fs, _ := g.Flows(ctx, c.ID)
	byID := map[string]domain.Flow{}
	for _, fl := range fs {
		byID[fl.ID] = fl
	}
	if got := byID[f2.ID].CompetesWith; len(got) != 1 || got[0] != f1.ID {
		t.Fatalf("flow 2 must compete with flow 1: %+v", byID[f2.ID])
	}
	if len(byID[f3.ID].CompetesWith) != 0 {
		t.Fatalf("flow 3 replaces other items: %+v", byID[f3.ID])
	}
	if _, err := g.AdoptFlow(ctx, c.ID, f2.ID, "alice"); !errors.Is(err, ErrConflict) {
		t.Fatalf("adopting a competing flow: %v", err)
	}
	if _, err := g.DiscardFlow(ctx, c.ID, f2.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.AdoptFlow(ctx, c.ID, f3.ID, "alice"); err != nil {
		t.Fatalf("independent flow: %v", err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
}

func TestFlowGuidanceIsPrivateToTheBranch(t *testing.T) { forEachRepo(t, testFlowGuidance) }

func testFlowGuidance(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, _ := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "guided", BaselineID: f.base.ID})
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{upd("p1", f.req, "old")}); err != nil {
		t.Fatal(err)
	}
	fl, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}, Items: []domain.ChangeItem{{Kind: domain.KindArtifact, Type: "guidance", ProducedBy: "alice",
		Data: map[string]any{"text": "keep PSP v1 compatibility"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Items: []domain.ChangeItem{{Kind: domain.KindFlow}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a flow item at the opening of a flow: %v", err)
	}
	has := func(flow string) bool {
		bb, _ := g.BlackboardIn(ctx, c.ID, flow)
		for _, it := range bb.Change.Items {
			if it.Type == "guidance" && it.Data["text"] == "keep PSP v1 compatibility" && it.ProducedBy == "alice" {
				return true
			}
		}
		return false
	}
	if !has(fl.ID) || has("") {
		t.Fatalf("guidance must be on the branch only: flow=%v main=%v", has(fl.ID), has(""))
	}
	// discarded: it disappears with the branch
	if _, err := g.DiscardFlow(ctx, c.ID, fl.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if has("") {
		t.Fatal("guidance leaked on the main flow")
	}
}
