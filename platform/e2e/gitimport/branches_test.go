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

// nodeOn is the node of a key at the head of a branch of the graph.
func nodeOn(t *testing.T, g *graph.Graph, branch, key string) domain.Node {
	t.Helper()
	ctx := context.Background()
	head, err := g.BranchHead(ctx, gitimport.Namespace, branch)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _, err := g.CommitGraph(ctx, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.Key == key {
			return n
		}
	}
	t.Fatalf("no %s on %s", key, branch)
	return domain.Node{}
}

// commitOf is the git commit the import of which a commit is the state.
func commitOf(t *testing.T, g *graph.Graph, id domain.CommitID) string {
	t.Helper()
	ctx := context.Background()
	b, err := g.CommitHeader(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.Change(ctx, b.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	git, _ := c.Data["git"].(map[string]any)
	s, _ := git["commit"].(string)
	return s
}

// The git branches are imported each on a branch of the graph of its own (ADR 0151), forked where it leaves what was
// imported (main, another branch), an orphan from the empty state; a file on two branches is one node; a run resumes
// each branch, and leaves out a branch rewritten since.
func TestImportTheBranches(t *testing.T) {
	ctx := context.Background()
	g := newGraph(t)
	r := newRepo(t)
	rev := func(x string) string { return strings.TrimSpace(r.git("rev-parse", x)) }
	r.write("README", "hello\n")
	r.write("src/a.c", "int a;\n")
	r.commit("c1")
	c1 := rev("HEAD")
	r.write("src/a.c", "int a = 1;\n")
	r.commit("c2")
	r.git("checkout", "-q", "-b", "feature")
	r.write("src/feature.c", "void f(){}\n")
	r.commit("f1")
	f1 := rev("HEAD")
	r.write("README", "hello feature\n")
	r.commit("f2")
	r.git("checkout", "-q", "-b", "fix/feature", f1)
	r.write("src/feature.c", "void f(){ /* fixed */ }\n")
	r.commit("x1")
	r.git("checkout", "-q", "-b", "old", c1)
	r.write("docs/x.md", "# x\n")
	r.commit("o1")
	r.git("checkout", "-q", "--orphan", "pages")
	r.git("rm", "-q", "-r", "--cached", ".")
	r.git("clean", "-q", "-f", "-d")
	r.write("index.html", "<p>pages</p>\n")
	r.commit("p1")
	r.git("checkout", "-q", "-f", "main")
	r.write("src/other.c", "int other;\n")
	r.commit("c3")
	r.git("merge", "-q", "--no-ff", "-m", "Merge feature", "feature")

	im := &gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project, Branches: gitimport.ParseBranches("all")}
	st, err := im.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// main: c1, c2, c3, merge; feature: f1, f2; fix/feature: x1; old: o1; pages: p1
	if st.Commits != 9 || st.Branches != 4 || len(st.Skipped) != 0 || st.Total != 9 {
		t.Fatalf("stats: %+v", st)
	}
	got, _ := graphTree(t, g, "demo")
	same(t, r.treeOf("main"), got)
	for _, b := range []string{"feature", "fix/feature", "old", "pages"} {
		got, _ := graphTreeOn(t, g, "demo", gitimport.GraphBranch("demo", b))
		same(t, r.treeOf(b), got)
	}
	// forked at the commit each branch leaves the history imported: old at c1 on main, fix/feature at f1 on feature
	for b, at := range map[string]string{"old": c1, "fix/feature": f1} {
		br, err := g.Branch(ctx, gitimport.Namespace, gitimport.GraphBranch("demo", b))
		if err != nil {
			t.Fatal(err)
		}
		if commitOf(t, g, br.ForkCommit) != at {
			t.Fatalf("%s forked at %s, want %s", b, commitOf(t, g, br.ForkCommit), at)
		}
	}
	if br, _ := g.Branch(ctx, gitimport.Namespace, gitimport.GraphBranch("demo", "pages")); br.ForkCommit != "" {
		t.Fatalf("an orphan branch starts from the empty state: %+v", br)
	}
	// feature.c, created on feature and by the merge on main, is one node with a version on each line
	onMain, onFeature := nodeOn(t, g, domain.MainBranch, "demo:src/feature.c"), nodeOn(t, g, gitimport.GraphBranch("demo", "feature"), "demo:src/feature.c")
	if onMain.ID != onFeature.ID || onMain.Version == onFeature.Version {
		t.Fatalf("one node, two versions: main %+v, feature %+v", onMain, onFeature)
	}
	c, err := g.Change(ctx, onFeature.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	if git, _ := c.Data["git"].(map[string]any); git["branch"] != "feature" {
		t.Fatalf("the change says its git branch: %+v", c.Data)
	}

	again := func() gitimport.Stats {
		t.Helper()
		st, err := (&gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project, Branches: []string{"*"}}).Run(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := again(); st.Commits != 0 || st.Branches != 4 {
		t.Fatalf("nothing new: %+v", st)
	}
	r.git("checkout", "-q", "feature")
	r.write("src/feature.c", "void f(){ return; }\n")
	r.commit("f3")
	// old rewritten: its last commit imported is no longer in its history
	r.git("checkout", "-q", "old")
	r.git("reset", "-q", "--hard", c1)
	r.write("docs/y.md", "# y\n")
	r.commit("o1'")
	r.git("checkout", "-q", "main")
	st = again()
	if st.Commits != 1 || st.Branches != 3 || len(st.Skipped) != 1 || !strings.HasPrefix(st.Skipped[0], "old: rewritten") {
		t.Fatalf("resumed: %+v", st)
	}
	got, _ = graphTreeOn(t, g, "demo", gitimport.GraphBranch("demo", "feature"))
	same(t, r.treeOf("feature"), got)

	// started at c2, a branch forked before is left out
	g2 := newGraph(t)
	st, err = (&gitimport.Importer{Graph: g2, Dir: r.dir, Name: "demo", ProjectID: project, Base: "main~2", Branches: []string{"old", "fix/*"}}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Branches != 1 || !slices.ContainsFunc(st.Skipped, func(s string) bool { return strings.HasPrefix(s, "old: it forks before") }) {
		t.Fatalf("with a base: %+v", st)
	}
}
