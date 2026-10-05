package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

var errNoPermission = errors.New("no permission")

// There is no explicit check-in (ADR 0077): a version written by the change stays a working version until the change
// lands. An accepted review is gated by what a frozen version must satisfy and freezes nothing.
func TestAcceptRunsValidatorsAndFreezesNothing(t *testing.T) {
	forEachRepo(t, testAcceptRunsValidatorsAndFreezesNothing)
}

func testAcceptRunsValidatorsAndFreezesNothing(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newAlgoWorld(t, repo)
	g := w.g
	c := w.change(t)
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "R7", Type: "Req", Properties: map[string]any{"code": "nope"}, Rationale: "new"}))
	// the property validator judges the version when it is accepted
	if _, err := g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "bob", "ok"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "must match") {
		t.Fatalf("a failing validator refuses the accept: %v", err)
	}
	if n := must[domain.Node](t)(g.Node(ctx, *cn.Post)); !n.CheckedOut || n.Properties["code"] != "nope" {
		t.Fatalf("the version is unchanged, a working version: %+v", n)
	}
	if seen := must[domain.ChangeImpact](t)(g.seenImpact(ctx, c.ID, cn.ID, "")); seen.Review != domain.ReviewProposed || len(seen.Reviews) != 0 {
		t.Fatalf("the review stays proposed, and nothing is recorded: %+v", seen)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"code": "REQ-77"}}))
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "bob", "ok"))
	if n := must[domain.Node](t)(g.Node(ctx, *cn.Post)); !n.CheckedOut {
		t.Fatalf("the accept froze nothing: %+v", n)
	}
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, cn.ID, edit{State: "approved"}))
	// landing freezes it, in the version the change wrote
	must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	if vs := must[[]domain.Node](t)(g.Versions(ctx, cn.Post.ID)); len(vs) != 1 || vs[0].CheckedOut || vs[0].State != "approved" {
		t.Fatalf("landing froze the one version: %+v", vs)
	}
}

// The flow of a user (ADR 0077): propose an existing node, check it out, update, accept, update again, accept, land:
// one version, two updated events, the review sent back to proposed in between, a frozen version at the end.
func TestEditAfterAcceptIsInPlace(t *testing.T) { forEachRepo(t, testEditAfterAcceptIsInPlace) }

func testEditAfterAcceptIsInPlace(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "edit", BaselineID: f.base.ID}))
	pre := f.req.Ref()
	prop := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "look"}}))
	co := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: prop[0].ID}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, co.ID, NodeUpdate{Properties: map[string]any{"title": "one"}}))
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, co.ID, domain.ReviewAccepted, "bob", "ok"))
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: co.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("an impact holding a working version is not checked out again: %v", err)
	}
	up := must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, co.ID, NodeUpdate{Properties: map[string]any{"title": "two"}}))
	if up.Review != domain.ReviewProposed || *up.Post != *co.Post {
		t.Fatalf("the update is in place and sends the review back: %+v", up)
	}
	// a link edit does the same
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, co.ID, domain.ReviewAccepted, "bob", "ok again"))
	must[domain.Link](t)(g.ImpactLinkCreate(ctx, c.ID, co.ID, LinkWrite{Type: "verifies", To: f.test.Ref()}, "", ""))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, co.ID, "bob", "and again"))

	type step struct {
		op     domain.ImpactOp
		status domain.NodeReview
	}
	var got []step
	for _, e := range must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, c.ID)) {
		st := step{op: e.Op}
		if e.Review != nil {
			st.status = e.Review.Status
		}
		got = append(got, st)
	}
	want := []step{{domain.ImpactProposed, ""}, {domain.ImpactCheckedOut, ""}, {domain.ImpactUpdated, ""}, {domain.ImpactReviewed, domain.ReviewAccepted},
		{domain.ImpactUpdated, ""}, {domain.ImpactReviewed, domain.ReviewProposed}, {domain.ImpactReviewed, domain.ReviewAccepted},
		{domain.ImpactUpdated, ""}, {domain.ImpactReviewed, domain.ReviewProposed}, {domain.ImpactReviewed, domain.ReviewAccepted}}
	if len(got) != len(want) {
		t.Fatalf("log %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("log %v, want %v", got, want)
		}
	}
	if vs := must[[]domain.Node](t)(g.Versions(ctx, f.req.ID)); len(vs) != int(pre.Version)+1 || !vs[len(vs)-1].CheckedOut {
		t.Fatalf("one version written, still a working version: %+v", vs)
	}
	res := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	landed := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: res.Nodes[f.req.ID]}))
	if landed.CheckedOut || landed.Version != pre.Version+1 || landed.Properties["title"] != "two" {
		t.Fatalf("landing freezes the working version: %+v", landed)
	}
}

