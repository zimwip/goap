package metamodel

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

func load(t *testing.T) *methodology.Methodology {
	t.Helper()
	m, err := methodology.LoadFile("../../methodologies/test-design.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSyncVersionsTheMethodology(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	m := load(t)
	r, err := Sync(ctx, g, m)
	if err != nil || !r.Changed() || r.Created == 0 || r.Links == 0 {
		t.Fatalf("first sync: %+v %v", r, err)
	}
	root, err := g.NodeByKey(ctx, "M:"+m.Name)
	if err != nil || root.Type != TypeMethodology || root.Properties["version"] != m.Version {
		t.Fatalf("root: %+v %v", root, err)
	}
	a := m.Actions[0]
	an, err := g.NodeByKey(ctx, Key(m.Name, TypeAction, a.Name))
	if err != nil || an.Properties["kind"] != a.Kind {
		t.Fatalf("action node: %+v %v", an, err)
	}
	v, _ := g.View(ctx, an.Ref())
	var requires int
	for _, l := range v.Out {
		if l.Type == LinkRequires {
			requires++
		}
	}
	if requires != len(a.Pre) {
		t.Fatalf("requires links %d, pre %v", requires, a.Pre)
	}
	// idempotent
	if r, err := Sync(ctx, g, m); err != nil || r.Changed() {
		t.Fatalf("second sync must not change: %+v %v", r, err)
	}
	// new version: one action changed, one removed from the agent, a specialization added
	m2 := *m
	m2.Version = "9.0.0"
	m2.Actions = slices.Clone(m.Actions)
	m2.Actions[0].Cost = 42
	m2.Actions[0].Description = ""
	m2.Actions = append(m2.Actions, methodology.Action{Name: "fast_" + a.Name, Kind: methodology.KindHuman, Specializes: a.Name, Priority: 1})
	r, err = Sync(ctx, g, &m2)
	if err != nil || r.Created != 1 || r.Updated < 2 {
		t.Fatalf("third sync: %+v %v", r, err)
	}
	an2, _ := g.NodeByKey(ctx, Key(m.Name, TypeAction, a.Name))
	if an2.Version != an.Version+1 || an2.Properties["cost"] != float64(42) || an2.Properties["description"] != nil {
		t.Fatalf("action v2: %+v", an2)
	}
	spec, _ := g.NodeByKey(ctx, Key(m.Name, TypeAction, "fast_"+a.Name))
	sv, _ := g.View(ctx, spec.Ref())
	if len(sv.Out) != 1 || sv.Out[0].Type != LinkSpecializes || sv.Out[0].To != an2.Ref() {
		t.Fatalf("specializes link: %+v", sv.Out)
	}
	// the change history explains every version
	cs, _ := g.Changes(ctx)
	// the shared domain, then the two versions of the methodology
	if len(cs) != 3 || cs[2].Data["metamodel"].(map[string]any)["version"] != "9.0.0" || cs[2].Status != domain.ChangeApplied {
		t.Fatalf("changes: %+v", cs)
	}
}

func TestParseKey(t *testing.T) {
	if m, k, n, ok := ParseKey("M:impact/action/write_report"); !ok || m != "impact" || k != "action" || n != "write_report" {
		t.Fatal(m, k, n, ok)
	}
	if _, _, _, ok := ParseKey("REQ-1"); ok {
		t.Fatal("not an element")
	}
}

// TestSyncNodeTypeIsMetadataLayer verifies ADR 0012: once a NodeType node
// exists, Sync never overwrites or deletes it again, even when the
// registry's declared schema changes or drops it.
func TestSyncNodeTypeIsMetadataLayer(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	m := load(t)
	if _, err := Sync(ctx, g, m); err != nil {
		t.Fatal(err)
	}
	nt, err := g.NodeByKey(ctx, DomainKey("alm", "Requirement"))
	if err != nil {
		t.Fatal(err)
	}

	// author the metadata layer directly, as ApplyNodeTypes would
	if _, err := ApplyNodeTypes(ctx, g, m.Name, []NodeTypeOp{{Name: "SecurityRequirement", Description: "added on the graph", Extends: "Requirement"}}); err != nil {
		t.Fatal(err)
	}

	// the registry changes Requirement's description and drops TestCase
	m2 := *m
	m2.Domain.NodeTypes = slices.Clone(m.Domain.NodeTypes)
	m2.Domain.NodeTypes[0].Description = "changed in the registry"
	m2.Domain.NodeTypes = m2.Domain.NodeTypes[:1] // TestCase no longer declared
	if _, err := Sync(ctx, g, &m2); err != nil {
		t.Fatal(err)
	}

	// Requirement kept its graph-native version and properties, untouched
	nt2, err := g.NodeByKey(ctx, DomainKey("alm", "Requirement"))
	if err != nil || nt2.Version != nt.Version || nt2.Properties["description"] != nt.Properties["description"] {
		t.Fatalf("Requirement was overwritten: %+v (was %+v)", nt2, nt)
	}
	// TestCase, dropped from the registry, still lives in the graph
	if _, err := g.NodeByKey(ctx, DomainKey("alm", "TestCase")); err != nil {
		t.Fatalf("TestCase was deleted: %v", err)
	}
	// SecurityRequirement, authored directly on the graph, survived too
	if _, err := g.NodeByKey(ctx, DomainKey("alm", "SecurityRequirement")); err != nil {
		t.Fatalf("SecurityRequirement was deleted: %v", err)
	}
}

// TestSupertypesMatchesDeclaredSchema verifies the graph-derived ancestor
// chains (ADR 0012) match Methodology.Supertypes() (the declared schema)
// after a Sync.
func TestSupertypesMatchesDeclaredSchema(t *testing.T) {
	ctx := context.Background()
	m, err := methodology.LoadFile("../../methodologies/sdlc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	if _, err := Sync(ctx, g, m); err != nil {
		t.Fatal(err)
	}
	got, err := Supertypes(ctx, g, m.Name)
	if err != nil {
		t.Fatal(err)
	}
	want := m.Supertypes()
	if len(got) == 0 || len(got["SecurityRequirement"]) != 2 {
		t.Fatalf("SecurityRequirement ancestors: %v", got["SecurityRequirement"])
	}
	for name, ancestors := range want {
		if !slices.Equal(got[name], ancestors) {
			t.Fatalf("%s: graph %v, declared %v", name, got[name], ancestors)
		}
	}
}

// TestSupertypesFallback verifies the permanent fallback (ADR 0012): a
// methodology never synced onto the graph has no NodeType nodes yet.
func TestSupertypesFallback(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	m := load(t)
	got, err := Supertypes(ctx, g, m.Name)
	if err != nil || got != nil {
		t.Fatalf("expected nil, nil for an unsynced methodology: %v %v", got, err)
	}
}

// TestBackfillInstanceOf verifies the phase-3 catch-up (ADR 0012): data nodes
// created before their methodology's metadata layer existed get linked once
// it does, and the operation is idempotent.
func TestBackfillInstanceOf(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	m := load(t)
	if _, err := g.CreateNode(ctx, graph.NewNode{Key: "REQ-1", Type: "Requirement"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CreateNode(ctx, graph.NewNode{Key: "OTHER-1", Type: "Unrelated"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CreateBaselineFromLatest(ctx, "Initial baseline"); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(ctx, g, m); err != nil {
		t.Fatal(err)
	}

	res, err := BackfillInstanceOf(ctx, g, m.Name)
	if err != nil || res.Links != 1 || !res.Changed() {
		t.Fatalf("first backfill: %+v %v", res, err)
	}
	req, err := g.NodeByKey(ctx, "REQ-1")
	if err != nil {
		t.Fatal(err)
	}
	nt, err := g.NodeByKey(ctx, DomainKey("alm", "Requirement"))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := g.View(ctx, req.Ref())
	var linked bool
	for _, l := range v.Out {
		linked = linked || (l.Type == LinkInstanceOf && l.To == nt.Ref())
	}
	if !linked {
		t.Fatalf("REQ-1 has no instanceOf link: %+v", v.Out)
	}

	if res, err := BackfillInstanceOf(ctx, g, m.Name); err != nil || res.Changed() {
		t.Fatalf("second backfill must not change: %+v %v", res, err)
	}
}

func TestCreateObject(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	m := load(t)
	if _, _, err := CreateObject(ctx, g, m.Name, "Requirement", "REQ-1", nil); !errors.Is(err, graph.ErrNotFound) {
		t.Fatalf("unsynced methodology: %v", err)
	}
	if _, err := Sync(ctx, g, m); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateObject(ctx, g, m.Name, "Requirement", " ", nil); !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("no key: %v", err)
	}
	n, b, err := CreateObject(ctx, g, m.Name, "Requirement", "REQ-1", map[string]any{"title": "Pay"})
	if err != nil || n.Type != "Requirement" || n.Properties["title"] != "Pay" || b.ID == "" {
		t.Fatalf("create: %+v %v", n, err)
	}
	nt, _ := g.NodeByKey(ctx, DomainKey("alm", "Requirement"))
	v, _ := g.View(ctx, n.Ref())
	var linked bool
	for _, l := range v.Out {
		linked = linked || (l.Type == LinkInstanceOf && l.To == nt.Ref())
	}
	if !linked {
		t.Fatalf("no instanceOf edge: %+v", v.Out)
	}
	if _, _, err := CreateObject(ctx, g, m.Name, "Requirement", "REQ-1", nil); !errors.Is(err, graph.ErrConflict) {
		t.Fatalf("duplicate key: %v", err)
	}
}
