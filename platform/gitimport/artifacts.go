package gitimport

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
)

// Build artifacts: what a build makes or a package manager installs (dist/, node_modules/, target/, minified bundles,
// compiled objects, generated sources) has no place in the graph of the code, yet repositories commit it. At its
// start a run looks for it in the tree of the revision it follows (Detect, by names only, so the same rules hold for
// every commit of the history), asks which of what it found to import anyway (Importer.Decide; the git importer asks
// the person through its job), and leaves the rest out of every commit: an excluded path is never read nor written,
// and a node the graph holds at such a path is deleted the next time a commit touches it. The decision is recorded on
// the repository node (its attribute artifacts) and the next runs ask only about what they find anew.

// Artifact is a kind of build artifact a tree holds: what the person decides on.
type Artifact struct {
	// ID names it in the decision: the directory name (node_modules) or the kind of file (minified).
	ID string
	// Label says what it is, how many of it the tree holds and where (the first few paths).
	Label string
	// Patterns are what an exclusion leaves out (see Excluded).
	Patterns []string
	// Paths are the paths found (the topmost: a directory, not what it holds), sorted.
	Paths []string
}

// artifactDir is a directory name a build or a package manager makes; weak ones are often sources too and count only
// when they hold compiled output.
type artifactDir struct {
	name, what string
	weak       bool
}

var artifactDirs = []artifactDir{
	{name: "node_modules", what: "packages installed by npm, yarn or pnpm"},
	{name: "bower_components", what: "packages installed by Bower"},
	{name: "jspm_packages", what: "packages installed by jspm"},
	{name: "dist", what: "distribution build"},
	{name: "target", what: "build output of Cargo, Maven or sbt"},
	{name: "obj", what: "intermediate build output (.NET, C)"},
	{name: "__pycache__", what: "compiled Python"},
	{name: ".venv", what: "Python virtual environment"},
	{name: "venv", what: "Python virtual environment"},
	{name: ".tox", what: "tox environments"},
	{name: ".pytest_cache", what: "pytest cache"},
	{name: ".mypy_cache", what: "mypy cache"},
	{name: ".gradle", what: "Gradle cache"},
	{name: ".next", what: "Next.js build"},
	{name: ".nuxt", what: "Nuxt build"},
	{name: ".svelte-kit", what: "SvelteKit build"},
	{name: ".angular", what: "Angular cache"},
	{name: ".parcel-cache", what: "Parcel cache"},
	{name: ".turbo", what: "Turborepo cache"},
	{name: ".nyc_output", what: "coverage data"},
	{name: "htmlcov", what: "coverage report"},
	{name: "coverage", what: "coverage report", weak: true},
	{name: "build", what: "build output", weak: true},
	{name: "out", what: "build output", weak: true},
	{name: "bin", what: "compiled binaries", weak: true},
}

// artifactFiles are kinds of files a build makes, by name.
var artifactFiles = []struct {
	id, what string
	patterns []string
}{
	{id: "minified", what: "minified bundles", patterns: []string{"*.min.js", "*.min.mjs", "*.min.css"}},
	{id: "sourcemaps", what: "source maps", patterns: []string{"*.js.map", "*.mjs.map", "*.css.map"}},
	{id: "compiled", what: "compiled objects and archives", patterns: []string{"*.class", "*.o", "*.obj", "*.pyc", "*.pyo", "*.so",
		"*.dylib", "*.dll", "*.exe", "*.a", "*.lib", "*.jar", "*.war", "*.ear", "*.wasm", "*.pdb"}},
	{id: "generated", what: "generated sources", patterns: []string{"*.pb.go", "*.pb.gw.go", "*_pb2.py", "*_pb2_grpc.py", "*.pb.cc",
		"*.pb.h", "*_pb.js", "*_pb.d.ts", "*.g.dart", "*.freezed.dart", "*.generated.*", "*_generated.*", "zz_generated.*",
		"*.Designer.cs", "__generated__", "generated"}},
}

// outputFile tells a file a build makes (what makes a weak directory a build output).
func outputFile(name string) bool {
	for _, k := range artifactFiles[:3] {
		for _, p := range k.patterns {
			if ok, _ := path.Match(p, name); ok {
				return true
			}
		}
	}
	return false
}

// maxShown bounds the paths a label names.
const maxShown = 3

