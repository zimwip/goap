package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// The guardian of a change (ADR 0098): a root change takes the default guardian unless it names one, a sub-change its
// parent's; the guardian judges a move after it is authorized; a change naming a guardian the graph cannot reach is
// refused its landing, its sub-changes and its moves; a free change asks nothing.
func TestGuardian(t *testing.T) { forEachRepo(t, testGuardians) }

func testGuardians(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newMoveWorld(t, repo)
	g := w.g
	gd := &testGuardian{}
	g.Guardians = map[string]Guardian{"test": gd}

	free := w.open(t, "PROJ-A")
	if free.Guardian != "" {
		t.Fatalf("no default guardian: %q", free.Guardian)
	}
	g.DefaultGuardian = "test"
	c := w.open(t, "PROJ-A")
	sub := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "s", ParentID: c.ID}))
	if c.Guardian != "test" || sub.Guardian != "test" {
		t.Fatalf("guardians: %q %q", c.Guardian, sub.Guardian)
	}
	if got := must[domain.Change](t)(g.Change(ctx, c.ID)); got.Guardian != "test" {
		t.Fatalf("the guardian is stored: %q", got.Guardian)
	}
	other := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "o", BaselineID: w.base.ID, OwnBranch: true, ProjectID: "PROJ-A", Guardian: "other"}))
	if other.Guardian != "other" {
		t.Fatalf("a named guardian wins over the default: %q", other.Guardian)
	}

	// a move: authorized first, then judged by the guardian of the root change, with the family
	var asked []domain.Change
	gd.move = func(_ context.Context, family []domain.Change, to string) error {
		asked = family
		return errors.New("not between these projects")
	}
	if _, err := g.MoveChange(ctx, c.ID, "PROJ-B"); err == nil || err.Error() != "not between these projects" {
		t.Fatalf("a move the guardian refuses: %v", err)
	}
	if len(asked) != 2 || asked[0].ID != c.ID || asked[1].ID != sub.ID {
		t.Fatalf("the guardian is asked about the family: %+v", asked)
	}
	gd.move = nil
	must[domain.Change](t)(g.MoveChange(ctx, c.ID, "PROJ-B"))

	// a guardian the graph has not: refused, whatever the operation
	if _, err := g.CreateChange(ctx, NewChange{Title: "s", ParentID: other.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a sub-change of a change whose guardian is missing: %v", err)
	}
	if _, err := g.MoveChange(ctx, other.ID, "PROJ-B"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a move of a change whose guardian is missing: %v", err)
	}
	if _, err := g.Apply(ctx, other.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("the landing of a change whose guardian is missing: %v", err)
	}
	// a free change lands without asking anyone
	gd.commit = func(context.Context, domain.Change, domain.Blackboard) (bool, bool, error) {
		t.Fatal("a free change asks no guardian")
		return false, false, nil
	}
	must[domain.Baseline](t)(g.Apply(ctx, free.ID, ""))
}
