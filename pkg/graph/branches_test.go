package graph

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func update(ref domain.NodeRef, props map[string]any) domain.ChangeItem {
	return domain.ChangeItem{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpUpdateNode,
		Node: &domain.NodeDraft{Base: &ref, Properties: props}}}
}

// applyOn creates a change on a branch from a baseline, adds items and applies it.
func applyOn(t *testing.T, g *Graph, branch string, from domain.BaselineID, items ...domain.ChangeItem) domain.Baseline {
	t.Helper()
	ctx := context.Background()
	c := must[domain.ChangeSet](t)(g.CreateChange(ctx, NewChange{Title: "c-" + branch, BaselineID: from, Branch: branch}))
	must[[]domain.ChangeItem](t)(g.AddItems(ctx, c.ID, items))
	return must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
}

func TestBranchMerge(t *testing.T) { forEachRepo(t, testBranchMerge) }

func testBranchMerge(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	req1 := f.req.Ref()

	if _, err := g.CreateChange(ctx, NewChange{Title: "x", BaselineID: f.base.ID, Branch: "opt-a"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("change on unknown branch: %v", err)
	}
	must[domain.Branch](t)(g.CreateBranch(ctx, NewBranch{Name: "opt-a", From: f.base.ID, Origin: "option:a"}))
	if _, err := g.CreateBranch(ctx, NewBranch{Name: "opt-a", From: f.base.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate branch: %v", err)
	}

	// opt-a: REQ-1 derived (v2) and a new design node refining it.
	c := must[domain.ChangeSet](t)(g.CreateChange(ctx, NewChange{Title: "option a", BaselineID: f.base.ID, Branch: "opt-a"}))
	items := must[[]domain.ChangeItem](t)(g.AddItems(ctx, c.ID, []domain.ChangeItem{
		update(req1, map[string]any{"title": "Use PSP v2", "psp": "stripe"}),
		{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Key: "DES-1", Type: "Design"}}},
	}))
	must[[]domain.ChangeItem](t)(g.AddItems(ctx, c.ID, []domain.ChangeItem{
		{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpAddLink,
			Link: &domain.LinkDraft{Type: "refines", From: domain.Endpoint{Item: items[1].ID}, To: domain.Endpoint{Node: &req1}}}},
	}))
	bA := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	if bA.Branch != "opt-a" || bA.Nodes[f.req.ID] != 2 {
		t.Fatalf("opt-a baseline: %+v", bA)
	}
	v2 := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: 2}))
	if v2.Branch != "opt-a" || v2.Reason != domain.ReasonDerive || !slices.Equal(v2.Parents, []domain.Version{1}) {
		t.Fatalf("derived version: %+v", v2)
	}
	if n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID})); n.Version != 1 {
		t.Fatalf("main latest must stay v1, got v%d", n.Version)
	}
	if h := must[domain.Baseline](t)(g.BranchHead(ctx, "opt-a")); h.ID != bA.ID {
		t.Fatalf("opt-a head %s, want %s", h.ID, bA.ID)
	}

	// main moves in parallel: REQ-1 v3 (revise of v1).
	bM := applyOn(t, g, "", f.base.ID, update(req1, map[string]any{"prio": "high"}))
	v3 := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: 3}))
	if v3.Branch != domain.MainBranch || v3.Reason != domain.ReasonRevise || bM.Nodes[f.req.ID] != 3 {
		t.Fatalf("main version: %+v", v3)
	}
	// a stale change on main now conflicts
	stale := must[domain.ChangeSet](t)(g.CreateChange(ctx, NewChange{Title: "stale", BaselineID: f.base.ID}))
	must[[]domain.ChangeItem](t)(g.AddItems(ctx, stale.ID, []domain.ChangeItem{update(req1, map[string]any{"x": 1})}))
	if _, err := g.Apply(ctx, stale.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale apply: %v", err)
	}

	anc := func() domain.NodeRef {
		var r domain.NodeRef
		must[struct{}](t)(struct{}{}, repo.InTx(ctx, func(tx Tx) (err error) { r, err = CommonAncestor(ctx, tx, f.req.ID, 3, 2); return }))
		return r
	}()
	if anc.Version != 1 {
		t.Fatalf("common ancestor v%d", anc.Version)
	}

	plan := must[MergePlan](t)(g.PlanMerge(ctx, "opt-a", ""))
	if len(plan.Candidates) != 2 || len(plan.Conflicting()) != 0 {
		t.Fatalf("plan: %+v", plan)
	}
	byKey := map[string]MergeCandidate{}
	for _, c := range plan.Candidates {
		byKey[c.Key] = c
	}
	if c := byKey["REQ-1"]; c.Kind != MergeThreeWay || c.Merged["prio"] != "high" || c.Merged["psp"] != "stripe" || c.Merged["title"] != "Use PSP v2" {
		t.Fatalf("REQ-1 candidate: %+v", c)
	}
	if c := byKey["DES-1"]; c.Kind != MergeAdded {
		t.Fatalf("DES-1 candidate: %+v", c)
	}

	res := must[MergeResult](t)(g.MergeBranch(ctx, MergeRequest{From: "opt-a", Into: domain.MainBranch}))
	b := res.Baseline
	if b.Branch != domain.MainBranch || b.Nodes[f.req.ID] != 4 {
		t.Fatalf("merged baseline: %+v", b)
	}
	v4 := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID}))
	if v4.Version != 4 || v4.Reason != domain.ReasonMerge || !slices.Equal(v4.Parents, []domain.Version{3, 2}) || v4.Properties["prio"] != "high" || v4.Properties["psp"] != "stripe" {
		t.Fatalf("merge version: %+v", v4)
	}
	nodes, links := must2(t)(g.BaselineGraph(ctx, b.ID))
	if len(nodes) != 4 {
		t.Fatalf("merged nodes: %+v", nodes)
	}
	var refines, satisfies bool
	for _, l := range links {
		refines = refines || (l.Type == "refines" && l.To == v4.Ref())
		satisfies = satisfies || (l.Type == "satisfies" && l.From == v4.Ref())
	}
	if !refines || !satisfies {
		t.Fatalf("merged links: %+v", links)
	}
	if br := must[domain.Branch](t)(g.Branch(ctx, "opt-a")); br.Status != domain.BranchMerged {
		t.Fatalf("branch status %s", br.Status)
	}
	if h := must[domain.Baseline](t)(g.BranchHead(ctx, "")); h.ID != b.ID {
		t.Fatalf("main head %s, want %s", h.ID, b.ID)
	}
	if _, err := g.PlanMerge(ctx, "opt-a", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("merged branch must not merge again: %v", err)
	}
}

