package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestChangeNodes(t *testing.T) { forEachRepo(t, testChangeNodes) }

func testChangeNodes(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, err := g.CreateChange(ctx, NewChange{Title: "PSP v2", BaselineID: f.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	pre, needRef := f.req.Ref(), f.need.Ref()

	// an impact: pre + intent + rationale, no post yet
	got, err := g.AddNodes(ctx, c.ID, []domain.ChangeNode{
		{Intent: domain.IntentModified, Pre: &pre, Rationale: "PSP v2 changes the payment API"},
		{Intent: domain.IntentCreated, Key: "TST-2", Type: "TestCase", Rationale: "cover the new API"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Key != "REQ-1" || got[0].Type != "Requirement" || got[0].Review != domain.ReviewProposed || !got[0].Planned() {
		t.Fatalf("key / type come from the pre node, review starts proposed: %+v", got[0])
	}
	if _, err := g.AddNodes(ctx, c.ID, []domain.ChangeNode{{Intent: domain.IntentModified, Pre: &pre, Rationale: "again"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a node appears once per change, got %v", err)
	}
	if _, err := g.AddNodes(ctx, c.ID, []domain.ChangeNode{{Intent: domain.IntentModified, Pre: &needRef}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a rationale is required, got %v", err)
	}
	if ch, _ := g.Change(ctx, c.ID); ch.Status != domain.ChangeActive || len(ch.Nodes) != 2 {
		t.Fatalf("change should be active with 2 nodes: %s %d", ch.Status, len(ch.Nodes))
	}

	// accepting needs a comment
	if _, err := g.ReviewNode(ctx, c.ID, got[0].ID, domain.ReviewAccepted, "alice", "  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a review needs a comment, got %v", err)
	}
	if _, err := g.ReviewNode(ctx, c.ID, got[0].ID, domain.ReviewAccepted, "alice", "impact confirmed with the PSP team"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ReviewNode(ctx, c.ID, got[0].ID, domain.ReviewRejected, "bob", "no"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a reviewed node cannot be reviewed again, got %v", err)
	}

	// realization: a version created by the change
	if _, err := g.RealizeNode(ctx, c.ID, got[0].ID, pre); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the pre version is not a post version, got %v", err)
	}
	var post domain.Node
	err = g.repo.InTx(ctx, func(tx Tx) error {
		post = domain.Node{ID: f.req.ID, Version: 2, Branch: domain.MainBranch, Parents: []domain.Version{1}, Reason: domain.ReasonRevise,
			Namespace: f.req.Namespace, Key: f.req.Key, Type: f.req.Type, Properties: map[string]any{"title": "Use PSP v2"}, ChangeID: c.ID, CreatedAt: g.now()}
		return tx.PutNode(ctx, post)
	})
	if err != nil {
		t.Fatal(err)
	}
	cn, err := g.RealizeNode(ctx, c.ID, got[0].ID, post.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if cn.Post == nil || *cn.Post != post.Ref() || cn.Planned() {
		t.Fatalf("post not set: %+v", cn)
	}
	n, err := g.Node(ctx, post.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if n.ChangeNode != got[0].ID || n.Comment != "impact confirmed with the PSP team" || n.ChangeID != c.ID {
		t.Fatalf("the version must record its origin: %+v", n)
	}

	list, err := g.ListChangeNodes(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Review != domain.ReviewAccepted || len(list[0].Reviews) != 1 || list[0].Reviews[0].By != "alice" || list[1].Key != "TST-2" || !list[1].Planned() {
		t.Fatalf("unexpected list: %+v", list)
	}
	if list[0].Pre == nil || *list[0].Pre != pre {
		t.Fatalf("pre lost: %+v", list[0])
	}
}

func TestApplyChangeNodes(t *testing.T) { forEachRepo(t, testApplyChangeNodes) }

func testApplyChangeNodes(t *testing.T, repo Repo) {
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
	nodes, err := g.AddNodes(ctx, c1.ID, []domain.ChangeNode{
		{Intent: domain.IntentModified, Pre: &pre, Rationale: "PSP v2 changes the API"},
		{Intent: domain.IntentCreated, Key: "TST-2", Type: "TestCase", Rationale: "cover the new API"},
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := g.AddNodes(ctx, c2.ID, []domain.ChangeNode{{Intent: domain.IntentModified, Pre: &pre, Rationale: "impact of PSP v3"}})
	if err != nil {
		t.Fatal(err)
	}

	// still proposed: the change cannot be applied
	if _, err := g.Apply(ctx, c1.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a change node awaiting review blocks Apply, got %v", err)
	}
	if _, err := g.WriteNode(ctx, c1.ID, nodes[0].ID, NodeWrite{Properties: map[string]any{"title": "Use PSP v2"}}); err != nil {
		t.Fatal(err)
	}
	req2, err := g.WriteNode(ctx, c1.ID, nodes[0].ID, NodeWrite{Properties: map[string]any{"owner": "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if req2.Post.Version != 3 {
		t.Fatalf("two writes = two versions on the branch, got %s", req2.Post)
	}
	tst, err := g.WriteNode(ctx, c1.ID, nodes[1].ID, NodeWrite{Properties: map[string]any{"title": "PSP v2 test"},
		AddLinks: []LinkWrite{{Type: "verifies", To: *req2.Post}}})
	if err != nil {
		t.Fatal(err)
	}
	// accepted but planned: refused
	if _, err := g.ReviewNode(ctx, c1.ID, nodes[0].ID, domain.ReviewAccepted, "alice", "as designed"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ReviewNode(ctx, c1.ID, nodes[1].ID, domain.ReviewAccepted, "alice", "needed"); err != nil {
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
	if head.ChangeID != c1.ID || head.ChangeNode != nodes[0].ID || head.Comment != "as designed" {
		t.Fatalf("landed version must record its origin: %+v", head)
	}
	list, err := g.ListChangeNodes(ctx, c1.ID)
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
	olist, err := g.ListChangeNodes(ctx, c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if olist[0].ID != other[0].ID || !olist[0].Recheck || olist[0].Pre == nil || *olist[0].Pre != head.Ref() {
		t.Fatalf("impact should move to the new head: %+v", olist[0])
	}
}

func TestChangeNodesLifecycle(t *testing.T) { forEachRepo(t, testChangeNodesLifecycle) }

func testChangeNodesLifecycle(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newLifecycleWorld(t, repo)
	g := w.g
	c, err := g.CreateChange(ctx, NewChange{Title: "edit REQ-1", BaselineID: w.base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	pre1, pre2 := w.req1.Ref(), w.req2.Ref()
	nodes, err := g.AddNodes(ctx, c.ID, []domain.ChangeNode{
		{Intent: domain.IntentModified, Pre: &pre1, Rationale: "reword"},
		{Intent: domain.IntentModified, Pre: &pre2, Rationale: "approve without title"},
	})
	if err != nil {
		t.Fatal(err)
	}
	review := func(id domain.ChangeNodeID) {
		t.Helper()
		if _, err := g.ReviewNode(ctx, c.ID, id, domain.ReviewAccepted, "alice", "ok"); err != nil {
			t.Fatal(err)
		}
	}
	title := NodeWrite{Properties: map[string]any{"title": "one v2"}}

	if _, err := g.WriteNode(ctx, c.ID, nodes[0].ID, title); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "not editable") {
		t.Fatalf("editing an approved node must be refused: %v", err)
	}
	if _, err := g.WriteNode(ctx, c.ID, nodes[0].ID, NodeWrite{State: "released"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no such transition: %v", err)
	}
	for _, step := range []NodeWrite{{State: "draft"}, title} {
		if _, err := g.WriteNode(ctx, c.ID, nodes[0].ID, step); err != nil {
			t.Fatal(err)
		}
	}
	// req2 goes proposed → draft → approved without the title the approval requires
	for _, st := range []string{"draft", "approved"} {
		if _, err := g.WriteNode(ctx, c.ID, nodes[1].ID, NodeWrite{State: st}); err != nil {
			t.Fatal(err)
		}
	}
	review(nodes[0].ID)
	review(nodes[1].ID)
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "editable state") {
		t.Fatalf("apply must refuse an editable leftover: %v", err)
	}
	if _, err := g.WriteNode(ctx, c.ID, nodes[0].ID, NodeWrite{State: "approved"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `attribute "title"`) {
		t.Fatalf("apply must refuse a transition that lacks its required attribute: %v", err)
	}
	if _, err := g.WriteNode(ctx, c.ID, nodes[1].ID, NodeWrite{State: "draft"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.WriteNode(ctx, c.ID, nodes[1].ID, NodeWrite{Properties: map[string]any{"title": "two"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.WriteNode(ctx, c.ID, nodes[1].ID, NodeWrite{State: "approved"}); err != nil {
		t.Fatal(err)
	}
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

func TestChangeNodesMerge(t *testing.T) { forEachRepo(t, testChangeNodesMerge) }

func testChangeNodesMerge(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	pre := f.req.Ref()
	run := func(title string, props map[string]any) domain.ChangeSet {
		t.Helper()
		c, err := g.CreateChange(ctx, NewChange{Title: title, BaselineID: f.base.ID, OwnBranch: true})
		if err != nil {
			t.Fatal(err)
		}
		ns, err := g.AddNodes(ctx, c.ID, []domain.ChangeNode{{Intent: domain.IntentModified, Pre: &pre, Rationale: title}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.ReviewNode(ctx, c.ID, ns[0].ID, domain.ReviewAccepted, "alice", "ok"); err != nil {
			t.Fatal(err)
		}
		// an impact accepted with nothing written is a confirmed impact: nothing to apply
		if _, err := g.WriteNode(ctx, c.ID, ns[0].ID, NodeWrite{Properties: props}); err != nil {
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
	if ch, _ := g.Change(ctx, b.ID); ch.Status != domain.ChangeMergePending {
		t.Fatalf("conflicting change must be merge_pending, is %s", ch.Status)
	}
	if _, err := g.MergeChange(ctx, b.ID, map[domain.NodeID]Resolution{f.req.ID: {Props: map[string]any{"title": "A+B", "owner": "carol"}}}); err != nil {
		t.Fatal(err)
	}
	head, _ = g.Node(ctx, domain.NodeRef{ID: f.req.ID})
	list, _ := g.ListChangeNodes(ctx, b.ID)
	if head.Properties["title"] != "A+B" || head.ChangeID != b.ID || list[0].Landed == nil || *list[0].Landed != head.Ref() {
		t.Fatalf("resolved merge must land as the version of b: %+v %+v", head, list)
	}
	if len(head.Parents) != 2 {
		t.Fatalf("a merge version has two parents: %+v", head.Parents)
	}
}
