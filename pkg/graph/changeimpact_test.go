package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestChangeImpacts(t *testing.T) { forEachRepo(t, testChangeImpacts) }

func testChangeImpacts(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, err := g.CreateChange(ctx, NewChange{Title: "PSP v2", BaselineID: f.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	pre, needRef := f.req.Ref(), f.need.Ref()

	// an impact: pre + intent + rationale, no post yet
	got, err := g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{
		{Intent: domain.IntentModified, Pre: &pre, Rationale: "PSP v2 changes the payment API"},
		{Intent: domain.IntentCreated, Key: "TST-2", Type: "TestCase", Rationale: "cover the new API"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Key != "REQ-1" || got[0].Type != "Requirement" || got[0].Review != domain.ReviewProposed || !got[0].Planned() {
		t.Fatalf("key / type come from the pre node, review starts proposed: %+v", got[0])
	}
	if _, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "again"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a node appears once per change, got %v", err)
	}
	if _, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &needRef}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a rationale is required, got %v", err)
	}
	if ch, _ := g.Change(ctx, c.ID); ch.Status != domain.ChangeActive || len(ch.Nodes) != 2 {
		t.Fatalf("change should be active with 2 nodes: %s %d", ch.Status, len(ch.Nodes))
	}

	// accepting needs a comment
	if _, err := g.accept(ctx, c.ID, got[0].ID, "alice", "  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a review needs a comment, got %v", err)
	}
	if _, err := g.accept(ctx, c.ID, got[0].ID, "alice", "impact confirmed with the PSP team"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ImpactNodeReview(ctx, c.ID, got[0].ID, domain.ReviewRejected, "bob", "no"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a reviewed node cannot be reviewed again, got %v", err)
	}

	// a checkout realizes the impact: a working version written by the change, and the review starts over (ADR 0076)
	cn, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: got[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if cn.Post == nil || cn.Planned() || cn.Review != domain.ReviewProposed {
		t.Fatalf("post not set, or the review not back to proposed: %+v", cn)
	}
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: got[0].ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a node is checked out once, got %v", err)
	}
	n, err := g.Node(ctx, *cn.Post)
	if err != nil {
		t.Fatal(err)
	}
	if !n.CheckedOut || n.ChangeImpact != got[0].ID || n.ChangeID != c.ID || len(n.Parents) != 1 || n.Parents[0] != pre.Version {
		t.Fatalf("the working version: %+v", n)
	}
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("landing needs an accepted review, got %v", err)
	}
	if _, err := g.ImpactNodeReview(ctx, c.ID, got[0].ID, domain.ReviewAccepted, "alice", "impact confirmed with the PSP team"); err != nil {
		t.Fatal(err)
	}
	if n, err = g.Node(ctx, *cn.Post); err != nil {
		t.Fatal(err)
	}
	if !n.CheckedOut || n.Comment != "impact confirmed with the PSP team" {
		t.Fatalf("the working version records its acceptance and stays a working version until the change lands: %+v", n)
	}

	list, err := g.ListChangeImpacts(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Review != domain.ReviewAccepted || len(list[0].Reviews) != 3 || list[0].Reviews[0].By != "alice" || list[1].Key != "TST-2" || list[1].Planned() {
		t.Fatalf("unexpected list: %+v", list)
	}
	if list[0].Pre == nil || *list[0].Pre != pre {
		t.Fatalf("pre lost: %+v", list[0])
	}
}

func TestApplyChangeImpacts(t *testing.T) { forEachRepo(t, testApplyChangeImpacts) }

func testApplyChangeImpacts(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c1, err := g.CreateChange(ctx, NewChange{Title: "PSP v2", BaselineID: f.base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := g.CreateChange(ctx, NewChange{Title: "PSP v3", BaselineID: f.base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	pre := f.req.Ref()
	nodes, err := g.proposeOrCreate(ctx, c1.ID, []domain.ChangeImpact{
		{Intent: domain.IntentModified, Pre: &pre, Rationale: "PSP v2 changes the API"},
		{Intent: domain.IntentCreated, Key: "TST-2", Type: "TestCase", Rationale: "cover the new API"},
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := g.ProposeImpact(ctx, c2.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "impact of PSP v3"}})
	if err != nil {
		t.Fatal(err)
	}

	// still proposed: the change cannot be applied
	if _, err := g.Apply(ctx, c1.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a change impact awaiting review blocks Apply, got %v", err)
	}
	if _, err := g.edit(ctx, c1.ID, nodes[0].ID, edit{Properties: map[string]any{"title": "Use PSP v2"}}); err != nil {
		t.Fatal(err)
	}
	req2, err := g.edit(ctx, c1.ID, nodes[0].ID, edit{Properties: map[string]any{"owner": "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if req2.Post.Version != 2 {
		t.Fatalf("two edits of the working version, one version on the branch (ADR 0076), got %s", req2.Post)
	}
	tst, err := g.edit(ctx, c1.ID, nodes[1].ID, edit{Properties: map[string]any{"title": "PSP v2 test"},
		AddLinks: []LinkWrite{{Type: "verifies", To: *req2.Post}}})
	if err != nil {
		t.Fatal(err)
	}
	// accepted but planned: refused
	if _, err := g.accept(ctx, c1.ID, nodes[0].ID, "alice", "as designed"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.accept(ctx, c1.ID, nodes[1].ID, "alice", "needed"); err != nil {
		t.Fatal(err)
	}
	b, err := g.Apply(ctx, c1.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	head, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID})
	if head.Properties["title"] != "Use PSP v2" || head.Properties["owner"] != "alice" {
		t.Fatalf("both writes must land: %v", head.Properties)
	}
	if b.Nodes[f.req.ID] != head.Version || b.Nodes[tst.Post.ID] == 0 {
		t.Fatalf("baseline %v does not hold the landed versions", b.Nodes)
	}
	if head.ChangeID != c1.ID || head.ChangeImpact != nodes[0].ID || head.Comment != "as designed" {
		t.Fatalf("landed version must record its origin: %+v", head)
	}
	list, err := g.ListChangeImpacts(ctx, c1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Landed == nil || *list[0].Landed != head.Ref() || list[1].Landed == nil {
		t.Fatalf("landed not recorded: %+v", list)
	}
	if ch, _ := g.Change(ctx, c1.ID); ch.Status != domain.ChangeApplied {
		t.Fatalf("change should be applied, is %s", ch.Status)
	}

	// the other change's impact (no post yet) follows the new head and is flagged to re-check
	olist, err := g.ListChangeImpacts(ctx, c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if olist[0].ID != other[0].ID || !olist[0].Recheck || olist[0].Pre == nil || *olist[0].Pre != head.Ref() {
		t.Fatalf("impact should move to the new head: %+v", olist[0])
	}
}

func TestChangeImpactsLifecycle(t *testing.T) { forEachRepo(t, testChangeImpactsLifecycle) }

func testChangeImpactsLifecycle(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	g := w.g
	c, err := g.CreateChange(ctx, NewChange{Title: "edit REQ-1", BaselineID: w.base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	pre1, pre2 := w.req1.Ref(), w.req2.Ref()
	nodes, err := g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{
		{Intent: domain.IntentModified, Pre: &pre1, Rationale: "reword"},
		{Intent: domain.IntentModified, Pre: &pre2, Rationale: "approve without title"},
	})
	if err != nil {
		t.Fatal(err)
	}
	review := func(id domain.ChangeImpactID) {
		t.Helper()
		if _, err := g.accept(ctx, c.ID, id, "alice", "ok"); err != nil {
			t.Fatal(err)
		}
	}
	title := edit{Properties: map[string]any{"title": "one v2"}}

	// a node is edited in any state while it is in a change (ADR 0078)
	if _, err := g.edit(ctx, c.ID, nodes[0].ID, title); err != nil {
		t.Fatalf("editing an approved node: %v", err)
	}
	if _, err := g.edit(ctx, c.ID, nodes[0].ID, edit{State: "released"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no such transition: %v", err)
	}
	for _, step := range []edit{{State: "draft"}, title} {
		if _, err := g.edit(ctx, c.ID, nodes[0].ID, step); err != nil {
			t.Fatal(err)
		}
	}
	// req2 goes proposed → draft, and cannot be approved without the title the approval requires: the transition is
	// refused when it is taken (ADR 0076)
	if _, err := g.edit(ctx, c.ID, nodes[1].ID, edit{State: "draft"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.edit(ctx, c.ID, nodes[1].ID, edit{State: "approved"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `attribute "title"`) {
		t.Fatalf("a transition that lacks its required attribute: %v", err)
	}
	review(nodes[0].ID)
	review(nodes[1].ID)
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cannot land") {
		t.Fatalf("apply must refuse a node left in a state that cannot land: %v", err)
	}
	if _, err := g.edit(ctx, c.ID, nodes[0].ID, edit{State: "approved"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.edit(ctx, c.ID, nodes[1].ID, edit{Properties: map[string]any{"title": "two"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.edit(ctx, c.ID, nodes[1].ID, edit{State: "approved"}); err != nil {
		t.Fatal(err)
	}
	review(nodes[1].ID) // the checkout sent the review back to proposed
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	n1, err := stateOf(t, g, w.req1.ID)
	if err != nil || n1.State != "approved" || n1.Properties["title"] != "one v2" || n1.ChangeID != c.ID {
		t.Fatalf("REQ-1 after apply: %+v %v", n1, err)
	}
	n2, err := stateOf(t, g, w.req2.ID)
	if err != nil || n2.State != "approved" || n2.Properties["title"] != "two" {
		t.Fatalf("REQ-2 after apply: %+v %v", n2, err)
	}
}

func TestChangeImpactsMerge(t *testing.T) { forEachRepo(t, testChangeImpactsMerge) }

func testChangeImpactsMerge(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	pre := f.req.Ref()
	run := func(title string, props map[string]any) domain.Change {
		t.Helper()
		c, err := g.CreateChange(ctx, NewChange{Title: title, BaselineID: f.base.ID, OwnBranch: true})
		if err != nil {
			t.Fatal(err)
		}
		ns, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: title}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.accept(ctx, c.ID, ns[0].ID, "alice", "ok"); err != nil {
			t.Fatal(err)
		}
		// a checkout after the acceptance is reviewed again (ADR 0076)
		if _, err := g.edit(ctx, c.ID, ns[0].ID, edit{Properties: props}); err != nil {
			t.Fatal(err)
		}
		if err := g.acceptImpact(ctx, c.ID, ns[0].ID, ""); err != nil {
			t.Fatal(err)
		}
		return c
	}
	a := run("a", map[string]any{"title": "A"})
	b := run("b", map[string]any{"title": "B"})
	c := run("c", map[string]any{"owner": "carol"})

	if sh, err := g.SharedNodes(ctx, a.ID); err != nil || len(sh) != 1 || len(sh[0].Changes) != 2 {
		t.Fatalf("the three changes share the node: %+v %v", sh, err)
	}
	if _, err := g.Apply(ctx, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	// c changes another property: merged automatically on top of a
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	head, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID})
	if head.Properties["title"] != "A" || head.Properties["owner"] != "carol" || head.ChangeID != c.ID {
		t.Fatalf("disjoint changes must merge: %+v", head)
	}
	// b changes the same property as a: a merge is required
	if _, err := g.Apply(ctx, b.ID, ""); err != nil {
		t.Fatal(err)
	}
	if ch, _ := g.Change(ctx, b.ID); ch.Status != domain.ChangeCommitted {
		t.Fatalf("conflicting change must be committed, is %s", ch.Status)
	}
	if _, err := g.IntegrateChange(ctx, b.ID, map[domain.NodeID]Resolution{f.req.ID: {Props: map[string]any{"title": "A+B", "owner": "carol"}}}); err != nil {
		t.Fatal(err)
	}
	head, _ = g.Node(ctx, domain.NodeRef{ID: f.req.ID})
	list, _ := g.ListChangeImpacts(ctx, b.ID)
	if head.Properties["title"] != "A+B" || head.ChangeID != b.ID || list[0].Landed == nil || *list[0].Landed != head.Ref() {
		t.Fatalf("resolved merge must land as the version of b: %+v %+v", head, list)
	}
	if len(head.Parents) != 2 {
		t.Fatalf("a merge version has two parents: %+v", head.Parents)
	}
}

// ImpactsOf scopes a change's impacts to one execution (Activity Run, architecture plan "Activity concept"): the
// context graph (Pre) and modified graph (Post) of exactly what that run touched, not the whole change.
func TestImpactsOf(t *testing.T) { forEachRepo(t, testImpactsOf) }

func testImpactsOf(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "PSP v2", BaselineID: f.base.ID}))
	pre, needRef := f.req.Ref(), f.need.Ref()

	added := must[[]domain.ChangeImpact](t)(g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{
		{Intent: domain.IntentModified, Pre: &pre, Rationale: "touched by run A", Execution: "run-a"},
		{Intent: domain.IntentModified, Pre: &needRef, Rationale: "touched by run B", Execution: "run-b"},
	}))
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, added[0].ID, edit{Execution: "run-a", Properties: map[string]any{"title": "A"}}))
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, added[1].ID, edit{Execution: "run-b", Properties: map[string]any{"title": "B"}}))

	a := must[[]domain.ChangeImpact](t)(g.ImpactsOf(ctx, c.ID, "run-a"))
	if len(a) != 1 || a[0].Key != "REQ-1" || a[0].Post == nil {
		t.Fatalf("run-a impacts: %+v", a)
	}
	b := must[[]domain.ChangeImpact](t)(g.ImpactsOf(ctx, c.ID, "run-b"))
	if len(b) != 1 || b[0].Key != "NEED-1" || b[0].Post == nil {
		t.Fatalf("run-b impacts: %+v", b)
	}
	if none := must[[]domain.ChangeImpact](t)(g.ImpactsOf(ctx, c.ID, "run-c")); len(none) != 0 {
		t.Fatalf("unknown execution: %+v", none)
	}
}

type refuseReviewer string

func (r refuseReviewer) Review(q domain.ReviewRequest) error {
	if q.Reviewer == string(r) {
		return errors.New("no")
	}
	return nil
}

// A ReviewPolicy refuses a review before anything is written; unset, anyone reviews (ADR 0075).
func TestReviewPolicy(t *testing.T) { forEachRepo(t, testReviewPolicy) }

func testReviewPolicy(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, err := g.CreateChange(ctx, NewChange{Title: "t", BaselineID: f.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	pre := f.req.Ref()
	got, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "r", ProducedBy: "bob"}})
	if err != nil {
		t.Fatal(err)
	}
	log := func() int {
		entries, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID})
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	before := log()
	g.ReviewPolicy = refuseReviewer("bob")
	if _, err := g.accept(ctx, c.ID, got[0].ID, "bob", "mine"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("the policy refuses: %v", err)
	}
	if ch, _ := g.Change(ctx, c.ID); ch.Nodes[0].Review != domain.ReviewProposed || len(ch.Nodes[0].Reviews) != 0 || log() != before {
		t.Fatalf("a refusal writes nothing: %+v", ch.Nodes[0])
	}
	if _, err := g.accept(ctx, c.ID, got[0].ID, "alice", "ok"); err != nil {
		t.Fatalf("another reviewer: %v", err)
	}
}

func TestNilReviewPolicyReviewsAsBefore(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		f := newFixture(t, repo)
		c, err := f.g.CreateChange(ctx, NewChange{Title: "t", BaselineID: f.base.ID})
		if err != nil {
			t.Fatal(err)
		}
		pre := f.req.Ref()
		got, err := f.g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "r", ProducedBy: "bob"}})
		if err != nil {
			t.Fatal(err)
		}
		if f.g.ReviewPolicy != nil {
			t.Fatal("unset by default")
		}
		if _, err := f.g.accept(ctx, c.ID, got[0].ID, "bob", "mine"); err != nil {
			t.Fatalf("anyone reviews: %v", err)
		}
	})
}

// ReopenImpacts sends accepted impacts back to proposed, as a review event; the others are left alone (ADR 0075).
func TestReopenImpacts(t *testing.T) { forEachRepo(t, testReopenImpacts) }

func testReopenImpacts(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, err := g.CreateChange(ctx, NewChange{Title: "t", BaselineID: f.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	pre := f.req.Ref()
	got, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "r", ProducedBy: "bob"}})
	if err != nil {
		t.Fatal(err)
	}
	id := got[0].ID
	if done, err := g.ReopenImpacts(ctx, c.ID, []domain.ChangeImpactID{id}, "why"); err != nil || len(done) != 0 {
		t.Fatalf("a proposed impact is left alone: %v %v", done, err)
	}
	if _, err := g.ReopenImpacts(ctx, c.ID, []domain.ChangeImpactID{id}, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a comment is mandatory: %v", err)
	}
	if _, err := g.accept(ctx, c.ID, id, "alice", "ok"); err != nil {
		t.Fatal(err)
	}
	done, err := g.ReopenImpacts(ctx, c.ID, []domain.ChangeImpactID{id}, "derogation expired")
	if err != nil || len(done) != 1 {
		t.Fatalf("reopen: %v %v", done, err)
	}
	ch, _ := g.Change(ctx, c.ID)
	if ch.Nodes[0].Review != domain.ReviewProposed || len(ch.Nodes[0].Reviews) != 2 {
		t.Fatalf("back to proposed, history kept: %+v", ch.Nodes[0])
	}
	if _, err := g.accept(ctx, c.ID, id, "carol", "again"); err != nil {
		t.Fatalf("reviewed again: %v", err)
	}
	if _, err := g.ReopenImpacts(ctx, c.ID, []domain.ChangeImpactID{"nope"}, "why"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown impact: %v", err)
	}
}

