package graph

import (
	"context"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// Two commits are compared node by node: added, removed, changed.
func TestDiffCommits(t *testing.T) { forEachRepo(t, testDiffCommits) }

func testDiffCommits(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "PSP v2", ForkCommit: f.base.ID, OwnBranch: true}))
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
	res := must[domain.Commit](t)(g.apply(ctx, c.ID, ""))
	diff := must[[]CommitDiff](t)(g.DiffCommits(ctx, f.base.ID, res.ID))
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
	for _, d := range must[[]CommitDiff](t)(g.DiffCommits(ctx, res.ID, f.base.ID)) {
		if d.Key == "REQ-9" && d.Kind != DiffRemoved {
			t.Fatalf("diff back of REQ-9: %q", d.Kind)
		}
	}
	if none := must[[]CommitDiff](t)(g.DiffCommits(ctx, res.ID, res.ID)); len(none) != 0 {
		t.Fatalf("a commit against itself: %v", none)
	}
	// from the empty state (ADR 0056: the commit id ""), every node of the commit is added
	first := must[[]CommitDiff](t)(g.DiffCommits(ctx, "", f.base.ID))
	keys := map[string]bool{}
	for _, d := range first {
		if d.Kind != DiffAdded || d.From != nil || d.To == nil {
			t.Fatalf("diff from the empty state: %+v", d)
		}
		keys[d.Key] = true
	}
	for _, n := range []domain.Node{f.req, f.test} {
		if !keys[n.Key] {
			t.Fatalf("diff from the empty state misses %s: %v", n.Key, keys)
		}
	}
}
