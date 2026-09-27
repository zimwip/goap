package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/domain"
)

const regexValidatorJS = `
var v = ctx.value();
if (v !== undefined && v !== null && !new RegExp(ctx.param("pattern")).test(String(v)))
  ctx.fail("must match " + ctx.param("pattern"));`

const noEmptyChildGuardJS = `
for (const c of ctx.children()) if (c.state !== ctx.param("state")) ctx.fail(c.key + " is " + c.state);`

const stampActionGo = `package action

import "github.com/zimwip/goap/pkg/dsl"

func Run(ctx *dsl.TransitionCtx) error {
	ctx.SetProp("approvedAt", ctx.Change().Title)
	ctx.SetProp("state_after", ctx.Transition().To)
	return nil
}`

type algoWorld struct {
	g        *Graph
	base     domain.Baseline
	req, doc domain.Node
}

// Requirement: property "code" must match ^REQ-[0-9]+$ (inherited by Sub);
// approve stamps the node. Doc contains requirements: release needs them approved (guard).
func newAlgoWorld(t *testing.T, repo Repo) algoWorld {
	t.Helper()
	ctx := context.Background()
	w := algoWorld{g: New(repo)}
	regex := algo.Bound{Instance: "req-code", Algorithm: "regex", Type: algo.UsagePropertyValidator, Language: "javascript",
		Code: regexValidatorJS, Params: map[string]any{"pattern": "^REQ-[0-9]+$"}, Property: "code"}
	stamp := algo.Bound{Instance: "stamp", Algorithm: "stamp", Type: algo.UsageTransitionAction, Language: "go", Code: stampActionGo}
	guard := algo.Bound{Instance: "children-approved", Algorithm: "children-in-state", Type: algo.UsageTransitionGuard, Language: "javascript",
		Code: noEmptyChildGuardJS, Params: map[string]any{"state": "approved"}}
	reqLC := domain.Lifecycle{Initial: "draft",
		States:      []domain.LifecycleState{{Name: "draft", Editable: true}, {Name: "approved"}},
		Transitions: []domain.Transition{{Name: "approve", From: "draft", To: "approved", ActionAlgos: []algo.Bound{stamp}}, {Name: "reopen", From: "approved", To: "draft"}}}
	docLC := domain.Lifecycle{Initial: "draft",
		States:      []domain.LifecycleState{{Name: "draft", Editable: true}, {Name: "released"}},
		Transitions: []domain.Transition{{Name: "release", From: "draft", To: "released", GuardAlgos: []algo.Bound{guard}}}}
	mk := func(key, typ string, props map[string]any, state string) domain.Node {
		n, err := w.g.CreateNode(ctx, NewNode{Key: key, Type: typ, Properties: props, State: state})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	types := testTypes{"Req": {Lifecycle: &reqLC, Validators: []algo.Bound{regex}}, "Sub": {Extends: "Req"}, "Doc": {Lifecycle: &docLC}}
	w.g.Types = func() TypeCatalog { return types }
	w.req = mk("R1", "Req", map[string]any{"code": "REQ-1"}, "draft")
	w.doc = mk("D1", "Doc", map[string]any{}, "draft")
	if _, err := w.g.Link(ctx, domain.LinkContains, w.doc.Ref(), w.req.Ref(), nil); err != nil {
		t.Fatal(err)
	}
	var err error
	if w.base, err = w.g.CreateBaseline(ctx, "B", []domain.NodeRef{w.req.Ref(), w.doc.Ref()}); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w algoWorld) change(t *testing.T) domain.Change {
	t.Helper()
	c, err := w.g.CreateChange(context.Background(), NewChange{Title: "chg", BaselineID: w.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// declareCreate adds a change impact that creates a node, and writes it.
func (w algoWorld) create(c domain.Change, key, typ string, props map[string]any) error {
	ns, err := w.g.AddNodes(context.Background(), c.ID, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: key, Type: typ, Rationale: "new " + key}})
	if err != nil {
		return err
	}
	_, err = w.g.WriteNode(context.Background(), c.ID, ns[0].ID, NodeWrite{Properties: props, State: "approved"})
	return err
}

// modify adds a change impact on an existing node and writes it.
func (w algoWorld) modify(t *testing.T, c domain.Change, n domain.Node, writes ...NodeWrite) error {
	t.Helper()
	ref := n.Ref()
	ns, err := w.g.AddNodes(context.Background(), c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &ref, Rationale: "modify " + n.Key}})
	if err != nil {
		t.Fatal(err)
	}
	for _, nw := range writes {
		if _, err := w.g.WriteNode(context.Background(), c.ID, ns[0].ID, nw); err != nil {
			return err
		}
	}
	return nil
}

