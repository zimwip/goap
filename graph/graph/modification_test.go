package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// ADR 0102: only a modification is accepted and committed. A candidate (an impact never checked out) and a draft left as
// it was checked out are refused at review, and a commit with nothing that modifies a node is refused.
func TestOnlyModificationsAreAcceptedAndCommitted(t *testing.T) {
	forEachRepo(t, testOnlyModificationsAreAcceptedAndCommitted)
}

func testOnlyModificationsAreAcceptedAndCommitted(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "touch", ForkCommit: f.base.ID, OwnBranch: true}))
	pre := f.req.Ref()
	cand := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "maybe"}}))[0]
	if _, err := g.ImpactNodeReview(ctx, c.ID, cand.ID, domain.ReviewAccepted, "bob", "ok"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a candidate is not accepted: %v", err)
	}
	if _, err := g.CommitChange(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nothing to commit: %v", err)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: cand.ID}))
	if _, err := g.ImpactNodeReview(ctx, c.ID, cand.ID, domain.ReviewAccepted, "bob", "ok"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unchanged draft is not accepted: %v", err)
	}
	if _, err := g.CommitChange(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unchanged draft is nothing to commit: %v", err)
	}
	// a candidate left as it is does not hold the commit of what is modified
	must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "D-9", Type: "Design", Rationale: "new"}))
	for _, cn := range must[[]domain.ChangeImpact](t)(g.ListChangeImpacts(ctx, c.ID)) {
		if cn.Key == "D-9" {
			must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "bob", "ok"))
		}
	}
	b := g.withState(t, ctx, must[domain.Commit](t)(g.CommitChange(ctx, c.ID, "")))
	if b.Nodes[f.req.ID] != f.req.Version {
		t.Fatalf("REQ-1 was not modified: no version of it is written: %+v", b.Nodes)
	}
	if ch := must[domain.Change](t)(g.Change(ctx, c.ID)); ch.Nodes[0].Review != domain.ReviewProposed || ch.Status != domain.ChangeActive {
		t.Fatalf("the candidate stays in the open change: %+v", ch)
	}
}

// ADR 0102: a committed change is iterated on: its nodes are checked out again from the versions it committed, edited and
// committed again; it is merged once, which closes it.
func TestIterateOnTheBranchOfAChange(t *testing.T) { forEachRepo(t, testIterateOnTheBranchOfAChange) }

func testIterateOnTheBranchOfAChange(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "iterate", ForkCommit: f.base.ID, OwnBranch: true}))
	if _, err := g.IntegrateChange(ctx, c.ID, nil); !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("nothing committed: nothing to merge: %v", err)
	}
	req := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "v2"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, req.ID, NodeUpdate{Properties: map[string]any{"title": "one"}}))
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, req.ID, domain.ReviewAccepted, "bob", "ok"))
	created := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "D-1", Type: "Design", Rationale: "new"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, created.ID, domain.ReviewAccepted, "bob", "ok"))
	first := g.withState(t, ctx, must[domain.Commit](t)(g.CommitChange(ctx, c.ID, "")))
	v1 := first.Nodes[f.req.ID]

	// edited again after the commit: checked out from the version the commit wrote
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: req.ID}); err != nil {
		t.Fatalf("a committed change is edited again: %v", err)
	}
	d, _ := g.draftOf(ctx, c.ID, "", req.ID)
	if d == nil || d.Base == nil || d.Base.Version != v1 || d.Properties["title"] != "one" {
		t.Fatalf("the draft starts from the committed version: %+v", d)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, req.ID, NodeUpdate{Properties: map[string]any{"title": "two"}}))
	if _, err := g.CloseChange(ctx, c.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("an uncommitted modification holds the close: %v", err)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, req.ID, domain.ReviewAccepted, "bob", "ok"))
	// a node it created is checked out again too, then left as it was
	must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: created.ID}))
	second := g.withState(t, ctx, must[domain.Commit](t)(g.CommitChange(ctx, c.ID, "")))
	v2 := second.Nodes[f.req.ID]
	if second.Seq <= first.Seq || second.ParentID != first.ParentID || v2 == v1 {
		t.Fatalf("the second commit follows the first: %+v", second)
	}
	n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: v2}))
	if n.Properties["title"] != "two" || len(n.Parents) != 1 || n.Parents[0] != v1 {
		t.Fatalf("the version of the second commit derives from the first: %+v", n)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeCancel(ctx, c.ID, created.ID, "", ""))
	ch, err := g.CloseChange(ctx, c.ID)
	if err != nil || ch.Status != domain.ChangeApplied {
		t.Fatalf("merge: %+v %v", ch, err)
	}
	head := must[domain.Commit](t)(g.Commit(ctx, ch.LandedCommit))
	if head.Kind != domain.CommitKindFastForward || head.Nodes[f.req.ID] != v2 || head.Nodes[nodeOf(created)] == 0 {
		t.Fatalf("main fast-forwards to the last commit: %+v", head)
	}
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: req.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a merged change is closed: %v", err)
	}
}

