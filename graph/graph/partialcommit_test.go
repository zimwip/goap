package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A partial commit writes the nodes named only: the others stay drafts, committed by a later commit; a node named with
// nothing to commit is refused, so is a version linking to the draft of a node left out. The commit state lists the
// candidates.
func TestPartialCommit(t *testing.T) { forEachRepo(t, testPartialCommit) }

func testPartialCommit(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "partial", BaselineID: f.base.ID, OwnBranch: true}))
	req := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "v2"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, req.ID, NodeUpdate{Properties: map[string]any{"title": "one"}}))
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, req.ID, domain.ReviewAccepted, "bob", "ok"))
	created := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "D-1", Type: "Design", Rationale: "new"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, created.ID, domain.ReviewAccepted, "bob", "ok"))

	st := must[CommitState](t)(g.CommitState(ctx, c.ID))
	kinds := map[domain.NodeID]string{}
	for _, cd := range st.Candidates {
		kinds[cd.Node] = cd.Kind
	}
	if len(st.Candidates) != 2 || kinds[f.req.ID] != CandidateModified || kinds[nodeOf(created)] != CandidateCreated {
		t.Fatalf("the candidates of the commit: %+v", st.Candidates)
	}
	if _, err := g.CommitChangeNodes(ctx, c.ID, "", []domain.NodeID{f.req.ID, "no-such-node"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a node with nothing to commit is refused: %v", err)
	}
	b := g.withState(t, ctx, must[domain.Baseline](t)(g.CommitChangeNodes(ctx, c.ID, "", []domain.NodeID{nodeOf(created)})))
	if b.Nodes[f.req.ID] != f.req.Version || b.Nodes[nodeOf(created)] == 0 {
		t.Fatalf("only the node named is committed: %+v", b.Nodes)
	}
	st = must[CommitState](t)(g.CommitState(ctx, c.ID))
	if len(st.Candidates) != 1 || st.Candidates[0].Node != f.req.ID || !st.CanCommit {
		t.Fatalf("the node left out is still to commit: %+v", st)
	}
	// what is committed merges, the change still open (ADR 0166); its close waits for the rest
	if st.Unmerged != 1 || !st.CanMerge || st.CanClose {
		t.Fatalf("one node to merge, the close held: %+v", st)
	}
	merged := must[domain.Change](t)(g.IntegrateChange(ctx, c.ID, nil))
	onMain := g.withState(t, ctx, must[domain.Baseline](t)(g.Baseline(ctx, merged.ResultBaselineID)))
	if merged.Status != domain.ChangeActive || onMain.Branch != domain.MainBranch || onMain.Nodes[nodeOf(created)] == 0 || onMain.Nodes[f.req.ID] != f.req.Version {
		t.Fatalf("the committed node merges, the change open: %+v %+v", merged, onMain.Nodes)
	}
	if _, err := g.CloseChange(ctx, c.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("an uncommitted modification holds the close: %v", err)
	}
	b = g.withState(t, ctx, must[domain.Baseline](t)(g.CommitChange(ctx, c.ID, "")))
	if b.Nodes[f.req.ID] == f.req.Version {
		t.Fatalf("the second commit writes the rest: %+v", b.Nodes)
	}

	// a version linking to the draft of a node left out is refused
	c2 := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "linked", BaselineID: f.base.ID, OwnBranch: true}))
	d2 := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c2.ID, NodeCreate{Key: "D-2", Type: "Design", Rationale: "new"}))
	r2 := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c2.ID, NodeCheckout{Node: f.req.ID, Rationale: "link"}))
	must[domain.Link](t)(g.ImpactLinkCreate(ctx, c2.ID, r2.ID, LinkWrite{Type: "refines", To: *d2.Post}, "", ""))
	for _, id := range []domain.ChangeImpactID{d2.ID, r2.ID} {
		must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c2.ID, id, domain.ReviewAccepted, "bob", "ok"))
	}
	if _, err := g.CommitChangeNodes(ctx, c2.ID, "", []domain.NodeID{f.req.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a link to the draft of a node left out is refused: %v", err)
	}
	if _, err := g.CommitChangeNodes(ctx, c2.ID, "", []domain.NodeID{f.req.ID, nodeOf(d2)}); err != nil {
		t.Fatalf("both named: %v", err)
	}
}
