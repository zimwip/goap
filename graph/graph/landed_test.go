package graph

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A change commits on its own branch when applied, and again on the branch it was forked from when merged into it: the
// last commit is the version on the target. A sub-change commits nothing: its parent does (ADR 0081).
func TestCommittedOnEveryBranchTheChangeLeavesItsState(t *testing.T) { forEachRepo(t, testCommitted) }

func testCommitted(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	commits := func(change domain.ChangeID) []domain.ImpactEvent {
		var out []domain.ImpactEvent
		for _, e := range must[[]domain.ImpactEvent](t)(impactEventsOf(ctx, g, change)) {
			if e.Op == domain.ImpactCommitted {
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
	ev := commits(res.Change)
	if len(ev) != 2 {
		t.Fatalf("a change with its own branch commits twice, got %d: %+v", len(ev), ev)
	}
	// the branch of a change holds no baseline (ADR 0140): the commit there names its branch only
	onMain := must[domain.Baseline](t)(g.Baseline(ctx, ev[1].Baseline))
	if ev[0].Baseline != "" || ev[0].Branch == domain.MainBranch || onMain.Branch != domain.MainBranch || onMain.ID != res.Baseline.ID {
		t.Fatalf("committed on %q (%s), then on %q (result %s)", ev[0].Branch, ev[0].Baseline, onMain.Branch, res.Baseline.ID)
	}
	if !onMain.Contains(*ev[0].Committed) || !onMain.Contains(*ev[1].Committed) {
		t.Fatalf("a commit names a version its baseline does not hold: %+v", ev)
	}
	c := must[domain.Change](t)(g.Change(ctx, res.Change))
	if len(c.Nodes) != 1 || c.Nodes[0].Committed == nil || *c.Nodes[0].Committed != *ev[1].Committed {
		t.Fatalf("the change impact holds the last commit: %+v", c.Nodes)
	}

	// a sub-change commits on its own branch, forked from its parent's, and commits on it at its merge (ADR 0103)
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
	b, err := g.apply(ctx, sub.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if ev = commits(sub.ID); len(ev) != 2 || ev[1].Branch != domain.BranchOf(parent.Branch) || b.Branch != domain.BranchOf(parent.Branch) {
		t.Fatalf("a sub-change commits on its branch, then on its parent's (baseline %+v): %+v", b, ev)
	}
	if pl := commits(parent.ID); len(pl) != 1 || *pl[0].Committed != *ev[1].Committed {
		t.Fatalf("the parent holds what its sub-change committed: %+v", pl)
	}
	if _, err := g.apply(ctx, parent.ID, ""); err != nil {
		t.Fatal(err)
	}
	ev = commits(parent.ID)
	if len(ev) != 2 || ev[0].Impact != ev[1].Impact || ev[1].Branch != domain.MainBranch {
		t.Fatalf("the parent holds the sub-change's commit on its branch, then commits it on main: %+v", ev)
	}
	if n, err := g.NodeByKey(ctx, "", "L-2"); err != nil || n.Properties["title"] != "t" || n.ChangeID != parent.ID {
		t.Fatalf("L-2 on main = %+v, %v", n, err)
	}
}

func impactEventsOf(ctx context.Context, g *Graph, change domain.ChangeID) (out []domain.ImpactEvent, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { out, err = impactEvents(ctx, tx, change); return err })
	return out, err
}

// Committing validates a change and records its state on its own branch; integrating commits it on its parent branch
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
	commit = g.withState(t, ctx, commit)
	got := must[domain.Change](t)(g.Change(ctx, c.ID))
	if got.Status != domain.ChangeActive || got.ResultBaselineID != commit.ID || commit.Kind != domain.BaselineCommit ||
		commit.Branch != got.Branch || commit.Branch == domain.MainBranch || commit.ChangeID != c.ID {
		t.Fatalf("committed change %+v, commit baseline %+v", got, commit)
	}
	if now := must[domain.Baseline](t)(g.BranchHead(ctx, "", domain.MainBranch)); now.ID != head.ID {
		t.Fatalf("committing moved main: %s then %s", head.ID, now.ID)
	}
	if _, err := g.CommitChange(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("committing again with nothing new: %v", err)
	}
	// a committed change stays open (ADR 0102): it takes impacts again
	late, err := g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: "S-2", Type: "Design", Rationale: "late"}})
	if err != nil {
		t.Fatalf("a committed change takes more impacts: %v", err)
	}
	if err := g.WithdrawImpact(ctx, c.ID, late[0].ID, "", ""); err != nil {
		t.Fatal(err)
	}
	// merged: a fast-forward onto main, the change still open (ADR 0166)
	done, err := g.IntegrateChange(ctx, c.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != domain.ChangeActive || done.ResultBaselineID == commit.ID {
		t.Fatalf("merged change %+v", done)
	}
	if _, err := g.IntegrateChange(ctx, c.ID, nil); !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("merging again with nothing new: %v", err)
	}
	// closed: applied at the baseline its merge left
	closed, err := g.CloseChange(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != domain.ChangeApplied || closed.ResultBaselineID != done.ResultBaselineID {
		t.Fatalf("closed change %+v, merged at %s", closed, done.ResultBaselineID)
	}
	onMain := must[domain.Baseline](t)(g.Baseline(ctx, done.ResultBaselineID))
	if onMain.Branch != domain.MainBranch || onMain.Kind != domain.BaselineFastForward || onMain.ChangeID != c.ID || !maps.Equal(onMain.Nodes, commit.Nodes) {
		t.Fatalf("integration baseline %+v, commit baseline nodes %v", onMain, commit.Nodes)
	}
	if _, err := g.IntegrateChange(ctx, c.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("integrating an applied change: %v", err)
	}

	// a change that wrote nothing has nothing to commit (ADR 0102)
	empty := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "nothing", BaselineID: onMain.ID}))
	if _, err := g.CommitChange(ctx, empty.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty change is not committed: %v", err)
	}
}
