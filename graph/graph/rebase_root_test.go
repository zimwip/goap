package graph

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A root change is rebased onto the head of the branch it was forked from before it commits, and commits as a fast-forward
// (ADR 0101).

// propChange opens a change from base that sets props on a node of the fixture, accepted.
func propChange(t *testing.T, g *Graph, base domain.CommitID, title string, node domain.NodeRef, props map[string]any) (domain.Change, domain.ChangeImpactID) {
	t.Helper()
	ctx := context.Background()
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: title, ForkCommit: base, OwnBranch: true}))
	ns := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &node, Rationale: title}}))
	if _, err := g.edit(ctx, c.ID, ns[0].ID, edit{Properties: props}); err != nil {
		t.Fatal(err)
	}
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, ns[0].ID, "u", "ok"))
	return c, ns[0].ID
}

// commitsOf lists the commits of a branch of the default namespace, in the order they were made.
func commitsOf(t *testing.T, g *Graph, branch string) []domain.Commit {
	t.Helper()
	var out []domain.Commit
	for _, b := range must[[]domain.Commit](t)(g.Commits(context.Background(), "")) {
		if domain.BranchOf(b.Branch) == branch {
			out = append(out, b)
		}
	}
	return out
}

func commitOfChange(t *testing.T, g *Graph, c domain.ChangeID, kind string) domain.Commit {
	t.Helper()
	for _, b := range must[[]domain.Commit](t)(g.Commits(context.Background(), "")) {
		if b.ChangeID == c && b.Kind == kind {
			return b
		}
	}
	t.Fatalf("no %s commit of change %s", kind, c)
	return domain.Commit{}
}

// rebaseMoves are the commitId moves recorded in the log of a change.
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
		if v, ok := h.Fields[HeaderForkCommit]; ok {
			out = append(out, v)
		}
	}
	return out
}

func rebasedEvents(t *testing.T, g *Graph, id domain.ChangeID) []domain.ImpactEvent {
	t.Helper()
	events := must[[]domain.ImpactEvent](t)(impactEventsOf(context.Background(), g, id))
	// the merge of a rebase is a checkout of the committed version, then an update with the difference (ADR 0144): the
	// update, with the draft of the checkout it follows
	var out []domain.ImpactEvent
	drafts := map[domain.ChangeImpactID]*domain.Draft{}
	for _, e := range events {
		switch {
		case e.Op == domain.ImpactCheckedOut:
			drafts[e.Impact] = e.Draft
		case e.Op == domain.ImpactUpdated && e.Patch["rebased"] != nil:
			e.Draft = drafts[e.Impact]
			out = append(out, e)
		}
	}
	return out
}

// test1 and test2 start from the same commit and change different nodes: test2 moves onto test1's result, main reads
// as two fast-forwards, the move is in test2's log and its branch keeps no rebase commit (ADR 0140).
func TestRebaseRootDifferentNodes(t *testing.T) { forEachRepo(t, testRebaseRootDifferentNodes) }

