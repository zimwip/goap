package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// A node is edited through its working version (ADR 0076): ImpactNodeUpdate and the link operations change it in place, no
// version is written; an accepted review freezes it (no explicit check-in, ADR 0077); changing it again is a new checkout.
func TestCheckoutEditAccept(t *testing.T) { forEachRepo(t, testCheckoutEditAccept) }

func testCheckoutEditAccept(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "PSP v2", BaselineID: f.base.ID}))
	if _, err := g.ImpactNodeUpdate(ctx, c.ID, "nope", NodeUpdate{Properties: map[string]any{"x": 1}}); err == nil {
		t.Fatal("an update names an impact of the change")
	}
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "new PSP"}))
	if cn.Intent != domain.IntentModified || cn.Pre == nil || *cn.Pre != f.req.Ref() || cn.Post == nil || cn.Post.Version != f.req.Version+1 {
		t.Fatalf("the checkout declares the impact and writes the working version: %+v", cn)
	}
	work := *cn.Post
	// the working version carries the links of the version it follows
	if out := must[[]domain.Link](t)(g.OutLinksOf(ctx, work)); len(out) != 1 || out[0].Type != "satisfies" {
		t.Fatalf("links carried: %+v", out)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "Use PSP v2"}}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"owner": "alice"}}))
	link := must[domain.Link](t)(g.ImpactLinkCreate(ctx, c.ID, cn.ID, LinkWrite{Type: "verifies", To: f.test.Ref()}, "", ""))
	must[domain.Link](t)(g.ImpactLinkUpdate(ctx, c.ID, link.ID, map[string]any{"why": "spec"}, "", ""))
	n := must[domain.Node](t)(g.Node(ctx, work))
	if latest := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID})); latest.Version != f.req.Version {
		t.Fatalf("nothing is on main before the change lands: v%d", latest.Version)
	}
	if !n.CheckedOut || n.Properties["title"] != "Use PSP v2" || n.Properties["owner"] != "alice" {
		t.Fatalf("edited in place: %+v", n)
	}
	vs := must[[]domain.Node](t)(g.Versions(ctx, f.req.ID))
	if len(vs) != int(work.Version) {
		t.Fatalf("one version for the checkout, whatever the edits: %d versions", len(vs))
	}
	out := must[[]domain.Link](t)(g.OutLinksOf(ctx, work))
	if len(out) != 2 {
		t.Fatalf("a link added in place: %+v", out)
	}
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, cn.ID, edit{}))
	if err := g.ImpactLinkDelete(ctx, c.ID, link.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if out := must[[]domain.Link](t)(g.OutLinksOf(ctx, work)); len(out) != 1 {
		t.Fatalf("a link removed in place: %+v", out)
	}
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a change with a review pending is not applied: %v", err)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "bob", "fine"))
	if n := must[domain.Node](t)(g.Node(ctx, work)); !n.CheckedOut || n.Comment != "fine" {
		t.Fatalf("accepted, still a working version until the change lands: %+v", n)
	}
	// a checkout of a working version is refused: the caller updates it, which sends the review back to proposed
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: cn.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("already checked out: %v", err)
	}
	again := must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "Use PSP v3"}}))
	if again.Post == nil || *again.Post != work || again.Review != domain.ReviewProposed {
		t.Fatalf("an edit after the accept is in place, and the review is back to proposed: %+v", again)
	}
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, cn.ID, "bob", "fine again"))
	if vs := must[[]domain.Node](t)(g.Versions(ctx, f.req.ID)); len(vs) != int(work.Version) {
		t.Fatalf("one version for the whole edit: %d versions", len(vs))
	}
	res := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	landed := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: res.Nodes[f.req.ID]}))
	if landed.Properties["title"] != "Use PSP v3" || landed.CheckedOut || landed.Version != work.Version {
		t.Fatalf("landing freezes the working version: %+v", landed)
	}
	// the guard keeps a frozen version as it is
	if err := g.repo.InTx(ctx, func(tx Tx) error {
		return tx.PutLink(ctx, domain.Link{ID: domain.LinkID(g.newID()), Type: "x", From: work, To: work, ChangeID: c.ID})
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a link added to a frozen version: %v", err)
	}
	if err := g.repo.InTx(ctx, func(tx Tx) error { return tx.SetNodeProps(ctx, work, map[string]any{"x": 1}) }); !errors.Is(err, ErrConflict) {
		t.Fatalf("properties set on a frozen version: %v", err)
	}
}

// Cancelling a checkout drops the working version (ADR 0076): a modified node goes back to the version the change saw,
// a creation cancelled before it is accepted removes the node and its impact, the one deletion of the graph.
func TestImpactNodeCancel(t *testing.T) { forEachRepo(t, testImpactNodeCancel) }

