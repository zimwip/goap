package graph

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// A root change is rebased onto the head of the branch it was forked from before it lands, and lands as a fast-forward
// (ADR 0101).

// propChange opens a change from base that sets props on a node of the fixture, accepted.
func propChange(t *testing.T, g *Graph, base domain.BaselineID, title string, node domain.NodeRef, props map[string]any) (domain.Change, domain.ChangeImpactID) {
	t.Helper()
	ctx := context.Background()
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: title, BaselineID: base, OwnBranch: true}))
	ns := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &node, Rationale: title}}))
	if _, err := g.edit(ctx, c.ID, ns[0].ID, edit{Properties: props}); err != nil {
		t.Fatal(err)
	}
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, ns[0].ID, "u", "ok"))
	return c, ns[0].ID
}

// baselinesOf lists the baselines of a branch of the default namespace, in the order they were made.
func baselinesOf(t *testing.T, g *Graph, branch string) []domain.Baseline {
	t.Helper()
	var out []domain.Baseline
	for _, b := range must[[]domain.Baseline](t)(g.Baselines(context.Background(), "")) {
		if domain.BranchOf(b.Branch) == branch {
			out = append(out, b)
		}
	}
	return out
}

func baselineOfChange(t *testing.T, g *Graph, c domain.ChangeID, kind string) domain.Baseline {
	t.Helper()
	for _, b := range must[[]domain.Baseline](t)(g.Baselines(context.Background(), "")) {
		if b.ChangeID == c && b.Kind == kind {
			return b
		}
	}
	t.Fatalf("no %s baseline of change %s", kind, c)
	return domain.Baseline{}
}

// rebaseMoves are the baselineId moves recorded in the log of a change.
func rebaseMoves(t *testing.T, g *Graph, id domain.ChangeID) []domain.HeaderValue {
	t.Helper()
	entries, _, err := g.ChangeLog(context.Background(), domain.LogFilter{Change: id})
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.HeaderValue
	for _, e := range entries {
		if e.Type != domain.LogChange+".updated" {
			continue
		}
		var h domain.HeaderEdit
		if err := json.Unmarshal(e.Payload, &h); err != nil {
			t.Fatal(err)
		}
		if v, ok := h.Fields[HeaderBaselineID]; ok {
			out = append(out, v)
		}
	}
	return out
}

func rebasedEvents(t *testing.T, g *Graph, id domain.ChangeID) []domain.ImpactEvent {
	t.Helper()
	events := must[[]domain.ImpactEvent](t)(impactEventsOf(context.Background(), g, id))
	return slices.DeleteFunc(events, func(e domain.ImpactEvent) bool { return e.Op != domain.ImpactTransitioned || e.Patch["rebased"] == nil })
}

// test1 and test2 start from the same baseline and change different nodes: test2 moves onto test1's result, main reads
// as two fast-forwards, the rebase is in test2's log and on its commit baseline.
func TestRebaseRootDifferentNodes(t *testing.T) { forEachRepo(t, testRebaseRootDifferentNodes) }

