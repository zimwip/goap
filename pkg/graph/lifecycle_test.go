package graph

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(v)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

type lcWorld struct {
	g                *Graph
	req1, req2, spec domain.Node
	base             domain.Baseline
	reqLC, specLC    domain.Lifecycle
}

// Requirement: proposed → draft (editable) → approved → draft…; a Spec document
// contains requirements and can only be released when they are all approved.
func newLifecycleWorld(t *testing.T, repo Repo) lcWorld { return newLifecycleWorldG(t, repo, "") }

func newLifecycleWorldG(t *testing.T, repo Repo, approveGuard string) lcWorld {
	t.Helper()
	ctx := context.Background()
	w := lcWorld{g: New(repo)}
	w.reqLC = domain.Lifecycle{Initial: "proposed",
		States: []domain.LifecycleState{{Name: "proposed"}, {Name: "draft", Editable: true}, {Name: "approved"}},
		Transitions: []domain.Transition{
			{Name: "start", From: "proposed", To: "draft"},
			{Name: "approve", From: "draft", To: "approved", Guard: approveGuard, Requires: domain.TransitionRequires{Attributes: []string{"title"}}},
			{Name: "reopen", From: "approved", To: "draft"},
		}}
	w.specLC = domain.Lifecycle{Initial: "released",
		States: []domain.LifecycleState{{Name: "draft", Editable: true}, {Name: "released"}},
		Transitions: []domain.Transition{
			{Name: "reopen", From: "released", To: "draft"},
			{Name: "release", From: "draft", To: "released", Children: &domain.ChildrenRule{States: []string{"approved"}}},
		}}
	mk := func(key, typ string, props map[string]any, state string) domain.Node {
		n, err := w.g.CreateNode(ctx, NewNode{Key: key, Type: typ, Properties: props, State: state})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	tReq := mk("D:x/nodetype/Requirement", NodeTypeNode, map[string]any{"name": "Requirement", "lifecycle": asMap(t, w.reqLC)}, "")
	tSpec := mk("D:x/nodetype/Spec", NodeTypeNode, map[string]any{"name": "Spec", "document": asMap(t, domain.DocumentSpec{Contains: []string{"Requirement"}}), "lifecycle": asMap(t, w.specLC)}, "")
	tNote := mk("D:x/nodetype/Note", NodeTypeNode, map[string]any{"name": "Note"}, "")
	w.req1 = mk("REQ-1", "Requirement", map[string]any{"title": "one"}, "approved")
	w.req2 = mk("REQ-2", "Requirement", map[string]any{}, "proposed")
	w.spec = mk("SPEC-1", "Spec", map[string]any{"title": "spec"}, "released")
	for _, to := range []domain.Node{w.req1, w.req2} {
		if _, err := w.g.Link(ctx, domain.LinkContains, w.spec.Ref(), to.Ref(), nil); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	// Link bumps nothing: the spec still is version 1 with its links
	w.base, err = w.g.CreateBaseline(ctx, "B1", []domain.NodeRef{tReq.Ref(), tSpec.Ref(), tNote.Ref(), w.req1.Ref(), w.req2.Ref(), w.spec.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (w lcWorld) change(t *testing.T, title string) domain.ChangeSet {
	t.Helper()
	c, err := w.g.CreateChange(context.Background(), NewChange{Title: title, BaselineID: w.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func moveItem(n domain.Node, to string) domain.ChangeItem {
	ref := n.Ref()
	return domain.ChangeItem{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpTransitionNode, Node: &domain.NodeDraft{Base: &ref, State: to}}}
}

func updateItem(n domain.Node, props map[string]any) domain.ChangeItem {
	ref := n.Ref()
	return domain.ChangeItem{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpUpdateNode, Node: &domain.NodeDraft{Base: &ref, Properties: props}}}
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

	// an approved node is not editable
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{updateItem(w.req1, map[string]any{"title": "x"})}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not editable") {
		t.Fatalf("editing an approved node must be refused: %v", err)
	}
	// reopen, edit, approve, all in the change
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{moveItem(w.req1, "draft"), updateItem(w.req1, map[string]any{"title": "one v2"})}); err != nil {
		t.Fatal(err)
	}
	// the change is attached to the node
	if refs, _ := w.g.ChangeNodes(ctx, c.ID); len(refs) != 1 || refs[0] != w.req1.Ref() {
		t.Fatalf("attachments: %+v", refs)
	}
	if cs, _ := w.g.NodeChanges(ctx, w.req1.ID); len(cs) != 1 || cs[0].ID != c.ID {
		t.Fatalf("node changes: %+v", cs)
	}
	// leaving the node in draft is refused at Apply
	if _, err := w.g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "editable state") {
		t.Fatalf("apply must refuse an editable leftover: %v", err)
	}
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{moveItem(w.req1, "approved")}); err != nil {
		t.Fatal(err)
	}
	b, err := w.g.Apply(ctx, c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	n, err := stateOf(t, w.g, w.req1.ID)
	if err != nil || n.Version != 2 || n.State != "approved" || n.Properties["title"] != "one v2" || b.Nodes[w.req1.ID] != 2 {
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
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{moveItem(w.req1, "proposed")}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cannot go from approved to proposed") {
		t.Fatalf("unknown transition: %v", err)
	}
	// a node type without lifecycle has no transitions
	c = w.change(t, "no lifecycle")
	tn, _ := w.g.NodeByKey(ctx, "", "D:x/nodetype/Note")
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{moveItem(tn, "x")}); err == nil {
		t.Fatal("a NodeType has no lifecycle: transition must be refused")
	}
	// required attribute: REQ-2 has no title
	c = w.change(t, "approve without title")
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{moveItem(w.req2, "draft"), moveItem(w.req2, "approved")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `attribute "title" is required`) {
		t.Fatalf("requires.attributes: %v", err)
	}
	// with the attribute set before the approval it goes through
	c = w.change(t, "approve with title")
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{moveItem(w.req2, "draft"), updateItem(w.req2, map[string]any{"title": "two"}), moveItem(w.req2, "approved")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	// the failed Apply left nothing behind, and REQ-2 is approved now
	if n, _ := stateOf(t, w.g, w.req2.ID); n.Version != 2 || n.State != "approved" {
		t.Fatalf("REQ-2: %+v", n)
	}
}

func TestLifecycleParallelChangesConflict(t *testing.T) {
	forEachRepo(t, testLifecycleParallelChangesConflict)
}

func testLifecycleParallelChangesConflict(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	var cs []domain.ChangeSet
	for _, title := range []string{"first", "second"} {
		c := w.change(t, title)
		if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{moveItem(w.req1, "draft"), updateItem(w.req1, map[string]any{"title": title}), moveItem(w.req1, "approved")}); err != nil {
			t.Fatal(err)
		}
		cs = append(cs, c)
	}
	if att, _ := w.g.NodeChanges(ctx, w.req1.ID); len(att) != 2 {
		t.Fatalf("both changes are attached: %+v", att)
	}
	if _, err := w.g.Apply(ctx, cs[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.Apply(ctx, cs[1].ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("the second change must conflict: %v", err)
	}
}

func TestLifecycleCreateNode(t *testing.T) { forEachRepo(t, testLifecycleCreateNode) }

func testLifecycleCreateNode(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	create := func(state string) domain.ChangeItem {
		return domain.ChangeItem{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpCreateNode,
			Node: &domain.NodeDraft{Key: "REQ-9", Type: "Requirement", Properties: map[string]any{"title": "nine"}, State: state}}}
	}
	c := w.change(t, "create")
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{create("approved")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no transition proposed → approved: %v", err)
	}
	// created in the (non-editable) initial state
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{create("")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n, err := w.g.NodeByKey(ctx, "", "REQ-9"); err != nil || n.State != "proposed" {
		t.Fatalf("created node: %+v %v", n, err)
	}
	// created directly in an editable state: refused when applied
	c = w.change(t, "create draft")
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpCreateNode,
		Node: &domain.NodeDraft{Key: "REQ-10", Type: "Requirement", State: "draft"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "editable state") {
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
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{moveItem(w.spec, "draft"), moveItem(w.spec, "released")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "REQ-2") || !strings.Contains(err.Error(), "proposed") {
		t.Fatalf("children validation: %v", err)
	}
	// a change that approves REQ-2 as well: children are judged on their projected states
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{
		moveItem(w.req2, "draft"), updateItem(w.req2, map[string]any{"title": "two"}), moveItem(w.req2, "approved")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatalf("spec release with approved children: %v", err)
	}
	if n, _ := stateOf(t, w.g, w.spec.ID); n.State != "released" || n.Version != 2 {
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
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{moveItem(w.req1, "draft")}); err != nil {
		t.Fatalf("permissions are checked when applying, not when proposing: %v", err)
	}
	if _, err := w.g.Apply(ctx, c.ID, ""); !errors.Is(err, deny) {
		t.Fatalf("authorizer: %v", err)
	}
}

func TestLifecycleGuard(t *testing.T) { forEachRepo(t, testLifecycleGuard) }

func testLifecycleGuard(t *testing.T, repo Repo) {
	ctx := context.Background()
	// guard: a CEL predicate over the node
	w := newLifecycleWorldG(t, repo, `node.props.title.startsWith("ok")`)
	req1 := w.req1
	c := w.change(t, "guard")
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{moveItem(req1, "draft"), updateItem(req1, map[string]any{"title": "nope"}), moveItem(req1, "approved")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "guard not satisfied") {
		t.Fatalf("guard: %v", err)
	}
	c = w.change(t, "guard ok")
	if _, err := w.g.AddItems(ctx, c.ID, []domain.ChangeItem{moveItem(req1, "draft"), updateItem(req1, map[string]any{"title": "ok!"}), moveItem(req1, "approved")}); err != nil {
		t.Fatal(err)
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