func testImpactNodeCancel(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "try", BaselineID: f.base.ID}))
	mod := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "try"}))
	created := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-9", Type: "Requirement", Rationale: "new",
		Links: []LinkWrite{{Type: "satisfies", To: f.need.Ref()}}}))
	// a link of the created node to the working version of REQ-1 holds it
	link := must[domain.Link](t)(g.ImpactLinkCreate(ctx, c.ID, created.ID, LinkWrite{Type: "refines", To: *mod.Post}, "", ""))
	if _, err := g.ImpactNodeCancel(ctx, c.ID, mod.ID, "", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a working version another version links to: %v", err)
	}
	if err := g.ImpactLinkDelete(ctx, c.ID, link.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	back := must[domain.ChangeImpact](t)(g.ImpactNodeCancel(ctx, c.ID, mod.ID, "", ""))
	if back.Post != nil {
		t.Fatalf("back to the impact without version: %+v", back)
	}
	if _, err := g.Node(ctx, *mod.Post); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the working version is dropped: %v", err)
	}
	if _, err := g.ImpactNodeCancel(ctx, c.ID, mod.ID, "", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("nothing checked out: %v", err)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeCancel(ctx, c.ID, created.ID, "", ""))
	if _, err := g.NodeByKey(ctx, domain.DefaultNamespace, "REQ-9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a cancelled creation leaves no node: %v", err)
	}
	impacts := must[[]domain.ChangeImpact](t)(g.ListChangeImpacts(ctx, c.ID))
	if len(impacts) != 1 || impacts[0].ID != mod.ID {
		t.Fatalf("nor its impact: %+v", impacts)
	}
	// checked out again, accepted: cancelling still drops the working version, back to the impact without version
	again := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: mod.ID}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, mod.ID, "bob", "ok"))
	if back := must[domain.ChangeImpact](t)(g.ImpactNodeCancel(ctx, c.ID, mod.ID, "", "")); back.Post != nil {
		t.Fatalf("back to the impact without version: %+v", back)
	}
	if _, err := g.Node(ctx, *again.Post); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the accepted working version is dropped: %v", err)
	}
}

// A flow is adopted with its versions frozen, accepted (ADR 0076, 0077).
func TestAdoptFlowNeedsAccept(t *testing.T) { forEachRepo(t, testAdoptFlowNeedsAccept) }

func testAdoptFlowNeedsAccept(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "options", BaselineID: f.base.ID, OwnBranch: true}))
	opt := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "A", Hypothesis: "a", By: "u"}))
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "A", Flow: opt.ID}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "A"}, Flow: opt.ID}))
	if _, err := g.AdoptFlow(ctx, c.ID, opt.ID, "u"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a flow with a checked-out version is adopted: %v", err)
	}
	must[domain.ChangeImpact](t)(g.acceptOn(ctx, c.ID, opt.ID, "", cn.ID, "u", "ok"))
	must[domain.Flow](t)(g.AdoptFlow(ctx, c.ID, opt.ID, "u"))
}

// A link a working version of the change holds to a node of the change follows the new version the change writes of
// it (ADR 0076 §4); the link of a frozen version stays where it is, suspect (ADR 0003).
func TestWorkingLinksFollowNewVersions(t *testing.T) {
	forEachRepo(t, testWorkingLinksFollowNewVersions)
}

func testWorkingLinksFollowNewVersions(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	g := w.g
	c := w.change(t, "follow")
	req2 := w.declare(t, c, w.req2)
	// a new Note links the baseline version of REQ-2; a transition of REQ-2 (a frozen version of its own) is a new
	// version the working link follows
	note := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "NOTE-1", Type: "Note", Rationale: "cover",
		Links: []LinkWrite{{Type: "refines", To: w.req2.Ref()}}}))
	moved := must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, req2, edit{State: "draft"}))
	out := must[[]domain.Link](t)(g.OutLinksOf(ctx, *note.Post))
	if len(out) != 1 || out[0].To != *moved.Post {
		t.Fatalf("the link of the working version follows REQ-2 to %s: %+v", moved.Post, out)
	}
}

// A creation writes its first version checked out (event created): checking it out again is refused.
func TestCreateIsCheckedOut(t *testing.T) { forEachRepo(t, testCreateIsCheckedOut) }

func testCreateIsCheckedOut(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "New", BaselineID: f.base.ID}))
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-NEW", Type: "alm@Requirement", Rationale: "new", Properties: map[string]any{"title": "x"}}))
	if cn.Post == nil || !must[domain.Node](t)(g.Node(ctx, *cn.Post)).CheckedOut {
		t.Fatalf("a created node starts checked out: %+v", cn)
	}
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: cn.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("checking out a checked-out node must fail with a conflict: %v", err)
	}
	evs := must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, c.ID))
	for _, e := range evs {
		if e.Op == domain.ImpactCheckedOut {
			t.Fatalf("a creation logs no checkout: %+v", e)
		}
	}
}