func testRebaseRootDifferentNodes(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	t1, _ := propChange(t, g, f.base.ID, "test1", f.req.Ref(), map[string]any{"title": "T1"})
	t2, _ := propChange(t, g, f.base.ID, "test2", f.need.Ref(), map[string]any{"title": "T2"})
	b1 := must[domain.Commit](t)(g.apply(ctx, t1.ID, ""))
	b2 := must[domain.Commit](t)(g.apply(ctx, t2.ID, ""))

	if b1.Kind != domain.CommitKindFastForward || b1.ParentID != f.base.ID || b2.Kind != domain.CommitKindFastForward || b2.ParentID != b1.ID {
		t.Fatalf("main must read T1 then T2: %+v %+v", b1, b2)
	}
	for _, b := range commitsOf(t, g, domain.MainBranch) {
		if b.MergedFrom != "" {
			t.Fatalf("a merge commit on main: %+v", b)
		}
	}
	if head := must[domain.Commit](t)(g.BranchHead(ctx, "", domain.MainBranch)); head.ID != b2.ID {
		t.Fatalf("head = %s, want %s", head.ID, b2.ID)
	}
	moves := rebaseMoves(t, g, t2.ID)
	if len(moves) != 1 || moves[0].From != string(f.base.ID) || moves[0].To != string(b1.ID) {
		t.Fatalf("test2's log must say it moved from B to T1: %+v", moves)
	}
	if got := rebaseMoves(t, g, t1.ID); len(got) != 0 {
		t.Fatalf("test1 was not behind: %+v", got)
	}
	// ADR 0102, 0140: test2 committed on its branch from B; no node changed on both sides, so the merge moved the fork onto
	// T1 and fast-forwarded over it: main gets one commit per change, its branch keeps its own (ADR 0175)
	for _, b := range must[[]domain.Commit](t)(g.Commits(ctx, "")) {
		if (b.ChangeID == t1.ID || b.ChangeID == t2.ID) && (b.Branch == domain.MainBranch) != (b.Kind == domain.CommitKindFastForward) {
			t.Fatalf("a change adds one fast-forward to main, its other commits on its branch: %+v", b)
		}
	}
	if br := must[domain.Branch](t)(g.Branch(ctx, "", t2.Branch)); br.RebasedFrom != f.base.ID || br.ForkCommit != b1.ID || br.Status != domain.BranchMerged {
		t.Fatalf("test2's branch: %+v", br)
	}
	if ch := must[domain.Change](t)(g.Change(ctx, t2.ID)); ch.ForkCommit != b1.ID || ch.Status != domain.ChangeApplied || ch.LandedCommit != b2.ID {
		t.Fatalf("test2: %+v", ch)
	}
}

// Both change the same node, different properties: test2's commit is merged with the version test1 wrote at its merge, and
// the version that commits is parented to both.
func TestRebaseRootSameNodeLinear(t *testing.T) { forEachRepo(t, testRebaseRootSameNodeLinear) }

