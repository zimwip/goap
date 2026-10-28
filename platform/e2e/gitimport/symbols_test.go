//go:build cgo

package gitimporte2e

import (
	"context"
	"maps"
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
	nodes, links, err := g.CommitGraph(ctx, head.ID)
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
		if s.Version != l.To.Version || s.Deleted {
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
	if keys(got) != "field:point.x function:add function:main struct:point" {
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
	if keys(got) != "field:point.x function:main function:sub struct:point" || got["struct:point"] != 1 || got["function:main"] != 2 {
		t.Fatalf("symbols: %v", got)
	}
	add, err := g.NodeByKey(ctx, gitimport.Namespace, file+"#function:add")
	if err != nil || !add.Deleted {
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
		if n, err := g.NodeByKey(ctx, gitimport.Namespace, file+k); err != nil || !n.Deleted {
			t.Fatalf("%s: %+v %v", k, n, err)
		}
	}
	tree, _ := graphTree(t, g, "demo")
	same(t, r.tree(), tree)
}

// codeGraph is the head of the namespace: the nodes by key, and the links (suspect ones included) as
// "<from key> <type> <to key>".
func codeGraph(t *testing.T, g *graph.Graph) (map[string]domain.Node, map[string]bool) {
	t.Helper()
	ctx := context.Background()
	head, err := g.BranchHead(ctx, gitimport.Namespace, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	nodes, links, err := g.CommitGraph(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	suspect, err := g.SuspectLinks(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	byKey, byID := map[string]domain.Node{}, map[domain.NodeID]domain.Node{}
	for _, n := range nodes {
		byKey[n.Key], byID[n.ID] = n, n
	}
	out := map[string]bool{}
	for _, l := range append(links, suspect...) {
		out[byID[l.From.ID].Key+" "+strings.TrimPrefix(l.Type, "code@")+" "+byID[l.To.ID].Key] = true
	}
	return byKey, out
}

// suspect tells whether the link of a type from a node to another is suspect at the head.
func suspect(t *testing.T, g *graph.Graph, from, to string) bool {
	t.Helper()
	ctx := context.Background()
	head, err := g.BranchHead(ctx, gitimport.Namespace, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	links, err := g.SuspectLinks(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := g.NodeByKey(ctx, gitimport.Namespace, from)
	d, _ := g.NodeByKey(ctx, gitimport.Namespace, to)
	for _, l := range links {
		if l.From.ID == f.ID && l.To.ID == d.ID {
			return true
		}
	}
	return false
}

func changeCount(t *testing.T, g *graph.Graph) int {
	t.Helper()
	cs, err := g.ListChanges(context.Background(), graph.ChangesFilter{Namespace: gitimport.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	return len(cs)
}

// TestImportTheCodeModel imports a Go module: a node per declaration of its type, the members of a struct, the calls
// and uses between declarations (across packages through the imports), the packages and their imports; the symbols
// keep their node and their version while their text and links do not change, a call to a removed function goes with
// it, and running again imports nothing.
func TestImportTheCodeModel(t *testing.T) {
	ctx := context.Background()
	g := newGraph(t)
	ts, err := gitimport.NewTreeSitter()
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	r := newRepo(t)
	run := func() gitimport.Stats {
		t.Helper()
		st, err := (&gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project, Symbols: ts, Code: true, Parallel: 3}).Run(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	point := `package shapes

import "fmt"

const Max = 10

type Point struct {
	X, Y int
}

func NewPoint(x, y int) *Point { return &Point{X: x, Y: y} }

func (p *Point) Scale(k int) { p.X *= k; p.check() }

func (p *Point) check() {
	if p.X > Max {
		fmt.Println("big")
	}
}
`
	r.write("go.mod", "module example.com/demo\n\ngo 1.22\n")
	r.write("shapes/point.go", point)
	r.write("main.go", "package main\n\nimport \"example.com/demo/shapes\"\n\nfunc main() { p := shapes.NewPoint(1, 2); p.Scale(2) }\n")
	r.commit("Initial")
	run()
	const (
		pt       = "demo:shapes/point.go"
		main     = "demo:main.go#function:main"
		shapes   = "demo@go:example.com/demo/shapes"
		root     = "demo@go:example.com/demo"
		fmtPkg   = "demo@go:fmt"
		newPoint = pt + "#function:NewPoint"
	)
	nodes, links := codeGraph(t, g)
	for k, typ := range map[string]string{pt + "#struct:Point": gitimport.TypeStruct, pt + "#method:Point.Scale": gitimport.TypeMethod,
		pt + "#field:Point.X": gitimport.TypeField, pt + "#constant:Max": gitimport.TypeConstant, newPoint: gitimport.TypeFunction,
		main: gitimport.TypeFunction, shapes: gitimport.TypePackage, root: gitimport.TypePackage, fmtPkg: gitimport.TypePackage} {
		if nodes[k].Type != typ {
			t.Errorf("%s: type %q, want %s", k, nodes[k].Type, typ)
		}
	}
	if code, _ := nodes[newPoint].Properties["code"].(string); code != "---\nlanguage: go\n---\n```go\nfunc NewPoint(x, y int) *Point { return &Point{X: x, Y: y} }\n```\n" {
		t.Errorf("code of NewPoint: %q", code)
	}
	if n := nodes[shapes].Properties["name"]; n != "shapes" {
		t.Errorf("package name %v", n)
	}
	if n := nodes[root].Properties["name"]; n != "main" {
		t.Errorf("package name %v", n)
	}
	want := []string{
		pt + "#method:Point.Scale member_of " + pt + "#struct:Point",
		pt + "#method:Point.check member_of " + pt + "#struct:Point",
		pt + "#field:Point.X member_of " + pt + "#struct:Point",
		pt + "#method:Point.Scale calls " + pt + "#method:Point.check",
		pt + "#method:Point.check uses " + pt + "#constant:Max",
		newPoint + " uses " + pt + "#struct:Point",
		main + " calls " + newPoint,
		pt + " in_package " + shapes,
		pt + " imports " + fmtPkg,
		"demo:main.go in_package " + root,
		"demo:main.go imports " + shapes,
		root + " depends_on " + shapes,
		shapes + " depends_on " + fmtPkg,
	}
	if testing.Verbose() {
		for _, l := range slices.Sorted(maps.Keys(links)) {
			t.Log(l)
		}
	}
	for _, w := range want {
		if !links[w] {
			t.Errorf("no link %s", w)
		}
	}
	// NewPoint becomes Make, main.go does not change: its call to NewPoint stays, suspect (NewPoint has another
	// version, removed), until main changes
	r.write("shapes/point.go", strings.Replace(point, "func NewPoint(", "func Make(", 1))
	r.commit("Rename NewPoint")
	st := run()
	nodes, links = codeGraph(t, g)
	if _, held := nodes[newPoint]; held || nodes[pt+"#function:Make"].Deleted {
		t.Fatalf("NewPoint %+v, Make %+v", nodes[newPoint], nodes[pt+"#function:Make"])
	}
	if !suspect(t, g, main, newPoint) {
		t.Error("main's call to NewPoint is not suspect")
	}
	for _, k := range []string{pt + "#struct:Point", pt + "#method:Point.Scale", pt + "#constant:Max", shapes} {
		if nodes[k].Version != 1 {
			t.Errorf("%s: v%d, it did not change", k, nodes[k].Version)
		}
	}
	if nodes[main].Version != 1 || st.Symbols != 2 { // NewPoint removed, Make created
		t.Errorf("main v%d, %+v", nodes[main].Version, st)
	}
	// main calls Make
	r.write("main.go", "package main\n\nimport \"example.com/demo/shapes\"\n\nfunc main() { p := shapes.Make(1, 2); p.Scale(2) }\n")
	r.write("README", "demo\n")
	r.commit("Call Make")
	run()
	nodes, links = codeGraph(t, g)
	if !links[main+" calls "+pt+"#function:Make"] || links[main+" calls "+newPoint] || nodes[main].Version != 2 {
		t.Errorf("main v%d does not call Make alone", nodes[main].Version)
	}
	// a commit of a file that is no source changes no symbol and no package
	r.write("README", "demo, again\n")
	r.commit("Readme")
	if st := run(); st.Symbols != 0 || st.Packages != 0 || st.Created != 0 {
		t.Errorf("%+v", st)
	}
	// running again imports nothing: no change, no node
	before, beforeNodes := changeCount(t, g), len(nodes)
	if st := run(); st.Commits != 0 || st.Created+st.Modified+st.Removed != 0 {
		t.Errorf("%+v", st)
	}
	if nodes, _ = codeGraph(t, g); changeCount(t, g) != before || len(nodes) != beforeNodes {
		t.Errorf("a run with nothing new made a change or a node")
	}
	tree, _ := graphTree(t, g, "demo")
	same(t, r.tree(), tree)
}

// TestImportLinksAcrossFiles resolves the names of Python, TypeScript and C files through their imports.
func TestImportLinksAcrossFiles(t *testing.T) {
	ctx := context.Background()
	g := newGraph(t)
	ts, err := gitimport.NewTreeSitter()
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	r := newRepo(t)
	r.write("pkg/__init__.py", "")
	r.write("pkg/base.py", "class Base:\n    pass\n")
	r.write("pkg/util.py", "LIMIT = 3\n\ndef helper(x):\n    return x\n")
	r.write("pkg/shape.py", "from pkg.base import Base\nfrom .util import helper as h\nimport pkg.util\n\nclass Shape(Base):\n    def area(self):\n        return h(self.side()) + pkg.util.LIMIT\n\n    def side(self):\n        return 1\n")
	r.write("web/lib/a.ts", "export function a(): number { return 1; }\nexport class Box {}\n")
	r.write("web/main.ts", "import { a, Box as B } from './lib/a';\nimport * as React from 'react';\nexport function main() { a(); new B(); React.render(); }\n")
	r.write("c/util.h", "int helper(int x);\n")
	r.write("c/util.c", "#include \"util.h\"\nint helper(int x) { return x; }\n")
	r.write("c/main.c", "#include <stdio.h>\n#include \"util.h\"\nint main(void) { printf(\"%d\", helper(1)); return 0; }\n")
	r.commit("Initial")
	if _, err := (&gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project, Symbols: ts}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	nodes, links := codeGraph(t, g)
	for _, w := range []string{
		"demo:pkg/shape.py#class:Shape uses demo:pkg/base.py#class:Base",
		"demo:pkg/shape.py#method:Shape.area calls demo:pkg/util.py#function:helper",
		"demo:pkg/shape.py#method:Shape.area calls demo:pkg/shape.py#method:Shape.side",
		"demo:pkg/shape.py#method:Shape.area uses demo:pkg/util.py#variable:LIMIT",
		"demo:pkg/shape.py in_package demo@python:pkg",
		"demo@python:pkg depends_on demo@python:pkg",
		"demo:web/main.ts#function:main calls demo:web/lib/a.ts#function:a",
		"demo:web/main.ts#function:main calls demo:web/lib/a.ts#class:Box",
		"demo:web/main.ts imports demo@js:web/lib",
		"demo:web/main.ts imports demo@js:react",
		"demo@js:web depends_on demo@js:web/lib",
		"demo:c/main.c#function:main calls demo:c/util.h#prototype:helper",
		"demo:c/main.c imports demo@c:stdio.h",
	} {
		if strings.Contains(w, "depends_on demo@python:pkg") {
			if links[w] {
				t.Errorf("a package depends on itself: %s", w)
			}
			continue
		}
		if !links[w] {
			t.Errorf("no link %s", w)
		}
	}
	if code, _ := nodes["demo:pkg/util.py#function:helper"].Properties["code"]; code != nil {
		t.Errorf("code kept without Code: %v", code)
	}
}

// TestImportVersionsWhatChanged gives a new version only to the declarations whose own text changed: a method edited
// leaves its class alone, the class's own text edited leaves its methods alone.
func TestImportVersionsWhatChanged(t *testing.T) {
	ctx := context.Background()
	g := newGraph(t)
	ts, err := gitimport.NewTreeSitter()
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	r := newRepo(t)
	run := func() gitimport.Stats {
		t.Helper()
		st, err := (&gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project, Symbols: ts, Code: true}).Run(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	src := "class Shape:\n    \"\"\"A shape of 3 sides.\"\"\"\n    sides = 3\n\n    def area(self):\n        return 1\n\n    def name(self):\n        return 'shape'\n"
	const (
		shape = "demo:shape.py#class:Shape"
		area  = "demo:shape.py#method:Shape.area"
		name  = "demo:shape.py#method:Shape.name"
	)
	versions := func() (v map[string]domain.Version) {
		t.Helper()
		nodes, _ := codeGraph(t, g)
		return map[string]domain.Version{shape: nodes[shape].Version, area: nodes[area].Version, name: nodes[name].Version}
	}
	r.write("shape.py", src)
	r.commit("Initial")
	run()
	if v := versions(); v[shape] != 1 || v[area] != 1 || v[name] != 1 {
		t.Fatalf("%v", v)
	}
	nodes, _ := codeGraph(t, g)
	if code, _ := nodes[shape].Properties["code"].(string); strings.Contains(code, "def area") {
		t.Errorf("the code of the class holds its methods: %q", code)
	}
	// a method's body
	src = strings.Replace(src, "return 1", "return 2", 1)
	r.write("shape.py", src)
	r.commit("Area")
	if st := run(); st.Symbols != 1 {
		t.Errorf("%+v", st)
	}
	if v := versions(); v[shape] != 1 || v[area] != 2 || v[name] != 1 {
		t.Errorf("a method edited: %v", v)
	}
	// a method removed, the class's own text kept
	src = strings.Replace(src, "\n    def name(self):\n        return 'shape'\n", "", 1)
	r.write("shape.py", src)
	r.commit("No name")
	run()
	if v := versions(); v[shape] != 1 || v[area] != 2 || v[name] != 0 { // deleted (ADR 0172): not in the head
		t.Errorf("a method removed: %v", v)
	}
	// the class's own text
	src = strings.Replace(src, "of 3 sides", "of 4 sides", 1)
	r.write("shape.py", src)
	r.commit("Sides")
	run()
	if v := versions(); v[shape] != 2 || v[area] != 2 {
		t.Errorf("the class edited: %v", v)
	}
}
