package graph

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/prov"
	"github.com/zimwip/goap/pkg/typecat"
)

// The domain of the tests: a Folder holds Items (contains is a composition), a Note points at an Item (refs is not).
const restructureDomain = `
name: docs
version: 1.0.0
nodeTypes:
  - {name: Folder}
  - {name: Item}
  - {name: Note}
linkTypes:
  - {name: contains, from: Folder, to: Item, compose: true}
  - {name: refs, from: Note, to: Item}
`

type docs struct {
	g   *Graph
	ctx context.Context
}

func newDocs(t *testing.T, repo Repo) docs {
	t.Helper()
	d, err := def.ParseDomain([]byte(restructureDomain))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(d)
	if err != nil {
		t.Fatal(err)
	}
	g := New(repo)
	g.Types = func() TypeCatalog { return cat }
	return docs{g: g, ctx: context.Background()}
}

func (d docs) node(t *testing.T, key, typ string) domain.Node {
	t.Helper()
	return must[domain.Node](t)(importNode(d.ctx, d.g, newNode{Namespace: "docs", Key: key, Type: "docs@" + typ}))
}

func (d docs) link(t *testing.T, typ string, from, to domain.Node) {
	t.Helper()
	must[domain.Link](t)(importLink(d.ctx, d.g, "docs@"+typ, from.Ref(), to.Ref(), nil))
}

func (d docs) change(t *testing.T) domain.Change {
	t.Helper()
	head := must[domain.Baseline](t)(d.g.BranchHead(d.ctx, "docs", domain.MainBranch))
	return must[domain.Change](t)(d.g.CreateChange(d.ctx, NewChange{Namespace: "docs", Title: "restructure", Intent: "restructure", BaselineID: head.ID, OwnBranch: true}))
}

// outKeys are the keys the latest version of a node on main links to, as "type:key".
func (d docs) outKeys(t *testing.T, key string) []string {
	t.Helper()
	n := must[domain.Node](t)(d.g.NodeByKey(d.ctx, "docs", key))
	var out []string
	for _, l := range must[[]domain.Link](t)(d.g.OutLinksOf(d.ctx, n.Ref())) {
		out = append(out, l.Type+":"+must[domain.Node](t)(d.g.Node(d.ctx, l.To)).Key)
	}
	slices.Sort(out)
	return out
}

func impactOf(t *testing.T, list []domain.ChangeImpact, key string) domain.ChangeImpact {
	t.Helper()
	for _, cn := range list {
		if cn.Key == key {
			return cn
		}
	}
	t.Fatalf("no change impact of %s", key)
	return domain.ChangeImpact{}
}

func TestMergeFromTheParent(t *testing.T) { forEachRepo(t, testMergeFromTheParent) }

