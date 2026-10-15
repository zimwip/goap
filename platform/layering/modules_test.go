package layering

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// modules lists the modules of the go.work (ADR 0125 §1), one directory each at the root of the repository.
var modules = []string{"contracts", "lib", "graph", "engine", "mcp", "gateway", "platform"}

// dependsOn lists, by component, the components it may depend on besides itself.
var dependsOn = map[string][]string{
	"contracts": {},
	"lib":       {"contracts"},
	"graph":     {"contracts", "lib"},
	"engine":    {"contracts", "lib"},
	"mcp":       {"contracts", "lib"},
	"gateway":   {"contracts", "lib"},
	"platform":  {"contracts", "lib", "graph", "engine", "mcp", "gateway"},
}

// componentOf is the module of a package of the repository: the first element of its path.
func componentOf(pkg string) string {
	c, _, _ := strings.Cut(pkg, "/")
	if slices.Contains(modules, c) {
		return c
	}
	return ""
}

// Every package, its tests included, depends only on the modules its own may depend on (ADR 0125 §1). In the
// workspace a package may import any module of the go.work, whatever its go.mod says, so the rule is checked here.
func TestComponents(t *testing.T) {
	args := []string{"list", "-json=ImportPath,Imports,TestImports,XTestImports"}
	for _, m := range modules {
		args = append(args, "./"+m+"/...")
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	var bad []string
	for dec.More() {
		var p struct {
			ImportPath                         string
			Imports, TestImports, XTestImports []string
		}
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		src := strings.TrimPrefix(p.ImportPath, mod)
		from := componentOf(src)
		if from == "" {
			t.Errorf("%s is in no module of the go.work", src)
			continue
		}
		check := func(deps []string, test string) {
			for _, d := range deps {
				if !strings.HasPrefix(d, mod) {
					continue
				}
				dep := strings.TrimPrefix(d, mod)
				to := componentOf(dep)
				if to != from && !slices.Contains(dependsOn[from], to) {
					bad = append(bad, from+" -> "+to+": "+test+src+" imports "+dep)
				}
			}
		}
		check(p.Imports, "")
		check(append(p.TestImports, p.XTestImports...), "the tests of ")
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Error(b)
	}
}

// Each go.mod requires the modules its module may depend on, and only those: a module builds outside the workspace too
// (the images build each service from its own go.mod's replace directives).
func TestModuleRequirements(t *testing.T) {
	for _, m := range modules {
		b, err := os.ReadFile(filepath.Join("..", "..", m, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(strings.TrimPrefix(line, "require "))
			if len(f) >= 2 && strings.HasPrefix(f[0], mod) && f[1] != "=>" && !strings.HasPrefix(line, "module") {
				got = append(got, strings.TrimPrefix(f[0], mod))
			}
		}
		slices.Sort(got)
		want := slices.Clone(dependsOn[m])
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s/go.mod requires %v, its module may depend on %v", m, got, want)
		}
	}
}
