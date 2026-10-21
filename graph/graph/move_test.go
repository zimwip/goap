package graph

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// moveWorld holds two sibling projects under the root one, and a node committed in the first.
type moveWorld struct {
	g    *Graph
	a, b domain.Node
	base domain.Baseline
}

func newMoveWorld(t *testing.T, repo Repo) moveWorld {
	t.Helper()
	ctx := context.Background()
	g := New(repo)
	g.Caller = func(context.Context) string { return "mover" }
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	root := must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, rootProject(g)))
	w := moveWorld{g: g}
	for _, key := range []string{"PROJ-A", "PROJ-B"} {
		must[CommitResult](t)(g.Commit(ctx, Commit{ProjectID: rootProject(g), Namespace: NamespaceOrganisation, Title: key, By: "t", Edits: []NodeEdit{
			{Key: key, Type: NodeTypeProjectUnit, Props: map[string]any{"name": key}, Links: []LinkEdit{{Type: LinkProjectPartOf, To: refPtr(root.Ref())}}}}}))
	}
	w.a = must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, "PROJ-A"))
	w.b = must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, "PROJ-B"))
	must[CommitResult](t)(g.Commit(ctx, Commit{ProjectID: "PROJ-A", Title: "old", By: "t", Edits: []NodeEdit{{Key: "OLD-1", Type: "Design"}}}))
	w.base = must[domain.Baseline](t)(g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch))
	return w
}

func (w moveWorld) open(t *testing.T, project string) domain.Change {
	t.Helper()
	return must[domain.Change](t)(w.g.CreateChange(context.Background(), NewChange{Title: "c", BaselineID: w.base.ID, OwnBranch: true, ProjectID: project}))
}

// projectMoves are the projectId fields of the change.updated entries of a change.
func projectMoves(t *testing.T, g *Graph, id domain.ChangeID) (moves []domain.HeaderValue, entries []domain.LogEntry) {
	t.Helper()
	log, _, err := g.ChangeLog(context.Background(), domain.LogFilter{Change: id, Types: []string{domain.LogChange + ".updated"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range log {
		var h domain.HeaderEdit
		if err := json.Unmarshal(e.Payload, &h); err != nil {
			t.Fatal(err)
		}
		if v, ok := h.Fields[HeaderProjectID]; ok {
			moves = append(moves, v)
			entries = append(entries, e)
		}
	}
	return moves, entries
}

// A move changes the project of the root change and of its open sub-changes, in the log of each; the nodes keep their
// project, and the node the change creates afterwards takes the new one when it commits (ADR 0091).
func TestMoveChange(t *testing.T) { forEachRepo(t, testMoveChange) }

func testMoveChange(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newMoveWorld(t, repo)
	g := w.g
	c := w.open(t, "PROJ-A")
	sub := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "s", ParentID: c.ID}))
	// a node drafted before the move
	pre := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "PRE-1", Type: "Design", Rationale: "before"}))

	moved, err := g.MoveChange(ctx, c.ID, "PROJ-B")
	if err != nil {
		t.Fatal(err)
	}
	if moved.ID != c.ID || moved.ProjectID != "PROJ-B" {
		t.Fatalf("moved = %+v", moved)
	}
	if got := must[domain.Change](t)(g.Change(ctx, sub.ID)); got.ProjectID != "PROJ-B" {
		t.Fatalf("the sub-change follows its parent: %s", got.ProjectID)
	}
	for _, id := range []domain.ChangeID{c.ID, sub.ID} {
		moves, entries := projectMoves(t, g, id)
		if len(moves) != 1 || moves[0].From != "PROJ-A" || moves[0].To != "PROJ-B" || entries[0].Subject != HeaderProjectID || entries[0].By != "mover" {
			t.Fatalf("log of %s: %+v %+v", id, moves, entries)
		}
	}
	// the draft made before the move and the one made after take the project of the change at commit
	post := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "POST-1", Type: "Design", Rationale: "after"}))
	for _, cn := range []domain.ChangeImpact{pre, post} {
		must[domain.ChangeImpact](t)(g.ImpactNodeReviewOn(ctx, c.ID, "", "", cn.ID, domain.ReviewAccepted, "reviewer", "ok"))
	}
	// the sub-change, which wrote nothing, is closed first: it has nothing to merge into its parent (ADR 0102)
	if _, err := g.apply(ctx, sub.ID, ""); !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("an empty sub-change: %v", err)
	}
	gone := domain.ChangeAbandoned
	must[domain.Change](t)(g.UpdateChange(ctx, sub.ID, ChangePatch{Status: &gone}))
	if _, err := g.apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	b := must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, "PROJ-B"))
	a := must[domain.Node](t)(g.NodeByKey(ctx, NamespaceOrganisation, "PROJ-A"))
	for _, key := range []string{"PRE-1", "POST-1"} {
		if n := must[domain.Node](t)(g.NodeByKey(ctx, "", key)); n.Project != b.Key {
			t.Fatalf("%s commits in the project of the change: %s", key, n.Project)
		}
	}
	if n := must[domain.Node](t)(g.NodeByKey(ctx, "", "OLD-1")); n.Project != a.Key {
		t.Fatalf("a node keeps its project: %s", n.Project)
	}
	// a committed change moves no more
	if _, err := g.MoveChange(ctx, c.ID, "PROJ-A"); !errors.Is(err, ErrConflict) {
		t.Fatalf("an applied change: %v", err)
	}
}