func testMergeFromTheParent(t *testing.T, repo Repo) {
	d := newDocs(t, repo)
	ctx, g := d.ctx, d.g
	f, a, b, x := d.node(t, "F", "Folder"), d.node(t, "A", "Item"), d.node(t, "B", "Item"), d.node(t, "X", "Item")
	d.link(t, "contains", f, a)
	d.link(t, "contains", f, b)
	d.link(t, "contains", f, x)
	a1, b1 := must[domain.Node](t)(g.NodeByKey(ctx, "docs", "A")), must[domain.Node](t)(g.NodeByKey(ctx, "docs", "B"))

	c := d.change(t)
	res := must[Restructured](t)(g.ImpactNodeMerge(ctx, c.ID, MergeInput{
		Sources: []NodeName{{Key: "A"}, {Key: "B"}},
		Into:    NodeCreate{Key: "C", Type: "docs@Item", Rationale: "merge A and B"},
	}))
	if len(res.Successors) != 1 || res.Successors[0].Post == nil || len(res.Parents) != 1 || len(res.Sources) != 2 || len(res.Suspect) != 0 {
		t.Fatalf("result: %+v", res)
	}
	parent := res.Parents[0]
	if parent.Key != "F" || parent.Intent != domain.IntentModified {
		t.Fatalf("the parent is modified: %+v", parent)
	}
	for _, s := range res.Sources {
		if s.Via != parent.ID || s.Intent != domain.IntentModified || s.Post != nil {
			t.Errorf("source %s: via %q, intent %s, post %v; want via %s, modified, no version", s.Key, s.Via, s.Intent, s.Post, parent.ID)
		}
	}

	// lineage, on the first version of the successor
	cn := must[domain.Node](t)(g.Node(ctx, *res.Successors[0].Post))
	if cn.Version != 1 || !slices.Equal(cn.Origins, []domain.NodeRef{a1.Ref(), b1.Ref()}) {
		t.Fatalf("origins of C: v%d %v", cn.Version, cn.Origins)
	}
	for _, o := range []domain.Node{a1, b1} {
		got := must[[]domain.Node](t)(g.DerivedNodes(ctx, o.Ref()))
		if len(got) != 1 || got[0].ID != cn.ID {
			t.Errorf("DerivedNodes(%s) = %v", o.Key, got)
		}
		if got := must[[]domain.Node](t)(g.DerivedNodes(ctx, domain.NodeRef{ID: o.ID})); len(got) != 1 {
			t.Errorf("DerivedNodes(%s, any version) = %v", o.Key, got)
		}
		if got := must[[]domain.Node](t)(g.DerivedNodes(ctx, domain.NodeRef{ID: o.ID, Version: o.Version + 5})); len(got) != 0 {
			t.Errorf("DerivedNodes of another version = %v", got)
		}
	}
	if got := must[[]domain.Node](t)(g.DerivedNodes(ctx, x.Ref())); len(got) != 0 {
		t.Errorf("X derives nothing: %v", got)
	}

	// the working version of the parent: X and C, no A, no B
	pf := must[domain.Node](t)(g.Node(ctx, *parent.Post))
	var held []string
	for _, l := range must[[]domain.Link](t)(g.OutLinksOf(ctx, pf.Ref())) {
		held = append(held, must[domain.Node](t)(g.Node(ctx, l.To)).Key)
	}
	slices.Sort(held)
	if !pf.CheckedOut || !slices.Equal(held, []string{"C", "X"}) {
		t.Fatalf("parent: checked out %v, links %v", pf.CheckedOut, held)
	}

	// the events: created with its origins, the parent updated with the patches
	var created, removed, added int
	for _, e := range must[[]domain.ImpactEvent](t)(g.ChangeEvents(ctx, c.ID)) {
		switch {
		case e.Op == domain.ImpactCreated && e.Patch["origins"] != nil:
			created++
		case e.Op == domain.ImpactUpdated && e.Patch["removeLink"] != nil:
			removed++
		case e.Op == domain.ImpactUpdated && e.Patch["addLink"] != nil:
			added++
		}
	}
	if created != 1 || removed != 2 || added != 1 {
		t.Fatalf("events: created %d, removed %d, added %d", created, removed, added)
	}

	// the review gate: C waits for its origins
	if _, err := g.ImpactNodeReview(ctx, c.ID, res.Successors[0].ID, domain.ReviewAccepted, "tester", "ok"); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepting C before A and B: %v", err)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, impactOf(t, res.Sources, "A").ID, domain.ReviewAccepted, "tester", "ok"))
	if _, err := g.ImpactNodeReview(ctx, c.ID, res.Successors[0].ID, domain.ReviewAccepted, "tester", "ok"); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepting C with B proposed: %v", err)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, impactOf(t, res.Sources, "B").ID, domain.ReviewAccepted, "tester", "ok"))
	if err := g.acceptAndCheckin(ctx, c.ID, res.Successors[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := g.acceptAndCheckin(ctx, c.ID, parent.ID, ""); err != nil {
		t.Fatal(err)
	}
	base := must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))

	// one baseline holds it all, and nothing disappeared
	nodes, _ := must2(t)(g.BaselineGraph(ctx, base.ID))
	keys := map[string]bool{}
	for _, n := range nodes {
		keys[n.Key] = true
	}
	for _, k := range []string{"A", "B", "C", "F", "X"} {
		if !keys[k] {
			t.Errorf("the baseline lost %s", k)
		}
	}
	if got := d.outKeys(t, "F"); !slices.Equal(got, []string{"docs@contains:C", "docs@contains:X"}) {
		t.Errorf("F after the landing: %v", got)
	}
	if after := must[domain.Node](t)(g.NodeByKey(ctx, "docs", "A")); after.Version != a1.Version {
		t.Errorf("A was written: v%d", after.Version)
	}
	done := must[domain.Change](t)(g.Change(ctx, c.ID))
	for _, cn := range done.Nodes {
		if cn.Review == domain.ReviewAccepted && cn.Post != nil && cn.Landed == nil {
			t.Errorf("%s did not land", cn.Key)
		}
	}
}

// Two parents of a source: each is modified, and the source is realized through the first.
func TestMergeTwoParents(t *testing.T) { forEachRepo(t, testMergeTwoParents) }

