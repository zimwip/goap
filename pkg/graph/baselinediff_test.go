package graph

import (
	"context"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// Two baselines are compared node by node: added, removed, changed.
func TestDiffBaselines(t *testing.T) { forEachRepo(t, testDiffBaselines) }

func testDiffBaselines(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "PSP v2", BaselineID: f.base.ID, OwnBranch: true}))
	req := f.req.Ref()
	added := must[[]domain.ChangeImpact](t)(g.AddNodes(ctx, c.ID, []domain.ChangeImpact{
		{Intent: domain.IntentModified, Pre: &req, Rationale: "new PSP"},
		{Intent: domain.IntentModified, Pre: new(f.test.Ref()), Rationale: "obsolete"},
		{Intent: domain.IntentCreated, Key: "REQ-9", Type: f.req.Type, Rationale: "new"},
	}))
	for i, w := range []NodeWrite{{Properties: map[string]any{"title": "Use PSP v2"}}, {Retire: true}, {Properties: map[string]any{"title": "New"}}} {
		must[domain.ChangeImpact](t)(g.WriteNode(ctx, c.ID, added[i].ID, w))
		must[domain.ChangeImpact](t)(g.ReviewNode(ctx, c.ID, added[i].ID, domain.ReviewAccepted, "u", "ok"))
	}
	res := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	diff := must[[]BaselineDiff](t)(g.DiffBaselines(ctx, f.base.ID, res.ID))
	got := map[string]string{}
	for _, d := range diff {
		got[d.Key] = d.Kind
	}
	want := map[string]string{f.req.Key: DiffChanged, f.test.Key: DiffRemoved, "REQ-9": DiffAdded}
	if len(got) != len(want) {
		t.Fatalf("diff: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("diff of %s: %q, want %q (%v)", k, got[k], v, got)
		}
	}
	if none := must[[]BaselineDiff](t)(g.DiffBaselines(ctx, res.ID, res.ID)); len(none) != 0 {
		t.Fatalf("a baseline against itself: %v", none)
	}
}
