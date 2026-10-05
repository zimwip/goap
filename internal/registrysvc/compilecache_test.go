package registrysvc

import (
	"sync"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
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
	if err != nil || c3 == c1 || c3.Version != "2" {
		t.Fatalf("a published version is compiled afresh: %v", err)
	}

	// a domain publish
	n := s.compiled.compiles.Load()
	if _, _, err := s.ImportDomain(ctx, []byte("name: other\nversion: \"1\"\nnodeTypes: [X]\n"), true); err != nil {
		t.Fatal(err)
	}
	if c4, err := s.Methodology(ctx, m.Name); err != nil || c4 == c3 || s.compiled.compiles.Load() != n+1 {
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