func must2(t *testing.T) func([]domain.Node, []domain.Link, error) ([]domain.Node, []domain.Link) {
	return func(n []domain.Node, l []domain.Link, err error) ([]domain.Node, []domain.Link) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return n, l
	}
}

func TestBranchMergeConflict(t *testing.T) { forEachRepo(t, testBranchMergeConflict) }

func testBranchMergeConflict(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	req1 := f.req.Ref()
	must[domain.Branch](t)(g.CreateBranch(ctx, NewBranch{Name: "opt-b", From: f.base.ID}))
	applyOn(t, g, "opt-b", f.base.ID, update(req1, map[string]any{"title": "Use PSP B"}))
	applyOn(t, g, "", f.base.ID, update(req1, map[string]any{"title": "Use PSP M"}))

	plan := must[MergePlan](t)(g.PlanMerge(ctx, "opt-b", ""))
	if cs := plan.Conflicting(); len(cs) != 1 || !slices.Equal(cs[0].Conflicts, []string{"title"}) || cs[0].Merged["title"] != "Use PSP M" {
		t.Fatalf("conflicts: %+v", plan)
	}
	if _, err := g.MergeBranch(ctx, MergeRequest{From: "opt-b"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unresolved merge: %v", err)
	}
	res := must[MergeResult](t)(g.MergeBranch(ctx, MergeRequest{From: "opt-b", Resolutions: map[domain.NodeID]Resolution{
		f.req.ID: {Props: map[string]any{"title": "Use PSP B+M"}}}}))
	n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: res.Baseline.Nodes[f.req.ID]}))
	if n.Properties["title"] != "Use PSP B+M" || n.Reason != domain.ReasonMerge {
		t.Fatalf("resolved: %+v", n)
	}
}

func TestRebase(t *testing.T) { forEachRepo(t, testRebase) }

