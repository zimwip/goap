package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// flowWorld is a change with its own branch where two steps ran (e1 wrote REQ-1, e2 created TST-2),
// then a flow relaunches from the first step.
type flowWorld struct {
	g      *Graph
	f      fixture
	change domain.Change
	req    domain.ChangeImpact // REQ-1 declared and written by e1
	tst    domain.ChangeImpact // TST-2 declared and written by e2
	flow   string
}

func newFlowWorld(t *testing.T, repo Repo) flowWorld {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, err := g.CreateChange(ctx, NewChange{Title: "PSP v2", BaselineID: f.base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	pre := f.req.Ref()
	must := func(cn domain.ChangeImpact, err error) domain.ChangeImpact {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return cn
	}
	added, err := g.AddNodes(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "PSP v2 changes the API", Execution: "e1"}})
	if err != nil {
		t.Fatal(err)
	}
	req := must(g.WriteNode(ctx, c.ID, added[0].ID, NodeWrite{Properties: map[string]any{"title": "A"}, Execution: "e1"}))
	if req, err = g.ReviewNodeOn(ctx, c.ID, "", "e1", req.ID, domain.ReviewAccepted, "bot", "first look"); err != nil {
		t.Fatal(err)
	}
	added, err = g.AddNodes(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: "TST-2", Type: "TestCase", Rationale: "cover it", Execution: "e2"}})
	if err != nil {
		t.Fatal(err)
	}
	tst := must(g.WriteNode(ctx, c.ID, added[0].ID, NodeWrite{Properties: map[string]any{"title": "test"}, Execution: "e2"}))
	fl, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Origin: map[string]any{"step": 0, "execution": "e1", "reason": "redo"}, StaleRuns: []string{"e1", "e2"}})
	if err != nil {
		t.Fatal(err)
	}
	return flowWorld{g: g, f: f, change: c, req: req, tst: tst, flow: fl.ID}
}

func viewOf(t *testing.T, g *Graph, id domain.ChangeID, flow string) map[string]domain.ChangeImpact {
	t.Helper()
	bb, err := g.BlackboardIn(context.Background(), id, flow)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]domain.ChangeImpact{}
	for _, cn := range bb.Change.Nodes {
		out[cn.Key] = cn
	}
	return out
}

func TestFlowChangeImpactsView(t *testing.T) { forEachRepo(t, testFlowChangeImpactsView) }

