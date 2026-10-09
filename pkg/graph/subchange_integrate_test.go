package graph

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// subWorld is a parent change with its own branch over the org world, and helpers to write in a change.
type subWorld struct {
	orgWorld
	parent domain.Change
}

func newSubWorld(t *testing.T, repo Repo) subWorld {
	t.Helper()
	w := newOrgWorld(t, repo)
	parent := must[domain.Change](t)(w.g.CreateChange(context.Background(), NewChange{ProjectID: "PROJ-ROOT", Title: "parent", BaselineID: w.base.ID, OwnBranch: true}))
	return subWorld{orgWorld: w, parent: parent}
}

func (w subWorld) sub(t *testing.T, title string) domain.Change {
	t.Helper()
	return must[domain.Change](t)(w.g.CreateChange(context.Background(), NewChange{Title: title, ParentID: w.parent.ID}))
}

// write checks a node out in a change by key (declaring its impact), sets its title and accepts it.
func (w subWorld) write(t *testing.T, c domain.Change, key, title string) domain.ChangeImpact {
	t.Helper()
	ctx := context.Background()
	cn := must[domain.ChangeImpact](t)(w.g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Key: key, Rationale: title}))
	return w.retitle(t, c, cn.ID, title)
}

// retitle edits the title of a node already checked out in a change and accepts it.
func (w subWorld) retitle(t *testing.T, c domain.Change, impact domain.ChangeImpactID, title string) domain.ChangeImpact {
	t.Helper()
	ctx := context.Background()
	if _, err := w.g.ImpactNodeUpdate(ctx, c.ID, impact, NodeUpdate{Properties: map[string]any{"title": title}}); err != nil {
		t.Fatal(err)
	}
	return must[domain.ChangeImpact](t)(w.g.accept(ctx, c.ID, impact, "reviewer", "ok "+title))
}

// title reads a node as a change sees it.
func (w subWorld) title(t *testing.T, c domain.Change, key string) string {
	t.Helper()
	v, err := w.g.ChangeNodeViewByKey(context.Background(), c.ID, "", domain.DefaultNamespace, key)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := v.Properties["title"].(string)
	return s
}

func eventsOp(t *testing.T, g *Graph, c domain.ChangeID, op domain.ImpactOp) []domain.ImpactEvent {
	t.Helper()
	var out []domain.ImpactEvent
	for _, e := range must[[]domain.ImpactEvent](t)(impactEventsOf(context.Background(), g, c)) {
		if e.Op == op {
			out = append(out, e)
		}
	}
	return out
}

// A parent and its sub-change edit one node: the sub-change starts from the parent's draft, its edit is integrated into
// the parent's log, and the parent lands one version (ADR 0081). This lost the parent's edit when the sub-change merged
// versions into the parent's branch.
func TestSubChangeBuildsOnTheParentDraft(t *testing.T) {
	forEachRepo(t, testSubChangeBuildsOnTheParentDraft)
}

