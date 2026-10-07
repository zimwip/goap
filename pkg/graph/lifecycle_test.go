package graph

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

type lcWorld struct {
	g                *Graph
	req1, req2, spec domain.Node
	base             domain.Baseline
	reqLC, specLC    domain.Lifecycle
}

// Requirement: proposed → draft (not landable) → approved → draft…; a Spec document
// contains requirements and can only be released when they are all approved.
func newLifecycleWorld(t *testing.T, repo Repo) lcWorld { return newLifecycleWorldG(t, repo, "") }

func newLifecycleWorldG(t *testing.T, repo Repo, approveGuard string) lcWorld {
	t.Helper()
	ctx := context.Background()
	w := lcWorld{g: New(repo)}
	w.reqLC = domain.Lifecycle{Initial: "proposed",
		States: []domain.LifecycleState{{Name: "proposed"}, {Name: "draft", NotLandable: true}, {Name: "approved"}},
		Transitions: []domain.Transition{
			{Name: "start", From: "proposed", To: "draft"},
			{Name: "approve", From: "draft", To: "approved", Guard: approveGuard, Requires: domain.TransitionRequires{Attributes: []string{"title"}}},
			{Name: "reopen", From: "approved", To: "draft"},
		}}
	w.specLC = domain.Lifecycle{Initial: "released",
		States: []domain.LifecycleState{{Name: "draft", NotLandable: true}, {Name: "released"}},
		Transitions: []domain.Transition{
			{Name: "reopen", From: "released", To: "draft"},
			{Name: "release", From: "draft", To: "released", Children: &domain.ChildrenRule{States: []string{"approved"}}},
		}}
	mk := func(key, typ string, props map[string]any, state string) domain.Node {
		n, err := seedNode(ctx, w.g, newNode{Key: key, Type: typ, Properties: props, State: state})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	types := testTypes{"Requirement": {Lifecycle: &w.reqLC}, "Spec": {Lifecycle: &w.specLC}, "Note": {}}
	w.g.Types = func() TypeCatalog { return types }
	w.req1 = mk("REQ-1", "Requirement", map[string]any{"title": "one"}, "approved")
	w.req2 = mk("REQ-2", "Requirement", map[string]any{}, "proposed")
	spec, err := seedNode(ctx, w.g, newNode{Key: "SPEC-1", Type: "Spec", Properties: map[string]any{"title": "spec"}, State: "released",
		Links: []LinkWrite{{Type: domain.LinkContains, To: w.req1.Ref()}, {Type: domain.LinkContains, To: w.req2.Ref()}}})
	if err != nil {
		t.Fatal(err)
	}
	w.spec = spec
	// the spec is version 1 with its links
	w.base = pinBaseline(t, w.g, "", w.req1.Ref(), w.req2.Ref(), w.spec.Ref())
	return w
}

func (w lcWorld) change(t *testing.T, title string) domain.Change {
	t.Helper()
	c, err := w.g.CreateChange(context.Background(), NewChange{ProjectID: "PROJ-ROOT", Title: title, BaselineID: w.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// declare adds the change impact of a node the change modifies.
func (w lcWorld) declare(t *testing.T, c domain.Change, n domain.Node) domain.ChangeImpactID {
	t.Helper()
	ref := n.Ref()
	ns, err := w.g.ProposeImpact(context.Background(), c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &ref, Rationale: "test " + n.Key}})
	if err != nil {
		t.Fatal(err)
	}
	return ns[0].ID
}

// write writes the next version of a change impact.
func (w lcWorld) write(c domain.Change, id domain.ChangeImpactID, nw edit) error {
	_, err := w.g.edit(context.Background(), c.ID, id, nw)
	return err
}

// accept accepts the change impacts of a change and checks them in.
func (w lcWorld) accept(t *testing.T, c domain.Change) {
	t.Helper()
	if err := w.g.acceptAll(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
}

// A change scoped to an Activity (architecture plan "Activity concept") is gated by its own goal condition
// (Graph.LandingGate), not the node-type lifecycle's landable-state floor: the activity's call on content/state
// maturity replaces the blanket "state cannot land" check, rather than adding to it.
func TestActivityGoalsGateReplacesLandableFloor(t *testing.T) {
	forEachRepo(t, testActivityGoalsGateReplacesLandableFloor)
}

func testActivityGoalsGateReplacesLandableFloor(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	c, err := w.g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "edit REQ-2", BaselineID: w.base.ID, Data: map[string]any{"scope": "deliver/draft-requirement"}})
	if err != nil {
		t.Fatal(err)
	}
	id := w.declare(t, c, w.req2)
	for _, step := range []edit{{State: "draft"}, {Properties: map[string]any{"title": "two"}}} {
		if err := w.write(c, id, step); err != nil {
			t.Fatal(err)
		}
	}
	w.accept(t, c)

	// no hook registered: the landable-state floor still applies, exactly as for a change the gate does not decide
	if _, err := w.g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cannot land") {
		t.Fatalf("no hook: landable floor must still apply: %v", err)
	}

	var gotRef string
	var gotBB domain.Blackboard
	met := false
	w.g.LandingGate = func(_ context.Context, c domain.Change, bb domain.Blackboard) (bool, bool, error) {
		ref, _ := c.Data["scope"].(string)
		if ref == "" {
			return false, false, nil
		}
		gotRef, gotBB = ref, bb
		return true, met, nil
	}

	// the hook says no: refused, by the activity's own message, not the landable-state one
	if _, err := w.g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "does not satisfy the goal") {
		t.Fatalf("hook unmet: %v", err)
	}
	if gotRef != "deliver/draft-requirement" || len(gotBB.Change.Nodes) != 1 || gotBB.Change.Nodes[0].ID != id {
		t.Fatalf("hook arguments: ref=%q change=%+v", gotRef, gotBB.Change)
	}
	// the blackboard is hydrated with the pending write this Apply is about to land, including its draft state
	post := gotBB.Change.Nodes[0].Post
	if post == nil {
		t.Fatalf("no post version on the impact: %+v", gotBB.Change.Nodes[0])
	}
	if v, ok := gotBB.Nodes[*post]; !ok || v.State != "draft" || v.Properties["title"] != "two" {
		t.Fatalf("blackboard must see this change's own pending write: %+v, ok=%v", v, ok)
	}

	// the hook says yes: applies even though REQ-2 is left in the not landable "draft" state
	met = true
	if _, err := w.g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatalf("hook met: the activity goal replaces the landable floor: %v", err)
	}
	if n, err := stateOf(t, w.g, w.req2.ID); err != nil || n.State != "draft" {
		t.Fatalf("REQ-2 must land in draft: %+v %v", n, err)
	}
}