func testRebaseRootDifferentNodes(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	t1, _ := propChange(t, g, f.base.ID, "test1", f.req.Ref(), map[string]any{"title": "T1"})
	t2, _ := propChange(t, g, f.base.ID, "test2", f.need.Ref(), map[string]any{"title": "T2"})
	b1 := must[domain.Baseline](t)(g.Apply(ctx, t1.ID, ""))
	b2 := must[domain.Baseline](t)(g.Apply(ctx, t2.ID, ""))

	if b1.Kind != domain.BaselineFastForward || b1.ParentID != f.base.ID || b2.Kind != domain.BaselineFastForward || b2.ParentID != b1.ID {
		t.Fatalf("main must read T1 then T2: %+v %+v", b1, b2)
	}
	for _, b := range baselinesOf(t, g, domain.MainBranch) {
		if b.MergedFrom != "" {
			t.Fatalf("a merge baseline on main: %+v", b)
		}
	}
	if head := must[domain.Baseline](t)(g.BranchHead(ctx, "", domain.MainBranch)); head.ID != b2.ID {
		t.Fatalf("head = %s, want %s", head.ID, b2.ID)
	}
	moves := rebaseMoves(t, g, t2.ID)
	if len(moves) != 1 || moves[0].From != string(f.base.ID) || moves[0].To != string(b1.ID) {
		t.Fatalf("test2's log must say it moved from B to T1: %+v", moves)
	}
	if got := rebaseMoves(t, g, t1.ID); len(got) != 0 {
		t.Fatalf("test1 was not behind: %+v", got)
	}
	if got := baselineOfChange(t, g, t2.ID, domain.BaselineCommit); got.RebasedFrom != f.base.ID || got.ParentID != b1.ID {
		t.Fatalf("test2's commit: %+v", got)
	}
	if got := baselineOfChange(t, g, t1.ID, domain.BaselineCommit); got.RebasedFrom != "" {
		t.Fatalf("test1's commit was never rebased: %+v", got)
	}
	if br := must[domain.Branch](t)(g.Branch(ctx, "", t2.Branch)); br.RebasedFrom != f.base.ID || br.ForkBaseline != b1.ID || br.Status != domain.BranchMerged {
		t.Fatalf("test2's branch: %+v", br)
	}
	if ch := must[domain.Change](t)(g.Change(ctx, t2.ID)); ch.BaselineID != b1.ID || ch.Status != domain.ChangeApplied || ch.ResultBaselineID != b2.ID {
		t.Fatalf("test2: %+v", ch)
	}
}

// Both change the same node, different properties: test2's draft is merged with the version test1 wrote, and the version
// test2 writes is parented to it.
func TestRebaseRootSameNodeLinear(t *testing.T) { forEachRepo(t, testRebaseRootSameNodeLinear) }

func testRebaseRootSameNodeLinear(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	t1, _ := propChange(t, g, f.base.ID, "test1", f.req.Ref(), map[string]any{"a": 1})
	t2, i2 := propChange(t, g, f.base.ID, "test2", f.req.Ref(), map[string]any{"b": 2})
	b1 := must[domain.Baseline](t)(g.Apply(ctx, t1.ID, ""))
	v1 := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID}))

	st := must[RebaseState](t)(g.RebaseState(ctx, t2.ID))
	if st.Branch != domain.MainBranch || st.Onto != b1.ID || !slices.Equal(st.Behind, []domain.ChangeImpactID{i2}) {
		t.Fatalf("test2 is behind main: %+v", st)
	}
	b2 := must[domain.Baseline](t)(g.Apply(ctx, t2.ID, ""))
	if b2.Kind != domain.BaselineFastForward || b2.ParentID != b1.ID || b2.MergedFrom != "" {
		t.Fatalf("T2 must fast-forward from T1: %+v", b2)
	}
	n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID}))
	if n.ChangeID != t2.ID || !slices.Equal(n.Parents, []domain.Version{v1.Version}) || n.Properties["a"] != float64(1) && n.Properties["a"] != 1 || n.Properties["b"] != float64(2) && n.Properties["b"] != 2 {
		t.Fatalf("test2's version = %+v, test1's = %+v", n, v1)
	}
	ev := rebasedEvents(t, g, t2.ID)
	if len(ev) != 1 || ev[0].Draft == nil || ev[0].Draft.Base == nil || *ev[0].Draft.Base != v1.Ref() {
		t.Fatalf("test2's log must hold the rebase of its draft onto %s: %+v", v1.Ref(), ev)
	}
	if r, _ := ev[0].Patch["rebased"].(map[string]any); r["branch"] != domain.MainBranch || r["baseline"] != string(b1.ID) {
		t.Fatalf("rebased patch: %+v", ev[0].Patch)
	}
	if n := len(rebaseMoves(t, g, t2.ID)); n != 1 {
		t.Fatalf("one move in the log, got %d", n)
	}
	// the review of the merged draft was kept, the version landed
	if cn := must[domain.ChangeImpact](t)(g.seenImpact(ctx, t2.ID, i2, "")); cn.Review != domain.ReviewAccepted || cn.Landed == nil || *cn.Landed != n.Ref() {
		t.Fatalf("test2's impact: %+v", cn)
	}
}