// Detect finds the build artifacts of a tree (its entries, trees included, as git ls-tree -r -t lists them): the
// directories named as a build or a package manager makes them (the weak names only when they hold compiled output),
// then the files named as a build makes them outside those directories. Nothing is read but the names.
func Detect(entries []Entry) []Artifact {
	byID := map[string]*Artifact{}
	var order []string
	add := func(id, label string, patterns []string, p string) {
		a := byID[id]
		if a == nil {
			a = &Artifact{ID: id, Label: label, Patterns: patterns}
			byID[id] = a
			order = append(order, id)
		}
		a.Paths = append(a.Paths, p)
	}
	// the directories holding compiled output, for the weak names
	output := map[string]bool{}
	for _, e := range entries {
		if IsTree(e.NewMode) || !outputFile(path.Base(e.Path)) {
			continue
		}
		for d := dirOf(e.Path); d != ""; d = dirOf(d) {
			if output[d] {
				break
			}
			output[d] = true
		}
	}
	sorted := slices.Clone(entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	var taken []string // the directories found: what they hold is theirs
	under := func(p string) bool {
		for _, d := range taken {
			if strings.HasPrefix(p, d+"/") {
				return true
			}
		}
		return false
	}
	for _, e := range sorted {
		if under(e.Path) {
			continue
		}
		name := path.Base(e.Path)
		if IsTree(e.NewMode) {
			for _, d := range artifactDirs {
				if d.name == name && (!d.weak || output[e.Path]) {
					add(d.name, d.name+"/ ("+d.what+")", []string{d.name}, e.Path)
					taken = append(taken, e.Path)
					break
				}
			}
			// a directory of generated sources (__generated__, generated) is one of the generated kind
			if g := artifactFiles[len(artifactFiles)-1]; !under(e.Path) && !slices.Contains(taken, e.Path) && matchAny(g.patterns, name) {
				add(g.id, kindLabel(g.what, g.patterns), g.patterns, e.Path)
				taken = append(taken, e.Path)
			}
			continue
		}
		for _, k := range artifactFiles {
			if matchAny(k.patterns, name) {
				add(k.id, kindLabel(k.what, k.patterns), k.patterns, e.Path)
				break
			}
		}
	}
	out := make([]Artifact, 0, len(order))
	for _, id := range order {
		a := byID[id]
		a.Label = labelOf(a)
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// labelOf is what the person reads of an artifact: what it is, how many and where.
func labelOf(a *Artifact) string {
	shown := a.Paths[:min(maxShown, len(a.Paths))]
	more := ""
	if n := len(a.Paths) - len(shown); n > 0 {
		more = fmt.Sprintf(" and %d more", n)
	}
	return fmt.Sprintf("%s: %d found, %s%s", a.Label, len(a.Paths), strings.Join(shown, ", "), more)
}

// kindLabel names a kind of file by what it is and its first patterns.
func kindLabel(what string, patterns []string) string {
	return what + " (" + strings.Join(patterns[:min(3, len(patterns))], ", ") + "…)"
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// Excluded tells a path an exclusion leaves out: a pattern without a / names an element of the path (node_modules,
// *.min.js), anywhere; one with a / names the path or one of its parent directories from the root (web/dist,
// docs/*/build). Patterns use the syntax of path.Match.
func Excluded(patterns []string, p string) bool {
	if len(patterns) == 0 || p == "" {
		return false
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		prefix := strings.Join(segs[:i+1], "/")
		for _, pat := range patterns {
			target := s
			if strings.Contains(pat, "/") {
				target = prefix
			}
			if ok, _ := path.Match(strings.Trim(pat, "/"), target); ok {
				return true
			}
		}
	}
	return false
}

// ParsePatterns reads a list of exclusion patterns separated by commas or new lines.
func ParsePatterns(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		if f = strings.Trim(strings.TrimSpace(f), "/"); f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// Decision is what a repository was decided on its artifacts, recorded on its node (attribute artifacts): the
// patterns left out and the artifacts imported anyway (their ids).
type Decision struct {
	Exclude []string `json:"exclude,omitempty"`
	Include []string `json:"include,omitempty"`
}

func (d Decision) empty() bool { return len(d.Exclude) == 0 && len(d.Include) == 0 }

// value is the decision as the attribute holds it.
func (d Decision) value() map[string]any {
	out := map[string]any{}
	if len(d.Exclude) > 0 {
		out["exclude"] = toAny(d.Exclude)
	}
	if len(d.Include) > 0 {
		out["include"] = toAny(d.Include)
	}
	return out
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// decisionOf reads the attribute artifacts of the repository node.
func decisionOf(v any) Decision {
	m, _ := v.(map[string]any)
	strs := func(k string) []string {
		var out []string
		l, _ := m[k].([]any)
		for _, x := range l {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return Decision{Exclude: strs("exclude"), Include: strs("include")}
}

// decided tells an artifact the decision settles: imported anyway, or all its patterns left out.
func (d Decision) decided(a Artifact) bool {
	if slices.Contains(d.Include, a.ID) {
		return true
	}
	for _, p := range a.Patterns {
		if !slices.Contains(d.Exclude, p) {
			return false
		}
	}
	return true
}

// decideArtifacts settles the exclusions of the run before its first commit: the patterns given (Exclude) and the
// decision recorded on the repository node, then the artifacts the tree of rev holds that neither settles, asked of
// Decide (none: they are imported, as before this check); nothing is asked when the run has nothing to import (ask
// false).
func (im *Importer) decideArtifacts(ctx context.Context, rev string, ask bool) error {
	d := Decision{Exclude: slices.Clone(im.ix.artifacts.Exclude), Include: slices.Clone(im.ix.artifacts.Include)}
	for _, p := range im.Exclude {
		if !slices.Contains(d.Exclude, p) {
			d.Exclude = append(d.Exclude, p)
		}
	}
	if im.Decide != nil && ask {
		im.phase("looking for build artifacts in " + im.Name)
		tree, err := Tree(ctx, im.Dir, rev)
		if err != nil {
			return err
		}
		var open []Artifact
		for _, a := range Detect(tree.Entries) {
			if !d.decided(a) && !Excluded(d.Exclude, a.Paths[0]) {
				open = append(open, a)
			}
		}
		if len(open) > 0 {
			include, err := im.Decide(ctx, open)
			if err != nil {
				return err
			}
			for _, a := range open {
				if slices.Contains(include, a.ID) {
					d.Include = append(d.Include, a.ID)
					continue
				}
				for _, p := range a.Patterns {
					if !slices.Contains(d.Exclude, p) {
						d.Exclude = append(d.Exclude, p)
					}
				}
			}
		}
	}
	slices.Sort(d.Include)
	im.decision = Decision{Exclude: d.Exclude, Include: slices.Compact(d.Include)}
	if len(im.decision.Exclude) > 0 {
		im.phase("left out: " + strings.Join(im.decision.Exclude, ", "))
	}
	return nil
}

// excluded tells a path the run leaves out.
func (im *Importer) excluded(p string) bool { return Excluded(im.decision.Exclude, p) }