func stateOf(t *testing.T, g *Graph, id domain.NodeID) (domain.Node, error) {
	t.Helper()
	var n domain.Node
	err := g.repo.InTx(context.Background(), func(tx Tx) (err error) { n, err = tx.Node(context.Background(), domain.NodeRef{ID: id}); return })
	return n, err
}

func TestLifecycleReopenEditAndApprove(t *testing.T) {
	forEachRepo(t, testLifecycleReopenEditAndApprove)
}

func testLifecycleReopenEditAndApprove(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	c := w.change(t, "edit REQ-1")
	id := w.declare(t, c, w.req1)

	// an approved node is edited in the change like any other: edits are allowed in any state (ADR 0078)
	// edit, reopen, edit again, approve, all in the change
	for _, step := range []edit{{Properties: map[string]any{"title": "x"}}, {State: "draft"}, {Properties: map[string]any{"title": "one v2"}}} {
		if err := w.write(c, id, step); err != nil {
			t.Fatal(err)
		}
	}
	w.accept(t, c)
	// the change acts on the node
	if refs, _ := w.g.ChangeImpacts(ctx, c.ID); len(refs) != 1 || refs[0] != w.req1.Ref() {
		t.Fatalf("pre versions: %+v", refs)
	}
	if cs, _ := w.g.NodeChanges(ctx, w.req1.ID); len(cs) != 1 || cs[0].ID != c.ID {
		t.Fatalf("node changes: %+v", cs)
	}
	// leaving the node in draft (not landable) is refused at Apply, the node listed with its state
	if _, err := w.g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "move them to a landable state first") || !strings.Contains(err.Error(), "REQ-1 (Requirement) in draft") {
		t.Fatalf("apply must refuse a node left in a state that cannot land, naming it: %v", err)
	}
	// the refusal leaves the change as it was: the node is still edited in its not landable state, then moved
	if err := w.write(c, id, edit{Properties: map[string]any{"title": "one v3"}}); err != nil {
		t.Fatalf("editing in a not landable state: %v", err)
	}
	if err := w.write(c, id, edit{State: "approved"}); err != nil {
		t.Fatal(err)
	}
	w.accept(t, c) // the edit sent the review back to proposed (ADR 0077)
	if _, err := w.g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	n, err := stateOf(t, w.g, w.req1.ID)
	if err != nil || n.State != "approved" || n.Properties["title"] != "one v3" || n.ChangeID != c.ID {
		t.Fatalf("REQ-1 after apply: %+v %v", n, err)
	}
	// persisted versions keep their state
	if v1, _ := w.g.Node(ctx, w.req1.Ref()); v1.State != "approved" {
		t.Fatalf("v1 state: %+v", v1)
	}
}

