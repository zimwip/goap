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
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "PSP v2", BaselineID: f.base.ID, OwnBranch: true}))
	req := f.req.Ref()
	added := must[[]domain.ChangeImpact](t)(g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{
		{Intent: domain.IntentModified, Pre: &req, Rationale: "new PSP"},
		{Intent: domain.IntentModified, Pre: new(f.test.Ref()), Rationale: "obsolete"},
		{Intent: domain.IntentCreated, Key: "REQ-9", Type: f.req.Type, Rationale: "new"},
	}))
	for i, w := range []edit{{Properties: map[string]any{"title": "Use PSP v2"}}, {Properties: map[string]any{"title": "Obsolete"}}, {Properties: map[string]any{"title": "New"}}} {
		must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, added[i].ID, w))
		if err := g.acceptImpact(ctx, c.ID, added[i].ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	res := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	diff := must[[]BaselineDiff](t)(g.DiffBaselines(ctx, f.base.ID, res.ID))
	got := map[string]string{}
	for _, d := range diff {
		got[d.Key] = d.Kind
	}
	want := map[string]string{f.req.Key: DiffChanged, f.test.Key: DiffChanged, "REQ-9": DiffAdded}
	if len(got) != len(want) {
		t.Fatalf("diff: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("diff of %s: %q, want %q (%v)", k, got[k], v, got)
		}
	}
	// backwards, the created node is removed (a node never leaves a namespace by a change: ADR 0076)
	for _, d := range must[[]BaselineDiff](t)(g.DiffBaselines(ctx, res.ID, f.base.ID)) {
		if d.Key == "REQ-9" && d.Kind != DiffRemoved {
			t.Fatalf("diff back of REQ-9: %q", d.Kind)
		}
	}
	if none := must[[]BaselineDiff](t)(g.DiffBaselines(ctx, res.ID, res.ID)); len(none) != 0 {
		t.Fatalf("a baseline against itself: %v", none)
	}
}