// A clean rebase keeps the acceptance of the impacts it merged (ADR 0101 §2).
func TestRebaseRootKeepsReviews(t *testing.T) { forEachRepo(t, testRebaseRootKeepsReviews) }

func testRebaseRootKeepsReviews(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	t1, _ := propChange(t, g, f.base.ID, "test1", f.req.Ref(), map[string]any{"a": 1})
	t2, i2 := propChange(t, g, f.base.ID, "test2", f.req.Ref(), map[string]any{"b": 2})
	b1 := must[domain.Baseline](t)(g.Apply(ctx, t1.ID, ""))

	res := must[RebaseResult](t)(g.RebaseChange(ctx, t2.ID))
	if res.Branch != domain.MainBranch || res.From != f.base.ID || res.Onto != b1.ID || res.Changed() || len(res.Impacts) != 1 || !res.Impacts[0].Merged {
		t.Fatalf("rebase = %+v", res)
	}
	if cn := must[domain.ChangeImpact](t)(g.seenImpact(ctx, t2.ID, i2, "")); cn.Review != domain.ReviewAccepted || cn.Pre == nil || cn.Pre.Version != 2 {
		t.Fatalf("the review is kept and the pre moved onto test1's version: %+v", cn)
	}
	if st := must[RebaseState](t)(g.RebaseState(ctx, t2.ID)); len(st.Behind) != 0 || len(st.Conflicts) != 0 || st.Onto != "" {
		t.Fatalf("not behind any more: %+v", st)
	}
	// a second rebase has nothing to do
	if again := must[RebaseResult](t)(g.RebaseChange(ctx, t2.ID)); len(again.Impacts) != 0 || again.Onto != b1.ID || again.From != b1.ID {
		t.Fatalf("again = %+v", again)
	}
	if n := len(rebaseMoves(t, g, t2.ID)); n != 1 {
		t.Fatalf("one move in the log, got %d", n)
	}
}

// The same property changed on both sides stops Apply, sends the impact back to proposed, and is settled in the change.
func TestRebaseRootConflict(t *testing.T) { forEachRepo(t, testRebaseRootConflict) }

