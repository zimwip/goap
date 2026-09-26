package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func upd(id string, n domain.Node, title string, derived ...domain.ItemID) domain.ChangeItem {
	ref := n.Ref()
	return domain.ChangeItem{ID: domain.ItemID(id), Kind: domain.KindProposal, DerivedFrom: derived,
		Proposal: &domain.Proposal{Op: domain.OpUpdateNode, Node: &domain.NodeDraft{Base: &ref, Properties: map[string]any{"title": title}}}}
}

func statusOf(t *testing.T, g *Graph, c domain.ChangeID, item string) domain.ItemStatus {
	t.Helper()
	full, err := g.Change(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return full.EffectiveStatus(domain.ItemID(item))
}

func TestFlowRelaunchAdopt(t *testing.T) { forEachRepo(t, testFlowRelaunchAdopt) }

func testFlowRelaunchAdopt(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, _ := g.CreateChange(ctx, NewChange{Title: "flow", BaselineID: f.base.ID})
	// step 1 produced p1; step 2 produced p2 from p1; p3 came from elsewhere
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{upd("p1", f.req, "old"), upd("p2", f.need, "derived from p1", "p1"), upd("p3", f.test, "independent")}); err != nil {
		t.Fatal(err)
	}
	fl, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}, FromStep: 1, Reason: "the PSP answer changed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fl.Stale) != 2 || fl.Status != domain.FlowOpen {
		t.Fatalf("flow = %+v", fl)
	}
	for item, want := range map[string]domain.ItemStatus{"p1": domain.ItemStale, "p2": domain.ItemStale, "p3": domain.ItemProposed} {
		if got := statusOf(t, g, c.ID, item); got != want {
			t.Fatalf("%s = %s, want %s", item, got, want)
		}
	}
	// no apply while a flow is open
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("apply with an open flow: %v", err)
	}

	// the replanned run appends candidates on the flow
	cand := upd("c1", f.req, "new")
	cand.Flow = fl.ID
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{cand}); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, g, c.ID, "c1"); got != domain.ItemCandidate {
		t.Fatalf("c1 = %s", got)
	}
	main, _ := g.Blackboard(ctx, c.ID)
	for _, it := range main.Change.Items {
		if it.ID == "c1" {
			t.Fatal("main blackboard must not show candidates")
		}
	}
	fb, err := g.BlackboardIn(ctx, c.ID, fl.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[domain.ItemID]bool{}
	for _, it := range fb.Change.Items {
		seen[it.ID] = true
	}
	if seen["p1"] || seen["p2"] || !seen["p3"] || !seen["c1"] {
		t.Fatalf("flow view = %v", seen)
	}
	// a batch cannot mix flows, nor target a closed flow
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{cand, upd("x", f.test, "y")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mixed batch: %v", err)
	}

	if _, err := g.AdoptFlow(ctx, c.ID, fl.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	for item, want := range map[string]domain.ItemStatus{"p1": domain.ItemSuperseded, "p2": domain.ItemSuperseded, "p3": domain.ItemProposed, "c1": domain.ItemProposed} {
		if got := statusOf(t, g, c.ID, item); got != want {
			t.Fatalf("after adopt %s = %s, want %s", item, got, want)
		}
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID}); n.Properties["title"] != "new" {
		t.Fatalf("REQ-1 = %v", n.Properties)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.need.ID}); n.Version != 1 {
		t.Fatalf("the superseded derived update must not apply: %v", n.Version)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.test.ID}); n.Properties["title"] != "independent" {
		t.Fatalf("independent item lost: %v", n.Properties)
	}
	fs, _ := g.Flows(ctx, c.ID)
	if len(fs) != 1 || fs[0].Status != domain.FlowAdopted || fs[0].DecidedBy != "alice" {
		t.Fatalf("flows = %+v", fs)
	}
}

func TestFlowDiscard(t *testing.T) { forEachRepo(t, testFlowDiscard) }

