package graph

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// The change-stack of a version and of a baseline (ADR 0133): two sub-changes of one parent change the same node, the
// second is rebased onto the first (a version of reason rebase, its second parent the first's), the parent lands both.
// The stack of the node's version on main and the one of main's head both name the four changes, sub-changes first,
// and each sub-change under its parent.
func TestChangeStack(t *testing.T) { forEachRepo(t, testChangeStack) }

func testChangeStack(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newOrgWorld(t, repo)
	g := w.g
	before := must[domain.Baseline](t)(g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch))
	parent := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "parent", BaselineID: w.base.ID, OwnBranch: true}))
	a := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "sub A", ParentID: parent.ID}))
	b := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "sub B", ParentID: parent.ID}))
	pre := w.cmp3.Ref()
	write := func(c domain.Change, props map[string]any) {
		t.Helper()
		added := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: c.Title}}))
		if _, err := g.edit(ctx, c.ID, added[0].ID, edit{Properties: props}); err != nil {
			t.Fatal(err)
		}
		must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, added[0].ID, "u", "ok"))
	}
	write(a, map[string]any{"title": "from A"})
	write(b, map[string]any{"title": "three", "version": "2.0"})
	must[domain.Baseline](t)(g.Apply(ctx, a.ID, ""))
	must[domain.Baseline](t)(g.Apply(ctx, b.ID, "")) // rebased onto A: different properties, no conflict
	head := must[domain.Baseline](t)(g.Apply(ctx, parent.ID, ""))

	n := must[domain.Node](t)(g.NodeByKey(ctx, domain.DefaultNamespace, "CMP-3"))
	if n.Reason != domain.ReasonRebase || len(n.Parents) != 2 || n.ChangeID != b.ID {
		t.Fatalf("B's version merges A's into its own: %+v", n)
	}
	ids := func(cs []domain.StackChange) []domain.ChangeID {
		var out []domain.ChangeID
		for _, c := range cs {
			out = append(out, c.ID)
		}
		return out
	}
	parentOf := func(cs []domain.StackChange, id domain.ChangeID) domain.ChangeID {
		for _, c := range cs {
			if c.ID == id {
				return c.Parent
			}
		}
		return "?"
	}

	vs := must[domain.ChangeStack](t)(g.ChangeStack(ctx, ChangeStackQuery{Node: n.Ref()}))
	got := ids(vs.Changes)
	if len(got) != 4 || got[0] != w.cmp3.ChangeID || !slices.Contains(got, a.ID) || !slices.Contains(got, b.ID) || !slices.Contains(got, parent.ID) {
		t.Fatalf("the stack of %s: %v (cmp3 %s, A %s, B %s, parent %s)", n.Ref(), got, w.cmp3.ChangeID, a.ID, b.ID, parent.ID)
	}
	if parentOf(vs.Changes, a.ID) != parent.ID || parentOf(vs.Changes, b.ID) != parent.ID || parentOf(vs.Changes, parent.ID) != "" {
		t.Fatalf("sub-changes name their parent: %+v", vs.Changes)
	}
	last := vs.Versions[len(vs.Versions)-1]
	if last.Version != n.Version || last.Reason != domain.ReasonRebase || len(vs.Versions) != 4 || vs.Versions[0].Version != w.cmp3.Version {
		t.Fatalf("lineage: %+v", vs.Versions)
	}
	if !slices.ContainsFunc(last.Joined, func(j domain.VersionJoin) bool { return j.Branch == domain.MainBranch && j.Change == parent.ID }) {
		t.Fatalf("B's version joined main by the parent's merge: %+v", last.Joined)
	}
	if cut := must[domain.ChangeStack](t)(g.ChangeStack(ctx, ChangeStackQuery{Node: n.Ref(), Limit: 2})); !cut.Truncated || len(cut.Versions) != 2 || cut.Versions[1].Version != n.Version {
		t.Fatalf("a lineage cut at 2: %+v", cut)
	}

	bs := must[domain.ChangeStack](t)(g.ChangeStack(ctx, ChangeStackQuery{Baseline: head.ID}))
	got = ids(bs.Changes)
	for _, c := range []domain.ChangeID{a.ID, b.ID, parent.ID} {
		if !slices.Contains(got, c) {
			t.Fatalf("main's head is made of %v, missing %s", got, c)
		}
	}
	if bs.Baselines[len(bs.Baselines)-1].ID != head.ID {
		t.Fatalf("the lineage ends with the baseline asked: %+v", bs.Baselines)
	}
	if !slices.ContainsFunc(bs.Baselines, func(s domain.BaselineStep) bool { return s.Kind == domain.BaselineRebase && s.Change == b.ID }) {
		t.Fatalf("B's rebase is part of main's head: %+v", bs.Baselines)
	}

	added, removed := mustStacks(t)(g.DiffStacks(ctx, before.ID, head.ID))
	if !slices.Equal(ids(removed), nil) || len(added) != 3 || !slices.Contains(ids(added), parent.ID) {
		t.Fatalf("what explains main moving: +%v -%v", ids(added), ids(removed))
	}
	if _, err := g.ChangeStack(ctx, ChangeStackQuery{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a stack of nothing: %v", err)
	}
}

func mustStacks(t *testing.T) func(a, b []domain.StackChange, err error) ([]domain.StackChange, []domain.StackChange) {
	return func(a, b []domain.StackChange, err error) ([]domain.StackChange, []domain.StackChange) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return a, b
	}
}
