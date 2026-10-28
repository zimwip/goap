package gitimporte2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graph/graphtest"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/typecat"
	"github.com/zimwip/goap/platform/gitimport"
)

const project = "PROJ-CODE"

func newGraph(t *testing.T) *graph.Graph {
	t.Helper()
	return newGraphOn(t, graph.NewMemory())
}

func newGraphOn(t *testing.T, repo graph.Repo) *graph.Graph {
	t.Helper()
	b, err := os.ReadFile("../../../domains/code.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := def.ParseDomain(b)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(d)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(repo)
	g.Types = func() graph.TypeCatalog { return cat }
	if _, err := graphtest.Project(context.Background(), g, project, "Code"); err != nil {
		t.Fatal(err)
	}
	return g
}

// repo is a git repository written by the test.
type repo struct {
	t   *testing.T
	dir string
	n   int
}

func newRepo(t *testing.T) *repo {
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.org", "GIT_COMMITTER_NAME=Ada",
		"GIT_COMMITTER_EMAIL=ada@example.org", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"GIT_CONFIG_NOSYSTEM=1", "HOME="+r.dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func (r *repo) write(p, content string) {
	r.t.Helper()
	f := filepath.Join(r.dir, p)
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit(msg string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
	r.n++
}

// tree is the tree of HEAD as git lists it: path → object id, directories with a trailing /.
func (r *repo) tree() map[string]string { return r.treeOf("HEAD") }

// treeOf is the tree of a revision as git lists it.
func (r *repo) treeOf(rev string) map[string]string {
	out := map[string]string{"": strings.TrimSpace(r.git("rev-parse", rev+"^{tree}"))}
	for _, line := range strings.Split(strings.TrimSpace(r.git("ls-tree", "-r", "-t", rev)), "\n") {
		if line == "" {
			continue
		}
		meta, p, _ := strings.Cut(line, "\t")
		f := strings.Fields(meta)
		if f[1] == "tree" {
			p += "/"
		}
		out[p] = f[2]
	}
	return out
}

// graphTree walks the repository from its node at the head: path → object id of each entry it reaches.
func graphTree(t *testing.T, g *graph.Graph, name string) (map[string]string, domain.Node) {
	t.Helper()
	return graphTreeOn(t, g, name, domain.MainBranch)
}

// graphTreeOn walks the repository from its node at the head of a branch of the graph.
func graphTreeOn(t *testing.T, g *graph.Graph, name, branch string) (map[string]string, domain.Node) {
	t.Helper()
	ctx := context.Background()
	head, err := g.BranchHead(ctx, gitimport.Namespace, branch)
	if err != nil {
		t.Fatal(err)
	}
	nodes, links, err := g.CommitGraph(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[domain.NodeID]domain.Node{}
	var root domain.Node
	for _, n := range nodes {
		byID[n.ID] = n
		if n.Key == name {
			root = n
		}
	}
	out := map[string][]domain.NodeID{}
	for _, l := range links {
		if l.Type != gitimport.LinkContains && byID[l.From.ID].Type != gitimport.TypeRepo && byID[l.From.ID].Type != gitimport.TypeDirectory {
			continue // the symbols and the packages of a file: symbols_test.go
		}
		if l.Type != gitimport.LinkContains {
			t.Fatalf("unexpected link %s", l.Type)
		}
		if l.To.Version != byID[l.To.ID].Version {
			t.Fatalf("%s holds %s at v%d, the head has v%d", byID[l.From.ID].Key, byID[l.To.ID].Key, l.To.Version, byID[l.To.ID].Version)
		}
		out[byID[l.From.ID].Key] = append(out[byID[l.From.ID].Key], l.To.ID)
	}
	got := map[string]string{}
	var walk func(n domain.Node)
	walk = func(n domain.Node) {
		p, _ := n.Properties["path"].(string)
		if n.Deleted {
			t.Fatalf("%s is held but deleted", n.Key)
		}
		switch n.Type {
		case gitimport.TypeFile:
			got[p], _ = n.Properties["blob"].(string)
		default:
			if p != "" {
				p += "/"
			}
			got[p], _ = n.Properties["tree"].(string)
		}
		for _, id := range out[n.Key] {
			walk(byID[id])
		}
	}
	if root.ID == "" {
		t.Fatalf("no repository node %s", name)
	}
	walk(root)
	return got, root
}

func same(t *testing.T, want, got map[string]string) {
	t.Helper()
	keys := func(m map[string]string) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if strings.Join(keys(want), ",") != strings.Join(keys(got), ",") {
		t.Fatalf("entries:\nwant %v\ngot  %v", keys(want), keys(got))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: want %s, got %s", k, v, got[k])
		}
	}
}

func TestImportFollowsTheHistory(t *testing.T) {
	ctx := context.Background()
	g := newGraph(t)
	r := newRepo(t)
	r.write("README", "hello\n")
	r.write("src/main.c", "int main(){}\n")
	r.write("src/lib/util.h", "#pragma once\n")
	r.commit("Initial import")
	r.write("src/main.c", "int main(){return 0;}\n")
	r.write("docs/guide.md", "# Guide\n")
	r.commit("Fix main\n\nAnd write a guide.")
	r.git("rm", "-q", "-r", "src/lib")
	r.commit("Drop lib")
	r.write("src/lib/util.h", "#pragma once\n// back\n")
	r.git("rm", "-q", "README")
	r.write("README/index.md", "a directory now\n")
	r.commit("Restore lib, README becomes a directory")
	// a merge: the first-parent history sees the merged work as one commit
	r.git("checkout", "-q", "-b", "topic")
	r.write("topic.txt", "topic\n")
	r.commit("Topic work")
	r.git("checkout", "-q", "main")
	r.write("src/other.c", "void other(){}\n")
	r.commit("Other")
	r.git("merge", "-q", "--no-ff", "-m", "Merge topic", "topic")
	r.git("commit", "-q", "--allow-empty", "-m", "Nothing changed")

	var progress bytes.Buffer
	im := &gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project, Progress: &progress, ProgressEvery: 1}
	st, err := im.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// main's first-parent history: initial, fix, drop, restore, other, merge, empty
	if st.Commits != 7 {
		t.Fatalf("commits: %+v", st)
	}
	got, root := graphTree(t, g, "demo")
	same(t, r.tree(), got)
	if root.Properties["commit"] != strings.TrimSpace(r.git("rev-parse", "HEAD")) {
		t.Fatalf("repository at %v", root.Properties["commit"])
	}
	changes, err := g.ListChanges(ctx, graph.ChangesFilter{Namespace: gitimport.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	applied := 0
	for _, c := range changes {
		if c.Status == domain.ChangeApplied {
			applied++
		}
	}
	if applied != 7 {
		t.Fatalf("%d applied changes, want one per commit", applied)
	}
	// the removed entries are deleted, their tombstone the last version (ADR 0172); a path back is the same node, restored
	lib, err := g.NodeByKey(ctx, gitimport.Namespace, "demo:src/lib/util.h")
	if err != nil || lib.Deleted || lib.Version != 3 {
		t.Fatalf("util.h: %+v %v", lib, err)
	}
	readme, err := g.NodeByKey(ctx, gitimport.Namespace, "demo:README")
	if err != nil || !readme.Deleted {
		t.Fatalf("README: %+v %v", readme, err)
	}
	if f, _ := g.NodeByKey(ctx, gitimport.Namespace, "demo:src/main.c"); f.Properties["extension"] != "c" || f.Properties["size"] == nil {
		t.Fatalf("main.c: %+v", f.Properties)
	}

	// nothing new: nothing written; new commits: only them
	if st, err := (&gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project}).Run(ctx); err != nil || st.Commits != 0 {
		t.Fatalf("again: %+v %v", st, err)
	}
	r.write("src/main.c", "int main(){return 1;}\n")
	r.commit("Exit 1")
	if st, err := (&gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project}).Run(ctx); err != nil || st.Commits != 1 || st.Created != 0 {
		t.Fatalf("resumed: %+v %v", st, err)
	}
	got, _ = graphTree(t, g, "demo")
	same(t, r.tree(), got)
}

func TestImportFromABase(t *testing.T) {
	ctx := context.Background()
	g := newGraph(t)
	r := newRepo(t)
	r.write("a/b/c.txt", "c\n")
	r.commit("one")
	r.write("a/d.txt", "d\n")
	r.commit("two")
	r.git("tag", "v1")
	r.write("a/b/c.txt", "c2\n")
	r.commit("three")
	st, err := (&gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project, Base: "v1"}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Commits != 2 { // the tree at v1, then three
		t.Fatalf("%+v", st)
	}
	got, _ := graphTree(t, g, "demo")
	same(t, r.tree(), got)
	// two repositories share the namespace
	other := newRepo(t)
	other.write("x.go", "package x\n")
	other.commit("x")
	if _, err := (&gitimport.Importer{Graph: g, Dir: other.dir, Name: "other", ProjectID: project}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = graphTree(t, g, "other")
	same(t, other.tree(), got)
	got, _ = graphTree(t, g, "demo")
	same(t, r.tree(), got)
}

// TestImportRepository imports a real repository into a SQLite graph and reports the throughput: run it by hand with
// GOAP_GITIMPORT_REPO=<path> (GOAP_GITIMPORT_MAX bounds the commits, GOAP_GITIMPORT_BASE starts at a revision, GOAP_GITIMPORT_SYMBOLS=1 reads the
// symbols).
func TestImportRepository(t *testing.T) {
	dir := os.Getenv("GOAP_GITIMPORT_REPO")
	if dir == "" {
		t.Skip("GOAP_GITIMPORT_REPO names no repository")
	}
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := platform.MigrateSQLite(ctx, db, "graph", graph.SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	g := newGraphOn(t, graph.NewSQLite(db))
	max, _ := strconv.Atoi(os.Getenv("GOAP_GITIMPORT_MAX"))
	im := &gitimport.Importer{Graph: g, Dir: dir, Name: filepath.Base(dir), ProjectID: project, Max: max,
		Base: os.Getenv("GOAP_GITIMPORT_BASE"), Progress: testWriter{t}, ProgressEvery: 50,
		OnPart: func(i, n int, name string) {
			if i%50 == 0 || i == n {
				t.Logf("part %d/%d: %s", i, n, name)
			}
		}}
	if os.Getenv("GOAP_GITIMPORT_SYMBOLS") == "1" {
		ts, err := gitimport.NewTreeSitter()
		if err != nil {
			t.Fatal(err)
		}
		defer ts.Close()
		im.Symbols, im.Code = ts, true
	}
	start := time.Now()
	run := ctx
	if d, err := time.ParseDuration(os.Getenv("GOAP_GITIMPORT_DEADLINE")); err == nil {
		var cancel context.CancelFunc
		run, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	st, err := im.Run(run)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v in %s", st, time.Since(start))
	// imported again: nothing to do
	again := *im
	again.Max = 0
	if max > 0 {
		again.Rev = st.Last
	}
	if st, err := again.Run(ctx); err != nil || st.Commits != 0 {
		t.Fatalf("imported again: %+v %v", st, err)
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}