// ADR 0102: an impact is withdrawn at any time; one the change committed already is reverted on its branch by the next
// commit (the node goes back to the version the branch was forked from), and the merge waits for that commit.
func TestWithdrawACommittedImpact(t *testing.T) { forEachRepo(t, testWithdrawACommittedImpact) }

func testWithdrawACommittedImpact(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "revert", ForkCommit: f.base.ID, OwnBranch: true}))
	req := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "v2"}))
	g.touch(t, c.ID, req.ID)
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, req.ID, domain.ReviewAccepted, "bob", "ok"))
	made := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "D-2", Type: "Design", Rationale: "new"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, made.ID, domain.ReviewAccepted, "bob", "ok"))
	if st := must[CommitState](t)(g.CommitState(ctx, c.ID)); !st.CanCommit || st.CanMerge || st.CanClose || len(st.ToCommit) != 2 || len(st.Unsettled) != 2 {
		t.Fatalf("two modifications to commit, nothing to merge: %+v", st)
	}
	first := g.withState(t, ctx, must[domain.Commit](t)(g.CommitChange(ctx, c.ID, "")))
	if first.Nodes[f.req.ID] == f.req.Version {
		t.Fatalf("REQ-1 committed: %+v", first.Nodes)
	}

	if err := g.WithdrawImpact(ctx, c.ID, made.ID, "", ""); err != nil {
		t.Fatalf("a committed impact is withdrawn: %v", err)
	}
	if _, err := g.CloseChange(ctx, c.ID); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "withdrawn") {
		t.Fatalf("the withdrawal waits for a commit: %v", err)
	}
	if st := must[CommitState](t)(g.CommitState(ctx, c.ID)); !st.CanCommit || st.CanClose || st.Reverts != 1 || !st.Commits || len(st.ToCommit) != 0 {
		t.Fatalf("a revert to commit, nothing else: %+v", st)
	}
	second := g.withState(t, ctx, must[domain.Commit](t)(g.CommitChange(ctx, c.ID, "")))
	if st := must[CommitState](t)(g.CommitState(ctx, c.ID)); st.CanCommit || !st.CanMerge || !st.CanClose || len(st.Unsettled) != 0 {
		t.Fatalf("committed, ready to merge: %+v", st)
	}
	if _, ok := second.Nodes[nodeOf(made)]; ok || second.Nodes[f.req.ID] != first.Nodes[f.req.ID] || second.Kind != domain.CommitKindRevert {
		t.Fatalf("D-2 reverted, REQ-1 kept: %+v", second)
	}
	ch := must[domain.Change](t)(g.CloseChange(ctx, c.ID))
	head := must[domain.Commit](t)(g.Commit(ctx, ch.LandedCommit))
	if _, ok := head.Nodes[nodeOf(made)]; ok || head.Nodes[f.req.ID] != first.Nodes[f.req.ID] {
		t.Fatalf("main holds REQ-1's commit and no D-2: %+v", head.Nodes)
	}
}
