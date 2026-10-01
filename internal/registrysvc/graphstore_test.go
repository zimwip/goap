package registrysvc

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

func canon(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var x any
	if err := json.Unmarshal(b, &x); err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(x) // map keys sorted
	return string(b)
}

func TestGraphStoreRoundTripsEveryMethodologyOfTheRepository(t *testing.T) {
	ctx := context.Background()
	s := NewGraphStore(graph.New(graph.NewMemory()))
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	files, _ := filepath.Glob("../../methodologies/*.yaml")
	for _, f := range files {
		m, err := methodology.LoadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if err := s.Save(ctx, Record{Methodology: *m, Status: StatusDraft, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		got, err := s.Get(ctx, m.Name, m.Version)
		if err != nil {
			t.Fatal(err)
		}
		if canon(t, got.Methodology) != canon(t, *m) {
			t.Errorf("methodology %s does not round-trip:\n have %s\n want %s", m.Name, canon(t, got.Methodology), canon(t, *m))
		}
	}
}

func TestGraphStoreEditsElementsNotDocuments(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	s := NewGraphStore(g)
	now := time.Now()
	m := example(t)
	m.Version = "9.0.0"
	if err := s.Save(ctx, Record{Methodology: m, Status: StatusDraft, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	version := func(k string) domain.Node {
		n, err := g.NodeByKey(ctx, NamespaceMethodology, k)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	hk := MethodologyVersionKey(m.Name, m.Version)
	first := m.Actions[0]
	ak := hk + "/action/" + first.Name
	other := hk + "/action/" + m.Actions[1].Name
	a0, o0 := version(ak).Version, version(other).Version

	// editing one action gives a new version of that node only
	m.Actions[0].Description = "edited"
	if err := s.Save(ctx, Record{Methodology: m, Status: StatusDraft, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if version(ak).Version != a0+1 || version(other).Version != o0 {
		t.Fatalf("only the edited action must move: %d→%d, other %d→%d", a0, version(ak).Version, o0, version(other).Version)
	}
	// unchanged content changes nothing
	head, _ := g.BranchHead(ctx, NamespaceMethodology, domain.MainBranch)
	if err := s.Save(ctx, Record{Methodology: m, Status: StatusDraft, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if h2, _ := g.BranchHead(ctx, NamespaceMethodology, domain.MainBranch); h2.ID != head.ID {
		t.Fatal("saving the same definition must not change the graph")
	}
	// an element removed and added again is revived, not created twice
	removed := m.Actions[len(m.Actions)-1]
	m2 := m
	m2.Actions = m.Actions[:len(m.Actions)-1]
	if err := s.Save(ctx, Record{Methodology: m2, Status: StatusDraft, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, m.Name, m.Version); len(got.Methodology.Actions) != len(m2.Actions) {
		t.Fatalf("removed action still there: %d", len(got.Methodology.Actions))
	}
	if err := s.Save(ctx, Record{Methodology: m, Status: StatusDraft, UpdatedAt: now}); err != nil {
		t.Fatalf("adding %s back: %v", removed.Name, err)
	}
	if got, _ := s.Get(ctx, m.Name, m.Version); canon(t, got.Methodology) != canon(t, m) {
		t.Fatal("the definition must be back as it was")
	}
	// the elements are tied to their version
	nodes, links, _ := g.BaselineGraph(ctx, head.ID)
	defines := 0
	for _, l := range links {
		if l.Type == linkDefines {
			defines++
		}
	}
	if defines == 0 || len(nodes) == 0 {
		t.Fatalf("expected defines links, got %d", defines)
	}
	// deleting a draft then saving it again works
	if err := s.Delete(ctx, m.Name, m.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, m.Name, m.Version); err == nil {
		t.Fatal("deleted draft must be gone")
	}
	if err := s.Save(ctx, Record{Methodology: m2, Status: StatusDraft, UpdatedAt: now}); err != nil {
		t.Fatalf("revive: %v", err)
	}
	if got, err := s.Get(ctx, m.Name, m.Version); err != nil || len(got.Methodology.Actions) != len(m2.Actions) {
		t.Fatalf("revived draft: %v", err)
	}
}

func TestGraphStoreKeepsMethodologiesInTheirNamespace(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	s := NewGraphStore(g)
	now := time.Now()
	m := example(t)
	if err := s.Save(ctx, Record{Methodology: m, Status: StatusDraft, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	mh, err := g.NodeByKey(ctx, NamespaceMethodology, MethodologyVersionKey(m.Name, m.Version))
	if err != nil || mh.Type != TypeMethodologyVersion {
		t.Fatalf("methodology header: %+v %v", mh, err)
	}
	if _, err := g.NodeByKey(ctx, "platform", MethodologyVersionKey(m.Name, m.Version)); err == nil {
		t.Fatal("nothing of the registry's storage belongs to the platform namespace")
	}
}

// A methodology's declared roles materialize as their own methodology@Role nodes (ADR 0035 §2 / ADR 0039),
// the same generic element-kind mechanism as agents, actions, goals, processes and methods.
func TestGraphStoreMaterializesRoles(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	s := NewGraphStore(g)
	now := time.Now()
	m := example(t)
	m.Version = "9.1.0"
	m.Roles = []methodology.Role{{Name: "developer", Description: "Implements the change"}}
	if err := s.Save(ctx, Record{Methodology: m, Status: StatusDraft, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	key := MethodologyVersionKey(m.Name, m.Version) + "/role/developer"
	n, err := g.NodeByKey(ctx, NamespaceMethodology, key)
	if err != nil {
		t.Fatalf("role node %s: %v", key, err)
	}
	if n.Type != "methodology@Role" {
		t.Fatalf("role node type = %s", n.Type)
	}
	if n.Properties["description"] != "Implements the change" {
		t.Fatalf("role node properties = %+v", n.Properties)
	}
	got, err := s.Get(ctx, m.Name, m.Version)
	if err != nil || len(got.Methodology.Roles) != 1 || got.Methodology.Roles[0].Name != "developer" {
		t.Fatalf("roles must round-trip: %+v, %v", got.Methodology.Roles, err)
	}
}

// A process's steps and sub-steps materialize as their own methodology@Step nodes, in addition to staying inline in
// the process's own JSON (decode ignores the step nodes: the round-trip is unaffected). Each parent (the process, or
// a step with sub-steps) links to its direct children by sub_activity.
func TestGraphStoreMaterializesSteps(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	s := NewGraphStore(g)
	now := time.Now()
	m := example(t)
	m.Version = "9.2.0"
	m.Processes = []methodology.Process{{
		Name: "deliver",
		Steps: []methodology.Step{
			{Name: "analysis", Pre: map[string]bool{"framed": true}, Steps: []methodology.Step{
				{Name: "scope", Action: "identify_scope"},
			}},
		},
	}}
	if err := s.Save(ctx, Record{Methodology: m, Status: StatusDraft, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	hk := MethodologyVersionKey(m.Name, m.Version)
	n, err := g.NodeByKey(ctx, NamespaceMethodology, hk+"/step/deliver/analysis")
	if err != nil {
		t.Fatalf("step node: %v", err)
	}
	if n.Type != "methodology@Step" {
		t.Fatalf("step node type = %s", n.Type)
	}
	if got, want := n.Properties["input"], map[string]any{"framed": true}; canon(t, got) != canon(t, want) {
		t.Fatalf("pre renamed to input: %+v", n.Properties)
	}
	if _, ok := n.Properties["steps"]; ok {
		t.Fatalf("nested steps are not duplicated onto the step node: %+v", n.Properties)
	}
	sub, err := g.NodeByKey(ctx, NamespaceMethodology, hk+"/step/deliver/analysis/scope")
	if err != nil {
		t.Fatalf("sub-step node: %v", err)
	}
	if sub.Properties["action"] != "identify_scope" {
		t.Fatalf("sub-step properties: %+v", sub.Properties)
	}
	head, err := g.BranchHead(ctx, NamespaceMethodology, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	_, links, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasSubActivity := func(fromKey, toKey string) bool {
		from, err := g.NodeByKey(ctx, NamespaceMethodology, fromKey)
		if err != nil {
			return false
		}
		to, err := g.NodeByKey(ctx, NamespaceMethodology, toKey)
		if err != nil {
			return false
		}
		for _, l := range links {
			if l.Type == linkSubActivity && l.From.ID == from.ID && l.To.ID == to.ID {
				return true
			}
		}
		return false
	}
	if !hasSubActivity(hk+"/process/deliver", hk+"/step/deliver/analysis") {
		t.Fatal("process -> step sub_activity link missing")
	}
	if !hasSubActivity(hk+"/step/deliver/analysis", hk+"/step/deliver/analysis/scope") {
		t.Fatal("step -> sub-step sub_activity link missing")
	}
	// decode ignores the step nodes: the methodology still round-trips to exactly what was saved
	got, err := s.Get(ctx, m.Name, m.Version)
	if err != nil || canon(t, got.Methodology.Processes) != canon(t, m.Processes) {
		t.Fatalf("processes must round-trip unaffected: %+v, %v", got.Methodology.Processes, err)
	}
}