func testMergeTwoParents(t *testing.T, repo Repo) {
	d := newDocs(t, repo)
	ctx, g := d.ctx, d.g
	f1, f2 := d.node(t, "F1", "Folder"), d.node(t, "F2", "Folder")
	a, b := d.node(t, "A", "Item"), d.node(t, "B", "Item")
	d.link(t, "contains", f1, a)
	d.link(t, "contains", f2, a)
	d.link(t, "contains", f2, b)
	c := d.change(t)
	res := must[Restructured](t)(g.ImpactNodeMerge(ctx, c.ID, MergeInput{Sources: []NodeName{{Key: "A"}, {Key: "B"}}, Into: NodeCreate{Key: "C", Type: "docs@Item", Rationale: "merge"}}))
	if len(res.Parents) != 2 {
		t.Fatalf("one modification per parent: %+v", res.Parents)
	}
	f1i, f2i := impactOf(t, res.Parents, "F1"), impactOf(t, res.Parents, "F2")
	if got := impactOf(t, res.Sources, "A").Via; got != f1i.ID {
		t.Errorf("A is realized through the first parent F1 (%s), got %s", f1i.ID, got)
	}
	if got := impactOf(t, res.Sources, "B").Via; got != f2i.ID {
		t.Errorf("B is realized through F2 (%s), got %s", f2i.ID, got)
	}
	for _, p := range res.Parents {
		pf := must[domain.Node](t)(g.Node(ctx, *p.Post))
		ls := must[[]domain.Link](t)(g.OutLinksOf(ctx, pf.Ref()))
		if len(ls) != 1 || must[domain.Node](t)(g.Node(ctx, ls[0].To)).Key != "C" {
			t.Errorf("%s links %v", p.Key, ls)
		}
	}
}

// The parent's impact is reused when the change already holds it.
func TestMergeReusesTheParentImpact(t *testing.T) { forEachRepo(t, testMergeReusesTheParentImpact) }

func testMergeReusesTheParentImpact(t *testing.T, repo Repo) {
	d := newDocs(t, repo)
	ctx, g := d.ctx, d.g
	f := d.node(t, "F", "Folder")
	a, b := d.node(t, "A", "Item"), d.node(t, "B", "Item")
	d.link(t, "contains", f, a)
	d.link(t, "contains", f, b)
	c := d.change(t)
	fi := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Key: "F", Rationale: "edit"}))
	res := must[Restructured](t)(g.ImpactNodeMerge(ctx, c.ID, MergeInput{Sources: []NodeName{{Key: "A"}, {Key: "B"}}, Into: NodeCreate{Key: "C", Type: "docs@Item", Rationale: "merge"}}))
	if res.Parents[0].ID != fi.ID || res.Sources[0].Via != fi.ID {
		t.Fatalf("the impact of F is reused: %+v", res)
	}
	if got := must[[]domain.ChangeImpact](t)(g.ListChangeImpacts(ctx, c.ID)); len(got) != 4 {
		t.Fatalf("F, A, B, C: %d impacts", len(got))
	}
}

func TestSplit(t *testing.T) { forEachRepo(t, testSplit) }

func testSplit(t *testing.T, repo Repo) {
	d := newDocs(t, repo)
	ctx, g := d.ctx, d.g
	f, a, n := d.node(t, "F", "Folder"), d.node(t, "A", "Item"), d.node(t, "N", "Note")
	d.link(t, "contains", f, a)
	d.link(t, "refs", n, a)
	c := d.change(t)
	res := must[Restructured](t)(g.ImpactNodeSplit(ctx, c.ID, SplitInput{Source: NodeName{Key: "A"},
		Into: []NodeCreate{{Key: "C", Type: "docs@Item", Rationale: "split"}, {Key: "D", Type: "docs@Item", Rationale: "split"}}}))
	if len(res.Successors) != 2 || len(res.Parents) != 1 || len(res.Sources) != 1 || res.Sources[0].Via != res.Parents[0].ID {
		t.Fatalf("result: %+v", res)
	}
	// the non-compose link is left and reported
	if len(res.Suspect) != 1 || res.Suspect[0].FromKey != "N" || res.Suspect[0].ToKey != "A" || res.Suspect[0].Type != "docs@refs" {
		t.Fatalf("suspect: %+v", res.Suspect)
	}
	pf := must[domain.Node](t)(g.Node(ctx, *res.Parents[0].Post))
	var held []string
	for _, l := range must[[]domain.Link](t)(g.OutLinksOf(ctx, pf.Ref())) {
		held = append(held, must[domain.Node](t)(g.Node(ctx, l.To)).Key)
	}
	slices.Sort(held)
	if !slices.Equal(held, []string{"C", "D"}) {
		t.Fatalf("F links %v", held)
	}
	for _, s := range res.Successors {
		got := must[domain.Node](t)(g.Node(ctx, *s.Post))
		if !slices.Equal(got.Origins, []domain.NodeRef{a.Ref()}) {
			t.Errorf("%s origins %v", s.Key, got.Origins)
		}
	}
	if got := must[[]domain.Node](t)(g.DerivedNodes(ctx, a.Ref())); len(got) != 2 {
		t.Errorf("A derives into C and D: %v", got)
	}
	// N was not touched
	for _, cn := range must[[]domain.ChangeImpact](t)(g.ListChangeImpacts(ctx, c.ID)) {
		if cn.Key == "N" {
			t.Errorf("N is not part of the change")
		}
	}
}

