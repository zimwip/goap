package graph

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

var errNoPermission = errors.New("no permission")

// A node has no version while the change works (ADR 0079): its draft stays until the change lands. An accepted review is
// gated by what a version must satisfy, checked on the draft, and writes nothing.
func TestAcceptRunsValidatorsOnTheDraft(t *testing.T) {
	forEachRepo(t, testAcceptRunsValidatorsOnTheDraft)
}

func testAcceptRunsValidatorsOnTheDraft(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newAlgoWorld(t, repo)
	g := w.g
	c := w.change(t)
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "R7", Type: "Req", Properties: map[string]any{"code": "nope"}, Rationale: "new"}))
	// the property validator judges the version when it is accepted
	if _, err := g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "bob", "ok"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "must match") {
		t.Fatalf("a failing validator refuses the accept: %v", err)
	}
	if n := must[domain.Node](t)(g.ChangeNode(ctx, c.ID, "", *cn.Post)); !n.IsDraft() || n.Properties["code"] != "nope" {
		t.Fatalf("the draft is unchanged: %+v", n)
	}
	if seen := must[domain.ChangeImpact](t)(g.seenImpact(ctx, c.ID, cn.ID, "")); seen.Review != domain.ReviewProposed || len(seen.Reviews) != 0 {
		t.Fatalf("the review stays proposed, and nothing is recorded: %+v", seen)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"code": "REQ-77"}}))
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "bob", "ok"))
	if g.versionCount(ctx, cn.Post.ID) != 0 {
		t.Fatalf("the accept wrote no version")
	}
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, cn.ID, edit{State: "approved"}))
	// landing writes the version from the draft
	must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	if vs := must[[]domain.Node](t)(g.Versions(ctx, cn.Post.ID)); len(vs) != 1 || vs[0].Version != 1 || vs[0].State != "approved" || vs[0].Properties["code"] != "REQ-77" {
		t.Fatalf("landing wrote the one version: %+v", vs)
	}
}

// The flow of a user (ADR 0077, 0079): propose an existing node, check it out, update, accept, update again, accept,
// land: one draft, no version until landing, the review sent back to proposed in between, one version at the end.
func TestEditAfterAcceptIsOnTheDraft(t *testing.T) { forEachRepo(t, testEditAfterAcceptIsOnTheDraft) }

func testEditAfterAcceptIsOnTheDraft(t *testing.T, repo Repo) {
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
		t.Fatalf("an impact holding a draft is not checked out again: %v", err)
	}
	up := must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, co.ID, NodeUpdate{Properties: map[string]any{"title": "two"}}))
	if up.Review != domain.ReviewProposed || *up.Post != *co.Post {
		t.Fatalf("the update is on the draft and sends the review back: %+v", up)
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
	if n := g.versionCount(ctx, f.req.ID); n != int(pre.Version) {
		t.Fatalf("no version written during the change: %d", n)
	}
	res := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	landed := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: res.Nodes[f.req.ID]}))
	if landed.Version != pre.Version+1 || landed.Properties["title"] != "two" {
		t.Fatalf("landing writes the version from the draft: %+v", landed)
	}
}

// A transition sets the state of the draft of the node (ADR 0079): no version, one transitioned event with the patch;
// permission and guard are checked when it is taken, and the guard sees the change, the impact and the draft.
func TestTransitionOnTheDraft(t *testing.T) {
	forEachRepo(t, testTransitionOnTheDraft)
}

