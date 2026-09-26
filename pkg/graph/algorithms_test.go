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
	tReq := mk("D:x/nodetype/Req", NodeTypeNode, map[string]any{"name": "Req", "lifecycle": asMap(t, reqLC), "validators": []any{asMap(t, regex)}}, "")
	tSub := mk("D:x/nodetype/Sub", NodeTypeNode, map[string]any{"name": "Sub", "extends": "Req"}, "")
	tDoc := mk("D:x/nodetype/Doc", NodeTypeNode, map[string]any{"name": "Doc", "document": asMap(t, domain.DocumentSpec{Contains: []string{"Req"}}), "lifecycle": asMap(t, docLC)}, "")
	w.req = mk("R1", "Req", map[string]any{"code": "REQ-1"}, "draft")
	w.doc = mk("D1", "Doc", map[string]any{}, "draft")
	if _, err := w.g.Link(ctx, domain.LinkContains, w.doc.Ref(), w.req.Ref(), nil); err != nil {
		t.Fatal(err)
	}
	var err error
	if w.base, err = w.g.CreateBaseline(ctx, "B", []domain.NodeRef{tReq.Ref(), tSub.Ref(), tDoc.Ref(), w.req.Ref(), w.doc.Ref()}); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w algoWorld) change(t *testing.T) domain.ChangeSet {
	t.Helper()
	c, err := w.g.CreateChange(context.Background(), NewChange{Title: "chg", BaselineID: w.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func createItem(key, typ string, props map[string]any) domain.ChangeItem {
	return domain.ChangeItem{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Key: key, Type: typ, Properties: props, State: "approved"}}}
}

func TestPropertyValidators(t *testing.T) { forEachRepo(t, testPropertyValidators) }

func testPropertyValidators(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newAlgoWorld(t, repo)
	c := w.change(t)

	// early feedback on create (also through the inherited validator of a subtype) and update
	for _, it := range []domain.ChangeItem{
		createItem("R2", "Req", map[string]any{"code": "nope"}),
		createItem("R3", "Sub", map[string]any{"code": "nope"}),
		updateItem(w.req, map[string]any{"code": "nope"}),
	} {
		if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{it}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "must match") {
			t.Fatalf("invalid property accepted: %v", err)
		}
	}
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{createItem("R2", "Sub", map[string]any{"code": "REQ-2"}), updateItem(w.req, map[string]any{"code": "REQ-9"}), moveItem(w.req, "approved")}); err != nil {
		t.Fatal(err)
	}
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
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{moveItem(w.doc, "released")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.Apply(ctx, c.ID, "x"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "R1 is draft") {
		t.Fatalf("guard must refuse: %v", err)
	}

	// approving the requirement in the same change satisfies the guard; the action stamps it
	c2 := w.change(t)
	if _, err := w.g.AddItems(ctx, c2.ID, []domain.ChangeItem{moveItem(w.req, "approved"), moveItem(w.doc, "released")}); err != nil {
		t.Fatal(err)
	}
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