func testRebaseRootSameNodeLinear(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	t1, _ := propChange(t, g, f.base.ID, "test1", f.req.Ref(), map[string]any{"a": 1})
	t2, i2 := propChange(t, g, f.base.ID, "test2", f.req.Ref(), map[string]any{"b": 2})
	// test2 commits on its branch before test1 lands (a draft is rebased at its commit, ADR 0165): what it committed is
	// rebased at its merge (ADR 0102)
	must[domain.Commit](t)(g.CommitChange(ctx, t2.ID, ""))
	if st := must[RebaseState](t)(g.RebaseState(ctx, t2.ID)); st.Onto != "" {
		t.Fatalf("a change over the head is not behind: %+v", st)
	}
	b1 := must[domain.Commit](t)(g.apply(ctx, t1.ID, ""))
	v1 := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID}))
	st := must[RebaseState](t)(g.RebaseState(ctx, t2.ID))
	if st.Branch != domain.MainBranch || st.Onto != b1.ID || !slices.Equal(st.Behind, []domain.ChangeImpactID{i2}) {
		t.Fatalf("test2 is behind main: %+v", st)
	}
	b2 := must[domain.Commit](t)(g.apply(ctx, t2.ID, ""))
	if b2.Kind != domain.CommitKindFastForward || b2.ParentID != b1.ID || b2.MergedFrom != "" {
		t.Fatalf("T2 must fast-forward from T1: %+v", b2)
	}
	// test2's commit, then the merge of it with test1's version: both its parents (ADR 0102)
	n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID}))
	if n.ChangeID != t2.ID || len(n.Parents) != 2 || n.Parents[1] != v1.Version || n.Reason != domain.ReasonRebase || n.Properties["a"] != float64(1) && n.Properties["a"] != 1 || n.Properties["b"] != float64(2) && n.Properties["b"] != 2 {
		t.Fatalf("test2's version = %+v, test1's = %+v", n, v1)
	}
	ev := rebasedEvents(t, g, t2.ID)
	if len(ev) != 1 || ev[0].Draft == nil || ev[0].Draft.MergedWith == nil || *ev[0].Draft.MergedWith != v1.Ref() {
		t.Fatalf("test2's log must hold the merge of its commit with %s: %+v", v1.Ref(), ev)
	}
	if r, _ := ev[0].Patch["rebased"].(map[string]any); r["branch"] != domain.MainBranch || r["commit"] != string(b1.ID) {
		t.Fatalf("rebased patch: %+v", ev[0].Patch)
	}
	if n := len(rebaseMoves(t, g, t2.ID)); n != 1 {
		t.Fatalf("one move in the log, got %d", n)
	}
	// the update carries the difference the merge makes: test1's property (ADR 0144)
	if p, _ := ev[0].Patch["props"].(map[string]any); len(p) != 1 || p["a"] == nil {
		t.Fatalf("the update carries the merge's difference: %+v", ev[0].Patch)
	}
	// the rebase in the log: the move, the node checked out and updated, the review of the merge, its commit
	var order []string
	for _, e := range must[[]domain.LogEntry](t)(logOf(g, t2.ID)) {
		switch e.Type {
		case "change.updated", "impact.checkedOut", "impact.updated", "fact.review", "impact.reviewed", "impact.committed":
			if len(order) == 0 || order[len(order)-1] != e.Type {
				order = append(order, e.Type)
			}
		}
	}
	if i := slices.Index(order, "change.updated"); i < 0 || !slices.Equal(order[i:i+5], []string{"change.updated", "impact.checkedOut", "impact.updated", "fact.review", "impact.reviewed"}) {
		t.Fatalf("the rebase in the log: %v", order)
	}
	// the review of the merged draft was kept, the version committed
	if cn := must[domain.ChangeImpact](t)(g.seenImpact(ctx, t2.ID, i2, "")); cn.Review != domain.ReviewAccepted || cn.Committed == nil || *cn.Committed != n.Ref() {
		t.Fatalf("test2's impact: %+v", cn)
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
	must[domain.Commit](t)(g.apply(ctx, t1.ID, ""))

	_, err := g.apply(ctx, t2.ID, "")
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
	b2 := must[domain.Commit](t)(g.apply(ctx, t2.ID, ""))
	if n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID})); n.Properties["x"] != float64(2) && n.Properties["x"] != 2 {
		t.Fatalf("test2 keeps its value: %+v", n.Properties)
	}
	// editing the field settles it as well; the second apply rebases onto test2
	if _, err := g.apply(ctx, t3.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("test3: %v", err)
	}
	must[domain.ChangeImpact](t)(g.edit(ctx, t3.ID, i3, edit{Properties: map[string]any{"x": 4}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, t3.ID, i3, "u", "ok"))
	b3 := must[domain.Commit](t)(g.apply(ctx, t3.ID, ""))
	if b3.ParentID != b2.ID || b3.Kind != domain.CommitKindFastForward {
		t.Fatalf("test3 commits on test2: %+v", b3)
	}
}

// A commit run in a transaction holding the change's lock from its start (the service's LockedWrites) keeps the rebase
// that left conflicts too: the transaction commits, then the commit is refused.
func TestRebaseRootConflictInChangeLock(t *testing.T) {
	forEachRepo(t, testRebaseRootConflictInChangeLock)
}