func testSubChangeBuildsOnTheParentDraft(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newSubWorld(t, repo)
	g := w.g
	w.write(t, w.parent, "CMP-3", "from parent")
	sub := w.sub(t, "sub")
	if got := w.title(t, sub, "CMP-3"); got != "from parent" {
		t.Fatalf("the sub-change sees %q, not the parent's draft", got)
	}
	if v := must[domain.Baseline](t)(g.ChangeView(ctx, sub.ID, "", ViewWritten)); v.Nodes[w.cmp3.ID] != 0 {
		t.Fatalf("the view of the sub-change holds the parent's draft (version 0), got %d", v.Nodes[w.cmp3.ID])
	}
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, sub.ID, NodeCheckout{Key: "CMP-3", Rationale: "refine"}))
	rows := must[[]domain.Draft](t)(draftsOf(ctx, g, sub.ID))
	if len(rows) != 1 || rows[0].Inherited == nil || rows[0].Inherited.Change != w.parent.ID || rows[0].Properties["title"] != "from parent" {
		t.Fatalf("the checkout copies the parent's draft and remembers it: %+v", rows)
	}
	w.retitle(t, sub, cn.ID, "from parent, then sub")
	if _, err := g.Apply(ctx, sub.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := must[domain.Change](t)(g.Change(ctx, sub.ID)); got.Status != domain.ChangeApplied || got.ResultBaselineID != "" {
		t.Fatalf("sub-change = %s, result %q", got.Status, got.ResultBaselineID)
	}
	if got := w.title(t, w.parent, "CMP-3"); got != "from parent, then sub" {
		t.Fatalf("the parent holds %q", got)
	}
	if n := must[domain.Node](t)(g.NodeByKeyOn(ctx, "", w.parent.Branch, "CMP-3")); n.Version != w.cmp3.Version {
		t.Fatalf("the sub-change wrote a version on the parent branch: v%d", n.Version)
	}
	pc := must[domain.Change](t)(g.Change(ctx, w.parent.ID))
	if len(pc.Nodes) != 1 || pc.Nodes[0].Review != domain.ReviewAccepted {
		t.Fatalf("the parent keeps one impact of the node, accepted: %+v", pc.Nodes)
	}
	if r := pc.Nodes[0].Reviews[len(pc.Nodes[0].Reviews)-1]; r.By != "reviewer" {
		t.Fatalf("the integration carries the sub-change's review: %+v", r)
	}
	if _, err := g.Apply(ctx, w.parent.ID, ""); err != nil {
		t.Fatal(err)
	}
	n := must[domain.Node](t)(g.NodeByKey(ctx, "", "CMP-3"))
	if n.Properties["title"] != "from parent, then sub" || n.Version != w.cmp3.Version+1 || n.ChangeID != w.parent.ID {
		t.Fatalf("main holds one version written by the parent: %+v", n)
	}
}

// The parent edits the property its sub-change edits after the sub-change took the node: Apply rebases the sub-change,
// names the conflict and stops; the sub-change settles it, reviews it again and Apply fast-forwards (ADR 0082).
func TestSubChangeConflictsWithTheParent(t *testing.T) {
	forEachRepo(t, testSubChangeConflictsWithTheParent)
}