func (w algoWorld) acceptAll(t *testing.T, c domain.Change) {
	t.Helper()
	nodes, _ := w.g.ListChangeImpacts(context.Background(), c.ID)
	for _, n := range nodes {
		if _, err := w.g.ReviewNode(context.Background(), c.ID, n.ID, domain.ReviewAccepted, "u", "ok"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPropertyValidators(t *testing.T) { forEachRepo(t, testPropertyValidators) }

func testPropertyValidators(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newAlgoWorld(t, repo)

	// early feedback on create (also through the inherited validator of a subtype) and update
	for i, try := range []func(c domain.Change) error{
		func(c domain.Change) error { return w.create(c, "R2", "Req", map[string]any{"code": "nope"}) },
		func(c domain.Change) error { return w.create(c, "R3", "Sub", map[string]any{"code": "nope"}) },
		func(c domain.Change) error {
			return w.modify(t, c, w.req, NodeWrite{Properties: map[string]any{"code": "nope"}})
		},
	} {
		if err := try(w.change(t)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "must match") {
			t.Fatalf("invalid property accepted (%d): %v", i, err)
		}
	}
	c := w.change(t)
	if err := w.create(c, "R2", "Sub", map[string]any{"code": "REQ-2"}); err != nil {
		t.Fatal(err)
	}
	if err := w.modify(t, c, w.req, NodeWrite{Properties: map[string]any{"code": "REQ-9"}}, NodeWrite{State: "approved"}); err != nil {
		t.Fatal(err)
	}
	w.acceptAll(t, c)
	if _, err := w.g.Apply(ctx, c.ID, "ok"); err != nil {
		t.Fatal(err)
	}
	n, err := stateOf(t, w.g, w.req.ID)
	if err != nil || n.Properties["code"] != "REQ-9" {
		t.Fatalf("%v %v", err, n.Properties)
	}
}

func TestTransitionGuardAndAction(t *testing.T) { forEachRepo(t, testTransitionGuardAndAction) }

func testTransitionGuardAndAction(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newAlgoWorld(t, repo)

	// the guard algorithm refuses releasing a document whose requirement is a draft
	c := w.change(t)
	if err := w.modify(t, c, w.doc, NodeWrite{State: "released"}); err != nil {
		t.Fatal(err)
	}
	w.acceptAll(t, c)
	if _, err := w.g.Apply(ctx, c.ID, "x"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "R1 is draft") {
		t.Fatalf("guard must refuse: %v", err)
	}

	// approving the requirement in the same change satisfies the guard; the action stamps it
	c2 := w.change(t)
	if err := w.modify(t, c2, w.req, NodeWrite{State: "approved"}); err != nil {
		t.Fatal(err)
	}
	if err := w.modify(t, c2, w.doc, NodeWrite{State: "released"}); err != nil {
		t.Fatal(err)
	}
	w.acceptAll(t, c2)
	if _, err := w.g.Apply(ctx, c2.ID, "y"); err != nil {
		t.Fatal(err)
	}
	n, err := stateOf(t, w.g, w.req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n.State != "approved" || n.Properties["approvedAt"] != "chg" || n.Properties["state_after"] != "approved" || n.Properties["code"] != "REQ-1" {
		t.Fatalf("action did not stamp the node: %+v", n)
	}
}