// A merge moves the other links to the sources onto the new node.
func TestMergeRetargetsOtherLinks(t *testing.T) { forEachRepo(t, testMergeRetargetsOtherLinks) }

func testMergeRetargetsOtherLinks(t *testing.T, repo Repo) {
	d := newDocs(t, repo)
	ctx, g := d.ctx, d.g
	f, a, b, n := d.node(t, "F", "Folder"), d.node(t, "A", "Item"), d.node(t, "B", "Item"), d.node(t, "N", "Note")
	d.link(t, "contains", f, a)
	d.link(t, "contains", f, b)
	d.link(t, "refs", n, a)
	d.link(t, "refs", n, b)
	c := d.change(t)
	res := must[Restructured](t)(g.ImpactNodeMerge(ctx, c.ID, MergeInput{Sources: []NodeName{{Key: "A"}, {Key: "B"}}, Into: NodeCreate{Key: "C", Type: "docs@Item", Rationale: "merge"}}))
	if len(res.Parents) != 2 || len(res.Suspect) != 0 {
		t.Fatalf("F and N are modified: %+v", res)
	}
	nf := must[domain.Node](t)(g.Node(ctx, *impactOf(t, res.Parents, "N").Post))
	ls := must[[]domain.Link](t)(g.OutLinksOf(ctx, nf.Ref()))
	if len(ls) != 1 || ls[0].Type != "docs@refs" || must[domain.Node](t)(g.Node(ctx, ls[0].To)).Key != "C" {
		t.Fatalf("N links %v", ls)
	}
	// the sources are realized through their parent, not through N
	if got := impactOf(t, res.Sources, "A").Via; got != impactOf(t, res.Parents, "F").ID {
		t.Errorf("A is realized through F, got %s", got)
	}
}

func TestRestructureRefusals(t *testing.T) { forEachRepo(t, testRestructureRefusals) }