// A transition of a node whose version is a working version of the change sets its state in place (ADR 0077): no new
// version, one transitioned event with the patch; permission and guard are checked when it is taken.
func TestTransitionInPlaceOnWorkingVersion(t *testing.T) {
	forEachRepo(t, testTransitionInPlaceOnWorkingVersion)
}

func testTransitionInPlaceOnWorkingVersion(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorldG(t, repo, `impact.review == "accepted"`)
	g := w.g
	c := w.change(t, "create")
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-20", Type: "Requirement", Properties: map[string]any{"title": "twenty"}, Rationale: "new"}))
	move := func(to string) error {
		_, err := g.ImpactNodeTransition(ctx, c.ID, NodeTransition{NodeCheckout: NodeCheckout{Impact: cn.ID}, To: to})
		return err
	}
	var asked []string
	g.Authorizer = func(_ context.Context, n domain.Node, tr domain.Transition) error {
		asked = append(asked, tr.Name)
		if tr.Name == "start" && n.Key == "REQ-20" && len(asked) == 1 {
			return errNoPermission
		}
		return nil
	}
	if err := move("draft"); !errors.Is(err, errNoPermission) {
		t.Fatalf("the authorizer is asked before an in-place transition: %v", err)
	}
	if n := must[domain.Node](t)(g.Node(ctx, *cn.Post)); n.State != "proposed" {
		t.Fatalf("a refused transition changes nothing: %+v", n)
	}
	if err := move("draft"); err != nil {
		t.Fatal(err)
	}
	// the guard sees the impact: its review is not accepted while the version is a working version
	if err := move("approved"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "guard not satisfied") {
		t.Fatalf("the guard is checked in place: %v", err)
	}
	vs := must[[]domain.Node](t)(g.Versions(ctx, cn.Post.ID))
	if len(vs) != 1 || vs[0].State != "draft" || !vs[0].CheckedOut {
		t.Fatalf("one working version, in the new state: %+v", vs)
	}
	var tr []domain.ImpactEvent
	for _, e := range must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, c.ID)) {
		if e.Op == domain.ImpactTransitioned {
			tr = append(tr, e)
		}
	}
	if len(tr) != 1 || tr[0].Post == nil || *tr[0].Post != *cn.Post || tr[0].WritesVersion() {
		t.Fatalf("one in-place transitioned event: %+v", tr)
	}
	st, _ := tr[0].Patch["state"].(map[string]any)
	if st["from"] != "proposed" || st["to"] != "draft" {
		t.Fatalf("patch: %+v", tr[0].Patch)
	}
	// accepted: the guard passes, still in place on the working version, and the acceptance stands
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "bob", "ok"))
	if err := move("approved"); err != nil {
		t.Fatal(err)
	}
	vs = must[[]domain.Node](t)(g.Versions(ctx, cn.Post.ID))
	if len(vs) != 1 || vs[0].State != "approved" || !vs[0].CheckedOut {
		t.Fatalf("still one working version: %+v", vs)
	}
	if seen := must[domain.ChangeImpact](t)(g.seenImpact(ctx, c.ID, cn.ID, "")); seen.Review != domain.ReviewAccepted {
		t.Fatalf("a transition does not undo the acceptance its guard read: %+v", seen)
	}
	must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	if n := must[domain.Node](t)(g.NodeByKey(ctx, "", "REQ-20")); n.State != "approved" {
		t.Fatalf("landed: %+v", n)
	}
}

// A creation then its transition in one change is one version, in the final state (ADR 0077): the in-place transition
// does not change the phase of the impact, and a frozen impact of a state the change left stays frozen (ADR 0058).
func TestCreateThenTransitionIsOneVersion(t *testing.T) {
	forEachRepo(t, testCreateThenTransitionIsOneVersion)
}

