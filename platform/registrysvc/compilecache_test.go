package registrysvc

import (
	"sync"
	"testing"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/engine/methodology"
)

func cachedService(t *testing.T) *Service {
	t.Helper()
	enf, _ := authz.NewCasbin(nil)
	s := &Service{Store: NewMemoryStore(), Authz: enf}
	withALM(t, s)
	m := example(t)
	if _, issues, err := s.Save(as("admin"), m); err != nil || len(issues) > 0 {
		t.Fatalf("save: %v %v", issues, err)
	}
	if _, err := s.Publish(as("admin"), m.Name, m.Version); err != nil {
		t.Fatal(err)
	}
	return s
}

// A second read of a methodology is the compile of the first; a new version, a new domain version and a change of
// the draft in place each make the next read compile again.
func TestMethodologyCompileIsCached(t *testing.T) {
	s := cachedService(t)
	m := example(t)
	ctx := as("admin")
	before := s.compiled.compiles.Load()
	c1, err := s.Methodology(ctx, m.Name)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := s.Methodology(ctx, m.Name)
	if err != nil || c1 != c2 {
		t.Fatalf("second read must be the cached compile: %v", err)
	}
	if n := s.compiled.compiles.Load() - before; n != 1 {
		t.Fatalf("compiles: %d", n)
	}
	if list, err := s.List(ctx); err != nil || len(list) != 1 || list[0] != c1 {
		t.Fatalf("List shares the cache: %v %v", list, err)
	}
	if n := s.compiled.compiles.Load() - before; n != 1 {
		t.Fatalf("compiles after List: %d", n)
	}

	// a new published version
	if _, err := s.CreateVersion(ctx, m.Name, m.Version, "2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(ctx, m.Name, "2"); err != nil {
		t.Fatal(err)
	}
	c3, err := s.Methodology(ctx, m.Name)
	if err != nil || c3 == c1 || c3.Definition().Version != "2" {
		t.Fatalf("a published version is compiled afresh: %v", err)
	}

	// a domain publish (its check of the published versions compiles them against the new types, ADR 0125 §3)
	n := s.compiled.compiles.Load()
	if _, _, err := domainsOf(s).ImportDomain(ctx, []byte("name: other\nversion: \"1\"\nnodeTypes: [X]\n"), true); err != nil {
		t.Fatal(err)
	}
	if c4, err := s.Methodology(ctx, m.Name); err != nil || c4 == c3 || s.compiled.compiles.Load() <= n {
		t.Fatalf("a domain publish invalidates: %v", err)
	}
}

// Parallel readers share one cache (run with -race).
func TestMethodologyCacheConcurrentReaders(t *testing.T) {
	s := cachedService(t)
	name := example(t).Name
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := s.Methodology(as("admin"), name); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// A draft with a warning only (nothing can start on an empty change) is saved with the warning and publishes.
func TestWarningsDoNotBlockPublish(t *testing.T) {
	enf, _ := authz.NewCasbin(nil)
	s := &Service{Store: NewMemoryStore(), Authz: enf}
	withALM(t, s)
	m := example(t)
	m.Name = "stuck"
	m.Goal = "p"
	m.Conditions = append(m.Conditions, methodology.Condition{Name: "wk_a", Expr: "false"}, methodology.Condition{Name: "wk_c", Expr: "false"})
	m.Actions = append(m.Actions, methodology.Action{Name: "wk_x", Kind: methodology.KindHuman, Pre: map[string]bool{"wk_c": true}, Effects: map[string]bool{"wk_a": true}})
	m.Processes = []methodology.Process{{Name: "p", Steps: []methodology.Step{{Name: "one", Action: "wk_x"}}}}
	_, issues, err := s.Save(as("admin"), m)
	if err != nil || issues.HasErrors() || len(issues.Warnings()) != 1 || issues.Warnings()[0].Path != "goal" {
		t.Fatalf("save: %v %v", issues, err)
	}
	if _, err := s.Publish(as("admin"), m.Name, m.Version); err != nil {
		t.Fatalf("a warning must not block the publish: %v", err)
	}
}

// The view and the flow of a stored version are read by its name and version (the version a change follows, ADR 0126
// step 4): the compiled methodology of the registry, with no draft sent.
func TestStoredVersionViewAndFlow(t *testing.T) {
	s := cachedService(t)
	m := example(t)
	h := &Handler{Service: s}
	ctx := as("admin")
	v, err := h.GetMethodologyView(ctx, connect.NewRequest(&registryv1.GetMethodologyViewRequest{Name: m.Name, Version: m.Version}))
	if err != nil || v.Msg.View == nil || v.Msg.View.Methodology != m.Name || v.Msg.View.Version != m.Version {
		t.Fatalf("view %+v %v", v, err)
	}
	if len(m.Processes) > 0 {
		g, err := h.GetProcessGraph(ctx, connect.NewRequest(&registryv1.GetProcessGraphRequest{Name: m.Name, Version: m.Version, Process: m.Processes[0].Name}))
		if err != nil || g.Msg.Graph == nil {
			t.Fatalf("graph %+v %v", g, err)
		}
	}
	if _, err := h.GetMethodologyView(ctx, connect.NewRequest(&registryv1.GetMethodologyViewRequest{Name: "nope", Version: "1"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("an unknown version is not found: %v", err)
	}
}