func testFlowDiscard(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, _ := g.CreateChange(ctx, NewChange{Title: "flow", BaselineID: f.base.ID})
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{upd("p1", f.req, "old")}); err != nil {
		t.Fatal(err)
	}
	fl, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}})
	if err != nil {
		t.Fatal(err)
	}
	cand := upd("c1", f.req, "new")
	cand.Flow = fl.ID
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{cand}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.DiscardFlow(ctx, c.ID, fl.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, g, c.ID, "p1"); got != domain.ItemProposed {
		t.Fatalf("p1 = %s: the old run counts again", got)
	}
	if got := statusOf(t, g, c.ID, "c1"); got != domain.ItemRejected {
		t.Fatalf("c1 = %s", got)
	}
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{cand}); !errors.Is(err, ErrConflict) {
		t.Fatalf("adding to a discarded flow: %v", err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID}); n.Properties["title"] != "old" {
		t.Fatalf("REQ-1 = %v", n.Properties)
	}
	// a new relaunch is possible once the flow is decided
	c2, _ := g.CreateChange(ctx, NewChange{Title: "flow2", BaselineID: f.base.ID})
	if _, err := g.AddItems(ctx, c2.ID, []domain.ChangeItem{upd("q1", f.test, "t")}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.OpenFlow(ctx, c2.ID, OpenFlowRequest{Seeds: []domain.ItemID{"nope"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown seed: %v", err)
	}
}

func addFlowItem(t *testing.T, g *Graph, c domain.ChangeID, flow string, it domain.ChangeItem) {
	t.Helper()
	it.Flow = flow
	if _, err := g.AddItems(context.Background(), c, []domain.ChangeItem{it}); err != nil {
		t.Fatal(err)
	}
}

func TestParallelFlowsCompete(t *testing.T) { forEachRepo(t, testParallelFlowsCompete) }

func testParallelFlowsCompete(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, _ := g.CreateChange(ctx, NewChange{Title: "parallel", BaselineID: f.base.ID})
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{upd("p1", f.req, "old"), upd("p2", f.need, "derived", "p1"), upd("p3", f.test, "independent")}); err != nil {
		t.Fatal(err)
	}
	// three alternatives open at the same time: two replace p1, one replaces p3
	f1, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}, Reason: "alternative 1"})
	if err != nil {
		t.Fatal(err)
	}
	f2, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}, Reason: "alternative 2"})
	if err != nil {
		t.Fatalf("a second open flow must be allowed: %v", err)
	}
	f3, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p3"}})
	if err != nil {
		t.Fatal(err)
	}
	addFlowItem(t, g, c.ID, f1.ID, upd("c1", f.req, "one"))
	addFlowItem(t, g, c.ID, f2.ID, upd("c2", f.req, "two"))
	addFlowItem(t, g, c.ID, f3.ID, upd("c3", f.test, "three"))

	// each flow reads its own view: the other alternatives are invisible to it
	seen := func(flow string) map[domain.ItemID]bool {
		bb, err := g.BlackboardIn(ctx, c.ID, flow)
		if err != nil {
			t.Fatal(err)
		}
		out := map[domain.ItemID]bool{}
		for _, it := range bb.Change.Items {
			out[it.ID] = true
		}
		return out
	}
	if v := seen(f1.ID); !v["c1"] || v["c2"] || v["p1"] || !v["p3"] {
		t.Fatalf("view of flow 1 = %v", v)
	}
	if v := seen(f2.ID); v["c1"] || !v["c2"] {
		t.Fatalf("view of flow 2 = %v", v)
	}

	if _, err := g.AdoptFlow(ctx, c.ID, f1.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	fs, _ := g.Flows(ctx, c.ID)
	byID := map[string]domain.Flow{}
	for _, fl := range fs {
		byID[fl.ID] = fl
	}
	if got := byID[f2.ID].CompetesWith; len(got) != 1 || got[0] != f1.ID {
		t.Fatalf("flow 2 must compete with flow 1: %+v", byID[f2.ID])
	}
	if len(byID[f3.ID].CompetesWith) != 0 {
		t.Fatalf("flow 3 replaces other items: %+v", byID[f3.ID])
	}
	if _, err := g.AdoptFlow(ctx, c.ID, f2.ID, "alice"); !errors.Is(err, ErrConflict) {
		t.Fatalf("adopting a competing flow: %v", err)
	}
	if _, err := g.DiscardFlow(ctx, c.ID, f2.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.AdoptFlow(ctx, c.ID, f3.ID, "alice"); err != nil {
		t.Fatalf("independent flow: %v", err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.req.ID}); n.Properties["title"] != "one" {
		t.Fatalf("REQ-1 = %v", n.Properties)
	}
	if n, _ := g.Node(ctx, domain.NodeRef{ID: f.test.ID}); n.Properties["title"] != "three" {
		t.Fatalf("TST-1 = %v", n.Properties)
	}
}

func TestFlowGuidanceIsPrivateToTheBranch(t *testing.T) { forEachRepo(t, testFlowGuidance) }

func testFlowGuidance(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, _ := g.CreateChange(ctx, NewChange{Title: "guided", BaselineID: f.base.ID})
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{upd("p1", f.req, "old")}); err != nil {
		t.Fatal(err)
	}
	fl, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}, Guidance: "keep PSP v1 compatibility", By: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	has := func(flow string) bool {
		bb, _ := g.BlackboardIn(ctx, c.ID, flow)
		for _, it := range bb.Change.Items {
			if it.Type == "guidance" && it.Data["text"] == "keep PSP v1 compatibility" && it.ProducedBy == "alice" {
				return true
			}
		}
		return false
	}
	if !has(fl.ID) || has("") {
		t.Fatalf("guidance must be on the branch only: flow=%v main=%v", has(fl.ID), has(""))
	}
	// discarded: it disappears with the branch
	if _, err := g.DiscardFlow(ctx, c.ID, fl.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if has("") {
		t.Fatal("guidance leaked on the main flow")
	}
}