func testRebaseRootConflictInChangeLock(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	t1, _ := propChange(t, g, f.base.ID, "test1", f.req.Ref(), map[string]any{"x": 1})
	t2, i2 := propChange(t, g, f.base.ID, "test2", f.req.Ref(), map[string]any{"x": 2})
	must[domain.Commit](t)(g.apply(ctx, t1.ID, ""))

	err := g.InChangeLock(ctx, t2.ID, func(ctx context.Context) error {
		_, err := g.CommitChange(ctx, t2.ID, "")
		return err
	})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "props.x") {
		t.Fatalf("the commit must stop on the conflict: %v", err)
	}
	cn := must[domain.ChangeImpact](t)(g.seenImpact(ctx, t2.ID, i2, ""))
	if cn.Review != domain.ReviewProposed || cn.Reviews[len(cn.Reviews)-1].Comment != "rebased onto main" {
		t.Fatalf("the rebase is kept, the impact back to proposed: %+v", cn)
	}
	if st := must[RebaseState](t)(g.RebaseState(ctx, t2.ID)); len(st.Conflicts) != 1 {
		t.Fatalf("state = %+v", st)
	}
}

// A node both changes create collides on its key: the commit of the second refuses it, settled by withdrawing the creation.
func TestRebaseRootCreationCollision(t *testing.T) { forEachRepo(t, testRebaseRootCreationCollision) }

func testRebaseRootCreationCollision(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	create := func(title string) (domain.Change, domain.ChangeImpactID) {
		c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: title, ForkCommit: f.base.ID, OwnBranch: true}))
		cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-9", Type: "Requirement", Rationale: title}))
		must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, cn.ID, edit{Properties: map[string]any{"title": title}}))
		must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, cn.ID, "u", "ok"))
		return c, cn.ID
	}
	t1, _ := create("test1")
	t2, i2 := create("test2")
	must[domain.Commit](t)(g.apply(ctx, t1.ID, ""))
	// the commit of test2 writes its REQ-9: the key is test1's now
	if _, err := g.apply(ctx, t2.ID, ""); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "REQ-9 (Requirement): the key is taken") {
		t.Fatalf("the collision stops the apply: %v", err)
	}
	if err := g.WithdrawImpact(ctx, t2.ID, i2, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.apply(ctx, t2.ID, ""); !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("test2 is left with nothing to commit (ADR 0102): %v", err)
	}
	if n := must[domain.Node](t)(g.NodeByKey(ctx, "", "REQ-9")); n.Properties["title"] != "test1" || n.ChangeID != t1.ID {
		t.Fatalf("REQ-9 stays test1's: %+v", n)
	}
}

// ADR 0102: a committed change stays open; when the target moved, its merge rebases its branch first (the nodes changed on
// both sides merged into drafts, committed when clean) and commits as a fast-forward.
func TestRebaseRootCommittedRebasedAtMerge(t *testing.T) {
	forEachRepo(t, testRebaseRootCommittedRebasedAtMerge)
}