func TestMoveChangeRefusals(t *testing.T) { forEachRepo(t, testMoveChangeRefusals) }

func testMoveChangeRefusals(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newMoveWorld(t, repo)
	g := w.g
	c := w.open(t, "PROJ-A")
	sub := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "s", ParentID: c.ID}))
	refuse := func(what string, id domain.ChangeID, project string, want error) {
		t.Helper()
		if _, err := g.MoveChange(ctx, id, project); !errors.Is(err, want) {
			t.Fatalf("%s: %v, want %v", what, err, want)
		}
		if got := must[domain.Change](t)(g.Change(ctx, id)); got.ProjectID == project && project != "PROJ-A" {
			t.Fatalf("%s: the change moved", what)
		}
	}
	refuse("a sub-change", sub.ID, "PROJ-B", ErrInvalid)
	refuse("the same project", c.ID, "PROJ-A", ErrInvalid)
	refuse("no project", c.ID, "", ErrInvalid)
	refuse("an unknown project", c.ID, "PROJ-NOPE", ErrInvalid)
	if _, err := g.MoveChange(ctx, "00000000-0000-4000-8000-000000000000", "PROJ-B"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown change: %v", err)
	}
	// a node that is not a project
	refuse("a unit", c.ID, rootOrg(g), ErrInvalid)

	// the gate refuses before anything is written
	g.ProjectMoveGate = func(_ context.Context, family []domain.Change, to string) error {
		if len(family) != 2 || family[0].ID != c.ID || family[1].ID != sub.ID || to != "PROJ-B" {
			t.Fatalf("gate asked about %+v -> %s", family, to)
		}
		return errors.New("no")
	}
	if _, err := g.MoveChange(ctx, c.ID, "PROJ-B"); err == nil || err.Error() != "no" {
		t.Fatalf("gate: %v", err)
	}
	if moves, _ := projectMoves(t, g, c.ID); len(moves) != 0 {
		t.Fatalf("a refused move is not logged: %+v", moves)
	}
	g.ProjectMoveGate = nil

	// committed (its sub-change is abandoned first: a change commits with none open), abandoned
	gone := domain.ChangeAbandoned
	must[domain.Change](t)(g.UpdateChange(ctx, sub.ID, ChangePatch{Status: &gone}))
	n := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "C-1", Type: "Design", Rationale: "x"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeReviewOn(ctx, c.ID, "", "", n.ID, domain.ReviewAccepted, "reviewer", "ok"))
	if _, err := g.CommitChange(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	refuse("a committed change", c.ID, "PROJ-B", ErrConflict)
	ab := w.open(t, "PROJ-A")
	st := domain.ChangeAbandoned
	must[domain.Change](t)(g.UpdateChange(ctx, ab.ID, ChangePatch{Status: &st}))
	refuse("an abandoned change", ab.ID, "PROJ-B", ErrConflict)
}

// projectChains is a guardian chain that depends on the project: "release" frames "sdlc" in PROJ-B only.
type projectChains struct{ chainGate }

func (projectChains) Guardians(_ context.Context, m, project string) ([]domain.GuardianRef, error) {
	if m == "sdlc" && project == "PROJ-B" {
		return []domain.GuardianRef{{Methodology: "release"}, {Methodology: "sdlc"}}, nil
	}
	return []domain.GuardianRef{{Methodology: m}}, nil
}

// A move computes the guardian chain of the change again for its new project, and of its sub-changes from it, in
// the same change.updated entry (ADR 0106).
func TestMoveChangeGuardians(t *testing.T) { forEachRepo(t, testMoveChangeGuardians) }

func testMoveChangeGuardians(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newMoveWorld(t, repo)
	g := w.g
	g.Lifecycles = &projectChains{}
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "c", Methodology: "sdlc", BaselineID: w.base.ID, OwnBranch: true, ProjectID: "PROJ-A"}))
	sub := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "s", Methodology: "review", ParentID: c.ID}))
	chain := func(c domain.Change) string {
		var out []string
		for _, r := range c.Guardians {
			out = append(out, r.Methodology)
		}
		return strings.Join(out, ",")
	}
	if chain(c) != "sdlc" || chain(sub) != "sdlc,review" {
		t.Fatalf("before the move: %s / %s", chain(c), chain(sub))
	}
	moved := must[domain.Change](t)(g.MoveChange(ctx, c.ID, "PROJ-B"))
	if chain(moved) != "release,sdlc" {
		t.Fatalf("the root takes the chain of its new project: %s", chain(moved))
	}
	if s := must[domain.Change](t)(g.Change(ctx, sub.ID)); chain(s) != "release,sdlc,review" {
		t.Fatalf("the sub-change takes its chain from its parent's: %s", chain(s))
	}
	log, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID, Types: []string{domain.LogChange + ".updated"}})
	if err != nil {
		t.Fatal(err)
	}
	var h domain.HeaderEdit
	if err := json.Unmarshal(log[len(log)-1].Payload, &h); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.Fields[HeaderGuardians]; !ok {
		t.Fatalf("the chain is journaled with the move: %+v", h.Fields)
	}
}