func TestFlowDomainBranchMergedOnAdopt(t *testing.T) {
	forEachRepo(t, testFlowDomainBranchMergedOnAdopt)
}

func testFlowDomainBranchMergedOnAdopt(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, err := g.CreateChange(ctx, NewChange{Title: "branchy", BaselineID: f.base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	reqRef := f.req.Ref()
	mk := domain.ChangeItem{ID: "n1", Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Key: "TST-9", Type: "TestCase"}}}
	link := domain.ChangeItem{ID: "l1", Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpAddLink,
		Link: &domain.LinkDraft{Type: "verifies", From: domain.Endpoint{Item: "n1"}, To: domain.Endpoint{Node: &reqRef}}}}
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{mk, link, upd("p3", f.need, "need v2"), upd("p1", f.req, "old")}); err != nil {
		t.Fatal(err)
	}
	fl, err := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}})
	if err != nil {
		t.Fatal(err)
	}
	addFlowItem(t, g, c.ID, fl.ID, upd("c1", f.req, "new"))

	// the candidate proposals (and the kept ones) are applied on a domain branch of the flow
	fl, err = g.MaterializeFlow(ctx, c.ID, fl.ID)
	if err != nil || fl.Branch == "" || len(fl.Materialized) != 4 {
		t.Fatalf("materialize = %+v, %v", fl, err)
	}
	b, err := g.Branch(ctx, fl.Branch)
	if err != nil || b.Status != domain.BranchOpen {
		t.Fatalf("branch = %+v, %v", b, err)
	}
	if n, err := g.NodeByKeyOn(ctx, "", fl.Branch, "REQ-1"); err != nil || n.Properties["title"] != "new" || n.Branch != fl.Branch {
		t.Fatalf("REQ-1 on the flow branch = %+v, %v", n, err)
	}
	if n, _ := g.NodeByKeyOn(ctx, "", c.Branch, "REQ-1"); n.Properties["title"] != "Use PSP v1" {
		t.Fatalf("the change branch must not see the preview yet: %v", n.Properties)
	}
	plan, err := g.PlanMerge(ctx, fl.Branch, c.Branch)
	if err != nil || len(plan.Candidates) < 3 {
		t.Fatalf("the reviewer can diff the branch: %+v, %v", plan, err)
	}
	// unchanged, materializing again does not open another branch
	if again, _ := g.MaterializeFlow(ctx, c.ID, fl.ID); again.Branch != fl.Branch {
		t.Fatalf("materialize twice: %q then %q", fl.Branch, again.Branch)
	}

	// adopting merges it into the change branch; the change then does not apply those proposals again
	fl, err = g.AdoptFlow(ctx, c.ID, fl.ID, "alice")
	if err != nil || len(fl.Merged) != 4 {
		t.Fatalf("adopt = %+v, %v", fl, err)
	}
	if b, _ := g.Branch(ctx, fl.Branch); b.Status != domain.BranchMerged {
		t.Fatalf("flow branch = %s", b.Status)
	}
	if n, _ := g.NodeByKeyOn(ctx, "", c.Branch, "REQ-1"); n.Properties["title"] != "new" {
		t.Fatalf("the change branch must have the flow: %v", n.Properties)
	}
	if is := mustIssues(t, g, c.ID, ""); len(is) != 0 {
		t.Fatalf("merged proposals must not look outdated: %+v", is)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatalf("apply after a merged flow: %v", err)
	}
	if got, _ := g.Change(ctx, c.ID); got.Status != domain.ChangeApplied {
		t.Fatalf("change = %s", got.Status)
	}
	for key, want := range map[string]string{"REQ-1": "new", "NEED-1": "need v2"} {
		if n, err := g.NodeByKey(ctx, "", key); err != nil || n.Properties["title"] != want {
			t.Fatalf("%s on main = %v, %v", key, n.Properties, err)
		}
	}
	tst, err := g.NodeByKey(ctx, "", "TST-9")
	if err != nil {
		t.Fatalf("TST-9 on main: %v", err)
	}
	v, _ := g.View(ctx, tst.Ref())
	linked := false
	for _, l := range v.Out {
		linked = linked || (l.Type == "verifies" && l.To.ID == f.req.ID)
	}
	if !linked {
		t.Fatalf("the link created through the flow is lost: %+v", v.Out)
	}
}

