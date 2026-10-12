package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/operations"
)

// Every operation of the catalogue asks the OperationAuthorizer first (ADR 0103): a reviewer accepts or rejects, and
// its checkout, edit or withdrawal is refused before anything is written.
func TestOperationAuthorizer(t *testing.T) { forEachRepo(t, testOperationAuthorizer) }

func testOperationAuthorizer(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "rights", BaselineID: f.base.ID}))
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "v2"}))

	refused := errors.New("refused")
	var asked []string
	reviewer := true
	g.OperationAuthorizer = func(_ context.Context, ch domain.Change, op string) error {
		if ch.ID != c.ID {
			t.Fatalf("asked for change %s", ch.ID)
		}
		asked = append(asked, op)
		if o, _ := operations.Lookup(op); reviewer && o.Kind != operations.Reviewer {
			return refused
		}
		return nil
	}
	if _, err := g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "x"}}); !errors.Is(err, refused) {
		t.Fatalf("a reviewer does not edit: %v", err)
	}
	if err := g.WithdrawImpact(ctx, c.ID, cn.ID, "", ""); !errors.Is(err, refused) {
		t.Fatalf("a reviewer does not withdraw: %v", err)
	}
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.test.ID, Rationale: "v2"}); !errors.Is(err, refused) {
		t.Fatalf("a reviewer does not check out: %v", err)
	}
	if got := must[domain.Change](t)(g.Change(ctx, c.ID)); len(got.Nodes) != 1 {
		t.Fatalf("nothing written: %+v", got.Nodes)
	}
	if _, err := g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewRejected, "bob", "not like this"); err != nil {
		t.Fatalf("a reviewer reviews: %v", err)
	}
	want := []string{operations.ImpactUpdate, operations.ImpactWithdraw, operations.ImpactCheckout, operations.ImpactReview}
	if len(asked) != len(want) {
		t.Fatalf("asked %v, want %v", asked, want)
	}
	for i := range want {
		if asked[i] != want[i] {
			t.Fatalf("asked %v, want %v", asked, want)
		}
	}
	reviewer = false
	if err := g.WithdrawImpact(ctx, c.ID, cn.ID, "", ""); err != nil {
		t.Fatalf("a doer withdraws: %v", err)
	}
}