func testRebaseRootConflict(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	t1, _ := propChange(t, g, f.base.ID, "test1", f.req.Ref(), map[string]any{"x": 1})
	t2, i2 := propChange(t, g, f.base.ID, "test2", f.req.Ref(), map[string]any{"x": 2})
	t3, i3 := propChange(t, g, f.base.ID, "test3", f.req.Ref(), map[string]any{"x": 3})
	must[domain.Baseline](t)(g.Apply(ctx, t1.ID, ""))

	_, err := g.Apply(ctx, t2.ID, "")
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "props.x") {
		t.Fatalf("apply must stop on the conflict: %v", err)
	}
	cn := must[domain.ChangeImpact](t)(g.seenImpact(ctx, t2.ID, i2, ""))
	if cn.Review != domain.ReviewProposed || cn.Reviews[len(cn.Reviews)-1].Comment != "rebased onto main" {
		t.Fatalf("the impact goes back to proposed: %+v", cn)
	}
	if ch := must[domain.Change](t)(g.Change(ctx, t2.ID)); ch.Status != domain.ChangeActive {
		t.Fatalf("test2 = %s", ch.Status)
	}
	st := must[RebaseState](t)(g.RebaseState(ctx, t2.ID))
	if len(st.Conflicts) != 1 || !slices.Equal(st.Conflicts[0].Conflicts, []string{"props.x"}) || len(st.Behind) != 0 {
		t.Fatalf("state = %+v", st)
	}
	if _, err := g.ImpactNodeReviewOn(ctx, t2.ID, "", "", i2, domain.ReviewAccepted, "u", "ok"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a conflict is settled before the review: %v", err)
	}
	// keeping its values settles it
	must[domain.ChangeImpact](t)(g.ImpactNodeResolve(ctx, t2.ID, i2, ""))
	must[domain.ChangeImpact](t)(g.accept(ctx, t2.ID, i2, "u", "ok"))
	b2 := must[domain.Baseline](t)(g.Apply(ctx, t2.ID, ""))
	if n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID})); n.Properties["x"] != float64(2) && n.Properties["x"] != 2 {
		t.Fatalf("test2 keeps its value: %+v", n.Properties)
	}
	// editing the field settles it as well; the second apply rebases onto test2
	if _, err := g.Apply(ctx, t3.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("test3: %v", err)
	}
	must[domain.ChangeImpact](t)(g.edit(ctx, t3.ID, i3, edit{Properties: map[string]any{"x": 4}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, t3.ID, i3, "u", "ok"))
	b3 := must[domain.Baseline](t)(g.Apply(ctx, t3.ID, ""))
	if b3.ParentID != b2.ID || b3.Kind != domain.BaselineFastForward {
		t.Fatalf("test3 lands on test2: %+v", b3)
	}
}

// A node both changes create collides on its key: the whole node is in conflict, settled by cancelling the creation.
func TestRebaseRootCreationCollision(t *testing.T) { forEachRepo(t, testRebaseRootCreationCollision) }

func testRebaseRootCreationCollision(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	create := func(title string) (domain.Change, domain.ChangeImpactID) {
		c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: title, BaselineID: f.base.ID, OwnBranch: true}))
		cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-9", Type: "Requirement", Rationale: title}))
		must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, cn.ID, edit{Properties: map[string]any{"title": title}}))
		must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, cn.ID, "u", "ok"))
		return c, cn.ID
	}
	t1, _ := create("test1")
	t2, i2 := create("test2")
	must[domain.Baseline](t)(g.Apply(ctx, t1.ID, ""))
	if _, err := g.Apply(ctx, t2.ID, ""); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "REQ-9 node") {
		t.Fatalf("the collision stops the apply: %v", err)
	}
	if st := must[RebaseState](t)(g.RebaseState(ctx, t2.ID)); len(st.Conflicts) != 1 || st.Conflicts[0].Conflicts[0] != domain.ConflictNode {
		t.Fatalf("state = %+v", st)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeCancel(ctx, t2.ID, i2, "", ""))
	if _, err := g.Apply(ctx, t2.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("test2 is left with nothing to commit (ADR 0102): %v", err)
	}
	if n := must[domain.Node](t)(g.NodeByKey(ctx, "", "REQ-9")); n.Properties["title"] != "test1" || n.ChangeID != t1.ID {
		t.Fatalf("REQ-9 stays test1's: %+v", n)
	}
}

// ADR 0102: a committed change stays open; when the target moved, its merge rebases its branch first (the nodes changed on
// both sides merged into drafts, committed when clean) and lands as a fast-forward.
func TestRebaseRootCommittedRebasedAtMerge(t *testing.T) {
	forEachRepo(t, testRebaseRootCommittedRebasedAtMerge)
}

