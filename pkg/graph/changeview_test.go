package graph

import (
	"context"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// A change is seen at three levels of its change impacts (ADR 0032): written, accepted, landed.
func TestChangeViewLevels(t *testing.T) { forEachRepo(t, testChangeViewLevels) }

func testChangeViewLevels(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "PSP v2", BaselineID: f.base.ID, OwnBranch: true}))
	req, need := f.req.Ref(), f.need.Ref()
	added := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{
		{Intent: domain.IntentModified, Pre: &req, Rationale: "new PSP"},
		{Intent: domain.IntentModified, Pre: &need, Rationale: "reword"},
		{Intent: domain.IntentModified, Pre: new(f.test.Ref()), Rationale: "obsolete"},
	}))
	reqW := must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, added[0].ID, edit{Properties: map[string]any{"title": "Use PSP v2"}}))
	needW := must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, added[1].ID, edit{Properties: map[string]any{"title": "Pay"}}))
	testW := must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, added[2].ID, edit{Properties: map[string]any{"title": "Obsolete"}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, reqW.ID, "u", "ok"))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, added[2].ID, "u", "ok"))

	view := func(level string) domain.Baseline {
		return must[domain.Baseline](t)(g.ChangeView(ctx, c.ID, "", level))
	}
	// written: everything proposed or accepted
	w := view(ViewWritten)
	if !w.Contains(*reqW.Post) || !w.Contains(*needW.Post) {
		t.Fatalf("written view: %v", w.Nodes)
	}
	if !w.Contains(*testW.Post) {
		t.Fatalf("the written view holds every version written: %v", w.Nodes)
	}
	// accepted: NEED-1 awaits its review, it stays as released
	a := view(ViewAccepted)
	if !a.Contains(*reqW.Post) || !a.Contains(need) {
		t.Fatalf("accepted view: %v", a.Nodes)
	}
	// landed: nothing yet
	if l := view(ViewLanded); l.ID != f.base.ID {
		t.Fatalf("landed view before apply: %s", l.ID)
	}
	// a rejected version leaves the written view
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, needW.ID, domain.ReviewRejected, "u", "no"))
	if w := view(ViewWritten); !w.Contains(need) {
		t.Fatalf("written view after the rejection: %v", w.Nodes)
	}
	res := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	l := view(ViewLanded)
	if l.ID != res.ID || l.Nodes[f.req.ID] != f.req.Version+1 || !l.Contains(need) {
		t.Fatalf("landed view: %+v", l)
	}
	if _, err := g.ChangeView(ctx, c.ID, "", "someday"); err == nil {
		t.Fatal("unknown level")
	}
}

// A flow sees its own versions, the main flow its own (ADR 0032: an option is a branch seen before it lands).
func TestChangeViewOfAFlow(t *testing.T) { forEachRepo(t, testChangeViewOfAFlow) }

func testChangeViewOfAFlow(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newFlowWorld(t, repo)
	g, c := w.g, w.change
	pre := w.f.req.Ref()
	added := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "second look", Flow: w.flow, Execution: "e3"}}))
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, added[0].ID, edit{Flow: w.flow, Execution: "e3", Properties: map[string]any{"title": "B"}}))
	flowView := must[domain.Baseline](t)(g.ChangeView(ctx, c.ID, w.flow, ViewWritten))
	mainView := must[domain.Baseline](t)(g.ChangeView(ctx, c.ID, "", ViewWritten))
	fv := must[domain.Node](t)(g.ChangeNode(ctx, c.ID, w.flow, domain.NodeRef{ID: pre.ID, Version: flowView.Nodes[pre.ID]}))
	if fv.Properties["title"] != "B" || !mainView.Contains(*w.req.Post) {
		t.Fatalf("flow view %v, main view %v", flowView.Nodes, mainView.Nodes)
	}
	// the flow relaunched the step that created TST-2: it does not see it; the main flow does
	if _, seen := flowView.Nodes[w.tst.Post.ID]; seen || !mainView.Contains(*w.tst.Post) {
		t.Fatalf("TST-2: flow %v, main %v", flowView.Nodes, mainView.Nodes)
	}
	// accepted: nothing of the flow is reviewed yet
	if a := must[domain.Baseline](t)(g.ChangeView(ctx, c.ID, w.flow, ViewAccepted)); !a.Contains(pre) {
		t.Fatalf("flow accepted view: %v", a.Nodes)
	}
}
