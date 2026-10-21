package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A change is seen at three levels of its change impacts (ADR 0032): written, accepted, committed.
func TestChangeViewLevels(t *testing.T) { forEachRepo(t, testChangeViewLevels) }

func testChangeViewLevels(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "PSP v2", BaselineID: f.base.ID, OwnBranch: true}))
	req, need := f.req.Ref(), f.need.Ref()
	added := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{
		{Intent: domain.IntentModified, Pre: &req, Rationale: "new PSP"},
		{Intent: domain.IntentModified, Pre: &need, Rationale: "reword"},
		{Intent: domain.IntentModified, Pre: new(f.test.Ref()), Rationale: "obsolete"},
	}))
	reqW := must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, added[0].ID, edit{Properties: map[string]any{"title": "Use PSP v2"}}))
	needW := must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, added[1].ID, edit{Properties: map[string]any{"title": "Pay"}}))
	testW := must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, added[2].ID, edit{Properties: map[string]any{"title": "Obsolete"}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, reqW.ID, "u", "ok"))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, added[2].ID, "u", "ok"))

	view := func(level string) domain.Baseline {
		return must[domain.Baseline](t)(g.ChangeView(ctx, c.ID, "", level))
	}
	// written: everything proposed or accepted
	w := view(ViewWritten)
	if !w.Contains(*reqW.Post) || !w.Contains(*needW.Post) {
		t.Fatalf("written view: %v", w.Nodes)
	}
	if !w.Contains(*testW.Post) {
		t.Fatalf("the written view holds every version written: %v", w.Nodes)
	}
	// accepted: NEED-1 awaits its review, it stays as released
	a := view(ViewAccepted)
	if !a.Contains(*reqW.Post) || !a.Contains(need) {
		t.Fatalf("accepted view: %v", a.Nodes)
	}
	// committed: nothing yet
	if l := view(ViewCommitted); l.ID != f.base.ID {
		t.Fatalf("committed view before apply: %s", l.ID)
	}
	// a rejected version leaves the written view
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, needW.ID, domain.ReviewRejected, "u", "no"))
	if w := view(ViewWritten); !w.Contains(need) {
		t.Fatalf("written view after the rejection: %v", w.Nodes)
	}
	// rejected: reworked or withdrawn before the change merges (ADR 0102)
	if _, err := g.apply(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a rejected impact holds the merge: %v", err)
	}
	if err := g.WithdrawImpact(ctx, c.ID, needW.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	res := must[domain.Baseline](t)(g.apply(ctx, c.ID, ""))
	l := view(ViewCommitted)
	if l.ID != res.ID || l.Nodes[f.req.ID] != f.req.Version+1 || !l.Contains(need) {
		t.Fatalf("committed view: %+v", l)
	}
	if _, err := g.ChangeView(ctx, c.ID, "", "someday"); err == nil {
		t.Fatal("unknown level")
	}
}

// A flow sees its own versions, the main flow its own (ADR 0032: an option is a branch seen before it commits).
func TestChangeViewOfAFlow(t *testing.T) { forEachRepo(t, testChangeViewOfAFlow) }

func testChangeViewOfAFlow(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newFlowWorld(t, repo)
	g, c := w.g, w.change
	pre := w.f.req.Ref()
	added := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "second look", Flow: w.flow, Execution: "e3"}}))
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, added[0].ID, edit{Flow: w.flow, Execution: "e3", Properties: map[string]any{"title": "B"}}))
	flowView := must[domain.Baseline](t)(g.ChangeView(ctx, c.ID, w.flow, ViewWritten))
	mainView := must[domain.Baseline](t)(g.ChangeView(ctx, c.ID, "", ViewWritten))
	fv := must[domain.Node](t)(g.ChangeNode(ctx, c.ID, w.flow, domain.NodeRef{ID: pre.ID, Version: flowView.Nodes[pre.ID]}))
	if fv.Properties["title"] != "B" || !mainView.Contains(*w.req.Post) {
		t.Fatalf("flow view %v, main view %v", flowView.Nodes, mainView.Nodes)
	}
	// the flow relaunched the step that created TST-2: it does not see it; the main flow does
	if _, seen := flowView.Nodes[w.tst.Post.ID]; seen || !mainView.Contains(*w.tst.Post) {
		t.Fatalf("TST-2: flow %v, main %v", flowView.Nodes, mainView.Nodes)
	}
	// accepted: nothing of the flow is reviewed yet
	if a := must[domain.Baseline](t)(g.ChangeView(ctx, c.ID, w.flow, ViewAccepted)); !a.Contains(pre) {
		t.Fatalf("flow accepted view: %v", a.Nodes)
	}
}

// The world of a change is its fork as it left it, what it committed and its drafts, read by node (the node editor
// of a change): a link to a node the change moved stays.
func TestChangeWorld(t *testing.T) { forEachRepo(t, testChangeWorld) }

