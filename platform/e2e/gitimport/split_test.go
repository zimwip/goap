//go:build cgo

package gitimporte2e

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/platform/gitimport"
)

// codeState is the head of the code namespace without its versions: each node's type and state, the links, those that
// are suspect.
func codeState(t *testing.T, g *graph.Graph) (nodes map[string]string, links, suspect map[string]bool) {
	t.Helper()
	ctx := context.Background()
	head, err := g.BranchHead(ctx, gitimport.Namespace, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	all, fresh, err := g.CommitGraph(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := g.SuspectLinks(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[domain.NodeID]string{}
	nodes = map[string]string{}
	for _, n := range all {
		byID[n.ID], nodes[n.Key] = n.Key, n.Type+" "+n.State
	}
	str := func(l domain.Link) string {
		return byID[l.From.ID] + " " + strings.TrimPrefix(l.Type, "code@") + " " + byID[l.To.ID]
	}
	links, suspect = map[string]bool{}, map[string]bool{}
	for _, l := range fresh {
		links[str(l)] = true
	}
	for _, l := range stale {
		links[str(l)], suspect[str(l)] = true, true
	}
	return nodes, links, suspect
}

func sameState(t *testing.T, what string, want, got *graph.Graph) {
	t.Helper()
	wn, wl, ws := codeState(t, want)
	gn, gl, gs := codeState(t, got)
	if !maps.Equal(wn, gn) {
		t.Errorf("%s: nodes differ:\nwant %v\ngot  %v", what, wn, gn)
	}
	diff := func(name string, w, g map[string]bool) {
		for _, k := range slices.Sorted(maps.Keys(w)) {
			if !g[k] {
				t.Errorf("%s: %s %s missing", what, name, k)
			}
		}
		for _, k := range slices.Sorted(maps.Keys(g)) {
			if !w[k] {
				t.Errorf("%s: %s %s unexpected", what, name, k)
			}
		}
	}
	diff("link", wl, gl)
	diff("suspect link", ws, gs)
}

// TestImportSplitsACommit writes the commits through sub-changes, one per file: the graph is the one a change per commit
// gives, the links between declarations of different files included; a run cancelled at any write is resumed to the
// same graph.
func TestImportSplitsACommit(t *testing.T) {
	ts, err := gitimport.NewTreeSitter()
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	r := newRepo(t)
	importer := func(g gitimport.Port, batch int) *gitimport.Importer {
		return &gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project, Symbols: ts, Code: true, Batch: batch}
	}
	run := func(g *graph.Graph, batch int) {
		t.Helper()
		if _, err := importer(g, batch).Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	whole, split := newGraph(t), newGraph(t)
	r.write("go.mod", "module example.com/demo\n\ngo 1.22\n")
	r.write("a/a.go", "package a\n\nimport \"example.com/demo/z\"\n\nfunc A() int { return z.Z() + B() }\n\nfunc B() int { return 1 }\n")
	r.write("z/z.go", "package z\n\nimport \"example.com/demo/a\"\n\nfunc Z() int { return 2 }\n\nfunc W() int { return a.B() }\n")
	r.write("main.go", "package main\n\nimport (\n\t\"example.com/demo/a\"\n\t\"example.com/demo/z\"\n)\n\nfunc main() { a.A(); z.W() }\n")
	r.commit("Initial")
	run(whole, 0)
	run(split, 3)
	sameState(t, "initial", whole, split)
	changes, err := split.ListChanges(context.Background(), graph.ChangesFilter{Namespace: gitimport.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	subs := 0
	for _, c := range changes {
		if c.ParentID != "" {
			subs++
		}
	}
	if subs < 3 {
		t.Fatalf("the commit was not written by sub-changes: %d changes", len(changes))
	}
	r.write("z/z.go", "package z\n\nimport \"example.com/demo/a\"\n\nfunc Z() int { return 3 }\n\nfunc W() int { return a.B() + a.A() }\n")
	r.write("a/a.go", "package a\n\nimport \"example.com/demo/z\"\n\nfunc A() int { return z.Z() + C() }\n\nfunc C() int { return 1 }\n")
	r.commit("Rename B")
	run(whole, 0)
	run(split, 3)
	sameState(t, "renamed", whole, split)

	// cancelled at every write of a split run, then resumed
	count := &cancelling{Port: newGraph(t), cancel: func() {}}
	if _, err := importer(count, 3).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= count.calls; n++ {
		g := newGraph(t)
		ctx, cancel := context.WithCancel(context.Background())
		_, err := importer(&cancelling{Port: g, cancel: cancel, n: n}, 3).Run(ctx)
		cancel()
		// a cancel at the last write may come once the work is done (see TestImportCancelled)
		if !errors.Is(err, context.Canceled) && (err != nil || n < count.calls) {
			t.Fatalf("cancelled at write %d: %v", n, err)
		}
		cs, _ := g.ListChanges(context.Background(), graph.ChangesFilter{Namespace: gitimport.Namespace})
		for _, c := range cs {
			if c.Status != domain.ChangeApplied && c.Status != domain.ChangeAbandoned {
				t.Fatalf("cancelled at write %d: change %q left %s", n, c.Title, c.Status)
			}
		}
		run(g, 3)
		sameState(t, "resumed", whole, g)
		if t.Failed() {
			t.Fatalf("cancelled at write %d", n)
		}
	}
}
