package graph

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// The recursive model of sub-changes (ADR 0103): a sub-change owns a branch forked from the branch of its parent,
// recursively, and the levels only meet through a commit, a rebase and a merge. These tests pin the three edges of it:
// a sub-change of a sub-change, the guardian of the change being merged, and a parent holding an uncommitted draft.

// nested is a root change with a sub-change and a sub-sub-change, each on its own branch.
type nested struct {
	w               orgWorld
	root, mid, leaf domain.Change
}

func newNested(t *testing.T, repo Repo) nested {
	t.Helper()
	ctx := context.Background()
	w := newOrgWorld(t, repo)
	n := nested{w: w}
	n.root = must[domain.Change](t)(w.g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "root", BaselineID: w.base.ID, OwnBranch: true}))
	n.mid = must[domain.Change](t)(w.g.CreateChange(ctx, NewChange{Title: "mid", ParentID: n.root.ID}))
	n.leaf = must[domain.Change](t)(w.g.CreateChange(ctx, NewChange{Title: "leaf", ParentID: n.mid.ID}))
	return n
}

// draftProps proposes a modification of node (as the change reads it) and edits its properties, leaving the draft
// unreviewed and uncommitted.
func draftProps(t *testing.T, g *Graph, c domain.Change, node domain.Node, props map[string]any) domain.ChangeImpactID {
	t.Helper()
	ctx := context.Background()
	pre := node.Ref()
	added := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: c.Title}}))
	if _, err := g.edit(ctx, c.ID, added[0].ID, edit{Properties: props}); err != nil {
		t.Fatal(err)
	}
	return added[0].ID
}

