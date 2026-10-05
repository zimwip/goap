package graph

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// A node has no version during a change (ADR 0079): the repository holds none for a creation and no new one for an
// edit, whatever the number of operations; the draft is the fold of the log, which tells every edit.
func TestNoVersionExistsDuringAChange(t *testing.T) { forEachRepo(t, testNoVersionExistsDuringAChange) }

func testNoVersionExistsDuringAChange(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "drafts", BaselineID: f.base.ID, OwnBranch: true}))
	made := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "TST-9", Type: "TestCase", Rationale: "new",
		Links: []LinkWrite{{Type: "verifies", To: f.req.Ref()}}}))
	pre := f.req.Ref()
	mod := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "edit"}}))
	var edited domain.ChangeImpact
	for i := 0; i < 3; i++ {
		edited = must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, mod[0].ID, edit{Properties: map[string]any{"title": "t" + string(rune('a'+i))}}))
	}
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, made.ID, edit{Properties: map[string]any{"title": "nine"}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, made.ID, "bob", "ok"))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, mod[0].ID, "bob", "ok"))

	err := g.repo.InTx(ctx, func(tx Tx) error {
		if _, err := tx.Versions(ctx, made.Post.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("a created node has no node_version row: %v", err)
		}
		if _, err := tx.NodeIDByKey(ctx, domain.DefaultNamespace, "TST-9"); !errors.Is(err, ErrNotFound) {
			t.Errorf("nor a node row, its key is free: %v", err)
		}
		vs, err := tx.Versions(ctx, f.req.ID)
		if err != nil || len(vs) != int(f.req.Version) {
			t.Errorf("an edited node gets no version: %d, %v", len(vs), err)
		}
		drafts, err := g.drafts(ctx, tx, c.ID)
		if err != nil || len(drafts) != 2 {
			t.Errorf("two drafts in the fold: %+v %v", drafts, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// only the change reads the drafts: main still has the last landed version
	if n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID})); n.Properties["title"] != "Use PSP v1" {
		t.Fatalf("main reads the landed version: %+v", n)
	}
	if v := must[domain.NodeView](t)(g.ChangeNodeView(ctx, c.ID, "", *edited.Post)); v.Properties["title"] != "tc" || !v.IsDraft() {
		t.Fatalf("the change reads its draft: %+v", v.Node)
	}
	var updates int
	for _, e := range must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, c.ID)) {
		if e.Op == domain.ImpactUpdated && e.Impact == mod[0].ID {
			updates++
		}
	}
	if updates != 3 {
		t.Fatalf("every edit is an updated event: %d", updates)
	}
}

// Landing writes one version per node from its draft (ADR 0079): the next number of the node, the version it was checked
// out from as its parent, the owner and the origins of the draft, the links of the draft with their targets resolved to
// the versions this landing writes for the nodes of the change (a draft reference, or the version a draft was checked
// out from).
func TestLandingWritesOneVersionPerDraft(t *testing.T) {
	forEachRepo(t, testLandingWritesOneVersionPerDraft)
}

