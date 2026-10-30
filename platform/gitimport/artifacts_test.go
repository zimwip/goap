package gitimport

import (
	"slices"
	"testing"
)

func tree(paths ...string) []Entry {
	var out []Entry
	for _, p := range paths {
		mode := "100644"
		if p[len(p)-1] == '/' {
			mode, p = ModeTree, p[:len(p)-1]
		}
		out = append(out, Entry{NewMode: mode, Status: 'A', Path: p})
	}
	return out
}

// The build artifacts of a tree are found by their names: the directories a build or a package manager makes (the
// weak names only when they hold compiled output), then the files a build makes outside them.
func TestDetect(t *testing.T) {
	got := Detect(tree(
		"src/", "src/main.go", "src/api.pb.go",
		"web/", "web/node_modules/", "web/node_modules/x/", "web/node_modules/x/dist/", "web/node_modules/x/dist/x.min.js",
		"web/dist/", "web/dist/app.js", "web/static/", "web/static/lib.min.js",
		"build/", "build/Dockerfile", // a build directory of sources: left alone
		"out/", "out/app.jar", // one of compiled output: found, with what it holds
		"tools/", "tools/__pycache__/", "tools/__pycache__/x.pyc",
	))
	ids := map[string][]string{}
	for _, a := range got {
		ids[a.ID] = a.Paths
	}
	want := map[string][]string{
		"node_modules": {"web/node_modules"},
		"dist":         {"web/dist"},
		"out":          {"out"},
		"__pycache__":  {"tools/__pycache__"},
		"minified":     {"web/static/lib.min.js"},
		"generated":    {"src/api.pb.go"},
	}
	if len(ids) != len(want) {
		t.Fatalf("found %v", ids)
	}
	for id, paths := range want {
		if !slices.Equal(ids[id], paths) {
			t.Errorf("%s: %v, want %v", id, ids[id], paths)
		}
	}
	for _, a := range got {
		if a.Label == "" || len(a.Patterns) == 0 {
			t.Errorf("%+v", a)
		}
	}
}

func TestExcluded(t *testing.T) {
	ps := []string{"node_modules", "*.min.js", "web/dist", "docs/*/build"}
	for p, want := range map[string]bool{
		"node_modules":            true,
		"web/node_modules/x/a.js": true,
		"web/static/lib.min.js":   true,
		"web/dist":                true,
		"web/dist/app.js":         true,
		"dist/app.js":             false,
		"docs/a/build/x":          true,
		"docs/build/x":            false,
		"src/main.go":             false,
		"":                        false,
	} {
		if got := Excluded(ps, p); got != want {
			t.Errorf("%q: %v", p, got)
		}
	}
	if got := ParsePatterns(" node_modules, /web/dist/ \n*.min.js,,node_modules"); !slices.Equal(got, []string{"node_modules", "web/dist", "*.min.js"}) {
		t.Fatalf("patterns %v", got)
	}
}

func TestDecision(t *testing.T) {
	d := decisionOf(Decision{Exclude: []string{"node_modules"}, Include: []string{"minified"}}.value())
	if !d.decided(Artifact{ID: "minified", Patterns: []string{"*.min.js"}}) || !d.decided(Artifact{ID: "node_modules", Patterns: []string{"node_modules"}}) ||
		d.decided(Artifact{ID: "dist", Patterns: []string{"dist"}}) {
		t.Fatalf("decision %+v", d)
	}
}