func testCreateThenTransitionIsOneVersion(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	g := w.g
	c := w.change(t, "create and approve")
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-30", Type: "Requirement", Properties: map[string]any{"title": "thirty"}, Rationale: "new"}))
	for _, to := range []string{"draft", "approved"} {
		must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, cn.ID, edit{State: to}))
	}
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, cn.ID, "bob", "ok"))
	must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	n := must[domain.Node](t)(g.NodeByKey(ctx, "", "REQ-30"))
	if vs := must[[]domain.Node](t)(g.Versions(ctx, n.ID)); len(vs) != 1 || vs[0].Version != 1 || vs[0].State != "approved" || vs[0].CheckedOut {
		t.Fatalf("one frozen version in the final state: %+v", vs)
	}
	var ops []domain.ImpactOp
	for _, e := range must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, c.ID)) {
		ops = append(ops, e.Op)
	}
	want := []domain.ImpactOp{domain.ImpactCreated, domain.ImpactTransitioned, domain.ImpactTransitioned, domain.ImpactReviewed, domain.ImpactLanded}
	if len(ops) < len(want) {
		t.Fatalf("log %v, want %v then landed events", ops, want)
	}
	for i := range want {
		if ops[i] != want[i] {
			t.Fatalf("log %v, want %v", ops, want)
		}
	}
}

// A transition of a node that has no working version in the change keeps writing a version of its own, frozen at once
// (ADR 0076): the impact cannot be withdrawn then, and a cancelled checkout goes back to it.
func TestTransitionOfFrozenVersionWritesFrozenVersion(t *testing.T) {
	forEachRepo(t, testTransitionOfFrozenVersionWritesFrozenVersion)
}

func testTransitionOfFrozenVersionWritesFrozenVersion(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	g := w.g
	c := w.change(t, "reopen")
	imp := w.declare(t, c, w.req1) // REQ-1 is approved in the baseline
	moved := must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, imp, edit{State: "draft"}))
	vs := must[[]domain.Node](t)(g.Versions(ctx, w.req1.ID))
	if len(vs) != int(w.req1.Version)+1 || vs[len(vs)-1].State != "draft" || vs[len(vs)-1].CheckedOut || *moved.Post != vs[len(vs)-1].Ref() {
		t.Fatalf("a frozen version in the new state: %+v", vs)
	}
	var last domain.ImpactEvent
	for _, e := range must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, c.ID)) {
		last = e
	}
	if last.Op != domain.ImpactTransitioned || !last.WritesVersion() {
		t.Fatalf("the transition of a frozen version is a written version: %+v", last)
	}
	if err := g.WithdrawImpact(ctx, c.ID, imp, "", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("an impact with a frozen version is not withdrawn: %v", err)
	}
	co := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: imp}))
	if co.Post.Version != moved.Post.Version+1 {
		t.Fatalf("the checkout follows the frozen version: %+v", co)
	}
	back := must[domain.ChangeImpact](t)(g.ImpactNodeCancel(ctx, c.ID, imp, "", ""))
	if back.Post == nil || *back.Post != *moved.Post {
		t.Fatalf("a cancelled checkout goes back to the frozen version: %+v", back)
	}
}

// The node index hears of a version when it is frozen, at landing (ADR 0077): not for the working version, nor for its
// acceptance.
func TestNodeEventAtLanding(t *testing.T) { forEachRepo(t, testNodeEventAtLanding) }

func testNodeEventAtLanding(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newAlgoWorld(t, repo)
	g := w.g
	sink := &recSink{}
	g.Observe(sink)
	c := w.change(t)
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "R8", Type: "Req", Properties: map[string]any{"code": "REQ-8"}, Rationale: "new"}))
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, cn.ID, edit{State: "approved"}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, cn.ID, "bob", "ok"))
	events := func() (n int) {
		for _, v := range sink.vals {
			if e, ok := v.(domain.NodeEvent); ok && e.ID == cn.Post.ID {
				n++
			}
		}
		return
	}
	if events() != 0 {
		t.Fatalf("no node event for a working version: %d", events())
	}
	must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	if events() != 1 {
		t.Fatalf("one node event for the version frozen at landing: %d", events())
	}
}