func testTransitionOnTheDraft(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorldG(t, repo, `impact.review == "accepted" && draft.new && draft.key == "REQ-20" && draft.state == "approved" && draft.props.title == "twenty" && change.title == "create" && node.state == "approved"`)
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
	if n := must[domain.Node](t)(g.ChangeNode(ctx, c.ID, "", *cn.Post)); n.State != "proposed" {
		t.Fatalf("a refused transition changes nothing: %+v", n)
	}
	if err := move("draft"); err != nil {
		t.Fatal(err)
	}
	// the guard sees the impact: its review is not accepted yet
	if err := move("approved"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "guard not satisfied") {
		t.Fatalf("the guard is checked in place: %v", err)
	}
	if n := must[domain.Node](t)(g.ChangeNode(ctx, c.ID, "", *cn.Post)); n.State != "draft" || g.versionCount(ctx, cn.Post.ID) != 0 {
		t.Fatalf("one draft, in the new state, and no version: %+v", n)
	}
	var tr []domain.ImpactEvent
	for _, e := range must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, c.ID)) {
		if e.Op == domain.ImpactTransitioned {
			tr = append(tr, e)
		}
	}
	if len(tr) != 1 || tr[0].Post == nil || *tr[0].Post != *cn.Post || tr[0].StartsDraft() {
		t.Fatalf("one transitioned event, on the draft: %+v", tr)
	}
	st, _ := tr[0].Patch["state"].(map[string]any)
	if st["from"] != "proposed" || st["to"] != "draft" {
		t.Fatalf("patch: %+v", tr[0].Patch)
	}
	// accepted: the guard passes, on the draft, and the acceptance stands
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "bob", "ok"))
	if err := move("approved"); err != nil {
		t.Fatal(err)
	}
	if n := must[domain.Node](t)(g.ChangeNode(ctx, c.ID, "", *cn.Post)); n.State != "approved" || g.versionCount(ctx, cn.Post.ID) != 0 {
		t.Fatalf("still one draft, no version: %+v", n)
	}
	if seen := must[domain.ChangeImpact](t)(g.seenImpact(ctx, c.ID, cn.ID, "")); seen.Review != domain.ReviewAccepted {
		t.Fatalf("a transition does not undo the acceptance its guard read: %+v", seen)
	}
	must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	if n := must[domain.Node](t)(g.NodeByKey(ctx, "", "REQ-20")); n.State != "approved" {
		t.Fatalf("landed: %+v", n)
	}
}

// A creation then its transition in one change is one version, in the final state (ADR 0079): the transition does not
// change the phase of the impact, and an impact written in a state the change left stays frozen (ADR 0058).
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
	if vs := must[[]domain.Node](t)(g.Versions(ctx, n.ID)); len(vs) != 1 || vs[0].Version != 1 || vs[0].State != "approved" {
		t.Fatalf("one version in the final state: %+v", vs)
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

// A transition of a node that has no draft in the change checks it out first (ADR 0079): events checkedOut then
// transitioned, no version; the impact can be withdrawn, and a cancelled checkout goes back to the impact without draft.
func TestTransitionChecksTheNodeOut(t *testing.T) {
	forEachRepo(t, testTransitionChecksTheNodeOut)
}

func testTransitionChecksTheNodeOut(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	g := w.g
	c := w.change(t, "reopen")
	imp := w.declare(t, c, w.req1) // REQ-1 is approved in the baseline
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, imp, "bob", "confirmed"))
	moved := must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, imp, edit{State: "draft"}))
	if moved.Post == nil || !moved.Post.IsDraft() || moved.Review != domain.ReviewAccepted {
		t.Fatalf("the move checked the node out, and is not an edit: the acceptance stands: %+v", moved)
	}
	if n := g.versionCount(ctx, w.req1.ID); n != int(w.req1.Version) {
		t.Fatalf("no version written: %d", n)
	}
	if n := must[domain.Node](t)(g.ChangeNode(ctx, c.ID, "", *moved.Post)); n.State != "draft" {
		t.Fatalf("the draft is in the new state: %+v", n)
	}
	var ops []domain.ImpactOp
	for _, e := range must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, c.ID)) {
		ops = append(ops, e.Op)
	}
	if want := []domain.ImpactOp{domain.ImpactProposed, domain.ImpactReviewed, domain.ImpactCheckedOut, domain.ImpactTransitioned}; !slices.Equal(ops, want) {
		t.Fatalf("log %v, want %v", ops, want)
	}
	back := must[domain.ChangeImpact](t)(g.ImpactNodeCancel(ctx, c.ID, imp, "", ""))
	if back.Post != nil {
		t.Fatalf("a cancelled checkout goes back to the impact without draft: %+v", back)
	}
	if err := g.WithdrawImpact(ctx, c.ID, imp, "", ""); err != nil {
		t.Fatalf("no version of the impact exists, it is withdrawn: %v", err)
	}
}

// The node index hears of a version when it is written, at landing (ADR 0079): not for the draft, nor for its
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
		t.Fatalf("no node event for a draft: %d", events())
	}
	must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	if events() != 1 {
		t.Fatalf("one node event for the version written at landing: %d", events())
	}
}
