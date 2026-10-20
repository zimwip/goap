package graph

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

func TestChangeDependencies(t *testing.T) { forEachRepo(t, testChangeDependencies) }

func testChangeDependencies(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newOrgWorld(t, repo)
	g := w.g
	create := func(title string, parent domain.ChangeID) domain.Change {
		return must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: title, Methodology: "sdlc",
			BaselineID: w.base.ID, OwnBranch: parent == "", ParentID: parent}))
	}
	modify := func(c domain.Change, n domain.Node, title string) {
		t.Helper()
		pre := n.Ref()
		added := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: title}}))
		if _, err := g.edit(ctx, c.ID, added[0].ID, edit{Properties: map[string]any{"title": title}}); err != nil {
			t.Fatal(err)
		}
		if _, err := g.accept(ctx, c.ID, added[0].ID, "u", "ok"); err != nil {
			t.Fatal(err)
		}
		if _, err := g.CommitChange(ctx, c.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(ds []domain.Dependency) []domain.ChangeID {
		var out []domain.ChangeID
		for _, d := range ds {
			out = append(out, d.ID)
		}
		return out
	}

	release := create("R1", "")
	b := create("B", release.ID) // created first, merged second
	a := create("A", release.ID)
	if _, err := g.SetChangeDependencies(ctx, b.ID, []domain.ChangeID{a.ID, a.ID}); err != nil {
		t.Fatal(err)
	}
	if c := must[domain.Change](t)(g.Change(ctx, b.ID)); !slices.Equal(c.DependsOn, []domain.ChangeID{a.ID}) {
		t.Fatalf("stored once: %v", c.DependsOn)
	}
	for name, try := range map[string]struct {
		id   domain.ChangeID
		deps []domain.ChangeID
	}{
		"cycle":      {a.ID, []domain.ChangeID{b.ID}},
		"itself":     {a.ID, []domain.ChangeID{a.ID}},
		"ancestor":   {a.ID, []domain.ChangeID{release.ID}},
		"sub-change": {release.ID, []domain.ChangeID{a.ID}},
	} {
		if _, err := g.SetChangeDependencies(ctx, try.id, try.deps); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	// the sub-changes of the release merge in their dependency order
	deps := must[ChangeDependencies](t)(g.ChangeDependencies(ctx, release.ID))
	if got := ids(deps.Order); !slices.Equal(got, []domain.ChangeID{a.ID, b.ID}) || !deps.Order[0].Satisfied || deps.Order[1].Satisfied {
		t.Fatalf("merge order: %+v", deps.Order)
	}
	if got := ids(must[ChangeDependencies](t)(g.ChangeDependencies(ctx, a.ID)).Dependents); !slices.Equal(got, []domain.ChangeID{b.ID}) {
		t.Fatalf("dependents of A: %v", got)
	}
	bb := must[domain.Blackboard](t)(g.Blackboard(ctx, b.ID))
	if fd := domain.Facet[[]domain.Dependency](bb, domain.FacetDependencies); len(fd) != 1 || fd[0].ID != a.ID || fd[0].Satisfied {
		t.Fatalf("the guards see the dependencies: %+v", fd)
	}

	modify(b, w.cmp1, "one by B")
	if _, err := g.IntegrateChange(ctx, b.ID, nil); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), string(a.ID)) {
		t.Fatalf("B merges before A: %v", err)
	}
	modify(a, w.cmp2, "two by A")
	if _, err := g.IntegrateChange(ctx, a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := g.IntegrateChange(ctx, b.ID, nil); err != nil {
		t.Fatalf("B merges once A merged into the release: %v", err)
	}
	if _, err := g.SetChangeDependencies(ctx, b.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("the dependencies of a merged change no longer change: %v", err)
	}

	// a root change depending on A waits for the release to commit: A's work is on the release's branch only
	x := create("X", "")
	if _, err := g.SetChangeDependencies(ctx, x.ID, []domain.ChangeID{a.ID}); err != nil {
		t.Fatal(err)
	}
	modify(x, w.cmp3, "three by X")
	if _, err := g.IntegrateChange(ctx, x.ID, nil); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "has not committed") {
		t.Fatalf("X merges before the release committed: %v", err)
	}
	if _, err := g.IntegrateChange(ctx, release.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := g.IntegrateChange(ctx, x.ID, nil); err != nil {
		t.Fatalf("X merges once the release committed: %v", err)
	}

	// the edits are journaled; an abandoned change is no dependency
	y := create("Y", "")
	z := create("Z", "")
	abandoned := domain.ChangeAbandoned
	if _, err := g.UpdateChange(ctx, z.ID, ChangePatch{Status: &abandoned}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.SetChangeDependencies(ctx, y.ID, []domain.ChangeID{z.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("depend on an abandoned change: %v", err)
	}
	log, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: x.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(log, func(e domain.LogEntry) bool { return strings.Contains(string(e.Payload), HeaderDependsOn) }) {
		t.Fatalf("the dependencies are journaled: %+v", log)
	}
}

func TestDependencyOrder(t *testing.T) {
	c := func(id string, deps ...domain.ChangeID) domain.Change {
		return domain.Change{ID: domain.ChangeID(id), DependsOn: deps}
	}
	got := domain.DependencyOrder([]domain.Change{c("d", "c", "x"), c("c", "a", "b"), c("b"), c("a", "b")})
	var order []domain.ChangeID
	for _, x := range got {
		order = append(order, x.ID)
	}
	if !slices.Equal(order, []domain.ChangeID{"b", "a", "c", "d"}) {
		t.Fatalf("order: %v", order)
	}
}
