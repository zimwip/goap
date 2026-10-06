package graph

import (
	"context"
	"errors"
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
	parent := must[domain.Change](t)(w.g.CreateChange(context.Background(), NewChange{Title: "parent", BaselineID: w.base.ID, OwnBranch: true}))
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

// The parent edits a node after its sub-change took it: the integration conflicts and waits for a resolution (ADR 0081 §5).
func TestSubChangeConflictsWithTheParent(t *testing.T) {
	forEachRepo(t, testSubChangeConflictsWithTheParent)
}

func testSubChangeConflictsWithTheParent(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newSubWorld(t, repo)
	g := w.g
	pcn := w.write(t, w.parent, "CMP-3", "parent v1")
	sub := w.sub(t, "sub")
	w.write(t, sub, "CMP-3", "sub")
	w.retitle(t, w.parent, pcn.ID, "parent v2") // the parent moves on

	if _, err := g.Apply(ctx, sub.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := must[domain.Change](t)(g.Change(ctx, sub.ID)); got.Status != domain.ChangeCommitted {
		t.Fatalf("a conflicting sub-change stays committed, got %s", got.Status)
	}
	if got := w.title(t, w.parent, "CMP-3"); got != "parent v2" {
		t.Fatalf("a conflict writes nothing in the parent: %q", got)
	}
	if _, err := g.IntegrateChange(ctx, sub.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("integrating without a resolution: %v", err)
	}
	if _, err := g.IntegrateChange(ctx, sub.ID, map[domain.NodeID]Resolution{w.cmp3.ID: {Skip: true}}); err != nil {
		t.Fatal(err)
	}
	if got := w.title(t, w.parent, "CMP-3"); got != "parent v2" {
		t.Fatalf("a skipped node keeps the parent's draft: %q", got)
	}
	ev := eventsOp(t, g, sub.ID, domain.ImpactIntegrated)
	if len(ev) != 1 || ev[0].Into.Change != w.parent.ID || ev[0].Into.Impact != "" {
		t.Fatalf("a skipped node is integrated with no install: %+v", ev)
	}

	// a sub-change that took the stored version conflicts with a parent that drafted the node meanwhile
	late := w.sub(t, "late")
	w.write(t, late, "CMP-2", "late")
	w.write(t, w.parent, "CMP-2", "parent")
	if _, err := g.Apply(ctx, late.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := must[domain.Change](t)(g.Change(ctx, late.ID)); got.Status != domain.ChangeCommitted {
		t.Fatalf("the late sub-change conflicts, got %s", got.Status)
	}
	if _, err := g.IntegrateChange(ctx, late.ID, map[domain.NodeID]Resolution{w.cmp2.ID: {Props: map[string]any{"title": "both"}}}); err != nil {
		t.Fatal(err)
	}
	if got := w.title(t, w.parent, "CMP-2"); got != "both" {
		t.Fatalf("a resolution installs its properties: %q", got)
	}
	if _, err := g.Apply(ctx, w.parent.ID, ""); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"CMP-2": "both", "CMP-3": "parent v2"} {
		if n := must[domain.Node](t)(g.NodeByKey(ctx, "", key)); n.Properties["title"] != want {
			t.Fatalf("%s on main = %v", key, n.Properties)
		}
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

// Two sub-changes copy the same draft of their parent: the first to integrate changes it, the second conflicts.
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
	w.write(t, b, "CMP-3", "b")
	if _, err := g.Apply(ctx, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, b.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := must[domain.Change](t)(g.Change(ctx, b.ID)); got.Status != domain.ChangeCommitted {
		t.Fatalf("the second sibling conflicts, got %s", got.Status)
	}
	if got := w.title(t, w.parent, "CMP-3"); got != "a" {
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
	sub := w.sub(t, "sub")
	child := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, sub.ID, NodeCreate{Key: "NEW-2", Type: "Component", Rationale: "child",
		Links: []LinkWrite{{Type: "uses", To: *made.Post}}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, sub.ID, child.ID, "reviewer", "ok"))
	allow := false
	var seen domain.Blackboard
	g.LandingGate = func(_ context.Context, _ domain.Change, bb domain.Blackboard) (bool, bool, error) {
		seen = bb
		return true, allow, nil
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