// commitProps drafts a modification, accepts it and commits it on the change's branch.
func commitProps(t *testing.T, g *Graph, c domain.Change, node domain.Node, props map[string]any) domain.ChangeImpactID {
	t.Helper()
	ctx := context.Background()
	id := draftProps(t, g, c, node, props)
	if _, err := g.accept(ctx, c.ID, id, "u", "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CommitChange(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	return id
}

// seen is a property of a node as a change reads it (its drafts, else its branch).
func seen(t *testing.T, g *Graph, c domain.Change, key, prop string) any {
	t.Helper()
	v := must[domain.NodeView](t)(g.ChangeNodeViewByKey(context.Background(), c.ID, "", domain.DefaultNamespace, key))
	return v.Properties[prop]
}

func headOf(t *testing.T, g *Graph, c domain.Change) domain.BaselineID {
	t.Helper()
	b := must[domain.Baseline](t)(g.BranchHead(context.Background(), c.Namespace, c.Branch))
	if b.ID != "" {
		return b.ID
	}
	// a change branch holds no baseline: its state is its fork and its commits
	return domain.BaselineID(fmt.Sprintf("%s@%d", b.ParentID, b.Seq))
}

func forkOf(t *testing.T, g *Graph, c domain.Change) domain.Branch {
	t.Helper()
	return must[domain.Branch](t)(g.Branch(context.Background(), c.Namespace, c.Branch))
}

// A sub-sub-change forks from the branch of its parent as that branch is when it is created: it sees what its parent
// committed, never its parent's drafts; its merge commits on its parent's branch only, and climbs one level per merge.
func TestSubSubChangeBranches(t *testing.T) { forEachRepo(t, testSubSubChangeBranches) }

func testSubSubChangeBranches(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newOrgWorld(t, repo)
	g := w.g
	root := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "root", BaselineID: w.base.ID, OwnBranch: true}))
	commitProps(t, g, root, w.cmp1, map[string]any{"title": "root's"})
	mid := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "mid", ParentID: root.ID}))
	if rb := forkOf(t, g, root); mid.BaselineID != rb.ForkBaseline {
		t.Fatalf("the middle change starts from the fork of the root's branch %s, not %s", rb.ForkBaseline, mid.BaselineID)
	}
	// a draft of the middle change is its own: its sub-change, created now, does not see it
	draftProps(t, g, mid, w.cmp2, map[string]any{"title": "mid's draft"})
	leaf := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "leaf", ParentID: mid.ID}))
	if b := must[domain.Branch](t)(g.Branch(ctx, leaf.Namespace, leaf.Branch)); b.Parent != mid.Branch || b.ForkBaseline != forkOf(t, g, mid).ForkBaseline || b.ForkSeq == 0 {
		t.Fatalf("the leaf forks from the head of its parent's branch %s: %+v", mid.Branch, b)
	}
	if got := seen(t, g, leaf, "CMP-1", "title"); got != "root's" {
		t.Fatalf("the leaf sees what the root committed: %v", got)
	}
	if got := seen(t, g, leaf, "CMP-2", "title"); got != "two" {
		t.Fatalf("the leaf does not see the draft of its parent: %v", got)
	}
	if got := seen(t, g, mid, "CMP-2", "title"); got != "mid's draft" {
		t.Fatalf("the middle change sees its own draft: %v", got)
	}

	// the leaf works on another node, merges into the middle change and nowhere else
	cmp3 := must[domain.Node](t)(g.NodeByKey(ctx, domain.DefaultNamespace, "CMP-3"))
	commitProps(t, g, leaf, cmp3, map[string]any{"title": "leaf's"})
	rootHead, mainHead := headOf(t, g, root), must[domain.Baseline](t)(g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)).ID
	if _, err := g.IntegrateChange(ctx, leaf.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := seen(t, g, mid, "CMP-3", "title"); got != "leaf's" {
		t.Fatalf("the middle change holds the leaf's work: %v", got)
	}
	if got := seen(t, g, mid, "CMP-2", "title"); got != "mid's draft" {
		t.Fatalf("the merge keeps the middle change's own draft: %v", got)
	}
	if headOf(t, g, root) != rootHead || seen(t, g, root, "CMP-3", "title") != "three" {
		t.Fatal("the leaf's merge reached the root's branch")
	}
	if must[domain.Baseline](t)(g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)).ID != mainHead {
		t.Fatal("the leaf's merge reached main")
	}
	// the middle change records the leaf's work as its own: accepted, committed
	var recorded *domain.ChangeImpact
	for _, cn := range must[domain.Change](t)(g.Change(ctx, mid.ID)).Nodes {
		if cn.Key == "CMP-3" {
			cn := cn
			recorded = &cn
		}
	}
	if recorded == nil || recorded.Review != domain.ReviewAccepted || recorded.Committed == nil {
		t.Fatalf("the middle change records the merged node, accepted and committed: %+v", recorded)
	}
	if c := must[domain.Change](t)(g.Change(ctx, leaf.ID)); c.Status != domain.ChangeApplied {
		t.Fatalf("the merged leaf is %s", c.Status)
	}
	// a merged sub-change takes no sub-change
	if _, err := g.CreateChange(ctx, NewChange{Title: "late", ParentID: leaf.ID}); err == nil {
		t.Fatal("a sub-change of a merged change was created")
	}

	// the middle change commits its own draft, merges into the root, the root commits everything on main
	mcn := must[domain.Change](t)(g.Change(ctx, mid.ID))
	for _, cn := range mcn.Nodes {
		if cn.Key == "CMP-2" {
			if _, err := g.accept(ctx, mid.ID, cn.ID, "u", "ok"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := g.Apply(ctx, mid.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, root.ID, ""); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"CMP-1": "root's", "CMP-2": "mid's draft", "CMP-3": "leaf's"} {
		if n := must[domain.Node](t)(g.NodeByKey(ctx, domain.DefaultNamespace, key)); n.Properties["title"] != want {
			t.Fatalf("%s on main = %v, want %q", key, n.Properties["title"], want)
		}
	}
}

// The guardian of a change holds its merge into its parent like a root change's into main (ADR 0102): it is asked for
// the change being merged, with the versions it commits; a refusal leaves the parent's branch as it was.
func TestSubChangeMergeGuardian(t *testing.T) { forEachRepo(t, testSubChangeMergeGuardian) }

func testSubChangeMergeGuardian(t *testing.T, repo Repo) {
	ctx := context.Background()
	n := newNested(t, repo)
	g := n.w.g
	commitProps(t, g, n.leaf, n.w.cmp3, map[string]any{"title": "leaf's"})

	var asked []domain.ChangeID
	var judged []string
	refuse := map[domain.ChangeID]bool{n.leaf.ID: true, n.mid.ID: true}
	g.Lifecycles = mergeGate(func(_ context.Context, c domain.Change, bb domain.Blackboard) (bool, bool, error) {
		asked = append(asked, c.ID)
		judged = judged[:0]
		for _, v := range bb.Nodes {
			if title, _ := v.Properties["title"].(string); title != "" {
				judged = append(judged, title)
			}
		}
		return true, !refuse[c.ID], nil
	})
	midHead := headOf(t, g, n.mid)
	if _, err := g.IntegrateChange(ctx, n.leaf.ID, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the guardian refuses the leaf's merge: %v", err)
	}
	if len(asked) != 1 || asked[0] != n.leaf.ID {
		t.Fatalf("the guardian is asked for the change being merged only: %v", asked)
	}
	hasLeaf := false
	for _, s := range judged {
		hasLeaf = hasLeaf || s == "leaf's"
	}
	if !hasLeaf {
		t.Fatalf("the guardian judges the version the leaf commits: %v", judged)
	}
	if headOf(t, g, n.mid) != midHead || seen(t, g, n.mid, "CMP-3", "title") != "three" {
		t.Fatal("a refused merge moved the parent's branch")
	}
	if len(must[domain.Change](t)(g.Change(ctx, n.mid.ID)).Nodes) != 0 {
		t.Fatal("a refused merge recorded impacts in the parent")
	}
	if c := must[domain.Change](t)(g.Change(ctx, n.leaf.ID)); c.Status == domain.ChangeApplied {
		t.Fatal("a refused merge applied the leaf")
	}

	// let the leaf go: the middle change holds its work; its own guardian then holds the merge into the root
	refuse[n.leaf.ID] = false
	if _, err := g.IntegrateChange(ctx, n.leaf.ID, nil); err != nil {
		t.Fatal(err)
	}
	asked = nil
	rootHead := headOf(t, g, n.root)
	if _, err := g.IntegrateChange(ctx, n.mid.ID, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the guardian refuses the middle change's merge: %v", err)
	}
	if len(asked) != 1 || asked[0] != n.mid.ID {
		t.Fatalf("the guardian is asked for the middle change: %v", asked)
	}
	if headOf(t, g, n.root) != rootHead {
		t.Fatal("a refused merge moved the root's branch")
	}
	if got := seen(t, g, n.mid, "CMP-3", "title"); got != "leaf's" {
		t.Fatalf("refusing the middle change's merge leaves the leaf's merge in it: %v", got)
	}
	refuse[n.mid.ID] = false
	if _, err := g.IntegrateChange(ctx, n.mid.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := seen(t, g, n.root, "CMP-3", "title"); got != "leaf's" {
		t.Fatalf("the root holds the work of both levels: %v", got)
	}
}

// A parent holding an uncommitted draft (ADR 0103): of the node its sub-change merges, it holds the merge until it
// commits or cancels it; of another node, or with no modification, it holds nothing and is kept as it is. What the
// parent committed meanwhile is rebased into the sub-change at its merge: merged when the two touched different
// properties, a conflict to settle in the sub-change when they touched the same.
func TestSubChangeUnderParentDraft(t *testing.T) {
	ctx := context.Background()
	run := func(name string, f func(t *testing.T, repo Repo)) {
		t.Run(name, func(t *testing.T) { forEachRepo(t, f) })
	}
	run("same node", func(t *testing.T, repo Repo) {
		n := newNested(t, repo)
		g := n.w.g
		commitProps(t, g, n.leaf, n.w.cmp3, map[string]any{"title": "leaf's"})
		held := draftProps(t, g, n.mid, n.w.cmp3, map[string]any{"title": "mid's draft"})
		midHead := headOf(t, g, n.mid)
		if _, err := g.IntegrateChange(ctx, n.leaf.ID, nil); !errors.Is(err, ErrConflict) {
			t.Fatalf("merge under an uncommitted draft of the parent: %v", err)
		}
		if headOf(t, g, n.mid) != midHead || seen(t, g, n.mid, "CMP-3", "title") != "mid's draft" {
			t.Fatal("the refused merge touched the parent's branch or its draft")
		}
		if c := must[domain.Change](t)(g.Change(ctx, n.leaf.ID)); c.Status == domain.ChangeApplied {
			t.Fatal("the leaf was applied")
		}
		// the parent cancels its draft: the leaf merges
		if _, err := g.ImpactNodeCancel(ctx, n.mid.ID, held, "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := g.IntegrateChange(ctx, n.leaf.ID, nil); err != nil {
			t.Fatal(err)
		}
		if got := seen(t, g, n.mid, "CMP-3", "title"); got != "leaf's" {
			t.Fatalf("the parent holds the leaf's work: %v", got)
		}
	})

	run("checkout without modification", func(t *testing.T, repo Repo) {
		n := newNested(t, repo)
		g := n.w.g
		commitProps(t, g, n.leaf, n.w.cmp3, map[string]any{"title": "leaf's"})
		pre := n.w.cmp3.Ref()
		added := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, n.mid.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "look"}}))
		if _, err := g.ImpactNodeCheckout(ctx, n.mid.ID, NodeCheckout{Impact: added[0].ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := g.IntegrateChange(ctx, n.leaf.ID, nil); err != nil {
			t.Fatalf("a draft that modifies nothing holds no merge: %v", err)
		}
		if got := seen(t, g, n.mid, "CMP-3", "title"); got != "leaf's" {
			t.Fatalf("the parent holds the leaf's work: %v", got)
		}
	})

	run("another node", func(t *testing.T, repo Repo) {
		n := newNested(t, repo)
		g := n.w.g
		commitProps(t, g, n.leaf, n.w.cmp3, map[string]any{"title": "leaf's"})
		other := draftProps(t, g, n.mid, n.w.cmp2, map[string]any{"title": "mid's draft"})
		if _, err := g.IntegrateChange(ctx, n.leaf.ID, nil); err != nil {
			t.Fatalf("a draft of another node holds no merge: %v", err)
		}
		if got := seen(t, g, n.mid, "CMP-3", "title"); got != "leaf's" {
			t.Fatalf("the parent holds the leaf's work: %v", got)
		}
		if got := seen(t, g, n.mid, "CMP-2", "title"); got != "mid's draft" {
			t.Fatalf("the parent keeps its draft: %v", got)
		}
		// and commits it afterwards, on top of the leaf's work
		if _, err := g.accept(ctx, n.mid.ID, other, "u", "ok"); err != nil {
			t.Fatal(err)
		}
		if _, err := g.CommitChange(ctx, n.mid.ID, ""); err != nil {
			t.Fatal(err)
		}
		head := must[domain.Baseline](t)(g.BranchHead(ctx, n.mid.Namespace, n.mid.Branch))
		if head.Nodes[n.w.cmp2.ID] == n.w.cmp2.Version || head.Nodes[n.w.cmp3.ID] == n.w.cmp3.Version {
			t.Fatalf("the parent's branch holds both nodes changed: %v", head.Nodes)
		}
	})

	run("committed meanwhile, other property", func(t *testing.T, repo Repo) {
		n := newNested(t, repo)
		g := n.w.g
		commitProps(t, g, n.leaf, n.w.cmp3, map[string]any{"title": "leaf's"})
		commitProps(t, g, n.mid, n.w.cmp3, map[string]any{"note": "mid's"})
		if _, err := g.IntegrateChange(ctx, n.leaf.ID, nil); err != nil {
			t.Fatalf("a clean rebase onto the parent's commit merges: %v", err)
		}
		if seen(t, g, n.mid, "CMP-3", "title") != "leaf's" || seen(t, g, n.mid, "CMP-3", "note") != "mid's" {
			t.Fatalf("the parent holds both: %v / %v", seen(t, g, n.mid, "CMP-3", "title"), seen(t, g, n.mid, "CMP-3", "note"))
		}
	})

	run("committed meanwhile, same property", func(t *testing.T, repo Repo) {
		n := newNested(t, repo)
		g := n.w.g
		leafImpact := commitProps(t, g, n.leaf, n.w.cmp3, map[string]any{"title": "leaf's"})
		commitProps(t, g, n.mid, n.w.cmp3, map[string]any{"title": "mid's"})
		midHead := headOf(t, g, n.mid)
		if _, err := g.IntegrateChange(ctx, n.leaf.ID, nil); !errors.Is(err, ErrConflict) {
			t.Fatalf("the rebase meets the parent's commit: %v", err)
		}
		if headOf(t, g, n.mid) != midHead || seen(t, g, n.mid, "CMP-3", "title") != "mid's" {
			t.Fatal("a conflicting merge touched the parent's branch")
		}
		cn := must[domain.ChangeImpact](t)(g.seenImpact(ctx, n.leaf.ID, leafImpact, ""))
		if cn.Review != domain.ReviewProposed {
			t.Fatalf("the conflict sends the leaf's impact back to review: %s", cn.Review)
		}
		// settled in the leaf, reviewed, committed: it merges
		if _, err := g.edit(ctx, n.leaf.ID, leafImpact, edit{Properties: map[string]any{"title": "both"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := g.accept(ctx, n.leaf.ID, leafImpact, "u", "settled"); err != nil {
			t.Fatal(err)
		}
		if _, err := g.CommitChange(ctx, n.leaf.ID, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := g.IntegrateChange(ctx, n.leaf.ID, nil); err != nil {
			t.Fatal(err)
		}
		if got := seen(t, g, n.mid, "CMP-3", "title"); got != "both" {
			t.Fatalf("the parent holds the settled title: %v", got)
		}
	})
}