func testLandingWritesOneVersionPerDraft(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "land", BaselineID: f.base.ID, OwnBranch: true}))
	pre := f.req.Ref()
	mod := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "edit"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, mod.ID, NodeUpdate{Properties: map[string]any{"title": "Use PSP v2"}}))
	// two new nodes link each other, and REQ-1: one by its draft, one by the version it was checked out from
	a := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "TST-A", Type: "TestCase", Rationale: "a", Links: []LinkWrite{{Type: "verifies", To: *mod.Post}}}))
	b := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "TST-B", Type: "TestCase", Rationale: "b", Links: []LinkWrite{{Type: "verifies", To: pre}}}))
	// a link from a created node to another one, in both directions: a cycle among drafts
	must[domain.Link](t)(g.ImpactLinkCreate(ctx, c.ID, a.ID, LinkWrite{Type: "refines", To: *b.Post}, "", ""))
	if view := must[domain.NodeView](t)(g.ChangeNodeView(ctx, c.ID, "", *b.Post)); len(view.In) != 1 || view.In[0].From != *a.Post {
		t.Fatalf("a link between drafts is read from both ends: %+v", view.In)
	}
	for _, cn := range []domain.ChangeImpact{mod, a, b} {
		must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, cn.ID, "bob", "ok"))
	}
	res := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))

	req2 := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: res.Nodes[f.req.ID]}))
	if req2.Version != pre.Version+1 || !slices.Equal(req2.Parents, []domain.Version{pre.Version}) || req2.Owner != f.req.Owner || req2.ChangeID != c.ID || req2.ChangeImpact != mod.ID {
		t.Fatalf("REQ-1 v2: %+v", req2)
	}
	for _, d := range []struct {
		key string
		cn  domain.ChangeImpact
	}{{"TST-A", a}, {"TST-B", b}} {
		n := must[domain.Node](t)(g.NodeByKey(ctx, domain.DefaultNamespace, d.key))
		if n.Version != 1 || len(n.Parents) != 0 || n.Reason != domain.ReasonCreate || n.ID != d.cn.Post.ID || res.Nodes[n.ID] != 1 || n.Owner == "" || n.Project == "" {
			t.Fatalf("%s: %+v", d.key, n)
		}
		if g.versionCount(ctx, n.ID) != 1 {
			t.Fatalf("exactly one version of %s", d.key)
		}
		verifies := 0
		for _, l := range must[[]domain.Link](t)(g.OutLinksOf(ctx, n.Ref())) {
			if l.Type == "verifies" {
				verifies++
				if l.To != req2.Ref() {
					t.Fatalf("%s verifies %s, the version this landing wrote is %s", d.key, l.To, req2.Ref())
				}
			}
		}
		if verifies != 1 {
			t.Fatalf("%s: %d verifies links", d.key, verifies)
		}
	}
	tstA := must[domain.Node](t)(g.NodeByKey(ctx, domain.DefaultNamespace, "TST-A"))
	tstB := must[domain.Node](t)(g.NodeByKey(ctx, domain.DefaultNamespace, "TST-B"))
	var refines []domain.NodeRef
	for _, l := range must[[]domain.Link](t)(g.OutLinksOf(ctx, tstA.Ref())) {
		if l.Type == "refines" {
			refines = append(refines, l.To)
		}
	}
	if len(refines) != 1 || refines[0] != tstB.Ref() {
		t.Fatalf("a link between drafts targets the version written for the node: %v", refines)
	}
	// the links REQ-1 carried follow it
	if out := must[[]domain.Link](t)(g.OutLinksOf(ctx, req2.Ref())); len(out) != 1 || out[0].Type != "satisfies" {
		t.Fatalf("carried links: %+v", out)
	}
	// every landed event of the change names the version written
	for _, cn := range must[[]domain.ChangeImpact](t)(g.ListChangeImpacts(ctx, c.ID)) {
		if cn.Landed == nil || cn.Post == nil || *cn.Post != *cn.Landed || cn.Post.IsDraft() {
			t.Fatalf("post is rewritten to the version written: %+v", cn)
		}
	}
}

// A draft equal to the version it was checked out from still lands a version of its own (an accepted checkout is a
// decision of the reviewer: no comparison of the content is made).
func TestUnchangedDraftStillLands(t *testing.T) { forEachRepo(t, testUnchangedDraftStillLands) }

func testUnchangedDraftStillLands(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "touch", BaselineID: f.base.ID, OwnBranch: true}))
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "look"}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, cn.ID, "bob", "unchanged"))
	res := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID}))
	if n.Version != f.req.Version+1 || res.Nodes[f.req.ID] != n.Version || n.Properties["title"] != f.req.Properties["title"] || n.Comment != "unchanged" {
		t.Fatalf("a version of its own, identical: %+v", n)
	}
}

// A flow checks a node out once: its draft is a copy of what the flow sees, the draft of its parent flow when that one
// holds one (not the stored version), and the parent's later edits do not reach it (ADR 0079).
func TestChildFlowForksTheParentDraft(t *testing.T) { forEachRepo(t, testChildFlowForksTheParentDraft) }

func testChildFlowForksTheParentDraft(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "flows", BaselineID: f.base.ID, OwnBranch: true}))
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "edit"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "main one"}}))
	child := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "child", Hypothesis: "h", By: "u"}))
	// the child flow sees the main draft, but it is the main flow's to edit
	if _, err := g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "child"}, Flow: child.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a draft of the parent flow is not edited from the child: %v", err)
	}
	co := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: cn.ID, Flow: child.ID}))
	d := must[*domain.Draft](t)(g.draftOf(ctx, c.ID, child.ID, cn.ID))
	if d == nil || d.Flow != child.ID || d.Properties["title"] != "main one" || d.Base == nil || *d.Base != f.req.Ref() || !co.Post.IsDraft() {
		t.Fatalf("the child's draft is a copy of the parent's: %+v", d)
	}
	// each edits its own
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "main two"}}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "child two"}, Flow: child.ID}))
	if got := must[domain.Node](t)(g.ChangeNode(ctx, c.ID, child.ID, *co.Post)); got.Properties["title"] != "child two" {
		t.Fatalf("child: %+v", got)
	}
	if got := must[domain.Node](t)(g.ChangeNode(ctx, c.ID, "", *co.Post)); got.Properties["title"] != "main two" {
		t.Fatalf("main: %+v", got)
	}
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: cn.ID, Flow: child.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("once per change and flow: %v", err)
	}
	// cancelling the child's draft falls back to the parent's
	back := must[domain.ChangeImpact](t)(g.ImpactNodeCancel(ctx, c.ID, cn.ID, child.ID, ""))
	if back.Post == nil || !back.Post.IsDraft() {
		t.Fatalf("back to the parent's draft: %+v", back)
	}
	if got := must[domain.Node](t)(g.ChangeNode(ctx, c.ID, child.ID, *co.Post)); got.Properties["title"] != "main two" {
		t.Fatalf("the child sees the parent's draft again: %+v", got)
	}
}
