package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// DiffFlows (ADR 0083): the impacts two flows see differ as added, removed or modified, the identical ones are counted.
func TestDiffFlows(t *testing.T) { forEachRepo(t, testDiffFlows) }

func testDiffFlows(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	req2, err := importNode(ctx, g, newNode{Key: "REQ-2", Type: "Requirement", Properties: map[string]any{"title": "Refund"}})
	if err != nil {
		t.Fatal(err)
	}
	base := must[domain.Baseline](t)(g.BranchHead(ctx, "", domain.MainBranch))
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "PSP", BaselineID: base.ID, OwnBranch: true}))
	pre, pre2 := f.req.Ref(), req2.Ref()

	// the main flow: REQ-1 modified and accepted, REQ-2 modified (identical on every flow), TST-9 created
	m1 := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "main"}}))[0]
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, m1.ID, edit{Flow: domain.MainFlow, Properties: map[string]any{"title": "main title"}}))
	must[domain.ChangeImpact](t)(g.acceptOn(ctx, c.ID, domain.MainFlow, "", m1.ID, "u", "ok"))
	m2 := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre2, Rationale: "r2"}}))[0]
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, m2.ID, edit{Flow: domain.MainFlow, Properties: map[string]any{"title": "Refund v2"}}))
	m3 := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "TST-9", Type: "TestCase", Rationale: "new", Flow: domain.MainFlow,
		Links: []LinkWrite{{Type: "verifies", To: pre}}}))

	a := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "stripe", By: "u"}))
	b := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "adyen", By: "u"}))

	// nothing differs yet: every impact is identical, whoever is on the left
	d := must[domain.FlowDiff](t)(g.DiffFlows(ctx, c.ID, "", a.ID, ""))
	if len(d.Impacts) != 0 || d.Identical != 3 || d.Left != domain.MainFlow || d.Right != a.ID || d.Level != ViewWritten {
		t.Fatalf("nothing differs: %+v", d)
	}
	if d = must[domain.FlowDiff](t)(g.DiffFlows(ctx, c.ID, a.ID, a.ID, "")); len(d.Impacts) != 0 || d.Identical != 3 {
		t.Fatalf("one flow against itself: %+v", d)
	}

	// option A: REQ-1 retitled, TST-9 linked to REQ-2 too, a creation of its own
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, m1.ID, edit{Flow: a.ID, Properties: map[string]any{"title": "A title", "note": "n"}}))
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, m3.ID, edit{Flow: a.ID, AddLinks: []LinkWrite{{Type: "verifies", To: pre2}}}))
	ma := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "TST-A", Type: "TestCase", Rationale: "only A", Flow: a.ID}))
	// option B: REQ-1 retitled in another way, a creation with the same key as A's
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, m1.ID, edit{Flow: b.ID, Properties: map[string]any{"title": "B title"}}))
	must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "TST-A", Type: "TestCase", Rationale: "also B", Flow: b.ID, Properties: map[string]any{"title": "b"}}))

	byKey := func(d domain.FlowDiff) map[string]domain.ImpactDiff {
		out := map[string]domain.ImpactDiff{}
		for _, i := range d.Impacts {
			out[i.Key] = i
		}
		return out
	}
	// main against A: REQ-1 and TST-9 modified, TST-A added, REQ-2 identical
	d = must[domain.FlowDiff](t)(g.DiffFlows(ctx, c.ID, domain.MainFlow, a.ID, ViewWritten))
	got := byKey(d)
	if len(d.Impacts) != 3 || d.Identical != 1 || d.DiffCounts()[domain.DiffAdded] != 1 || d.DiffCounts()[domain.DiffModified] != 2 {
		t.Fatalf("main against A: %+v", d)
	}
	req := got["REQ-1"]
	if req.Category != domain.DiffModified || req.Node != pre.ID || len(req.Changes) != 2 ||
		req.Changes[0] != (domain.FieldChange{Kind: domain.FieldProperty, Name: "note", Op: domain.OpAdded, New: "n"}) ||
		req.Changes[1] != (domain.FieldChange{Kind: domain.FieldProperty, Name: "title", Op: domain.OpChanged, Old: "main title", New: "A title"}) {
		t.Fatalf("REQ-1 modified: %+v", req)
	}
	tst := got["TST-9"]
	if tst.Category != domain.DiffModified || len(tst.Changes) != 1 || tst.Changes[0].Kind != domain.FieldLink || tst.Changes[0].Op != domain.OpAdded ||
		tst.Changes[0].Name != "verifies" || tst.Changes[0].Target != "REQ-2" {
		t.Fatalf("TST-9 link added: %+v", tst)
	}
	if add := got["TST-A"]; add.Category != domain.DiffAdded || add.Left != nil || add.Right == nil || add.Right.Impact != ma.ID || add.Right.Intent != domain.IntentCreated || add.Right.Flow != a.ID {
		t.Fatalf("TST-A added: %+v", add)
	}

	// the other way round: removed, and the old value is the left one
	d = must[domain.FlowDiff](t)(g.DiffFlows(ctx, c.ID, a.ID, "main", ""))
	got = byKey(d)
	if got["TST-A"].Category != domain.DiffRemoved || got["TST-A"].Right != nil || got["TST-9"].Changes[0].Op != domain.OpRemoved ||
		got["REQ-1"].Changes[1].Old != "A title" || got["REQ-1"].Changes[1].New != "main title" {
		t.Fatalf("A against main: %+v", d.Impacts)
	}

	// option against option: REQ-1 differs by title, TST-A (created on both, two nodes) by its properties, TST-9 by its link
	d = must[domain.FlowDiff](t)(g.DiffFlows(ctx, c.ID, a.ID, b.ID, ""))
	got = byKey(d)
	if len(d.Impacts) != 3 || d.Identical != 1 || got["TST-A"].Category != domain.DiffModified || got["TST-A"].Changes[0].Name != "title" ||
		got["TST-9"].Changes[0].Op != domain.OpRemoved || got["REQ-1"].Changes[len(got["REQ-1"].Changes)-1].New != "B title" {
		t.Fatalf("A against B: %+v", d.Impacts)
	}

	// accepted level: only impacts accepted on the flow; A's REQ-1 went back to proposed with its edit
	d = must[domain.FlowDiff](t)(g.DiffFlows(ctx, c.ID, domain.MainFlow, a.ID, ViewAccepted))
	if len(d.Impacts) != 1 || d.Impacts[0].Key != "REQ-1" || d.Impacts[0].Category != domain.DiffRemoved || d.Identical != 0 {
		t.Fatalf("accepted, A has not accepted REQ-1: %+v", d)
	}
	must[domain.ChangeImpact](t)(g.acceptOn(ctx, c.ID, a.ID, "", m1.ID, "u", "ok"))
	d = must[domain.FlowDiff](t)(g.DiffFlows(ctx, c.ID, domain.MainFlow, a.ID, ViewAccepted))
	if len(d.Impacts) != 1 || d.Impacts[0].Key != "REQ-1" || d.Impacts[0].Category != domain.DiffModified || d.Impacts[0].Right.Review != domain.ReviewAccepted {
		t.Fatalf("accepted on both: %+v", d)
	}

	// a decided option is still comparable
	must[domain.Flow](t)(g.RejectOption(ctx, c.ID, b.ID, "u"))
	if d = must[domain.FlowDiff](t)(g.DiffFlows(ctx, c.ID, domain.MainFlow, b.ID, "")); len(d.Impacts) == 0 {
		t.Fatalf("a rejected option: %+v", d)
	}

	// refused: an unknown flow or level
	if _, err := g.DiffFlows(ctx, c.ID, "", "no-such-flow", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown flow: %v", err)
	}
	if _, err := g.DiffFlows(ctx, c.ID, "", a.ID, "landed"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown level: %v", err)
	}
	_ = m2
}