func testFlowChangeImpactsView(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newFlowWorld(t, repo)
	g, c := w.g, w.change
	pre := w.f.req.Ref()

	// the main flow keeps what it has; the flow sees neither stale change impact
	main := viewOf(t, g, c.ID, "")
	if len(main) != 2 || main["REQ-1"].Post == nil || main["REQ-1"].Review != domain.ReviewAccepted {
		t.Fatalf("main view: %+v", main)
	}
	if v := viewOf(t, g, c.ID, w.flow); len(v) != 0 {
		t.Fatalf("a flow relaunched from step 1 starts from nothing: %+v", v)
	}

	// the flow declares REQ-1 again, writes and reviews it on its own branch
	added, err := g.AddNodes(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "PSP v2, second look", Flow: w.flow, Execution: "e3"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.AddNodes(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "twice", Flow: w.flow, Execution: "e3"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a node appears once per flow, got %v", err)
	}
	if _, err := g.WriteNode(ctx, c.ID, added[0].ID, NodeWrite{Flow: w.flow, Execution: "e3", Properties: map[string]any{"title": "B"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ReviewNodeOn(ctx, c.ID, w.flow, "e3", added[0].ID, domain.ReviewAccepted, "bot", "second look is right"); err != nil {
		t.Fatal(err)
	}
	fv := viewOf(t, g, c.ID, w.flow)
	if len(fv) != 1 || fv["REQ-1"].Rationale != "PSP v2, second look" || fv["REQ-1"].Post == nil || fv["REQ-1"].Review != domain.ReviewAccepted {
		t.Fatalf("flow view: %+v", fv)
	}
	// the main flow does not see the candidate, and its own post is untouched
	if m := viewOf(t, g, c.ID, ""); len(m) != 2 || m["REQ-1"].Rationale != "PSP v2 changes the API" || m["REQ-1"].Post.Version != 2 {
		t.Fatalf("main view after the flow wrote: %+v", m)
	}
	if head, _ := g.Node(ctx, domain.NodeRef{ID: w.f.req.ID}); head.Properties["title"] != "Use PSP v1" {
		t.Fatalf("nothing reached main: %+v", head)
	}
	// the flow's version lives on its branch, derived from the released version (the change branch one is stale)
	post, err := g.Node(ctx, *fv["REQ-1"].Post)
	if err != nil || post.Branch != flowBranchName(w.flow) || post.Parents[0] != 1 || post.Properties["title"] != "B" || post.Execution != "e3" {
		t.Fatalf("flow version: %+v %v", post, err)
	}
	// a change with an open flow is not applied
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("Apply must refuse an open flow, got %v", err)
	}
}

func TestFlowChangeImpactsAdopt(t *testing.T) { forEachRepo(t, testFlowChangeImpactsAdopt) }

func testFlowChangeImpactsAdopt(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newFlowWorld(t, repo)
	g, c := w.g, w.change
	pre := w.f.req.Ref()
	added, err := g.AddNodes(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "second look", Flow: w.flow, Execution: "e3"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.WriteNode(ctx, c.ID, added[0].ID, NodeWrite{Flow: w.flow, Execution: "e3", Properties: map[string]any{"title": "B"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ReviewNodeOn(ctx, c.ID, w.flow, "e3", added[0].ID, domain.ReviewAccepted, "bot", "second look is right"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.AdoptFlow(ctx, c.ID, w.flow, "alice"); err != nil {
		t.Fatal(err)
	}

	all, err := g.ListChangeImpacts(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[domain.ChangeImpactID]domain.ChangeImpact{}
	for _, cn := range all {
		byID[cn.ID] = cn
	}
	if !byID[w.req.ID].Superseded || !byID[w.tst.ID].Superseded {
		t.Fatalf("the stale change impacts are superseded: %+v", all)
	}
	adopted := byID[added[0].ID]
	if adopted.Flow != "" || adopted.Superseded || adopted.Review != domain.ReviewAccepted || adopted.Post == nil {
		t.Fatalf("the flow's change impact becomes the change's: %+v", adopted)
	}
	head, err := g.NodeByKeyOn(ctx, domain.DefaultNamespace, changeBranchName(c.ID), "REQ-1")
	if err != nil || head.Ref() != *adopted.Post || head.Properties["title"] != "B" || head.Reason != domain.ReasonAdopt || head.Execution != "e3" {
		t.Fatalf("the change branch must equal the flow: %+v %v (post %s)", head, err, adopted.Post)
	}
	// TST-2 was created by steps that no longer exist: it is retired on the change branch
	if n, err := g.NodeByKeyOn(ctx, domain.DefaultNamespace, changeBranchName(c.ID), "TST-2"); err != nil || !n.Deleted {
		t.Fatalf("TST-2 must be retired: %+v %v", n, err)
	}
	// the flow does not show any more, the main flow sees the adopted change impact only
	if m := viewOf(t, g, c.ID, ""); len(m) != 1 || m["REQ-1"].ID != added[0].ID {
		t.Fatalf("main view: %+v", m)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	final, _ := g.Node(ctx, domain.NodeRef{ID: w.f.req.ID})
	if final.Properties["title"] != "B" || final.ChangeImpact != added[0].ID || final.Comment != "second look is right" {
		t.Fatalf("landed: %+v", final)
	}
	if n, err := g.NodeByKey(ctx, domain.DefaultNamespace, "TST-2"); err == nil && !n.Deleted {
		t.Fatalf("TST-2 must not be live on main: %+v", n)
	}
}

func TestFlowChangeImpactsDiscard(t *testing.T) { forEachRepo(t, testFlowChangeImpactsDiscard) }

func testFlowChangeImpactsDiscard(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newFlowWorld(t, repo)
	g, c := w.g, w.change
	pre := w.f.req.Ref()
	added, _ := g.AddNodes(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "second look", Flow: w.flow, Execution: "e3"}})
	if _, err := g.WriteNode(ctx, c.ID, added[0].ID, NodeWrite{Flow: w.flow, Execution: "e3", Properties: map[string]any{"title": "B"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.DiscardFlow(ctx, c.ID, w.flow, "alice"); err != nil {
		t.Fatal(err)
	}
	all, _ := g.ListChangeImpacts(ctx, c.ID)
	for _, cn := range all {
		switch cn.ID {
		case added[0].ID:
			if cn.Review != domain.ReviewRejected {
				t.Fatalf("the flow's change impact is rejected: %+v", cn)
			}
		case w.req.ID, w.tst.ID:
			if cn.Superseded {
				t.Fatalf("the stale change impacts count again: %+v", cn)
			}
		}
	}
	if b, err := g.Branch(ctx, "", flowBranchName(w.flow)); err != nil || b.Status != domain.BranchAbandoned {
		t.Fatalf("the flow branch is abandoned: %+v %v", b, err)
	}
	// the change goes on as before: REQ-1 at the version step 1 wrote
	if _, err := g.ReviewNodeOn(ctx, c.ID, "", "e4", w.tst.ID, domain.ReviewAccepted, "bot", "needed"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	final, _ := g.Node(ctx, domain.NodeRef{ID: w.f.req.ID})
	if final.Properties["title"] != "A" {
		t.Fatalf("the main flow's write must land: %+v", final)
	}
}

func TestFlowChangeImpactsConflict(t *testing.T) { forEachRepo(t, testFlowChangeImpactsConflict) }

func testFlowChangeImpactsConflict(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newFlowWorld(t, repo)
	g, c := w.g, w.change
	pre := w.f.req.Ref()
	added, _ := g.AddNodes(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "second look", Flow: w.flow, Execution: "e3"}})
	if _, err := g.WriteNode(ctx, c.ID, added[0].ID, NodeWrite{Flow: w.flow, Execution: "e3", Properties: map[string]any{"title": "B"}}); err != nil {
		t.Fatal(err)
	}
	// another run of the main flow (not stale) writes the same node meanwhile
	if _, err := g.WriteNode(ctx, c.ID, w.req.ID, NodeWrite{Execution: "e9", Properties: map[string]any{"owner": "carol"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.AdoptFlow(ctx, c.ID, w.flow, "alice"); !errors.Is(err, ErrConflict) {
		t.Fatalf("adopting a flow whose node moved on the change branch must conflict, got %v", err)
	}
}

func TestParallelAndNestedFlowsOnChangeImpacts(t *testing.T) {
	forEachRepo(t, testParallelAndNestedFlowsOnChangeImpacts)
}

// Each flow sees the change impacts through its own chain of events (ADR 0029 §5): parallel flows do not see each
// other, a flow inside a flow sees its parent's writes except the ones of the step it relaunches.
func testParallelAndNestedFlowsOnChangeImpacts(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newFlowWorld(t, repo) // REQ-1 written by e1 ("A"), TST-2 by e2; w.flow relaunches from e1
	g, c := w.g, w.change
	open := func(parent string, stale ...string) string {
		t.Helper()
		f, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Parent: parent, Origin: map[string]any{"step": 1, "reason": "again"}, StaleRuns: stale})
		if err != nil {
			t.Fatal(err)
		}
		return f.ID
	}
	title := func(flow string) any {
		t.Helper()
		v := viewOf(t, g, c.ID, flow)["REQ-1"]
		if v.Post == nil {
			return nil
		}
		n, err := g.Node(ctx, *v.Post)
		if err != nil {
			t.Fatal(err)
		}
		return n.Properties["title"]
	}
	// two parallel flows relaunch the second step: both keep REQ-1 as the main flow wrote it
	f1, f2 := open("", "e2"), open("", "e2")
	if title(f1) != "A" || title(f2) != "A" {
		t.Fatalf("parallel flows start from the main flow: %v %v", title(f1), title(f2))
	}
	// f1 rewrites REQ-1 (a change impact of the main flow): only f1 sees it
	if _, err := g.WriteNode(ctx, c.ID, w.req.ID, NodeWrite{Flow: f1, Execution: "e4", Properties: map[string]any{"title": "F1"}}); err != nil {
		t.Fatal(err)
	}
	if title(f1) != "F1" || title(f2) != "A" || title("") != "A" {
		t.Fatalf("after f1 wrote: f1 %v, f2 %v, main %v", title(f1), title(f2), title(""))
	}
	// a flow inside f1 sees f1's write; one that relaunches f1's writing step does not
	if inner := open(f1); title(inner) != "F1" {
		t.Fatalf("a flow inside f1 sees f1's writes: %v", title(inner))
	}
	redo := open(f1, "e4")
	if title(redo) != "A" {
		t.Fatalf("a flow relaunching f1's step does not see what it wrote: %v", title(redo))
	}
	// it writes its own version, derived from the one it sees
	cn, err := g.WriteNode(ctx, c.ID, w.req.ID, NodeWrite{Flow: redo, Execution: "e5", Properties: map[string]any{"title": "R"}})
	if err != nil {
		t.Fatal(err)
	}
	n, err := g.Node(ctx, *cn.Post)
	if err != nil || n.Branch != flowBranchName(redo) || n.Parents[0] != w.req.Post.Version {
		t.Fatalf("version of the nested flow: %+v %v", n, err)
	}
	if title(redo) != "R" || title(f1) != "F1" || title("") != "A" {
		t.Fatalf("after the nested flow wrote: redo %v, f1 %v, main %v", title(redo), title(f1), title(""))
	}
}