func testSubChangeConflictsWithTheParent(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newSubWorld(t, repo)
	g := w.g
	pcn := w.write(t, w.parent, "CMP-3", "parent v1")
	sub := w.sub(t, "sub")
	scn := w.write(t, sub, "CMP-3", "sub")
	w.retitle(t, w.parent, pcn.ID, "parent v2") // the parent moves on

	if _, err := g.IntegrateChange(ctx, sub.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("a sub-change behind its parent does not integrate: %v", err)
	}
	if _, err := g.Apply(ctx, sub.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("apply rebases and stops on the conflict: %v", err)
	}
	if got := w.title(t, w.parent, "CMP-3"); got != "parent v2" {
		t.Fatalf("a conflict writes nothing in the parent: %q", got)
	}
	got := must[domain.Change](t)(g.Change(ctx, sub.ID))
	if got.Status == domain.ChangeApplied || got.Nodes[0].Review != domain.ReviewProposed {
		t.Fatalf("the rebased impact is to be reviewed again: %s, %s", got.Status, got.Nodes[0].Review)
	}
	if _, err := g.accept(ctx, sub.ID, scn.ID, "reviewer", "ok"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a conflict not settled cannot be accepted: %v", err)
	}
	ev := eventsOp(t, g, sub.ID, domain.ImpactTransitioned)
	if last := ev[len(ev)-1]; last.Patch["rebased"] == nil || last.Draft == nil || last.Draft.Inherited.Seq == 0 {
		t.Fatalf("the rebase installs the merged draft with the parent's position: %+v", last)
	}
	// the sub-change settles the conflict by editing the property, reviews it again, and Apply integrates
	w.retitle(t, sub, scn.ID, "parent v2 and sub")
	if _, err := g.Apply(ctx, sub.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := w.title(t, w.parent, "CMP-3"); got != "parent v2 and sub" {
		t.Fatalf("parent = %q", got)
	}

	// keeping the sub-change's value as it is: ImpactNodeResolve
	keep := w.sub(t, "keep")
	kcn := w.write(t, keep, "CMP-3", "keep")
	pc := must[domain.Change](t)(g.Change(ctx, w.parent.ID))
	w.retitle(t, w.parent, pc.Nodes[0].ID, "parent v3")
	if _, err := g.Apply(ctx, keep.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("apply: %v", err)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeResolve(ctx, keep.ID, kcn.ID, ""))
	must[domain.ChangeImpact](t)(g.accept(ctx, keep.ID, kcn.ID, "reviewer", "ours"))
	if _, err := g.Apply(ctx, keep.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := w.title(t, w.parent, "CMP-3"); got != "keep" {
		t.Fatalf("parent = %q", got)
	}

	// taking the parent's: the sub-change cancels its draft, its impact is a confirmation, nothing goes into the parent
	late := w.sub(t, "late")
	lcn := w.write(t, late, "CMP-2", "late")
	w.write(t, w.parent, "CMP-2", "parent")
	if _, err := g.Apply(ctx, late.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("the late sub-change took the stored version the parent then drafted: %v", err)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeCancel(ctx, late.ID, lcn.ID, "", ""))
	must[domain.ChangeImpact](t)(g.accept(ctx, late.ID, lcn.ID, "reviewer", "take the parent's"))
	if _, err := g.Apply(ctx, late.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := w.title(t, w.parent, "CMP-2"); got != "parent" {
		t.Fatalf("parent CMP-2 = %q", got)
	}
	if _, err := g.Apply(ctx, w.parent.ID, ""); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"CMP-2": "parent", "CMP-3": "keep"} {
		if n := must[domain.Node](t)(g.NodeByKey(ctx, "", key)); n.Properties["title"] != want {
			t.Fatalf("%s on main = %v", key, n.Properties)
		}
	}
}

// The parent and its sub-change change different properties of a node: the rebase merges them without a conflict, the
// review goes back to proposed, and the parent receives both (ADR 0082 §1).
func TestSubChangeRebaseMerges(t *testing.T) { forEachRepo(t, testSubChangeRebaseMerges) }

func testSubChangeRebaseMerges(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newSubWorld(t, repo)
	g := w.g
	pcn := w.write(t, w.parent, "CMP-3", "p")
	sub := w.sub(t, "sub")
	scn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, sub.ID, NodeCheckout{Key: "CMP-3", Rationale: "note"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, sub.ID, scn.ID, NodeUpdate{Properties: map[string]any{"note": "from sub"}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, sub.ID, scn.ID, "reviewer", "ok"))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, w.parent.ID, pcn.ID, NodeUpdate{Properties: map[string]any{"size": "L"}}))

	res := must[RebaseResult](t)(g.RebaseChange(ctx, sub.ID))
	if len(res.Impacts) != 1 || !res.Impacts[0].Changed || len(res.Impacts[0].Conflicts) != 0 {
		t.Fatalf("rebase = %+v", res)
	}
	if again := must[RebaseResult](t)(g.RebaseChange(ctx, sub.ID)); len(again.Impacts) != 0 {
		t.Fatalf("a rebased sub-change is up to date: %+v", again)
	}
	v := must[domain.NodeView](t)(g.ChangeNodeViewByKey(ctx, sub.ID, "", "", "CMP-3"))
	if v.Properties["title"] != "p" || v.Properties["note"] != "from sub" || v.Properties["size"] != "L" {
		t.Fatalf("merged draft = %v", v.Properties)
	}
	if _, err := g.Apply(ctx, sub.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("the merged impact awaits its review again: %v", err)
	}
	must[domain.ChangeImpact](t)(g.accept(ctx, sub.ID, scn.ID, "reviewer", "merged"))
	if _, err := g.Apply(ctx, sub.ID, ""); err != nil {
		t.Fatal(err)
	}
	pv := must[domain.NodeView](t)(g.ChangeNodeViewByKey(ctx, w.parent.ID, "", "", "CMP-3"))
	if pv.Properties["title"] != "p" || pv.Properties["note"] != "from sub" || pv.Properties["size"] != "L" {
		t.Fatalf("parent = %v", pv.Properties)
	}
}

// The parent rejects the impact its sub-change works on: a conflict on the whole node, and the integration refuses
// until the sub-change withdraws it.
func TestSubChangeRejectedByTheParent(t *testing.T) { forEachRepo(t, testSubChangeRejectedByTheParent) }