func testChangeWorld(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "PSP v2", BaselineID: f.base.ID, OwnBranch: true}))
	mod := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "PSP v2"}))
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, mod.ID, edit{Properties: map[string]any{"title": "Use PSP v2"}}))
	must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-9", Type: "Requirement", Rationale: "new",
		Links: []LinkWrite{{Type: "satisfies", To: f.need.Ref()}}}))

	type world struct {
		nodes map[string]domain.Node
		links map[string]bool
	}
	read := func(get func(context.Context, domain.ChangeID, string) ([]domain.Node, []domain.Link, error)) world {
		t.Helper()
		nodes, links, err := get(ctx, c.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		w := world{nodes: map[string]domain.Node{}, links: map[string]bool{}}
		keys := map[domain.NodeID]string{}
		for _, n := range nodes {
			w.nodes[n.Key] = n
			keys[n.ID] = n.Key
		}
		for _, l := range links {
			w.links[keys[l.From.ID]+" "+l.Type+" "+keys[l.To.ID]] = true
		}
		return w
	}
	check := func(w world, title string) {
		t.Helper()
		if len(w.nodes) != 4 || w.nodes["REQ-1"].Properties["title"] != title || w.nodes["REQ-9"].ID == "" || w.nodes["TST-1"].Version != f.test.Version {
			t.Fatalf("world nodes: %+v", w.nodes)
		}
		for _, l := range []string{"REQ-1 satisfies NEED-1", "REQ-9 satisfies NEED-1", "TST-1 verifies REQ-1"} {
			if !w.links[l] {
				t.Fatalf("world links lack %q: %v", l, w.links)
			}
		}
	}
	check(read(g.ChangeWorld), "Use PSP v2")
	checkChangeHoods(t, g, c.ID)
	// the graph a call on the change reads stays its reference baseline
	if ref := read(g.ChangeGraph); ref.nodes["REQ-1"].Properties["title"] != "Use PSP v1" || ref.nodes["REQ-9"].ID != "" {
		t.Fatalf("change graph: %+v", ref.nodes)
	}
	// committed: the version it wrote on its branch, the created node still a draft
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, mod.ID, "u", "ok"))
	must[domain.Baseline](t)(g.CommitChange(ctx, c.ID, ""))
	w := read(g.ChangeWorld)
	check(w, "Use PSP v2")
	if w.nodes["REQ-1"].Version != f.req.Version+1 || w.nodes["REQ-9"].Version != 0 {
		t.Fatalf("after the commit: REQ-1 v%d, REQ-9 v%d", w.nodes["REQ-1"].Version, w.nodes["REQ-9"].Version)
	}
	checkChangeHoods(t, g, c.ID)
}

// checkChangeHoods checks that the neighbourhood of every node of the world of a change, read by node
// (ChangeNodeNeighbourhood), is the one the whole world holds (ChangeWorld): the same node at the same version, the
// same links, the same neighbours at the same versions.
func checkChangeHoods(t *testing.T, g *Graph, id domain.ChangeID) {
	t.Helper()
	ctx := context.Background()
	nodes, links, err := g.ChangeWorld(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[domain.NodeID]domain.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	for _, n := range nodes {
		want := map[string]bool{}
		for _, l := range links {
			if l.From.ID == n.ID || l.To.ID == n.ID {
				want[fmt.Sprintf("%s %s %s v%d", byID[l.From.ID].Key, l.Type, byID[l.To.ID].Key, byID[l.To.ID].Version)] = true
			}
		}
		nb, err := g.ChangeNodeNeighbourhood(ctx, id, "", n.ID)
		if err != nil {
			t.Fatalf("%s: %v", n.Key, err)
		}
		if nb.Node.Version != n.Version || nb.Node.Properties["title"] != n.Properties["title"] {
			t.Fatalf("%s: node v%d %v, want v%d %v", n.Key, nb.Node.Version, nb.Node.Properties, n.Version, n.Properties)
		}
		near := map[domain.NodeID]domain.Node{n.ID: nb.Node}
		for _, m := range nb.Nodes {
			if w, ok := byID[m.ID]; !ok || w.Version != m.Version {
				t.Fatalf("%s: neighbour %s v%d, the world holds v%d", n.Key, m.Key, m.Version, w.Version)
			}
			near[m.ID] = m
		}
		got := map[string]bool{}
		for _, l := range nb.Links {
			got[fmt.Sprintf("%s %s %s v%d", near[l.From.ID].Key, l.Type, near[l.To.ID].Key, near[l.To.ID].Version)] = true
		}
		if !maps.Equal(got, want) {
			t.Fatalf("%s: links %v, want %v", n.Key, got, want)
		}
	}
}
