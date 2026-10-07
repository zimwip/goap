package graph

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// A node is edited through its draft (ADR 0079): ImpactNodeUpdate and the link operations change it, no version is written
// during the change; an accepted review checks it and writes nothing; landing writes the version.
func TestCheckoutEditAccept(t *testing.T) { forEachRepo(t, testCheckoutEditAccept) }

func testCheckoutEditAccept(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "PSP v2", BaselineID: f.base.ID}))
	if _, err := g.ImpactNodeUpdate(ctx, c.ID, "nope", NodeUpdate{Properties: map[string]any{"x": 1}}); err == nil {
		t.Fatal("an update names an impact of the change")
	}
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "new PSP"}))
	if cn.Intent != domain.IntentModified || cn.Pre == nil || *cn.Pre != f.req.Ref() || cn.Post == nil || !cn.Post.IsDraft() || cn.Post.ID != f.req.ID {
		t.Fatalf("the checkout declares the impact and gives the node a draft: %+v", cn)
	}
	work := *cn.Post
	// the draft carries the links of the version it follows
	if out := must[domain.NodeView](t)(g.ChangeNodeView(ctx, c.ID, "", work)).Out; len(out) != 1 || out[0].Type != "satisfies" {
		t.Fatalf("links carried: %+v", out)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "Use PSP v2"}}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"owner": "alice"}}))
	link := must[domain.Link](t)(g.ImpactLinkCreate(ctx, c.ID, cn.ID, LinkWrite{Type: "verifies", To: f.test.Ref()}, "", ""))
	must[domain.Link](t)(g.ImpactLinkUpdate(ctx, c.ID, link.ID, map[string]any{"why": "spec"}, "", ""))
	view := must[domain.NodeView](t)(g.ChangeNodeView(ctx, c.ID, "", work))
	if latest := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID})); latest.Version != f.req.Version {
		t.Fatalf("nothing is on main before the change lands: v%d", latest.Version)
	}
	if !view.IsDraft() || view.Properties["title"] != "Use PSP v2" || view.Properties["owner"] != "alice" {
		t.Fatalf("edited: %+v", view.Node)
	}
	if n := g.versionCount(ctx, f.req.ID); n != int(f.req.Version) {
		t.Fatalf("no version for the checkout, whatever the edits: %d versions", n)
	}
	if len(view.Out) != 2 {
		t.Fatalf("a link added to the draft: %+v", view.Out)
	}
	// every edit is in the log, and nothing else
	var ops []domain.ImpactOp
	for _, e := range must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, c.ID)) {
		ops = append(ops, e.Op)
	}
	if want := []domain.ImpactOp{domain.ImpactProposed, domain.ImpactCheckedOut, domain.ImpactUpdated, domain.ImpactUpdated, domain.ImpactUpdated, domain.ImpactUpdated}; !slices.Equal(ops, want) {
		t.Fatalf("a user edit logs the checkout and its updates, and no review: %v", ops)
	}
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, cn.ID, edit{}))
	if err := g.ImpactLinkDelete(ctx, c.ID, link.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if out := must[domain.NodeView](t)(g.ChangeNodeView(ctx, c.ID, "", work)).Out; len(out) != 1 {
		t.Fatalf("a link removed from the draft: %+v", out)
	}
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a change with a review pending is not applied: %v", err)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "bob", "fine"))
	if n := g.versionCount(ctx, f.req.ID); n != int(f.req.Version) {
		t.Fatalf("accepting writes no version: %d versions", n)
	}
	// a checkout of a node with a draft is refused: the caller updates it, which sends the review back to proposed
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: cn.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("already checked out: %v", err)
	}
	again := must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "Use PSP v3"}}))
	if again.Post == nil || *again.Post != work || again.Review != domain.ReviewProposed {
		t.Fatalf("an edit after the accept is on the draft, and the review is back to proposed: %+v", again)
	}
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, cn.ID, "bob", "fine again"))
	if n := g.versionCount(ctx, f.req.ID); n != int(f.req.Version) {
		t.Fatalf("no version for the whole edit: %d versions", n)
	}
	res := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	landed := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: res.Nodes[f.req.ID]}))
	if landed.Properties["title"] != "Use PSP v3" || landed.Version != f.req.Version+1 || landed.Comment != "fine again" || len(landed.Parents) != 1 || landed.Parents[0] != f.req.Version {
		t.Fatalf("landing writes the version from the draft: %+v", landed)
	}
	if out := must[[]domain.Link](t)(g.OutLinksOf(ctx, landed.Ref())); len(out) != 1 || out[0].Type != "satisfies" {
		t.Fatalf("with its links: %+v", out)
	}
	if d := must[*domain.Draft](t)(g.draftOf(ctx, c.ID, "", cn.ID)); d != nil {
		t.Fatalf("the draft goes when its version is written: %+v", d)
	}
	got := must[domain.ChangeImpact](t)(g.seenImpact(ctx, c.ID, cn.ID, ""))
	if got.Post == nil || *got.Post != landed.Ref() || got.Landed == nil || *got.Landed != landed.Ref() {
		t.Fatalf("post is rewritten to the landed version: %+v", got)
	}
	// a version never changes once written
	if err := g.repo.InTx(ctx, func(tx Tx) error {
		return tx.PutLink(ctx, domain.Link{ID: domain.LinkID(g.newID()), Type: "x", From: landed.Ref(), To: landed.Ref(), ChangeID: c.ID})
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a link added to a written version: %v", err)
	}
}