func testSubChangeRejectedByTheParent(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newSubWorld(t, repo)
	g := w.g
	pcn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, w.parent.ID, NodeCheckout{Key: "CMP-3", Rationale: "maybe"}))
	sub := w.sub(t, "sub")
	scn := w.write(t, sub, "CMP-3", "sub")
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, w.parent.ID, pcn.ID, domain.ReviewRejected, "parent", "no"))
	res := must[RebaseResult](t)(g.RebaseChange(ctx, sub.ID))
	if len(res.Impacts) != 1 || len(res.Impacts[0].Conflicts) != 1 || res.Impacts[0].Conflicts[0] != domain.ConflictNode {
		t.Fatalf("rebase = %+v", res)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeResolve(ctx, sub.ID, scn.ID, ""))
	must[domain.ChangeImpact](t)(g.accept(ctx, sub.ID, scn.ID, "reviewer", "ours"))
	if _, err := g.IntegrateChange(ctx, sub.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("a node the parent rejected does not integrate: %v", err)
	}
	if err := g.WithdrawImpact(ctx, sub.ID, scn.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, sub.ID, ""); err != nil {
		t.Fatal(err)
	}
}

// A node the parent creates is taken by the sub-change as a creation of its own, from the parent's draft; a node of the
// sub-change links to it; the parent lands both, the link resolved to the version it writes (ADR 0081 §2, §4).
func TestSubChangeTakesWhatTheParentCreates(t *testing.T) {
	forEachRepo(t, testSubChangeTakesWhatTheParentCreates)
}

func testSubChangeTakesWhatTheParentCreates(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newSubWorld(t, repo)
	g := w.g
	made := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, w.parent.ID, NodeCreate{Key: "NEW-1", Type: "Component", Rationale: "new",
		Properties: map[string]any{"title": "new"}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, w.parent.ID, made.ID, "reviewer", "ok"))
	sub := w.sub(t, "sub")
	if got := w.title(t, sub, "NEW-1"); got != "new" {
		t.Fatalf("the sub-change sees the node its parent creates: %q", got)
	}
	if _, err := g.ImpactNodeCreate(ctx, sub.ID, NodeCreate{Key: "NEW-1", Type: "Component", Rationale: "again"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("creating a key the parent creates: %v", err)
	}
	taken := w.write(t, sub, "NEW-1", "new, refined")
	if taken.Intent != domain.IntentCreated || taken.Post == nil || taken.Post.ID != made.Post.ID {
		t.Fatalf("the sub-change takes the parent's creation (same node): %+v", taken)
	}
	child := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, sub.ID, NodeCreate{Key: "NEW-2", Type: "Component", Rationale: "child",
		Properties: map[string]any{"title": "child"}, Links: []LinkWrite{{Type: "uses", To: *made.Post}}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, sub.ID, child.ID, "reviewer", "ok"))
	if _, err := g.Apply(ctx, sub.ID, ""); err != nil {
		t.Fatal(err)
	}
	pc := must[domain.Change](t)(g.Change(ctx, w.parent.ID))
	if len(pc.Nodes) != 2 {
		t.Fatalf("the parent holds its creation and the sub-change's: %+v", pc.Nodes)
	}
	if got := w.title(t, w.parent, "NEW-1"); got != "new, refined" {
		t.Fatalf("parent NEW-1 = %q", got)
	}
	if _, err := g.Apply(ctx, w.parent.ID, ""); err != nil {
		t.Fatal(err)
	}
	n1 := must[domain.Node](t)(g.NodeByKey(ctx, "", "NEW-1"))
	n2 := must[domain.Node](t)(g.NodeByKey(ctx, "", "NEW-2"))
	if n1.Version != 1 || n1.Properties["title"] != "new, refined" || n2.Version != 1 {
		t.Fatalf("one version each: %+v, %+v", n1, n2)
	}
	links := must[[]domain.Link](t)(g.OutLinksOf(ctx, n2.Ref()))
	if len(links) != 1 || links[0].To != n1.Ref() {
		t.Fatalf("the link of the sub-change's node goes to the version the parent wrote: %+v", links)
	}
}

func draftsOf(ctx context.Context, g *Graph, id domain.ChangeID) (out []domain.Draft, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { out, err = g.drafts(ctx, tx, id); return err })
	return out, err
}

