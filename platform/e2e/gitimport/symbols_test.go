//go:build cgo

package gitimporte2e

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/platform/gitimport"
)

// symbols lists what a file defines at the head: "<kind>:<name>" → version, each link checked to name the head's version.
func symbols(t *testing.T, g *graph.Graph, file string) map[string]domain.Version {
	t.Helper()
	ctx := context.Background()
	head, err := g.BranchHead(ctx, gitimport.Namespace, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	nodes, links, err := g.BaselineGraph(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[domain.NodeID]domain.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	out := map[string]domain.Version{}
	for _, l := range links {
		if l.Type != gitimport.LinkDefines || byID[l.From.ID].Key != file {
			continue
		}
		s := byID[l.To.ID]
		if s.Version != l.To.Version || s.State != gitimport.StatePresent {
			t.Fatalf("%s defines %s v%d (%s), the head has v%d", file, s.Key, l.To.Version, s.State, s.Version)
		}
		name := s.Properties["name"].(string)
		if c, _ := s.Properties["container"].(string); c != "" {
			name = c + "." + name
		}
		out[s.Properties["kind"].(string)+":"+name] = s.Version
	}
	return out
}

func keys(m map[string]domain.Version) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}

func TestImportReadsTheSymbols(t *testing.T) {
	ctx := context.Background()
	g := newGraph(t)
	ts, err := gitimport.NewTreeSitter()
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	r := newRepo(t)
	dir := r.dir
	run := func() gitimport.Stats {
		t.Helper()
		st, err := (&gitimport.Importer{Graph: g, Dir: dir, Name: "demo", ProjectID: project, Symbols: ts}).Run(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	r.write("src/main.c", "#include <stdio.h>\nstruct point { int x; };\nint add(int a, int b) { return a + b; }\nint main(void) { return add(1, 2); }\n")
	r.write("README", "not a source\n")
	r.commit("Initial")
	run()
	const file = "demo:src/main.c"
	got := symbols(t, g, file)
	if keys(got) != "function:add function:main import:stdio.h struct:point" {
		t.Fatalf("symbols: %s", keys(got))
	}
	// main changes, add is removed, sub is new: point and the include keep their version
	r.write("src/main.c", "#include <stdio.h>\nstruct point { int x; };\nint sub(int a, int b) { return a - b; }\n\n\nint main(void) { return sub(2, 1); }\n")
	r.commit("Sub instead of add")
	st := run()
	if st.Symbols != 3 { // main modified, sub created, add removed
		t.Fatalf("%+v", st)
	}
	got = symbols(t, g, file)
	if keys(got) != "function:main function:sub import:stdio.h struct:point" || got["struct:point"] != 1 || got["function:main"] != 2 {
		t.Fatalf("symbols: %v", got)
	}
	add, err := g.NodeByKey(ctx, gitimport.Namespace, file+"#function:add")
	if err != nil || add.State != gitimport.StateRemoved {
		t.Fatalf("add: %+v %v", add, err)
	}
	// add is back: the same node, present again; the file removed takes its symbols with it
	r.write("src/main.c", "#include <stdio.h>\nstruct point { int x; };\nint add(int a, int b) { return a + b; }\nint sub(int a, int b) { return a - b; }\n\n\nint main(void) { return sub(2, 1); }\n")
	r.commit("Add is back")
	run()
	if got = symbols(t, g, file); got["function:add"] != 3 {
		t.Fatalf("symbols: %v", got)
	}
	r.git("rm", "-q", "src/main.c")
	r.write("src/other.go", "package src\n\nfunc Other() {}\n")
	r.commit("Go")
	run()
	if got = symbols(t, g, "demo:src/other.go"); keys(got) != "function:Other" {
		t.Fatalf("symbols: %v", got)
	}
	for _, k := range []string{"#function:add", "#function:sub", "#struct:point"} {
		if n, err := g.NodeByKey(ctx, gitimport.Namespace, file+k); err != nil || n.State != gitimport.StateRemoved {
			t.Fatalf("%s: %+v %v", k, n, err)
		}
	}
	tree, _ := graphTree(t, g, "demo")
	same(t, r.tree(), tree)
}
