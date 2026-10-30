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
	res, err := g.CommitEdits(ctx, Edits{ProjectID: "PROJ-ROOT", Title: "own", Commit: f.base.ID, By: "test", Edits: []NodeEdit{{Key: "L-1", Type: "Design"}}})
	if err != nil {
		t.Fatal(err)
	}
	ev := commits(res.Change)
	if len(ev) != 2 {
		t.Fatalf("a change with its own branch commits twice, got %d: %+v", len(ev), ev)
	}
	// the commit on the branch of the change is a commit of that branch (ADR 0175)
	onMain := must[domain.Commit](t)(g.Commit(ctx, ev[1].Commit))
	if own := must[domain.Commit](t)(g.Commit(ctx, ev[0].Commit)); own.Branch != ev[0].Branch || ev[0].Branch == domain.MainBranch || onMain.Branch != domain.MainBranch || onMain.ID != res.Commit.ID {
		t.Fatalf("committed on %q (%s), then on %q (result %s)", ev[0].Branch, ev[0].Commit, onMain.Branch, res.Commit.ID)
	}
	if !onMain.Contains(*ev[0].Committed) || !onMain.Contains(*ev[1].Committed) {
		t.Fatalf("a commit names a version its commit does not hold: %+v", ev)
	}
	c := must[domain.Change](t)(g.Change(ctx, res.Change))
	if len(c.Nodes) != 1 || c.Nodes[0].Committed == nil || *c.Nodes[0].Committed != *ev[1].Committed {
		t.Fatalf("the change impact holds the last commit: %+v", c.Nodes)
	}

	// a sub-change commits on its own branch, forked from its parent's, and commits on it at its merge (ADR 0103)
	head := must[domain.Commit](t)(g.BranchHead(ctx, "", domain.MainBranch))
	parent := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "parent", ForkCommit: head.ID, OwnBranch: true}))
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
		t.Fatalf("a sub-change commits on its branch, then on its parent's (commit %+v): %+v", b, ev)
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
	// the version keeps the change that wrote it; the branches that took it name the changes whose merge took it, and
	// the entry of their log recording it (ADR 0174 addendum: no version is rewritten)
	n, err := g.NodeByKey(ctx, "", "L-2")
	if err != nil || n.Properties["title"] != "t" || n.ChangeID != sub.ID || n.ChangeImpact != added[0].ID {
		t.Fatalf("L-2 on main = %+v, %v", n, err)
	}
	var joins []domain.VersionJoin
	if err := g.repo.InTx(ctx, func(tx Tx) error { joins, err = tx.VersionJoins(ctx, n.ID); return err }); err != nil {
		t.Fatal(err)
	}
	took := map[domain.ChangeID]int64{}
	for _, j := range joins {
		took[j.Change] = j.LogSeq
	}
	if took[sub.ID] == 0 || took[parent.ID] == 0 {
		t.Fatalf("the branches of the parent and of main took L-2's commit, by an entry of the sub-change and of the parent: %+v", joins)
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
	head := must[domain.Commit](t)(g.BranchHead(ctx, "", domain.MainBranch))
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "split", ForkCommit: head.ID}))
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
	if got.Status != domain.ChangeActive || got.LandedCommit != "" || commit.Kind != domain.CommitKindCommit ||
		commit.Branch != got.Branch || commit.Branch == domain.MainBranch || commit.ChangeID != c.ID {
		t.Fatalf("committed change %+v, commit %+v", got, commit)
	}
	if now := must[domain.Commit](t)(g.BranchHead(ctx, "", domain.MainBranch)); now.ID != head.ID {
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
	if done.Status != domain.ChangeActive || done.LandedCommit == commit.ID {
		t.Fatalf("merged change %+v", done)
	}
	if _, err := g.IntegrateChange(ctx, c.ID, nil); !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("merging again with nothing new: %v", err)
	}
	// closed: applied at the commit its merge left
	closed, err := g.CloseChange(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != domain.ChangeApplied || closed.LandedCommit != done.LandedCommit {
		t.Fatalf("closed change %+v, merged at %s", closed, done.LandedCommit)
	}
	onMain := must[domain.Commit](t)(g.Commit(ctx, done.LandedCommit))
	if onMain.Branch != domain.MainBranch || onMain.Kind != domain.CommitKindFastForward || onMain.ChangeID != c.ID || !maps.Equal(onMain.Nodes, commit.Nodes) {
		t.Fatalf("integration commit %+v, commit nodes %v", onMain, commit.Nodes)
	}
	if _, err := g.IntegrateChange(ctx, c.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("integrating an applied change: %v", err)
	}

	// a change that wrote nothing has nothing to commit (ADR 0102)
	empty := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "nothing", ForkCommit: onMain.ID}))
	if _, err := g.CommitChange(ctx, empty.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty change is not committed: %v", err)
	}
}