func testRebase(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	req1, test1 := f.req.Ref(), f.test.Ref()

	// C1 and C3 start from B1; C2 moves REQ-1 on main first.
	c1 := must[domain.ChangeSet](t)(g.CreateChange(ctx, NewChange{Title: "c1", BaselineID: f.base.ID}))
	it1 := must[[]domain.ChangeItem](t)(g.AddItems(ctx, c1.ID, []domain.ChangeItem{
		update(req1, map[string]any{"owner": "alice"}),
		{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpAddLink,
			Link: &domain.LinkDraft{Type: "covers", From: domain.Endpoint{Node: &test1}, To: domain.Endpoint{Node: &req1}}}},
	}))
	c3 := must[domain.ChangeSet](t)(g.CreateChange(ctx, NewChange{Title: "c3", BaselineID: f.base.ID}))
	it3 := must[[]domain.ChangeItem](t)(g.AddItems(ctx, c3.ID, []domain.ChangeItem{update(req1, map[string]any{"title": "Use PSP 3"})}))
	head := applyOn(t, g, "", f.base.ID, update(req1, map[string]any{"title": "Use PSP 2", "prio": "high"}))

	ds := must[[]Divergence](t)(g.Divergences(ctx, c1.ID))
	if len(ds) != 2 || len(ds[0].Conflicts) != 0 || ds[0].Merged["prio"] != "high" || ds[0].Merged["owner"] != "alice" {
		t.Fatalf("divergences: %+v", ds)
	}
	res := must[RebaseResult](t)(g.Rebase(ctx, c1.ID, nil))
	if res.Change.BaselineID != head.ID || len(res.Superseded) != 2 {
		t.Fatalf("rebase: %+v", res)
	}
	c := res.Change
	if c.EffectiveStatus(it1[0].ID) != domain.ItemSuperseded || c.Active(it1[0].ID) {
		t.Fatal("rebased item must be superseded")
	}
	repl, _ := c.Item(res.Superseded[it1[0].ID])
	if repl.Proposal.Node.Base.Version != 2 || len(repl.Proposal.Node.Properties) != 1 || repl.Proposal.Node.Properties["owner"] != "alice" {
		t.Fatalf("replacement: %+v", repl.Proposal.Node)
	}
	if len(c.ItemsOfKind(domain.KindMerge)) != 2 || c.Data["rebases"] == nil {
		t.Fatalf("rebase records: %+v", c.Data)
	}
	if ds := must[[]Divergence](t)(g.Divergences(ctx, c1.ID)); len(ds) != 0 {
		t.Fatalf("still diverged: %+v", ds)
	}
	b := must[domain.Baseline](t)(g.Apply(ctx, c1.ID, ""))
	n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID, Version: b.Nodes[f.req.ID]}))
	if n.Properties["owner"] != "alice" || n.Properties["prio"] != "high" || n.Properties["title"] != "Use PSP 2" {
		t.Fatalf("applied after rebase: %+v", n)
	}
	_, links := must2(t)(g.BaselineGraph(ctx, b.ID))
	var covers bool
	for _, l := range links {
		covers = covers || (l.Type == "covers" && l.To == n.Ref())
	}
	if !covers {
		t.Fatalf("rebased link must target the head version: %+v", links)
	}

	// conflicting divergence needs a resolution
	if _, err := g.Rebase(ctx, c3.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting rebase: %v", err)
	}
	res = must[RebaseResult](t)(g.Rebase(ctx, c3.ID, map[domain.ItemID]map[string]any{it3[0].ID: {"title": "Use PSP 3"}}))
	repl, _ = res.Change.Item(res.Superseded[it3[0].ID])
	if repl.Status != domain.ItemProposed || repl.Proposal.Node.Properties["title"] != "Use PSP 3" {
		t.Fatalf("resolved replacement: %+v", repl)
	}
	must[domain.Baseline](t)(g.Apply(ctx, c3.ID, ""))
}

func TestMergeProps(t *testing.T) {
	m, c := MergeProps(
		map[string]any{"a": 1, "b": 1, "c": 1, "d": 1},
		map[string]any{"a": 2, "b": 1, "c": 3, "e": 5},
		map[string]any{"a": 1, "b": 2, "c": 4, "d": 1},
	)
	if m["a"] != 2 || m["b"] != 2 || m["c"] != 3 || m["e"] != 5 || len(m) != 4 || !slices.Equal(c, []string{"c"}) {
		t.Fatalf("merged %v conflicts %v", m, c)
	}
}
