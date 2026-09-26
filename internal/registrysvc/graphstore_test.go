package registrysvc

import (
	"context"
	"encoding/json"
	"os"
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

func TestGraphStoreRoundTripsEveryDefinitionOfTheRepository(t *testing.T) {
	ctx := context.Background()
	s := NewGraphStore(graph.New(graph.NewMemory()))
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	files, _ := filepath.Glob("../../domains/*.yaml")
	if len(files) == 0 {
		t.Fatal("no domain found")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		d, err := methodology.ParseDomain(data)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if err := s.SaveDomain(ctx, DomainRecord{Domain: *d, Status: StatusDraft, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		got, err := s.GetDomain(ctx, d.Name, d.Version)
		if err != nil {
			t.Fatal(err)
		}
		if canon(t, got.Domain) != canon(t, *d) {
			t.Errorf("domain %s does not round-trip:\n have %s\n want %s", d.Name, canon(t, got.Domain), canon(t, *d))
		}
	}
	files, _ = filepath.Glob("../../methodologies/*.yaml")
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
		n, err := g.NodeByKey(ctx, domain.NamespacePlatform, k)
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
	head, _ := g.BranchHead(ctx, domain.MainBranch)
	if err := s.Save(ctx, Record{Methodology: m, Status: StatusDraft, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if h2, _ := g.BranchHead(ctx, domain.MainBranch); h2.ID != head.ID {
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
		if l.Type == LinkDefines {
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