func testRebaseRootCommittedRebasedAtMerge(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	t1, _ := propChange(t, g, f.base.ID, "test1", f.req.Ref(), map[string]any{"a": 1})
	t2, _ := propChange(t, g, f.base.ID, "test2", f.req.Ref(), map[string]any{"b": 2})
	c2 := must[domain.Baseline](t)(g.CommitChange(ctx, t2.ID, ""))
	if ch := must[domain.Change](t)(g.Change(ctx, t2.ID)); ch.Status != domain.ChangeActive || ch.ResultBaselineID != c2.ID {
		t.Fatalf("a committed change stays open: %+v", ch)
	}
	b1 := must[domain.Baseline](t)(g.Apply(ctx, t1.ID, ""))

	st := must[RebaseState](t)(g.RebaseState(ctx, t2.ID))
	if st.Onto != b1.ID || len(st.Behind) != 1 {
		t.Fatalf("test2 is behind test1 on REQ-1: %+v", st)
	}
	ch, err := g.IntegrateChange(ctx, t2.ID, nil)
	if err != nil || ch.Status != domain.ChangeApplied {
		t.Fatalf("merge: %+v %v", ch, err)
	}
	res := must[domain.Baseline](t)(g.Baseline(ctx, ch.ResultBaselineID))
	if res.Kind != domain.BaselineFastForward || res.ParentID != b1.ID {
		t.Fatalf("the merge of a rebased branch is a fast-forward: %+v", res)
	}
	n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: res.Nodes[f.req.ID]}))
	if n.Properties["a"] == nil || n.Properties["b"] == nil || len(n.Parents) != 2 {
		t.Fatalf("REQ-1 holds both changes, merged from both versions: %+v", n)
	}
	if rb := baselineOfChange(t, g, t2.ID, domain.BaselineRebase); rb.RebasedFrom != f.base.ID || rb.ParentID != c2.ID || rb.MergedFrom != b1.ID {
		t.Fatalf("the rebase of the branch: %+v", rb)
	}
}

// The target moves between the rebase and the integration: the fast-forward is impossible, the three-way merge runs and
// waits for the conflict to be resolved.
func TestRebaseRootRaceFallsBackToMerge(t *testing.T) {
	forEachRepo(t, testRebaseRootRaceFallsBackToMerge)
}

func testRebaseRootRaceFallsBackToMerge(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	t1, _ := propChange(t, g, f.base.ID, "test1", f.req.Ref(), map[string]any{"x": 1})
	t2, _ := propChange(t, g, f.base.ID, "test2", f.req.Ref(), map[string]any{"x": 2})
	t0, _ := propChange(t, g, f.base.ID, "test0", f.need.Ref(), map[string]any{"title": "N"})
	must[domain.Baseline](t)(g.Apply(ctx, t0.ID, ""))
	if err := g.rebaseFirst(ctx, t2.ID, false); err != nil { // the rebase of Apply onto test0...
		t.Fatal(err)
	}
	b1 := must[domain.Baseline](t)(g.Apply(ctx, t1.ID, "")) // ...then the target moves before test2 lands
	if err := g.repo.InTx(ctx, func(tx Tx) error {
		if _, err := g.commitTx(ctx, tx, t2.ID, "", nil, true); err != nil {
			return err
		}
		c, err := tx.Change(ctx, t2.ID)
		if err != nil {
			return err
		}
		c, err = g.integrateTx(ctx, tx, c, nil)
		if c.Status != domain.ChangeCommitted {
			t.Fatalf("a conflicting integration waits: %s", c.Status)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ch, err := g.IntegrateChange(ctx, t2.ID, map[domain.NodeID]Resolution{f.req.ID: {Props: map[string]any{"title": "Use PSP v1", "x": 3}}})
	if err != nil || ch.Status != domain.ChangeApplied {
		t.Fatalf("resolution: %+v %v", ch, err)
	}
	res := must[domain.Baseline](t)(g.Baseline(ctx, ch.ResultBaselineID))
	if res.Kind != domain.BaselineMerge || res.ParentID != b1.ID || res.MergedFrom == "" {
		t.Fatalf("the race lands by a merge: %+v", res)
	}
	if got := baselineOfChange(t, g, t2.ID, domain.BaselineCommit); got.RebasedFrom != f.base.ID {
		t.Fatalf("test2 was rebased onto test0 before: %+v", got)
	}
}