// Two sub-changes copy the same draft of their parent: the first to integrate changes it, the second is rebased onto it
// and settles the conflict.
func TestSiblingSubChangesOnOneParentDraft(t *testing.T) {
	forEachRepo(t, testSiblingSubChangesOnOneParentDraft)
}

func testSiblingSubChangesOnOneParentDraft(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newSubWorld(t, repo)
	g := w.g
	w.write(t, w.parent, "CMP-3", "parent")
	a, b := w.sub(t, "a"), w.sub(t, "b")
	w.write(t, a, "CMP-3", "a")
	bcn := w.write(t, b, "CMP-3", "b")
	if _, err := g.Apply(ctx, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, b.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("the second sibling is rebased onto the first: %v", err)
	}
	if got := w.title(t, w.parent, "CMP-3"); got != "a" {
		t.Fatalf("parent = %q", got)
	}
	w.retitle(t, b, bcn.ID, "a then b")
	if _, err := g.Apply(ctx, b.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := w.title(t, w.parent, "CMP-3"); got != "a then b" {
		t.Fatalf("parent = %q", got)
	}
}

// The landing gate decides a sub-change on its drafts, without a version written: a draft linking to a node its parent
// creates is no obstacle (ADR 0081 §3).
func TestSubChangeLandingGate(t *testing.T) { forEachRepo(t, testSubChangeLandingGate) }

func testSubChangeLandingGate(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newSubWorld(t, repo)
	g := w.g
	made := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, w.parent.ID, NodeCreate{Key: "NEW-1", Type: "Component", Rationale: "new"}))
	gd := &testGuardian{}
	g.Guardians = map[string]Guardian{"test": gd}
	sub := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "sub", ParentID: w.parent.ID, Guardian: "test"}))
	child := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, sub.ID, NodeCreate{Key: "NEW-2", Type: "Component", Rationale: "child",
		Links: []LinkWrite{{Type: "uses", To: *made.Post}}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, sub.ID, child.ID, "reviewer", "ok"))
	allow := false
	var seen domain.Blackboard
	gd.commit = func(_ context.Context, _ domain.Change, bb domain.Blackboard) error {
		seen = bb
		if !allow {
			return fmt.Errorf("refused: %w", ErrInvalid)
		}
		return nil
	}
	if _, err := g.Apply(ctx, sub.ID, ""); err == nil {
		t.Fatal("a gate that refuses keeps the sub-change from committing")
	}
	if v, ok := seen.Nodes[*child.Post]; !ok || v.Key != "NEW-2" || len(v.Out) != 1 || v.Out[0].To != *made.Post {
		t.Fatalf("the gate sees the draft with its link to the parent's draft: %+v", seen.Nodes)
	}
	allow = true
	if _, err := g.Apply(ctx, sub.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := must[domain.Change](t)(g.Change(ctx, sub.ID)); got.Status != domain.ChangeApplied {
		t.Fatalf("sub-change = %s", got.Status)
	}
}

// The parent cancels the draft its sub-change copied: what the parent sees again is the stored version, so the rebase takes
// back the parent's fields the sub-change did not change (ADR 0082 §1).
func TestSubChangeRebaseOnACancelledParentDraft(t *testing.T) {
	forEachRepo(t, testSubChangeRebaseOnACancelledParentDraft)
}

func testSubChangeRebaseOnACancelledParentDraft(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newSubWorld(t, repo)
	g := w.g
	pcn := w.write(t, w.parent, "CMP-3", "parent title")
	sub := w.sub(t, "sub")
	scn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, sub.ID, NodeCheckout{Key: "CMP-3", Rationale: "note"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, sub.ID, scn.ID, NodeUpdate{Properties: map[string]any{"note": "n"}}))
	must[domain.ChangeImpact](t)(g.ImpactNodeCancel(ctx, w.parent.ID, pcn.ID, "", ""))
	must[RebaseResult](t)(g.RebaseChange(ctx, sub.ID))
	v := must[domain.NodeView](t)(g.ChangeNodeViewByKey(ctx, sub.ID, "", "", "CMP-3"))
	if v.Properties["title"] != "three" || v.Properties["note"] != "n" {
		t.Fatalf("the parent's cancelled title goes, the sub-change's note stays: %v", v.Properties)
	}
}