func TestDiscardedFlowAbandonsItsBranch(t *testing.T) {
	forEachRepo(t, testDiscardedFlowAbandonsItsBranch)
}

func testDiscardedFlowAbandonsItsBranch(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, _ := g.CreateChange(ctx, NewChange{Title: "d", BaselineID: f.base.ID, OwnBranch: true})
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{upd("p1", f.req, "old")}); err != nil {
		t.Fatal(err)
	}
	fl, _ := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}})
	addFlowItem(t, g, c.ID, fl.ID, upd("c1", f.req, "new"))
	fl, err := g.MaterializeFlow(ctx, c.ID, fl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.DiscardFlow(ctx, c.ID, fl.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if b, _ := g.Branch(ctx, fl.Branch); b.Status != domain.BranchAbandoned {
		t.Fatalf("branch = %s", b.Status)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.NodeByKey(ctx, "", "REQ-1"); n.Properties["title"] != "old" {
		t.Fatalf("REQ-1 = %v", n.Properties)
	}
}

func TestAdoptWithoutOwnBranchDoesNotMerge(t *testing.T) { forEachRepo(t, testAdoptWithoutOwnBranch) }

func testAdoptWithoutOwnBranch(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c, _ := g.CreateChange(ctx, NewChange{Title: "main", BaselineID: f.base.ID})
	if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{upd("p1", f.req, "old")}); err != nil {
		t.Fatal(err)
	}
	fl, _ := g.OpenFlow(ctx, c.ID, OpenFlowRequest{Seeds: []domain.ItemID{"p1"}})
	addFlowItem(t, g, c.ID, fl.ID, upd("c1", f.req, "new"))
	if fl, err := g.MaterializeFlow(ctx, c.ID, fl.ID); err != nil || fl.Branch == "" {
		t.Fatalf("materialize: %+v %v", fl, err)
	}
	fl, err := g.AdoptFlow(ctx, c.ID, fl.ID, "a")
	if err != nil || len(fl.Merged) != 0 {
		t.Fatalf("a change acting on main must not publish before it is applied: %+v %v", fl, err)
	}
	if n, _ := g.NodeByKey(ctx, "", "REQ-1"); n.Properties["title"] != "Use PSP v1" {
		t.Fatalf("main = %v", n.Properties)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.NodeByKey(ctx, "", "REQ-1"); n.Properties["title"] != "new" {
		t.Fatalf("main after apply = %v", n.Properties)
	}
}
