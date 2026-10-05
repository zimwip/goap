package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// A node is edited through its working version (ADR 0076): UpdateNode and the link operations change it in place, no
// version is written; an accepted review authorizes the check-in, which freezes it; changing it again is a new checkout.
func TestCheckoutEditCheckin(t *testing.T) { forEachRepo(t, testCheckoutEditCheckin) }

func testCheckoutEditCheckin(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "PSP v2", BaselineID: f.base.ID}))
	if _, err := g.UpdateNode(ctx, c.ID, "nope", NodeUpdate{Properties: map[string]any{"x": 1}}); err == nil {
		t.Fatal("an update names an impact of the change")
	}
	cn := must[domain.ChangeImpact](t)(g.CheckoutNode(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "new PSP"}))
	if cn.Intent != domain.IntentModified || cn.Pre == nil || *cn.Pre != f.req.Ref() || cn.Post == nil || cn.Post.Version != f.req.Version+1 {
		t.Fatalf("the checkout declares the impact and writes the working version: %+v", cn)
	}
	work := *cn.Post
	// the working version carries the links of the version it follows
	if out := must[[]domain.Link](t)(g.OutLinksOf(ctx, work)); len(out) != 1 || out[0].Type != "satisfies" {
		t.Fatalf("links carried: %+v", out)
	}
	must[domain.ChangeImpact](t)(g.UpdateNode(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "Use PSP v2"}}))
	must[domain.ChangeImpact](t)(g.UpdateNode(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"owner": "alice"}}))
	link := must[domain.Link](t)(g.CreateLink(ctx, c.ID, cn.ID, LinkWrite{Type: "verifies", To: f.test.Ref()}, "", ""))
	must[domain.Link](t)(g.UpdateLink(ctx, c.ID, link.ID, map[string]any{"why": "spec"}, "", ""))
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
	if err := g.DeleteLink(ctx, c.ID, link.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if out := must[[]domain.Link](t)(g.OutLinksOf(ctx, work)); len(out) != 1 {
		t.Fatalf("a link removed in place: %+v", out)
	}
	// a transition starts from a checked-in version; the check-in needs an accepted review
	if _, err := g.TransitionNode(ctx, c.ID, NodeTransition{NodeCheckout: NodeCheckout{Impact: cn.ID}, To: "x"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a transition of a checked-out node: %v", err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a change with a review pending is not applied: %v", err)
	}
	if _, err := g.CheckinNode(ctx, c.ID, cn.ID, "", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a check-in before the review: %v", err)
	}
	must[domain.ChangeImpact](t)(g.ReviewNode(ctx, c.ID, cn.ID, domain.ReviewAccepted, "bob", "fine"))
	if _, err := g.UpdateNode(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "late"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("an accepted version is checked in, not edited: %v", err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a checked-out version is not applied: %v", err)
	}
	must[domain.ChangeImpact](t)(g.CheckinNode(ctx, c.ID, cn.ID, "", ""))
	if n := must[domain.Node](t)(g.Node(ctx, work)); n.CheckedOut || n.Comment != "fine" {
		t.Fatalf("checked in: %+v", n)
	}
	// the guard keeps a checked-in version as it is
	if err := g.repo.InTx(ctx, func(tx Tx) error {
		return tx.PutLink(ctx, domain.Link{ID: domain.LinkID(g.newID()), Type: "x", From: work, To: work, ChangeID: c.ID})
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a link added to a checked-in version: %v", err)
	}
	if err := g.repo.InTx(ctx, func(tx Tx) error { return tx.SetNodeProps(ctx, work, map[string]any{"x": 1}) }); !errors.Is(err, ErrConflict) {
		t.Fatalf("properties set on a checked-in version: %v", err)
	}
	// changing it again is a new checkout, reviewed again
	again := must[domain.ChangeImpact](t)(g.CheckoutNode(ctx, c.ID, NodeCheckout{Impact: cn.ID}))
	if again.Post.Version != work.Version+1 || again.Review != domain.ReviewProposed {
		t.Fatalf("a new checkout: %+v", again)
	}
	must[domain.ChangeImpact](t)(g.UpdateNode(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "Use PSP v3"}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, cn.ID, "bob", "fine again"))
	res := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	if landed := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: res.Nodes[f.req.ID]})); landed.Properties["title"] != "Use PSP v3" {
		t.Fatalf("landed: %+v", landed)
	}
}

// Cancelling a checkout drops the working version (ADR 0076): a modified node goes back to the version the change saw,
// a creation cancelled before its first check-in removes the node and its impact, the one deletion of the graph.
func TestCancelCheckout(t *testing.T) { forEachRepo(t, testCancelCheckout) }

