package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A merge leaves the change open (ADR 0166): it works on, commits on what it merged, merges again, and its close lands
// what is left and applies it.
func TestMergeKeepsTheChangeOpenUntilClosed(t *testing.T) {
	forEachRepo(t, testMergeKeepsTheChangeOpenUntilClosed)
}

func testMergeKeepsTheChangeOpenUntilClosed(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, id := propChange(t, g, f.base.ID, "open", f.req.Ref(), map[string]any{"a": 1})
	must[domain.Commit](t)(g.CommitChange(ctx, c.ID, ""))
	first := must[domain.Change](t)(g.IntegrateChange(ctx, c.ID, nil))
	if first.Status != domain.ChangeActive || first.ForkCommit != c.ForkCommit || first.LandedCommit == "" {
		t.Fatalf("merged and open, its fork kept (ADR 0177): %+v", first)
	}
	if st := must[CommitState](t)(g.CommitState(ctx, c.ID)); st.CanMerge || st.Unmerged != 0 || !st.CanClose {
		t.Fatalf("nothing left to merge, the change may close: %+v", st)
	}

	// another change lands on the same node meanwhile
	cur := must[domain.Node](t)(g.NodeByKey(ctx, domain.DefaultNamespace, f.req.Key))
	d, _ := propChange(t, g, first.LandedCommit, "other", cur.Ref(), map[string]any{"b": 2})
	other := must[domain.Commit](t)(g.apply(ctx, d.ID, ""))

	// the open change edits its node again: the commit rebases the draft onto the head, the merge fast-forwards
	must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: id}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, id, NodeUpdate{Properties: map[string]any{"c": 3}}))
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, id, domain.ReviewAccepted, "bob", "ok"))
	must[domain.Commit](t)(g.CommitChange(ctx, c.ID, ""))
	if st := must[CommitState](t)(g.CommitState(ctx, c.ID)); !st.CanMerge || st.Unmerged != 1 {
		t.Fatalf("the second commit is to merge: %+v", st)
	}
	second := must[domain.Change](t)(g.IntegrateChange(ctx, c.ID, nil))
	res := must[domain.Commit](t)(g.Commit(ctx, second.LandedCommit))
	if second.Status != domain.ChangeActive || res.Kind != domain.CommitKindFastForward || res.ParentID != other.ID {
		t.Fatalf("the second merge fast-forwards main from the other change: %+v %+v", second, res)
	}
	n := must[domain.Node](t)(g.NodeByKey(ctx, domain.DefaultNamespace, f.req.Key))
	if n.Properties["a"] == nil || n.Properties["b"] == nil || n.Properties["c"] == nil {
		t.Fatalf("main holds both changes' edits: %+v", n.Properties)
	}

	closed := must[domain.Change](t)(g.CloseChange(ctx, c.ID))
	if closed.Status != domain.ChangeApplied || closed.LandedCommit != second.LandedCommit {
		t.Fatalf("closed at its last merge: %+v", closed)
	}
	if head := must[domain.Commit](t)(g.BranchHead(ctx, "", domain.MainBranch)); head.ID != res.ID {
		t.Fatalf("a close with nothing left to merge writes no commit: %s, want %s", head.ID, res.ID)
	}
	if _, err := g.CloseChange(ctx, c.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("a closed change closes once: %v", err)
	}
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: id}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a closed change takes no work: %v", err)
	}
}
