package metamodel

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

func sharedDomain() methodology.Domain {
	return methodology.Domain{Name: "alm", Version: "1", Schema: methodology.Schema{
		NodeTypes: []methodology.NodeType{{Name: "Need"}, {Name: "Requirement", Extends: "Need"}, {Name: "SecurityRequirement", Extends: "Requirement"}},
		LinkTypes: []methodology.LinkType{{Name: "derives", From: "Requirement", To: "Need"}},
	}}
}

// refMethodology returns a methodology referencing the shared domain, as the
// registry hands it out: the domain filled in and the reference kept.
func refMethodology(name string, d methodology.Domain) *methodology.Methodology {
	return &methodology.Methodology{
		Name: name, Version: "1", DomainRef: "alm@1", Domain: d.Schema,
		Conditions: []methodology.Condition{{Name: "c", Expr: `true`}},
		Actions:    []methodology.Action{{Name: "a", Kind: methodology.KindHuman, Effects: map[string]bool{"c": true}}},
		Goals:      []methodology.Goal{{Name: "g", Pre: map[string]bool{"c": true}}},
	}
}

func nodeTypeKeys(t *testing.T, g *graph.Graph) []string {
	t.Helper()
	head, err := g.BranchHead(context.Background(), domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _, err := g.BaselineGraph(context.Background(), head.ID)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, n := range nodes {
		if n.Type == TypeNodeType {
			keys = append(keys, n.Key)
		}
	}
	return keys
}

func TestSharedDomainHasOneNodeTypePerType(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	d := sharedDomain()
	a, b := refMethodology("first", d), refMethodology("second", d)
	if _, err := Sync(ctx, g, a); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(ctx, g, b); err != nil {
		t.Fatal(err)
	}
	keys := nodeTypeKeys(t, g)
	if len(keys) != 3 {
		t.Fatalf("one NodeType node per domain type, shared by both methodologies: %v", keys)
	}
	for _, k := range keys {
		if len(k) < 2 || k[:2] != "D:" {
			t.Fatalf("node types belong to the domain: %v", keys)
		}
	}
	if r, err := Sync(ctx, g, a); err != nil || r.Changed() {
		t.Fatalf("idempotent: %+v %v", r, err)
	}
	root, _ := g.NodeByKey(ctx, domain.NamespacePlatform, Key("first", TypeMethodology, ""))
	if root.Properties["domainRef"] != "alm@1" {
		t.Fatalf("root must carry the domain reference: %v", root.Properties)
	}
}

func TestSharedDomainSupertypesBackfillAndObjects(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	d := sharedDomain()
	if _, err := g.CreateNode(ctx, graph.NewNode{Key: "REQ-1", Type: "Requirement"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CreateBaselineFromLatest(ctx, "Initial baseline"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		if _, err := Sync(ctx, g, refMethodology(name, d)); err != nil {
			t.Fatal(err)
		}
	}
	st, err := Supertypes(ctx, g, "second")
	if err != nil || len(st["SecurityRequirement"]) != 2 || st["SecurityRequirement"][1] != "Need" {
		t.Fatalf("supertypes from the shared domain: %v %v", st, err)
	}

	// the object is linked once, whichever methodology backfills first
	res, err := BackfillInstanceOf(ctx, g, "first")
	if err != nil || res.Links != 1 {
		t.Fatalf("backfill: %+v %v", res, err)
	}
	if res, err := BackfillInstanceOf(ctx, g, "second"); err != nil || res.Changed() {
		t.Fatalf("second methodology must find the node already linked: %+v %v", res, err)
	}

	n, _, err := CreateObject(ctx, g, "second", "", "SecurityRequirement", "SEC-1", nil)
	if err != nil {
		t.Fatalf("create object through a methodology referencing the domain: %v", err)
	}
	nt, err := g.NodeByKey(ctx, domain.NamespacePlatform, DomainKey("alm", "SecurityRequirement"))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := g.View(ctx, n.Ref())
	var linked bool
	for _, l := range v.Out {
		linked = linked || (l.Type == LinkInstanceOf && l.To == nt.Ref())
	}
	if !linked {
		t.Fatalf("no instanceOf edge to the shared NodeType: %+v", v.Out)
	}
}

func TestSharedDomainNewTypesReachMethodologies(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	d := sharedDomain()
	if _, err := Sync(ctx, g, refMethodology("first", d)); err != nil {
		t.Fatal(err)
	}
	// a later version adds a type; nothing is overwritten, the new type is created
	d.Schema.NodeTypes = append(d.Schema.NodeTypes, methodology.NodeType{Name: "Release"})
	r, err := Sync(ctx, g, refMethodology("first", d))
	if err != nil || r.Created != 0 {
		t.Fatalf("methodology sync: %+v %v", r, err)
	}
	if keys := nodeTypeKeys(t, g); len(keys) != 4 {
		t.Fatalf("Release must be added: %v", keys)
	}
}

func TestTypeNamespace(t *testing.T) {
	if got := TypeNamespace(nil, "sdlc"); got != "sdlc" {
		t.Fatalf("no root node: %q", got)
	}
	root := domain.Node{Key: "M:sdlc", Properties: map[string]any{"domainRef": "alm@2"}}
	if got := TypeNamespace([]domain.Node{root}, "sdlc"); got != "D:alm" {
		t.Fatalf("referenced domain: %q", got)
	}
	if name, ok := TypeName("D:alm/nodetype/Need", "D:alm"); !ok || name != "Need" {
		t.Fatalf("type name: %q %v", name, ok)
	}
	if _, ok := TypeName("M:sdlc/nodetype/Need", "D:alm"); ok {
		t.Fatal("a methodology's own type is not in the domain namespace")
	}
}

func TestDomainMetadataIsProjectedAsNodes(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	d := sharedDomain()
	d.Schema.NodeTypes[1].Lifecycle = "life"
	d.Schema.Lifecycles = []domain.Lifecycle{{Name: "life", Initial: "draft", States: []domain.LifecycleState{{Name: "draft", Editable: true}, {Name: "done"}},
		Transitions: []domain.Transition{{Name: "finish", From: "draft", To: "done"}}}}
	m := refMethodology("first", d)
	if _, err := Sync(ctx, g, m); err != nil {
		t.Fatal(err)
	}
	if r, err := Sync(ctx, g, m); err != nil || r.Changed() {
		t.Fatalf("idempotent: %+v %v", r, err)
	}
	head, _ := g.BranchHead(ctx, domain.MainBranch)
	nodes, links, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]domain.Node{}
	byID := map[domain.NodeID]domain.Node{}
	for _, n := range nodes {
		byKey[n.Key], byID[n.ID] = n, n
	}
	lt, ok := byKey[DomainMetaKey("alm", TypeLinkType, "derives")]
	if !ok || lt.Type != TypeLinkType || lt.Properties["from"] != "Requirement" || lt.Properties["to"] != "Need" {
		t.Fatalf("link type node: %+v", lt)
	}
	lc, ok := byKey[DomainMetaKey("alm", TypeLifecycle, "life")]
	if !ok || lc.Type != TypeLifecycle || lc.Properties["initial"] != "draft" {
		t.Fatalf("lifecycle node: %+v", lc)
	}
	edge := map[[3]string]bool{}
	for _, l := range links {
		edge[[3]string{l.Type, byID[l.From.ID].Key, byID[l.To.ID].Key}] = true
	}
	for _, e := range [][3]string{
		{LinkLifecycle, DomainKey("alm", "Requirement"), lc.Key},
		{LinkFrom, lt.Key, DomainKey("alm", "Requirement")},
		{LinkTo, lt.Key, DomainKey("alm", "Need")},
	} {
		if !edge[e] {
			t.Errorf("missing edge %v", e)
		}
	}
	// the link type now binds the graph: a Need cannot derive from a Need
	need, _ := g.CreateNode(ctx, graph.NewNode{Key: "N-1", Type: "Need"})
	need2, _ := g.CreateNode(ctx, graph.NewNode{Key: "N-2", Type: "Need"})
	b, err := g.CreateBaselineFromLatest(ctx, "with data")
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "l", BaselineID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	from, to := need.Ref(), need2.Ref()
	_, err = g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpAddLink,
		Link: &domain.LinkDraft{Type: "derives", From: domain.Endpoint{Node: &from}, To: domain.Endpoint{Node: &to}}}}})
	if err == nil || !strings.Contains(err.Error(), "cannot join") {
		t.Fatalf("a Need must not derive from a Need: %v", err)
	}
}