func testRebaseRootCommittedRebasedAtMerge(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	t1, _ := propChange(t, g, f.base.ID, "test1", f.req.Ref(), map[string]any{"a": 1})
	t2, _ := propChange(t, g, f.base.ID, "test2", f.req.Ref(), map[string]any{"b": 2})
	c2 := must[domain.Commit](t)(g.CommitChange(ctx, t2.ID, ""))
	if ch := must[domain.Change](t)(g.Change(ctx, t2.ID)); ch.Status != domain.ChangeActive || ch.LandedCommit != "" || c2.Branch != ch.Branch {
		t.Fatalf("a committed change stays open: %+v", ch)
	}
	b1 := must[domain.Commit](t)(g.apply(ctx, t1.ID, ""))

	st := must[RebaseState](t)(g.RebaseState(ctx, t2.ID))
	if st.Onto != b1.ID || len(st.Behind) != 1 {
		t.Fatalf("test2 is behind test1 on REQ-1: %+v", st)
	}
	ch, err := g.IntegrateChange(ctx, t2.ID, nil)
	if err != nil || ch.Status != domain.ChangeActive {
		t.Fatalf("merge: %+v %v", ch, err)
	}
	res := must[domain.Commit](t)(g.Commit(ctx, ch.LandedCommit))
	if res.Kind != domain.CommitKindFastForward || res.ParentID != b1.ID {
		t.Fatalf("the merge of a rebased branch is a fast-forward: %+v", res)
	}
	n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: res.Nodes[f.req.ID]}))
	if n.Properties["a"] == nil || n.Properties["b"] == nil || len(n.Parents) != 2 || n.Reason != domain.ReasonRebase {
		t.Fatalf("REQ-1 holds both changes, merged from both versions: %+v", n)
	}
	// the rebase moved test2's fork from B to T1, then its merge to what it merged (the change open, ADR 0166)
	if moves := rebaseMoves(t, g, t2.ID); len(moves) != 2 || moves[0].From != string(f.base.ID) || moves[0].To != string(b1.ID) ||
		moves[1].From != string(b1.ID) || moves[1].To != string(res.ID) {
		t.Fatalf("the moves of test2's fork: %+v", moves)
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
	must[domain.Commit](t)(g.apply(ctx, t0.ID, ""))
	must[domain.Commit](t)(g.CommitChange(ctx, t2.ID, ""))
	if _, err := g.RebaseChange(ctx, t2.ID); err != nil { // test2 brought up to date with test0...
		t.Fatal(err)
	}
	b1 := must[domain.Commit](t)(g.apply(ctx, t1.ID, "")) // ...then the target moves before test2 commits
	if err := g.repo.InTx(ctx, func(tx Tx) error {
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
	res := must[domain.Commit](t)(g.Commit(ctx, ch.LandedCommit))
	if res.Kind != domain.CommitKindMerge || res.ParentID != b1.ID {
		t.Fatalf("the race commits by a merge: %+v", res)
	}
	if moves := rebaseMoves(t, g, t2.ID); len(moves) == 0 || moves[0].From != string(f.base.ID) {
		t.Fatalf("test2 was rebased onto test0 before: %+v", moves)
	}
}

// logOf is the whole log of a change.
func logOf(g *Graph, id domain.ChangeID) ([]domain.LogEntry, error) {
	entries, _, err := g.ChangeLog(context.Background(), domain.LogFilter{Change: id})
	return entries, err
}

// A key created by a change that committed but never merged is taken while that change is open: a creation of it is
// refused at once. Once the change is abandoned its node never reached the graph, and a creation takes the node over.
func TestCreationTakesAbandonedKey(t *testing.T) { forEachRepo(t, testCreationTakesAbandonedKey) }

func testCreationTakesAbandonedKey(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	newChange := func(title string) domain.Change {
		return must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: title, ForkCommit: f.base.ID, OwnBranch: true}))
	}
	create := func(c domain.Change) (domain.ChangeImpact, error) {
		cn, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-9", Type: "Requirement", Rationale: c.Title})
		if err != nil {
			return cn, err
		}
		must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, cn.ID, edit{Properties: map[string]any{"title": c.Title}}))
		return g.accept(ctx, c.ID, cn.ID, "u", "ok")
	}
	t1 := newChange("test1")
	must[domain.ChangeImpact](t)(create(t1))
	must[domain.Commit](t)(g.CommitChange(ctx, t1.ID, ""))
	t2 := newChange("test2")
	if _, err := create(t2); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "the key is taken by a node change "+string(t1.ID)) {
		t.Fatalf("the key is test1's while it is open: %v", err)
	}
	abandoned := domain.ChangeAbandoned
	must[domain.Change](t)(g.UpdateChange(ctx, t1.ID, ChangePatch{Status: &abandoned}))
	cn := must[domain.ChangeImpact](t)(create(t2))
	must[domain.Commit](t)(g.apply(ctx, t2.ID, ""))
	n := must[domain.Node](t)(g.NodeByKey(ctx, "", "REQ-9"))
	if n.Properties["title"] != "test2" || n.ChangeID != t2.ID || n.ID != nodeOf(cn) || n.Version != 2 || n.Reason != domain.ReasonCreate || len(n.Parents) != 0 {
		t.Fatalf("test2 takes the node over with a creation: %+v", n)
	}
}