// ItemAuthorizer is asked about the items of a kind that asks a permission of its writer, and only those (ADR 0075).
func TestItemAuthorizer(t *testing.T) {
	const kind domain.ItemKind = "test-signed"
	domain.RegisterItemKind(kind, nil)
	domain.RequireItemPermission(kind, domain.ItemPermission{Permission: "thing:sign", SubjectField: "who"})
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		f := newFixture(t, repo)
		g := f.g
		c, err := g.CreateChange(ctx, NewChange{Title: "t", BaselineID: f.base.ID})
		if err != nil {
			t.Fatal(err)
		}
		var asked []string
		g.ItemAuthorizer = func(_ context.Context, ch domain.Change, it domain.ChangeItem, p domain.ItemPermission) error {
			asked = append(asked, p.Permission+"/"+p.SubjectField)
			if ch.ID != c.ID {
				t.Errorf("change %s", ch.ID)
			}
			if it.Data["who"] == "mallory" {
				return errors.New("refused")
			}
			return nil
		}
		if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindArtifact, Type: "note", Data: map[string]any{"text": "x"}}}); err != nil || len(asked) != 0 {
			t.Fatalf("an ordinary item asks nothing: %v %v", asked, err)
		}
		if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: kind, Data: map[string]any{"who": "mallory"}}}); err == nil || err.Error() != "refused" {
			t.Fatalf("refused: %v", err)
		}
		if ch, _ := g.Change(ctx, c.ID); len(ch.ItemsOfKind(kind)) != 0 {
			t.Fatal("a refused item is not stored")
		}
		if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: kind, Data: map[string]any{"who": "alice"}}}); err != nil || len(asked) != 2 || asked[1] != "thing:sign/who" {
			t.Fatalf("authorized: %v %v", asked, err)
		}
	})
}

// ItemPolicy is asked about every item AddItems stores, and an error refuses the write (ADR 0075 §3).
func TestItemPolicy(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		f := newFixture(t, repo)
		g := f.g
		c, err := g.CreateChange(ctx, NewChange{Title: "t", BaselineID: f.base.ID, Data: map[string]any{"level": "high"}})
		if err != nil {
			t.Fatal(err)
		}
		g.ItemPolicy = func(_ context.Context, ch domain.Change, it domain.ChangeItem) error {
			if ch.Data["level"] == "high" && it.Data["text"] == "no" {
				return errors.New("not on a high change")
			}
			return nil
		}
		item := func(text string) domain.ChangeItem {
			return domain.ChangeItem{Kind: domain.KindArtifact, Type: "note", Data: map[string]any{"text": text}}
		}
		if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{item("yes")}); err != nil {
			t.Fatal(err)
		}
		if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{item("no")}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not on a high change") {
			t.Fatalf("refused: %v", err)
		}
		if ch, _ := g.Change(ctx, c.ID); len(ch.Items) != 1 {
			t.Fatalf("a refused item is not stored: %d", len(ch.Items))
		}
	})
}