// Cancelling a checkout drops the draft (ADR 0079): a modified node goes back to the version the change saw, a
// creation cancelled removes its impact; no node was ever written.
func TestImpactNodeCancel(t *testing.T) { forEachRepo(t, testImpactNodeCancel) }

func testImpactNodeCancel(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "try", BaselineID: f.base.ID}))
	mod := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "try"}))
	created := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-9", Type: "Requirement", Rationale: "new",
		Links: []LinkWrite{{Type: "satisfies", To: f.need.Ref()}}}))
	// a link of the created node to the draft of REQ-1
	link := must[domain.Link](t)(g.ImpactLinkCreate(ctx, c.ID, created.ID, LinkWrite{Type: "refines", To: *mod.Post}, "", ""))
	if link.To != *mod.Post {
		t.Fatalf("a link between drafts targets the node by id: %+v", link)
	}
	back := must[domain.ChangeImpact](t)(g.ImpactNodeCancel(ctx, c.ID, mod.ID, "", ""))
	if back.Post != nil {
		t.Fatalf("back to the impact without a draft: %+v", back)
	}
	if d := must[*domain.Draft](t)(g.draftOf(ctx, c.ID, "", mod.ID)); d != nil {
		t.Fatalf("the draft is dropped: %+v", d)
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
	// checked out again, accepted: cancelling still drops the draft, back to the impact without draft
	must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: mod.ID}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, mod.ID, "bob", "ok"))
	if back := must[domain.ChangeImpact](t)(g.ImpactNodeCancel(ctx, c.ID, mod.ID, "", "")); back.Post != nil {
		t.Fatalf("back to the impact without draft: %+v", back)
	}
}

// A flow is adopted with its drafts accepted (ADR 0076, 0079).
func TestAdoptFlowNeedsAccept(t *testing.T) { forEachRepo(t, testAdoptFlowNeedsAccept) }

func testAdoptFlowNeedsAccept(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "options", BaselineID: f.base.ID, OwnBranch: true}))
	opt := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "A", Hypothesis: "a", By: "u"}))
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "A", Flow: opt.ID}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "A"}, Flow: opt.ID}))
	if _, err := g.AdoptFlow(ctx, c.ID, opt.ID, "u"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a flow with a draft it did not accept is adopted: %v", err)
	}
	must[domain.ChangeImpact](t)(g.acceptOn(ctx, c.ID, opt.ID, "", cn.ID, "u", "ok"))
	must[domain.Flow](t)(g.AdoptFlow(ctx, c.ID, opt.ID, "u"))
}

// A link a draft of the change holds to a node of the change targets the draft: it follows the node to the version
// landing writes for it (ADR 0079); the link of a version written before stays where it is, suspect (ADR 0003).
func TestDraftLinksFollowNewVersions(t *testing.T) {
	forEachRepo(t, testDraftLinksFollowNewVersions)
}

func testDraftLinksFollowNewVersions(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	g := w.g
	c := w.change(t, "follow")
	req2 := w.declare(t, c, w.req2)
	// a new Note links the baseline version of REQ-2; REQ-2 is then moved (its draft): the link targets it by id
	note := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "NOTE-1", Type: "Note", Rationale: "cover",
		Links: []LinkWrite{{Type: "refines", To: w.req2.Ref()}}}))
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, req2, edit{Properties: map[string]any{"title": "two"}}))
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, req2, edit{State: "draft"}))
	moved := must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, req2, edit{State: "approved"}))
	out := must[domain.NodeView](t)(g.ChangeNodeView(ctx, c.ID, "", *note.Post)).Out
	if len(out) != 1 || out[0].To != *moved.Post || !out[0].To.IsDraft() {
		t.Fatalf("the link of the draft targets the draft of REQ-2: %+v", out)
	}
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, note.ID, "u", "ok"))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, req2, "u", "ok"))
	res := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	landed := must[[]domain.Link](t)(g.OutLinksOf(ctx, domain.NodeRef{ID: note.Post.ID, Version: res.Nodes[note.Post.ID]}))
	if len(landed) != 1 || landed[0].To != (domain.NodeRef{ID: w.req2.ID, Version: res.Nodes[w.req2.ID]}) || res.Nodes[w.req2.ID] != w.req2.Version+1 {
		t.Fatalf("landing resolves the link to the version it writes for REQ-2 (%v): %+v", res.Nodes[w.req2.ID], landed)
	}
}

// A creation gives the node a draft (event created), no version: checking it out again is refused.
func TestCreateIsCheckedOut(t *testing.T) { forEachRepo(t, testCreateIsCheckedOut) }

func testCreateIsCheckedOut(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "New", BaselineID: f.base.ID}))
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-NEW", Type: "alm@Requirement", Rationale: "new", Properties: map[string]any{"title": "x"}}))
	if cn.Post == nil || !cn.Post.IsDraft() || !must[domain.Node](t)(g.ChangeNode(ctx, c.ID, "", *cn.Post)).IsDraft() {
		t.Fatalf("a created node starts as a draft: %+v", cn)
	}
	if g.versionCount(ctx, cn.Post.ID) != 0 {
		t.Fatalf("a created node has no version during the change")
	}
	if _, err := g.NodeByKey(ctx, domain.DefaultNamespace, "REQ-NEW"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outside the change the node does not exist yet: %v", err)
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
