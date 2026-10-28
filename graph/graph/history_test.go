package graph

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// The history of a branch is read a page at a time (ADR 0140): the pages cover its commits once each, newest page
// first, each with the commits the changes of the page left on their own branches; a branch list carries the heads and
// the counts of the branches. Changes started from the same commit and changing other nodes commit with one commit on
// main each and their commit on their own branch: no rebase commit.
func TestCommitHistory(t *testing.T) { forEachRepo(t, testCommitHistory) }

func testCommitHistory(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	var changes []domain.Change
	base := f.base
	for i := range 5 {
		ref := domain.NodeRef{ID: f.req.ID, Version: must[domain.Commit](t)(g.Commit(ctx, base.ID)).Nodes[f.req.ID]}
		if i%2 == 1 {
			ref = domain.NodeRef{ID: f.need.ID, Version: must[domain.Commit](t)(g.Commit(ctx, base.ID)).Nodes[f.need.ID]}
		}
		c, _ := propChange(t, g, base.ID, fmt.Sprintf("c%d", i), ref, map[string]any{"title": fmt.Sprintf("T%d", i)})
		changes = append(changes, c)
		if i%2 == 1 {
			// two changes started from the same commit, the second over other nodes than the first
			must[domain.Commit](t)(g.apply(ctx, changes[i-1].ID, ""))
			base = must[domain.Commit](t)(g.apply(ctx, c.ID, ""))
		}
	}
	main := commitsOf(t, g, domain.MainBranch)
	var got []domain.Commit
	var related []domain.Commit
	before := int64(0)
	for range 10 {
		h := must[CommitHistory](t)(g.CommitHistory(ctx, "", "", before, 2))
		if len(h.Commits) > 2 {
			t.Fatalf("a page of %d", len(h.Commits))
		}
		got = append(slices.Clone(h.Commits), got...)
		related = append(related, h.Related...)
		if before = h.Next; before == 0 {
			break
		}
	}
	if !slices.EqualFunc(got, main, func(a, b domain.Commit) bool { return a.ID == b.ID }) {
		t.Fatalf("pages = %d commits, main has %d", len(got), len(main))
	}
	for _, c := range changes[:4] {
		// a change's branch holds no commit: the change is on the page by the commit it committed in
		for _, b := range must[[]domain.Commit](t)(g.Commits(ctx, "")) {
			if b.ChangeID == c.ID && (b.Kind == domain.CommitKindCommit || b.Kind == domain.CommitKindRebase) {
				t.Fatalf("%s left a commit on its own branch: %+v", c.Title, b)
			}
		}
	}
	brs, heads, counts := must3(t)(g.BranchHeads(ctx, ""))
	if counts[domain.MainBranch] != len(main) {
		t.Fatalf("counts = %v, main has %d", counts, len(main))
	}
	for _, br := range brs {
		if br.Head != "" && !slices.ContainsFunc(heads, func(b domain.Commit) bool { return b.ID == br.Head }) {
			t.Fatalf("no head header for %s", br.Name)
		}
	}
}

func must3(t *testing.T) func([]domain.Branch, []domain.Commit, map[string]int, error) ([]domain.Branch, []domain.Commit, map[string]int) {
	return func(a []domain.Branch, b []domain.Commit, c map[string]int, err error) ([]domain.Branch, []domain.Commit, map[string]int) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return a, b, c
	}
}
