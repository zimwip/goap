package graph

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// The history of a branch is read a page at a time (ADR 0140): the pages cover its baselines once each, newest page
// first, each with the baselines the changes of the page left on their own branches; a branch list carries the heads and
// the counts of the branches. Changes started from the same baseline and changing other nodes land with one baseline on
// main each and their commit on their own branch: no rebase baseline.
func TestBaselineHistory(t *testing.T) { forEachRepo(t, testBaselineHistory) }

func testBaselineHistory(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	var changes []domain.Change
	base := f.base
	for i := range 5 {
		ref := domain.NodeRef{ID: f.req.ID, Version: must[domain.Baseline](t)(g.Baseline(ctx, base.ID)).Nodes[f.req.ID]}
		if i%2 == 1 {
			ref = domain.NodeRef{ID: f.need.ID, Version: must[domain.Baseline](t)(g.Baseline(ctx, base.ID)).Nodes[f.need.ID]}
		}
		c, _ := propChange(t, g, base.ID, fmt.Sprintf("c%d", i), ref, map[string]any{"title": fmt.Sprintf("T%d", i)})
		changes = append(changes, c)
		if i%2 == 1 {
			// two changes started from the same baseline, the second over other nodes than the first
			must[domain.Baseline](t)(g.Apply(ctx, changes[i-1].ID, ""))
			base = must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
		}
	}
	main := baselinesOf(t, g, domain.MainBranch)
	var got []domain.Baseline
	var related []domain.Baseline
	before := int64(0)
	for range 10 {
		h := must[BaselineHistory](t)(g.BaselineHistory(ctx, "", "", before, 2))
		if len(h.Baselines) > 2 {
			t.Fatalf("a page of %d", len(h.Baselines))
		}
		got = append(slices.Clone(h.Baselines), got...)
		related = append(related, h.Related...)
		if before = h.Next; before == 0 {
			break
		}
	}
	if !slices.EqualFunc(got, main, func(a, b domain.Baseline) bool { return a.ID == b.ID }) {
		t.Fatalf("pages = %d baselines, main has %d", len(got), len(main))
	}
	for _, c := range changes[:4] {
		commit := baselineOfChange(t, g, c.ID, domain.BaselineCommit)
		if !slices.ContainsFunc(related, func(b domain.Baseline) bool { return b.ID == commit.ID }) {
			t.Fatalf("the commit of %s is not related to its page", c.Title)
		}
		for _, b := range must[[]domain.Baseline](t)(g.Baselines(ctx, "")) {
			if b.ChangeID == c.ID && b.Kind == domain.BaselineRebase {
				t.Fatalf("%s rebased with nothing to merge: %+v", c.Title, b)
			}
		}
	}
	brs, heads, counts := must3(t)(g.BranchHeads(ctx, ""))
	if counts[domain.MainBranch] != len(main) {
		t.Fatalf("counts = %v, main has %d", counts, len(main))
	}
	for _, br := range brs {
		if br.Head != "" && !slices.ContainsFunc(heads, func(b domain.Baseline) bool { return b.ID == br.Head }) {
			t.Fatalf("no head header for %s", br.Name)
		}
	}
}

func must3(t *testing.T) func([]domain.Branch, []domain.Baseline, map[string]int, error) ([]domain.Branch, []domain.Baseline, map[string]int) {
	return func(a []domain.Branch, b []domain.Baseline, c map[string]int, err error) ([]domain.Branch, []domain.Baseline, map[string]int) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return a, b, c
	}
}
