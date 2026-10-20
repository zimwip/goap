package graph

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A creation is planned before it is written (an impact analysis on a green field names what the change will create):
// ProposeImpact with intent created gives an impact with no draft; creating or checking out its key gives it its draft,
// on the same impact; the change does not merge while one is still planned.
func TestPlannedCreation(t *testing.T) { forEachRepo(t, testPlannedCreation) }

func testPlannedCreation(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "green field", BaselineID: f.base.ID}))
	plan := func(key, typ string) ([]domain.ChangeImpact, error) {
		return g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: key, Type: typ, Rationale: "the change needs " + key}})
	}
	req := must[[]domain.ChangeImpact](t)(plan("REQ-NEW", "Requirement"))[0]
	if req.Intent != domain.IntentCreated || req.Post != nil || !req.Planned() || req.Review != domain.ReviewProposed {
		t.Fatalf("a planned creation has no draft: %+v", req)
	}
	if _, err := plan("REQ-1", "Requirement"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("a key the baseline holds is not planned: %v", err)
	}
	if _, err := plan("REQ-NEW", "Requirement"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("a key planned twice: %v", err)
	}
	if _, err := g.ImpactNodeReview(ctx, c.ID, req.ID, domain.ReviewAccepted, "bob", "fine"); err == nil {
		t.Fatal("a planned creation modifies nothing yet: it is not accepted")
	}

	// the creation of its key fulfils it: same impact, a draft with the properties
	made := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-NEW", Type: "Requirement", Rationale: "written", Properties: map[string]any{"title": "Pay by card"}}))
	if made.ID != req.ID || made.Post == nil || !made.Post.IsDraft() || made.Rationale != req.Rationale {
		t.Fatalf("the creation fulfils the planned impact: %+v", made)
	}
	if view := must[domain.NodeView](t)(g.ChangeNodeView(ctx, c.ID, "", *made.Post)); view.Properties["title"] != "Pay by card" || view.Key != "REQ-NEW" {
		t.Fatalf("the draft: %+v", view.Node)
	}
	if _, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-NEW", Type: "Requirement", Rationale: "again"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a fulfilled creation is not created again: %v", err)
	}

	// the checkout of a planned key gives it a bare draft, edited next
	tst := must[[]domain.ChangeImpact](t)(plan("TST-NEW", "TestCase"))[0]
	out := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Key: "TST-NEW"}))
	if out.ID != tst.ID || out.Post == nil || !out.Post.IsDraft() {
		t.Fatalf("the checkout fulfils the planned impact: %+v", out)
	}
	must[domain.Link](t)(g.ImpactLinkCreate(ctx, c.ID, tst.ID, LinkWrite{Type: "verifies", To: domain.NodeRef{ID: made.Post.ID}}, "", ""))
	var ops []domain.ImpactOp
	for _, e := range must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, c.ID)) {
		if e.Impact == req.ID || e.Impact == tst.ID {
			ops = append(ops, e.Op)
		}
	}
	if want := []domain.ImpactOp{domain.ImpactProposed, domain.ImpactCheckedOut, domain.ImpactProposed, domain.ImpactCheckedOut, domain.ImpactUpdated}; !slices.Equal(ops, want) {
		t.Fatalf("a planned creation is proposed, then checked out by its creation: %v", ops)
	}

	// one still planned holds the merge
	must[[]domain.ChangeImpact](t)(plan("REQ-LATER", "Requirement"))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, req.ID, "bob", "fine"))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, tst.ID, "bob", "fine"))
	if _, err := g.Apply(ctx, c.ID, ""); err == nil || !strings.Contains(err.Error(), "REQ-LATER is planned") {
		t.Fatalf("a planned creation holds the merge: %v", err)
	}
	later := impactsOn(t, g, c.ID, "REQ-LATER")
	if err := g.WithdrawImpact(ctx, c.ID, later[0].ID, "", ""); err != nil {
		t.Fatal(err)
	}
	must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	for _, key := range []string{"REQ-NEW", "TST-NEW"} {
		if n, err := g.NodeByKey(ctx, domain.DefaultNamespace, key); err != nil || n.Version != 1 {
			t.Fatalf("%s commits as a new node: %+v %v", key, n, err)
		}
	}
	checkImpactLogs(t, repo)
}