func testRestructureRefusals(t *testing.T, repo Repo) {
	d := newDocs(t, repo)
	ctx, g := d.ctx, d.g
	f, a, b := d.node(t, "F", "Folder"), d.node(t, "A", "Item"), d.node(t, "B", "Item")
	d.node(t, "O", "Item") // an orphan
	d.node(t, "T", "Note")
	d.link(t, "contains", f, a)
	d.link(t, "contains", f, b)
	c := d.change(t)
	into := NodeCreate{Key: "C", Type: "docs@Item", Rationale: "merge"}

	if _, err := g.ImpactNodeMerge(ctx, c.ID, MergeInput{Sources: []NodeName{{Key: "A"}, {Key: "O"}}, Into: into}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a source with no parent: %v", err)
	}
	if _, err := g.ImpactNodeMerge(ctx, c.ID, MergeInput{Sources: []NodeName{{Key: "A"}}, Into: into}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a merge of one node: %v", err)
	}
	if _, err := g.ImpactNodeMerge(ctx, c.ID, MergeInput{Sources: []NodeName{{Key: "A"}, {Key: "A"}}, Into: into}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a node twice: %v", err)
	}
	if _, err := g.ImpactNodeMerge(ctx, c.ID, MergeInput{Sources: []NodeName{{Key: "A"}, {Key: "B"}}, Into: NodeCreate{Key: "C", Type: "docs@Note"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a type incompatible with the sources: %v", err)
	}
	if _, err := g.ImpactNodeSplit(ctx, c.ID, SplitInput{Source: NodeName{Key: "A"}, Into: []NodeCreate{{Key: "C", Type: "docs@Item"}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a split into one: %v", err)
	}
	if _, err := g.ImpactNodeMerge(ctx, c.ID, MergeInput{Sources: []NodeName{{Key: "F"}, {Key: "A"}}, Into: NodeCreate{Key: "C", Type: "docs@Item"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a parent merged with its part: %v", err)
	}
	// a refused call declares nothing
	if got := must[[]domain.ChangeImpact](t)(g.ListChangeImpacts(ctx, c.ID)); len(got) != 0 {
		t.Fatalf("a refusal left %d impacts", len(got))
	}
	// a source checked out by the change
	must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Key: "B", Rationale: "edit"}))
	if _, err := g.ImpactNodeMerge(ctx, c.ID, MergeInput{Sources: []NodeName{{Key: "A"}, {Key: "B"}}, Into: into}); !errors.Is(err, ErrConflict) {
		t.Errorf("a source checked out: %v", err)
	}
}

// What a change creates cannot be merged: it has no parent in the baseline.
func TestRestructureRefusesCreatedSources(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		d := newDocs(t, repo)
		d.node(t, "A", "Item")
		c := d.change(t)
		must[domain.ChangeImpact](t)(d.g.ImpactNodeCreate(d.ctx, c.ID, NodeCreate{Key: "N1", Type: "docs@Item", Rationale: "new"}))
		if _, err := d.g.ImpactNodeMerge(d.ctx, c.ID, MergeInput{Sources: []NodeName{{Key: "A"}, {Key: "N1"}}, Into: NodeCreate{Key: "C", Type: "docs@Item"}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a created source: %v", err)
		}
	})
}

// The gate holds again at check-in and at landing, when an origin is rejected after the review of the successor.
func TestOriginsGateAtLanding(t *testing.T) { forEachRepo(t, testOriginsGateAtLanding) }

func testOriginsGateAtLanding(t *testing.T, repo Repo) {
	d := newDocs(t, repo)
	ctx, g := d.ctx, d.g
	f, a, b := d.node(t, "F", "Folder"), d.node(t, "A", "Item"), d.node(t, "B", "Item")
	d.link(t, "contains", f, a)
	d.link(t, "contains", f, b)
	c := d.change(t)
	res := must[Restructured](t)(g.ImpactNodeMerge(ctx, c.ID, MergeInput{Sources: []NodeName{{Key: "A"}, {Key: "B"}}, Into: NodeCreate{Key: "C", Type: "docs@Item", Rationale: "merge"}}))
	for _, s := range res.Sources {
		must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, s.ID, domain.ReviewAccepted, "tester", "ok"))
	}
	if err := g.acceptAndCheckin(ctx, c.ID, res.Successors[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := g.acceptAndCheckin(ctx, c.ID, res.Parents[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	// an origin goes back to proposed: the successor no longer lands
	must[[]domain.ChangeImpactID](t)(g.ReopenImpacts(ctx, c.ID, []domain.ChangeImpactID{res.Sources[0].ID}, "rethink"))
	if _, err := g.Apply(ctx, c.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("landing with an origin awaiting its review: %v", err)
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, res.Sources[0].ID, domain.ReviewAccepted, "tester", "ok again"))
	must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
}

// The export names what a successor derives from (ADR 0077).
func TestMergeProvenance(t *testing.T) { forEachRepo(t, testMergeProvenance) }

func testMergeProvenance(t *testing.T, repo Repo) {
	d := newDocs(t, repo)
	ctx, g := d.ctx, d.g
	f, a, b := d.node(t, "F", "Folder"), d.node(t, "A", "Item"), d.node(t, "B", "Item")
	d.link(t, "contains", f, a)
	d.link(t, "contains", f, b)
	c := d.change(t)
	res := must[Restructured](t)(g.ImpactNodeMerge(ctx, c.ID, MergeInput{Sources: []NodeName{{Key: "A"}, {Key: "B"}}, Into: NodeCreate{Key: "C", Type: "docs@Item", Rationale: "merge"}}))
	entries, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := prov.Export(must[domain.Change](t)(g.Change(ctx, c.ID)), entries)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc)
	post := "urn:goap:node:" + string(res.Successors[0].Post.ID) + "@v1"
	for _, o := range []domain.Node{a, b} {
		if !strings.Contains(string(raw), `"prov:wasDerivedFrom":`) || !strings.Contains(string(raw), "urn:goap:node:"+string(o.ID)+"@v1") {
			t.Fatalf("no derivation of %s in %s", o.Key, raw)
		}
	}
	var back struct {
		Graph []map[string]any `json:"@graph"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for _, n := range back.Graph {
		if n["@id"] == post {
			if got, _ := json.Marshal(n["prov:wasDerivedFrom"]); !strings.Contains(string(got), string(a.ID)) || !strings.Contains(string(got), string(b.ID)) {
				t.Errorf("C derives from %s", got)
			}
			return
		}
	}
	t.Fatalf("C is not in the export")
}
