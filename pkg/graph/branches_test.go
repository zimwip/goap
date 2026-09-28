package graph

import (
	"context"
	"errors"
	"slices"
	"strings"
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

// upd is an edit of a node: new properties merged over the version the change starts from.
func setEdit(ref domain.NodeRef, props map[string]any) NodeEdit {
	return NodeEdit{Pre: &ref, Props: props}
}

// commitOn makes a change of edits that lands on a branch ("" = main) and returns its resulting baseline.
func commitOn(t *testing.T, g *Graph, branch string, from domain.BaselineID, edits ...NodeEdit) domain.Baseline {
	t.Helper()
	res, err := g.Commit(context.Background(), Commit{Title: "c-" + branch, Baseline: from, Branch: branch, By: "test", Edits: edits})
	if err != nil {
		t.Fatal(err)
	}
	return res.Baseline
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
	bA := commitOn(t, g, "opt-a", f.base.ID,
		setEdit(req1, map[string]any{"title": "Use PSP v2", "psp": "stripe"}),
		NodeEdit{Key: "DES-1", Type: "Design", Links: []LinkEdit{{Type: "refines", To: &req1}}})
	if bA.Branch != "opt-a" || bA.Nodes[f.req.ID] != 2 {
		t.Fatalf("opt-a baseline: %+v", bA)
	}
	versions := func() []domain.Node { return must[[]domain.Node](t)(g.Versions(ctx, f.req.ID)) }
	// written on the branch of the change, joined opt-a when the change landed there (ADR 0032)
	v2 := versions()[1]
	if !strings.HasPrefix(v2.Branch, "change-") || !slices.Equal(v2.Joined, []string{"opt-a"}) || v2.Reason != domain.ReasonDerive || !slices.Equal(v2.Parents, []domain.Version{1}) {
		t.Fatalf("derived version: %+v", v2)
	}
	if n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID})); n.Version != 1 {
		t.Fatalf("main latest must stay v1, got v%d", n.Version)
	}
	if h := must[domain.Baseline](t)(g.BranchHead(ctx, "", "opt-a")); h.ID != bA.ID {
		t.Fatalf("opt-a head %s, want %s", h.ID, bA.ID)
	}

	// main moves in parallel: REQ-1 v3, derived from v1 on the branch of the change and landed on main.
	bM := commitOn(t, g, "", f.base.ID, setEdit(req1, map[string]any{"prio": "high"}))
	v3 := versions()[2]
	if !slices.Equal(v3.Joined, []string{domain.MainBranch}) || !slices.Equal(v3.Parents, []domain.Version{1}) || bM.Nodes[f.req.ID] != 3 {
		t.Fatalf("main version: %+v", v3)
	}
	// a change that started before and changes the same property conflicts
	if _, err := g.Commit(ctx, Commit{Title: "stale", Baseline: f.base.ID, By: "test", Edits: []NodeEdit{setEdit(req1, map[string]any{"prio": "low"})}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale commit: %v", err)
	}

	anc := func() domain.NodeRef {
		var r domain.NodeRef
		must[struct{}](t)(struct{}{}, repo.InTx(ctx, func(tx Tx) (err error) { r, err = CommonAncestor(ctx, tx, f.req.ID, 3, 2); return }))
		return r
	}()
	if anc.Version != 1 {
		t.Fatalf("common ancestor v%d", anc.Version)
	}

	plan := must[MergePlan](t)(g.PlanMerge(ctx, "", "opt-a", ""))
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
	v4 := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID}))
	if b.Branch != domain.MainBranch || b.Nodes[f.req.ID] != v4.Version {
		t.Fatalf("merged baseline: %+v", b)
	}
	if v4.Version < 4 || v4.Reason != domain.ReasonMerge || !slices.Equal(v4.Parents, []domain.Version{3, 2}) || v4.Properties["prio"] != "high" || v4.Properties["psp"] != "stripe" {
		t.Fatalf("merge version: %+v", v4)
	}
	nodes, links := must2(t)(g.BaselineGraph(ctx, b.ID))
	if len(nodes) != 4 {
		t.Fatalf("merged nodes: %+v", nodes)
	}
	satisfies := false
	for _, l := range links {
		satisfies = satisfies || (l.Type == "satisfies" && l.From == v4.Ref())
	}
	if !satisfies {
		t.Fatalf("merged links: %+v", links)
	}
	// DES-1, added on opt-a only, joins main as is (no merge version, ADR 0032): its link to the REQ-1 it refined on
	// the branch is suspect, REQ-1 got a merge version with the changes of main
	des := must[domain.Node](t)(g.NodeByKey(ctx, "", "DES-1"))
	if des.Version != 1 || b.Nodes[des.ID] != 1 {
		t.Fatalf("DES-1 must land as is: %+v", des)
	}
	suspect := must[[]domain.Link](t)(g.SuspectLinks(ctx, b.ID))
	if !slices.ContainsFunc(suspect, func(l domain.Link) bool { return l.Type == "refines" && l.From == des.Ref() && l.To == v2.Ref() }) {
		t.Fatalf("suspect links: %+v", suspect)
	}
	if br := must[domain.Branch](t)(g.Branch(ctx, "", "opt-a")); br.Status != domain.BranchMerged {
		t.Fatalf("branch status %s", br.Status)
	}
	if h := must[domain.Baseline](t)(g.BranchHead(ctx, "", "")); h.ID != b.ID {
		t.Fatalf("main head %s, want %s", h.ID, b.ID)
	}
	if _, err := g.PlanMerge(ctx, "", "opt-a", ""); !errors.Is(err, ErrConflict) {
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
	commitOn(t, g, "opt-b", f.base.ID, setEdit(req1, map[string]any{"title": "Use PSP B"}))
	commitOn(t, g, "", f.base.ID, setEdit(req1, map[string]any{"title": "Use PSP M"}))

	plan := must[MergePlan](t)(g.PlanMerge(ctx, "", "opt-b", ""))
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

// A branch merge is a change of the platform: each merge version records the change impact that explains it.
func TestBranchMergeRecordsChangeImpacts(t *testing.T) {
	forEachRepo(t, testBranchMergeRecordsChangeImpacts)
}

func testBranchMergeRecordsChangeImpacts(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	must[domain.Branch](t)(g.CreateBranch(ctx, NewBranch{Name: "opt-a", From: f.base.ID}))
	commitOn(t, g, "opt-a", f.base.ID, setEdit(f.req.Ref(), map[string]any{"title": "on the branch"}), NodeEdit{Key: "DES-1", Type: "Design"})
	// main moves on the same node: a 3-way merge with a conflict, resolved by hand
	commitOn(t, g, "main", f.base.ID, setEdit(f.req.Ref(), map[string]any{"title": "on main"}))
	res, err := g.MergeBranch(ctx, MergeRequest{From: "opt-a", Into: "main", Resolutions: map[domain.NodeID]Resolution{f.req.ID: {Props: map[string]any{"title": "both"}}}})
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := g.ListChangeImpacts(ctx, res.Change.ID)
	if err != nil || len(nodes) != 2 {
		t.Fatalf("one change impact per merged node: %+v %v", nodes, err)
	}
	byKey := map[string]domain.ChangeImpact{}
	for _, n := range nodes {
		byKey[n.Key] = n
	}
	req, des := byKey["REQ-1"], byKey["DES-1"]
	if req.Intent != domain.IntentModified || req.Pre == nil || req.Landed == nil || req.Review != domain.ReviewAccepted || des.Intent != domain.IntentCreated || des.Pre != nil {
		t.Fatalf("change impacts: %+v %+v", req, des)
	}
	head, err := g.Node(ctx, domain.NodeRef{ID: f.req.ID})
	if err != nil || head.Reason != domain.ReasonMerge || head.Properties["title"] != "both" || head.ChangeImpact != req.ID || len(head.Parents) != 2 || head.Ref() != *req.Landed {
		t.Fatalf("merge version: %+v %v", head, err)
	}
	if head.Comment == "" || res.Change.Status != domain.ChangeApplied {
		t.Fatalf("origin: %+v", head)
	}
}

func TestBranchNamesAreScopedToTheirNamespace(t *testing.T) {
	forEachRepo(t, testBranchNamesAreScopedToTheirNamespace)
}

func testBranchNamesAreScopedToTheirNamespace(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	def, err := g.CreateNode(ctx, NewNode{Key: "N-1", Type: "Thing"})
	if err != nil {
		t.Fatal(err)
	}
	org, err := g.CreateNode(ctx, NewNode{Namespace: "organisation", Key: "N-1", Type: "OrgUnit"})
	if err != nil {
		t.Fatal(err)
	}
	defBase := must[domain.Baseline](t)(g.CreateBaseline(ctx, "", "B", []domain.NodeRef{def.Ref()}))
	orgBase := must[domain.Baseline](t)(g.CreateBaseline(ctx, "organisation", "B", []domain.NodeRef{org.Ref()}))
	// two namespaces can each have their own branch of the same name
	defBranch := must[domain.Branch](t)(g.CreateBranch(ctx, NewBranch{Name: "feature-x", From: defBase.ID}))
	orgBranch := must[domain.Branch](t)(g.CreateBranch(ctx, NewBranch{Namespace: "organisation", Name: "feature-x", From: orgBase.ID}))
	if defBranch.Namespace != domain.DefaultNamespace || orgBranch.Namespace != "organisation" {
		t.Fatalf("branch namespaces: %+v %+v", defBranch, orgBranch)
	}
	if got, err := g.Branch(ctx, "", "feature-x"); err != nil || got.ForkBaseline != defBase.ID {
		t.Fatalf("default feature-x: %+v %v", got, err)
	}
	if got, err := g.Branch(ctx, "organisation", "feature-x"); err != nil || got.ForkBaseline != orgBase.ID {
		t.Fatalf("organisation feature-x: %+v %v", got, err)
	}
}

func TestCreateBaselineFromLatestScopesToOneNamespace(t *testing.T) {
	forEachRepo(t, testCreateBaselineFromLatestScopesToOneNamespace)
}

func testCreateBaselineFromLatestScopesToOneNamespace(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	def, err := g.CreateNode(ctx, NewNode{Key: "N-1", Type: "Thing"})
	if err != nil {
		t.Fatal(err)
	}
	org, err := g.CreateNode(ctx, NewNode{Namespace: "organisation", Key: "N-1", Type: "OrgUnit"})
	if err != nil {
		t.Fatal(err)
	}
	snap := must[domain.Baseline](t)(g.CreateBaselineFromLatest(ctx, "organisation", "snap"))
	if snap.Namespace != "organisation" {
		t.Fatalf("snapshot namespace = %q", snap.Namespace)
	}
	if _, ok := snap.Nodes[org.ID]; !ok {
		t.Fatalf("snapshot must contain the organisation node: %+v", snap.Nodes)
	}
	if _, ok := snap.Nodes[def.ID]; ok {
		t.Fatalf("snapshot must not contain the default-namespace node: %+v", snap.Nodes)
	}
}

// A merge without conflict writes no version (ADR 0032): what changed on one side only joins the target as is.
func TestMergeWithoutConflictJoinsVersions(t *testing.T) {
	forEachRepo(t, testMergeWithoutConflictJoinsVersions)
}

func testMergeWithoutConflictJoinsVersions(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	// main moves on REQ-1, then a change that started before lands NEED-1 and a new node: a merge, not a fast-forward
	commitOn(t, g, "", f.base.ID, setEdit(f.req.Ref(), map[string]any{"prio": "high"}))
	res, err := g.Commit(ctx, Commit{Title: "late", Baseline: f.base.ID, By: "test", Edits: []NodeEdit{
		setEdit(f.need.Ref(), map[string]any{"title": "Pay online, twice"}),
		{Key: "DES-2", Type: "Design"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	vs := must[[]domain.Node](t)(g.Versions(ctx, f.need.ID))
	if len(vs) != 2 || vs[1].Reason == domain.ReasonMerge || !slices.Contains(vs[1].Joined, domain.MainBranch) {
		t.Fatalf("NEED-1 must join main as the version the change wrote: %+v", vs)
	}
	if n := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.need.ID})); n.Version != 2 || n.Properties["title"] != "Pay online, twice" {
		t.Fatalf("latest on main: %+v", n)
	}
	head := must[domain.Baseline](t)(g.BranchHead(ctx, "", ""))
	if head.ID != res.Baseline.ID || head.Nodes[f.need.ID] != 2 || head.Nodes[f.req.ID] != 2 {
		t.Fatalf("head: %+v, result %+v", head, res.Baseline)
	}
	des := must[domain.Node](t)(g.NodeByKey(ctx, "", "DES-2"))
	if des.Version != 1 || head.Nodes[des.ID] != 1 {
		t.Fatalf("DES-2 must land as is: %+v", des)
	}
	// the change impacts of the change landed as the versions they wrote, in the result baseline
	impacts := must[[]domain.ChangeImpact](t)(g.ListChangeImpacts(ctx, res.Change))
	for _, cn := range impacts {
		if cn.Landed == nil || cn.Post == nil || *cn.Landed != *cn.Post {
			t.Fatalf("landed: %+v", cn)
		}
	}
	events := must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, res.Change))
	landed := 0
	for _, e := range events {
		if e.Op == domain.ImpactLanded {
			landed++
			if e.Baseline != res.Baseline.ID {
				t.Fatalf("landed event baseline %s, want %s", e.Baseline, res.Baseline.ID)
			}
		}
	}
	if landed != len(impacts) {
		t.Fatalf("%d landed events for %d impacts", landed, len(impacts))
	}
}
