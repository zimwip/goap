package layering

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Both compositions boot through graphsvc.Boot: neither spells the platform seeds by hand.
func TestCompositionsUseBoot(t *testing.T) {
	for _, dir := range []string{"../cmd/goap-dev", "../cmd/graph"} {
		// a composition may spread its wiring over several files of its package (cmd/goap-dev, ADR 0073)
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: no source (%v)", dir, err)
		}
		path, src := dir, ""
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src += string(b)
		}
		if !strings.Contains(src, "graphsvc.Boot(") {
			t.Errorf("%s does not call graphsvc.Boot", path)
		}
		for _, seed := range []string{"SeedAccess", "SeedBuiltins", "SeedModels", "SeedProtectedAliases", "SeedBehaviors", "SeedDefaults", "SeedDemo"} {
			if strings.Contains(src, "graphsvc."+seed) {
				t.Errorf("%s calls graphsvc.%s directly", path, seed)
			}
		}
	}
}