func TestLifecycleTransitionRules(t *testing.T) { forEachRepo(t, testLifecycleTransitionRules) }

func testLifecycleTransitionRules(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)

	// no such transition
	c := w.change(t, "bad move")
	id := w.declare(t, c, w.req1)
	if err := w.write(c, id, edit{State: "proposed"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cannot go from approved to proposed") {
		t.Fatalf("unknown transition: %v", err)
	}
	// a node type without lifecycle has no transitions
	c = w.change(t, "no lifecycle")
	notes, err := w.g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: "NOTE-1", Type: "Note", Rationale: "a note"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.write(c, notes[0].ID, edit{State: "x"}); err == nil {
		t.Fatal("a Note has no lifecycle: a state must be refused")
	}
	// required attribute: REQ-2 has no title, the transition is refused when it is taken (ADR 0076)
	c = w.change(t, "approve without title")
	id = w.declare(t, c, w.req2)
	if err := w.write(c, id, edit{State: "draft"}); err != nil {
		t.Fatal(err)
	}
	if err := w.write(c, id, edit{State: "approved"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `attribute "title" is required`) {
		t.Fatalf("requires.attributes: %v", err)
	}
	// with the attribute set before the approval it goes through
	c = w.change(t, "approve with title")
	id = w.declare(t, c, w.req2)
	for _, step := range []edit{{State: "draft"}, {Properties: map[string]any{"title": "two"}}, {State: "approved"}} {
		if err := w.write(c, id, step); err != nil {
			t.Fatal(err)
		}
	}
	w.accept(t, c)
	if _, err := w.g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	// REQ-2 is approved now
	if n, _ := stateOf(t, w.g, w.req2.ID); n.State != "approved" || n.Properties["title"] != "two" || n.ChangeID != c.ID {
		t.Fatalf("REQ-2: %+v", n)
	}
}

func TestLifecycleParallelChangesConflict(t *testing.T) {
	forEachRepo(t, testLifecycleParallelChangesConflict)
}

func testLifecycleParallelChangesConflict(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	var cs []domain.Change
	for _, title := range []string{"first", "second"} {
		c := w.change(t, title)
		id := w.declare(t, c, w.req1)
		for _, step := range []edit{{State: "draft"}, {Properties: map[string]any{"title": title}}, {State: "approved"}} {
			if err := w.write(c, id, step); err != nil {
				t.Fatal(err)
			}
		}
		w.accept(t, c)
		cs = append(cs, c)
	}
	if att, _ := w.g.NodeChanges(ctx, w.req1.ID); len(att) != 2 {
		t.Fatalf("both changes act on the node: %+v", att)
	}
	if _, err := w.g.Apply(ctx, cs[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	// the second one changes the same property: a merge is needed
	if _, err := w.g.Apply(ctx, cs[1].ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.g.Change(ctx, cs[1].ID); got.Status != domain.ChangeCommitted {
		t.Fatalf("the second change must wait for a merge: %s", got.Status)
	}
}

func TestLifecycleImpactNodeCreate(t *testing.T) { forEachRepo(t, testLifecycleImpactNodeCreate) }

func testLifecycleImpactNodeCreate(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	create := func(c domain.Change, key, state string) error {
		ns, err := w.g.proposeOrCreate(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: key, Type: "Requirement", Rationale: "new"}})
		if err != nil {
			t.Fatal(err)
		}
		return w.write(c, ns[0].ID, edit{Properties: map[string]any{"title": "nine"}, State: state})
	}
	if err := create(w.change(t, "create approved"), "REQ-8", "approved"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no transition proposed → approved: %v", err)
	}
	c := w.change(t, "create")
	// created in the (landable) initial state
	if err := create(c, "REQ-9", ""); err != nil {
		t.Fatal(err)
	}
	w.accept(t, c)
	if _, err := w.g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n, err := w.g.NodeByKey(ctx, "", "REQ-9"); err != nil || n.State != "proposed" {
		t.Fatalf("created node: %+v %v", n, err)
	}
	// created directly in a not landable state: refused when applied
	c = w.change(t, "create draft")
	if err := create(c, "REQ-10", "draft"); err != nil {
		t.Fatal(err)
	}
	w.accept(t, c)
	if _, err := w.g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "REQ-10 (Requirement) in draft") {
		t.Fatalf("a created node in draft must be refused: %v", err)
	}
}

func TestLifecycleDocumentValidatesChildren(t *testing.T) {
	forEachRepo(t, testLifecycleDocumentValidatesChildren)
}

func testLifecycleDocumentValidatesChildren(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	// reopen the spec and release it again while REQ-2 is only proposed
	c := w.change(t, "release the spec")
	spec := w.declare(t, c, w.spec)
	if err := w.write(c, spec, edit{State: "draft"}); err != nil {
		t.Fatal(err)
	}
	if err := w.write(c, spec, edit{State: "released"}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "REQ-2") || !strings.Contains(err.Error(), "proposed") {
		t.Fatalf("children validation: %v", err)
	}
	// the change approves REQ-2 first: children are judged on the states the change gives them
	req2 := w.declare(t, c, w.req2)
	for _, step := range []edit{{State: "draft"}, {Properties: map[string]any{"title": "two"}}, {State: "approved"}} {
		if err := w.write(c, req2, step); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.write(c, spec, edit{State: "released"}); err != nil {
		t.Fatal(err)
	}
	w.accept(t, c)
	if _, err := w.g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatalf("spec release with approved children: %v", err)
	}
	if n, _ := stateOf(t, w.g, w.spec.ID); n.State != "released" || n.ChangeID != c.ID {
		t.Fatalf("spec: %+v", n)
	}
}

func TestLifecycleAuthorizerAndGuard(t *testing.T) { forEachRepo(t, testLifecycleAuthorizerAndGuard) }

func testLifecycleAuthorizerAndGuard(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	deny := errors.New("no")
	w.g.Authorizer = func(_ context.Context, n domain.Node, tr domain.Transition) error {
		if tr.Name == "reopen" {
			return deny
		}
		return nil
	}
	c := w.change(t, "denied")
	id := w.declare(t, c, w.req1)
	if err := w.write(c, id, edit{State: "draft"}); !errors.Is(err, deny) {
		t.Fatalf("a transition is authorized when it is taken (ADR 0076): %v", err)
	}
	if n, _ := stateOf(t, w.g, w.req1.ID); n.State != "approved" {
		t.Fatalf("a refused transition writes nothing: %+v", n)
	}
	_ = ctx
}

// The authorizer reads the graph (the access policies are graph data): it runs outside the transaction of the
// transition, which would otherwise wait for itself.
func TestLifecycleAuthorizerReadsTheGraph(t *testing.T) {
	forEachRepo(t, testLifecycleAuthorizerReadsTheGraph)
}

func testLifecycleAuthorizerReadsTheGraph(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	var asked []string
	w.g.Authorizer = func(ctx context.Context, n domain.Node, tr domain.Transition) error {
		if _, err := w.g.Baselines(ctx, n.Namespace); err != nil {
			return err
		}
		asked = append(asked, tr.Name)
		return nil
	}
	c := w.change(t, "reads")
	id := w.declare(t, c, w.req1)
	done := make(chan error, 1)
	go func() { done <- w.write(c, id, edit{State: "draft"}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("transition: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the transition waits for itself: the authorizer runs inside its transaction")
	}
	_ = ctx
	if len(asked) == 0 {
		t.Fatal("the transition was not authorized")
	}
}

func TestLifecycleGuard(t *testing.T) { forEachRepo(t, testLifecycleGuard) }

func testLifecycleGuard(t *testing.T, repo Repo) {
	ctx := context.Background()
	// guard: a CEL predicate over the node
	w := newLifecycleWorldG(t, repo, `node.props.title.startsWith("ok")`)
	for _, tc := range []struct {
		title string
		ok    bool
	}{{"nope", false}, {"ok!", true}} {
		c := w.change(t, "guard "+tc.title)
		id := w.declare(t, c, w.req1)
		for _, step := range []edit{{State: "draft"}, {Properties: map[string]any{"title": tc.title}}} {
			if err := w.write(c, id, step); err != nil {
				t.Fatal(err)
			}
		}
		err := w.write(c, id, edit{State: "approved"})
		if tc.ok && err != nil {
			t.Fatal(err)
		}
		if tc.ok {
			w.accept(t, c)
			if _, err := w.g.Apply(ctx, c.ID, ""); err != nil {
				t.Fatal(err)
			}
		}
		if !tc.ok && (!errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "guard not satisfied")) {
			t.Fatalf("guard: %v", err)
		}
	}
}

// A transition may require a review (ADR 0076): the review is the change's, its guard reads it from the impact of the
// node.
func TestLifecycleGuardRequiresAReview(t *testing.T) {
	forEachRepo(t, testLifecycleGuardRequiresAReview)
}

func testLifecycleGuardRequiresAReview(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorldG(t, repo, `impact.review == "accepted"`)
	c := w.change(t, "approve after review")
	id := w.declare(t, c, w.req1)
	approve := NodeTransition{NodeCheckout: NodeCheckout{Impact: id}, To: "approved"}
	if _, err := w.g.ImpactNodeTransition(ctx, c.ID, NodeTransition{NodeCheckout: NodeCheckout{Impact: id}, To: "draft"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.ImpactNodeTransition(ctx, c.ID, approve); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "guard not satisfied") {
		t.Fatalf("approving before the review: %v", err)
	}
	if _, err := w.g.ImpactNodeReview(ctx, c.ID, id, domain.ReviewAccepted, "reviewer", "fine"); err != nil {
		t.Fatal(err)
	}
	cn, err := w.g.ImpactNodeTransition(ctx, c.ID, approve)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := w.g.ChangeNode(ctx, c.ID, "", *cn.Post); n.State != "approved" || !n.IsDraft() {
		t.Fatalf("the transition moves the draft: %+v", n)
	}
	if _, err := w.g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
}

func TestChangeStatusMachine(t *testing.T) { forEachRepo(t, testChangeStatusMachine) }

func testChangeStatusMachine(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	c := w.change(t, "status")
	abandoned := domain.ChangeAbandoned
	if _, err := w.g.UpdateChange(ctx, c.ID, ChangePatch{Status: &abandoned}); err != nil {
		t.Fatal(err)
	}
	active := domain.ChangeActive
	if _, err := w.g.UpdateChange(ctx, c.ID, ChangePatch{Status: &active}); !errors.Is(err, ErrConflict) {
		t.Fatalf("abandoned is final: %v", err)
	}
	applied := domain.ChangeApplied
	c2 := w.change(t, "other")
	if _, err := w.g.UpdateChange(ctx, c2.ID, ChangePatch{Status: &applied}); !errors.Is(err, ErrConflict) {
		t.Fatalf("only Apply applies a change: %v", err)
	}
}
