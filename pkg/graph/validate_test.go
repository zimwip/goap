package graph

import (
	"context"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func issuesByCode(is []domain.BoardIssue) map[string][]domain.BoardIssue {
	out := map[string][]domain.BoardIssue{}
	for _, i := range is {
		out[i.Code] = append(out[i.Code], i)
	}
	return out
}

func note(id string, derived ...domain.ItemID) domain.ChangeItem {
	return domain.ChangeItem{ID: domain.ItemID(id), Kind: domain.KindArtifact, Type: "note", DerivedFrom: derived}
}

func TestValidateBoard(t *testing.T) { forEachRepo(t, testValidateBoard) }

func testValidateBoard(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, _ := g.CreateChange(ctx, NewChange{Title: "v", BaselineID: f.base.ID})
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{note("p1"), note("p2", "p1")}); err != nil {
		t.Fatal(err)
	}
	if is, err := g.ValidateBoard(ctx, c.ID, ""); err != nil || len(is) != 0 {
		t.Fatalf("clean board: %+v, %v", is, err)
	}

	// a decision rejects p1: p2 builds on an invalid item, the culprit is p1
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{ID: "d1", Kind: domain.KindDecision, Decision: &domain.Decision{Item: "p1", Accept: false}}}); err != nil {
		t.Fatal(err)
	}
	is, _ := g.ValidateBoard(ctx, c.ID, "")
	if got := issuesByCode(is)["derived_from_invalid"]; len(got) != 1 || got[0].Item != "p2" || got[0].Culprit != "p1" {
		t.Fatalf("issues = %+v", is)
	}

	// dangling provenance
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{note("n1", "ghost"), note("n2", "ghost")}); err != nil {
		t.Fatal(err)
	}
	by := issuesByCode(mustIssues(t, g, c.ID, ""))
	if len(by["dangling"]) != 2 {
		t.Fatalf("dangling = %+v", by["dangling"])
	}
}

func mustIssues(t *testing.T, g *Graph, id domain.ChangeID, flow string) []domain.BoardIssue {
	t.Helper()
	is, err := g.ValidateBoard(context.Background(), id, flow)
	if err != nil {
		t.Fatal(err)
	}
	return is
}

func TestValidateBoardOutdatedAndFlowView(t *testing.T) {
	forEachRepo(t, testValidateBoardOutdatedAndFlowView)
}

func testValidateBoardOutdatedAndFlowView(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	a, _ := g.CreateChange(ctx, NewChange{Title: "a", BaselineID: f.base.ID})
	b, _ := g.CreateChange(ctx, NewChange{Title: "b", BaselineID: f.base.ID})
	pre := f.req.Ref()
	as, err := g.ProposeImpact(ctx, a.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "from a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.edit(ctx, a.ID, as[0].ID, edit{Properties: map[string]any{"title": "from a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.accept(ctx, a.ID, as[0].ID, "u", "ok"); err != nil {
		t.Fatal(err)
	}
	bs, err := g.ProposeImpact(ctx, b.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "from b", Execution: "e1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	// REQ-1 moved on main since the impact of b was written: it is outdated
	id := domain.ItemID(bs[0].ID)
	by := issuesByCode(mustIssues(t, g, b.ID, ""))
	if o := by["outdated"]; len(o) != 1 || o[0].Item != id || o[0].Culprit != id || o[0].Severity != domain.IssueWarning {
		t.Fatalf("outdated = %+v", by)
	}
	// relaunching the run that declared it marks it stale: the flow view no longer contains it, so it is clean
	fl, err := g.OpenFlow(ctx, b.ID, OpenFlowRequest{StaleRuns: []string{"e1"}})
	if err != nil {
		t.Fatal(err)
	}
	if is := mustIssues(t, g, b.ID, fl.ID); len(is) != 0 {
		t.Fatalf("flow view issues = %+v", is)
	}
	if is := mustIssues(t, g, b.ID, ""); len(issuesByCode(is)["outdated"]) != 1 {
		t.Fatalf("main view must still show the problem: %+v", is)
	}
}