func testCancelCheckout(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "try", BaselineID: f.base.ID}))
	mod := must[domain.ChangeImpact](t)(g.CheckoutNode(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "try"}))
	created := must[domain.ChangeImpact](t)(g.CreateNode(ctx, c.ID, NodeCreate{Key: "REQ-9", Type: "Requirement", Rationale: "new",
		Links: []LinkWrite{{Type: "satisfies", To: f.need.Ref()}}}))
	// a link of the created node to the working version of REQ-1 holds it
	link := must[domain.Link](t)(g.CreateLink(ctx, c.ID, created.ID, LinkWrite{Type: "refines", To: *mod.Post}, "", ""))
	if _, err := g.CancelCheckout(ctx, c.ID, mod.ID, "", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a working version another version links to: %v", err)
	}
	if err := g.DeleteLink(ctx, c.ID, link.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	back := must[domain.ChangeImpact](t)(g.CancelCheckout(ctx, c.ID, mod.ID, "", ""))
	if back.Post != nil {
		t.Fatalf("back to the impact without version: %+v", back)
	}
	if _, err := g.Node(ctx, *mod.Post); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the working version is dropped: %v", err)
	}
	if _, err := g.CancelCheckout(ctx, c.ID, mod.ID, "", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("nothing checked out: %v", err)
	}
	must[domain.ChangeImpact](t)(g.CancelCheckout(ctx, c.ID, created.ID, "", ""))
	if _, err := g.NodeByKey(ctx, domain.DefaultNamespace, "REQ-9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a cancelled creation leaves no node: %v", err)
	}
	impacts := must[[]domain.ChangeImpact](t)(g.ListChangeImpacts(ctx, c.ID))
	if len(impacts) != 1 || impacts[0].ID != mod.ID {
		t.Fatalf("nor its impact: %+v", impacts)
	}
	// checked out again, then checked in: a later checkout cancelled goes back to the checked-in version
	again := must[domain.ChangeImpact](t)(g.CheckoutNode(ctx, c.ID, NodeCheckout{Impact: mod.ID}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, mod.ID, "bob", "ok"))
	must[domain.ChangeImpact](t)(g.CheckoutNode(ctx, c.ID, NodeCheckout{Impact: mod.ID}))
	if back := must[domain.ChangeImpact](t)(g.CancelCheckout(ctx, c.ID, mod.ID, "", "")); back.Post == nil || *back.Post != *again.Post {
		t.Fatalf("back to the checked-in version: %+v", back)
	}
	if seen := must[domain.ChangeImpact](t)(g.seenImpact(ctx, c.ID, mod.ID, "")); seen.Post == nil || *seen.Post != *again.Post {
		t.Fatalf("the impact log replays the cancellation: %+v", seen)
	}
}

// A flow is adopted with its versions checked in (ADR 0076).
func TestAdoptFlowNeedsCheckin(t *testing.T) { forEachRepo(t, testAdoptFlowNeedsCheckin) }

func testAdoptFlowNeedsCheckin(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "options", BaselineID: f.base.ID, OwnBranch: true}))
	opt := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "A", Hypothesis: "a", By: "u"}))
	cn := must[domain.ChangeImpact](t)(g.CheckoutNode(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "A", Flow: opt.ID}))
	must[domain.ChangeImpact](t)(g.UpdateNode(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "A"}, Flow: opt.ID}))
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
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "follow", BaselineID: f.base.ID}))
	req := must[domain.ChangeImpact](t)(g.CheckoutNode(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "edit"}))
	tst := must[domain.ChangeImpact](t)(g.CreateNode(ctx, c.ID, NodeCreate{Key: "TST-9", Type: "TestCase", Rationale: "cover",
		Links: []LinkWrite{{Type: "verifies", To: *req.Post}}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, req.ID, "bob", "ok"))
	again := must[domain.ChangeImpact](t)(g.CheckoutNode(ctx, c.ID, NodeCheckout{Impact: req.ID}))
	out := must[[]domain.Link](t)(g.OutLinksOf(ctx, *tst.Post))
	if len(out) != 1 || out[0].To != *again.Post {
		t.Fatalf("the link of the working version follows REQ-1 to %s: %+v", again.Post, out)
	}
	// the test version is frozen: a later version of REQ-1 leaves its link where it is
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, tst.ID, "bob", "ok"))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, req.ID, "bob", "ok"))
	third := must[domain.ChangeImpact](t)(g.CheckoutNode(ctx, c.ID, NodeCheckout{Impact: req.ID}))
	if out := must[[]domain.Link](t)(g.OutLinksOf(ctx, *tst.Post)); len(out) != 1 || out[0].To != *again.Post || *third.Post == *again.Post {
		t.Fatalf("a frozen link stays: %+v", out)
	}
}
