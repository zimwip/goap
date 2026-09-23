package metamodel

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

func load(t *testing.T) *methodology.Methodology {
	t.Helper()
	data, err := os.ReadFile("../../methodologies/test-design.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := methodology.Parse(data)
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
	if len(cs) != 2 || cs[1].Data["metamodel"].(map[string]any)["version"] != "9.0.0" || cs[1].Status != domain.ChangeApplied {
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
