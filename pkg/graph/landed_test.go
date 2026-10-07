package graph

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// A change lands on its own branch when applied, and again on the branch it was forked from when merged into it: the
// last landing is the version on the target. A sub-change lands nothing: its parent does (ADR 0081).
func TestLandedOnEveryBranchTheChangeLeavesItsState(t *testing.T) { forEachRepo(t, testLanded) }

func testLanded(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	landings := func(change domain.ChangeID) []domain.ImpactEvent {
		var out []domain.ImpactEvent
		for _, e := range must[[]domain.ImpactEvent](t)(impactEventsOf(ctx, g, change)) {
			if e.Op == domain.ImpactLanded {
				out = append(out, e)
			}
		}
		return out
	}

	// Commit opens a branch of its own: applied on it, then merged into main
	res, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: "own", Baseline: f.base.ID, By: "test", Edits: []NodeEdit{{Key: "L-1", Type: "Design"}}})
	if err != nil {
		t.Fatal(err)
	}
	ev := landings(res.Change)
	if len(ev) != 2 {
		t.Fatalf("a change with its own branch lands twice, got %d: %+v", len(ev), ev)
	}
	onBranch := must[domain.Baseline](t)(g.Baseline(ctx, ev[0].Baseline))
	onMain := must[domain.Baseline](t)(g.Baseline(ctx, ev[1].Baseline))
	if onBranch.Branch == domain.MainBranch || onMain.Branch != domain.MainBranch || onMain.ID != res.Baseline.ID {
		t.Fatalf("landed on %q, then on %q (result %s)", onBranch.Branch, onMain.Branch, res.Baseline.ID)
	}
	if !onBranch.Contains(*ev[0].Landed) || !onMain.Contains(*ev[1].Landed) {
		t.Fatalf("a landing names a version its baseline does not hold: %+v", ev)
	}
	c := must[domain.Change](t)(g.Change(ctx, res.Change))
	if len(c.Nodes) != 1 || c.Nodes[0].Landed == nil || *c.Nodes[0].Landed != *ev[1].Landed {
		t.Fatalf("the change impact holds the last landing: %+v", c.Nodes)
	}

	// a sub-change lands nothing on a branch: it is integrated into its parent's log, and the parent lands its work (ADR 0081)
	head := must[domain.Baseline](t)(g.BranchHead(ctx, "", domain.MainBranch))
	parent := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "parent", BaselineID: head.ID, OwnBranch: true}))
	sub := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "sub", ParentID: parent.ID}))
	added := must[[]domain.ChangeImpact](t)(g.proposeOrCreate(ctx, sub.ID, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: "L-2", Type: "Design", Rationale: "sub"}}))
	if _, err := g.edit(ctx, sub.ID, added[0].ID, edit{Properties: map[string]any{"title": "t"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.accept(ctx, sub.ID, added[0].ID, "test", "ok"); err != nil {
		t.Fatal(err)
	}
	b, err := g.Apply(ctx, sub.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if ev = landings(sub.ID); len(ev) != 0 || b.ID != "" {
		t.Fatalf("a sub-change lands nothing (baseline %q): %+v", b.ID, ev)
	}
	var into *domain.ImpactRef
	for _, e := range must[[]domain.ImpactEvent](t)(impactEventsOf(ctx, g, sub.ID)) {
		if e.Op == domain.ImpactIntegrated {
			into = e.Into
		}
	}
	if into == nil || into.Change != parent.ID || into.Impact == "" {
		t.Fatalf("the sub-change records where its impact went: %+v", into)
	}
	if _, err := g.Apply(ctx, parent.ID, ""); err != nil {
		t.Fatal(err)
	}
	ev = landings(parent.ID)
	if len(ev) != 2 || ev[0].Impact != into.Impact {
		t.Fatalf("the parent lands the integrated impact on its branch, then on main: %+v", ev)
	}
	if n, err := g.NodeByKey(ctx, "", "L-2"); err != nil || n.Properties["title"] != "t" || n.ChangeID != parent.ID {
		t.Fatalf("L-2 on main = %+v, %v", n, err)
	}
}

func impactEventsOf(ctx context.Context, g *Graph, change domain.ChangeID) (out []domain.ImpactEvent, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { out, err = impactEvents(ctx, tx, change); return err })
	return out, err
}

// Committing validates a change and records its state on its own branch; integrating lands it on its parent branch
// (ADR 0056). Apply is both.
func TestCommitThenIntegrate(t *testing.T) { forEachRepo(t, testCommitThenIntegrate) }

func testCommitThenIntegrate(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	head := must[domain.Baseline](t)(g.BranchHead(ctx, "", domain.MainBranch))
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "split", BaselineID: head.ID}))
	added := must[[]domain.ChangeImpact](t)(g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: "S-1", Type: "Design", Rationale: "split"}}))
	if _, err := g.edit(ctx, c.ID, added[0].ID, edit{Properties: map[string]any{"title": "t"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.accept(ctx, c.ID, added[0].ID, "test", "ok"); err != nil {
		t.Fatal(err)
	}
	// committed: its state is on its own branch, main did not move
	commit, err := g.CommitChange(ctx, c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	got := must[domain.Change](t)(g.Change(ctx, c.ID))
	if got.Status != domain.ChangeCommitted || got.ResultBaselineID != commit.ID || commit.Kind != domain.BaselineCommit ||
		commit.Branch != got.Branch || commit.Branch == domain.MainBranch || commit.ChangeID != c.ID {
		t.Fatalf("committed change %+v, commit baseline %+v", got, commit)
	}
	if now := must[domain.Baseline](t)(g.BranchHead(ctx, "", domain.MainBranch)); now.ID != head.ID {
		t.Fatalf("committing moved main: %s then %s", head.ID, now.ID)
	}
	if _, err := g.CommitChange(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("committing twice: %v", err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("applying a committed change: %v", err)
	}
	if _, err := g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: "S-2", Type: "Design", Rationale: "late"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a committed change takes no more impact: %v", err)
	}
	// integrated: applied, a fast-forward onto main
	done, err := g.IntegrateChange(ctx, c.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != domain.ChangeApplied || done.ResultBaselineID == commit.ID {
		t.Fatalf("integrated change %+v", done)
	}
	onMain := must[domain.Baseline](t)(g.Baseline(ctx, done.ResultBaselineID))
	if onMain.Branch != domain.MainBranch || onMain.Kind != domain.BaselineFastForward || onMain.ChangeID != c.ID || !maps.Equal(onMain.Nodes, commit.Nodes) {
		t.Fatalf("integration baseline %+v, commit baseline nodes %v", onMain, commit.Nodes)
	}
	if _, err := g.IntegrateChange(ctx, c.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("integrating an applied change: %v", err)
	}

	// a change that wrote nothing gets its branch when committed
	empty := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "nothing", BaselineID: onMain.ID}))
	if empty.Branch != domain.MainBranch {
		t.Fatalf("a change that wrote nothing has no branch yet: %q", empty.Branch)
	}
	if _, err := g.CommitChange(ctx, empty.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := must[domain.Change](t)(g.Change(ctx, empty.ID)); got.Branch == domain.MainBranch || got.Status != domain.ChangeCommitted {
		t.Fatalf("committed empty change %+v", got)
	}
}
