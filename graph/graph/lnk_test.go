package graph_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/lnk"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graph/graphtest"
	"github.com/zimwip/goap/lib/platform"
)

// TestGraphResolver: every namespace of the graph is a graph source of its name with no location (ADR 0154), a URL
// names a node by its type and key, a version pins one, and a node gives its URL back.
func TestGraphResolver(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	req, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "alm", Key: "REQ-1", Type: "alm@Requirement", Properties: map[string]any{"title": "Sign in"}})
	if err != nil {
		t.Fatal(err)
	}
	if req.SourceName() != "alm" || req.URL().String() != "lnk:alm/Requirement/REQ-1" {
		t.Fatalf("node source %q, URL %s", req.SourceName(), req.URL())
	}
	v2, err := graphtest.Edit(ctx, g, req.ID, map[string]any{"title": "Sign in with a passkey"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := g.Resolve(ctx, lnk.MustParse("lnk:alm/Requirement/REQ-1"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != lnk.TargetNode || got.Type != "alm@Requirement" || got.Node.ID != string(req.ID) || got.Node.Version != int(v2.Version) || got.Title != "Sign in with a passkey" {
		t.Fatalf("Resolve = %+v", got)
	}
	got, err = g.Resolve(ctx, lnk.MustParse("lnk:alm/Requirement/REQ-1?v=1"))
	if err != nil || got.Node.Version != 1 || got.Title != "Sign in" || got.URL.String() != "lnk:alm/Requirement/REQ-1?v=1" {
		t.Fatalf("Resolve v1 = %+v, %v", got, err)
	}
	for _, s := range []string{"lnk:alm/Requirement/REQ-2", "lnk:alm/Function/REQ-1", "lnk:nowhere/Requirement/REQ-1", "lnk:alm/Requirement/REQ-1?v=9"} {
		if _, err := g.Resolve(ctx, lnk.MustParse(s)); !lnk.IsNotFound(err) {
			t.Fatalf("Resolve(%s) = %v, want not found", s, err)
		}
	}

	u, err := g.Lnk().URLOf(ctx, "alm", lnk.Target{Node: &lnk.NodeRef{ID: string(req.ID)}})
	if err != nil || u.String() != "lnk:alm/Requirement/REQ-1" {
		t.Fatalf("URLOf = %s, %v", u, err)
	}
	if _, err := g.Lnk().URLOf(ctx, "platform", lnk.Target{Node: &lnk.NodeRef{ID: string(req.ID)}}); !lnk.IsNotFound(err) {
		t.Fatalf("URLOf in another source: %v", err)
	}

	// a link's target as a logical URL
	spec, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "alm", Key: "SPEC-1", Type: "alm@Specification",
		Links: []graph.LinkWrite{{Type: "alm@refines", To: req.Ref()}}})
	if err != nil {
		t.Fatal(err)
	}
	links, err := g.OutLinksOf(ctx, spec.Ref())
	if err != nil || len(links) != 1 {
		t.Fatalf("links = %v, %v", links, err)
	}
	if u, err := g.LinkURL(ctx, links[0]); err != nil || u.String() != "lnk:alm/Requirement/REQ-1" {
		t.Fatalf("LinkURL = %s, %v", u, err)
	}
}

// TestDeclaredSources: a platform@Source node declares a source the resolver of its kind reads; a malformed
// declaration refuses, and a source follows its declaration.
func TestDeclaredSources(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	var asked lnk.Source
	g := graph.New(graph.NewMemory())
	g.Resolvers = map[string]lnk.Resolver{
		lnk.KindFiles: lnk.Files{},
		lnk.KindGraph: lnk.ResolverFuncs{ResolveFunc: func(_ context.Context, src lnk.Source, u lnk.URL) (lnk.Target, error) {
			asked = src
			return lnk.Target{Kind: lnk.TargetNode, Type: src.NamespaceOf() + "@" + u.Type, Key: u.Key}, nil
		}},
	}
	docs, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: domain.NamespacePlatform, Key: domain.SourceKey("docs"), Type: domain.TypeSource,
		Properties: map[string]any{"name": "docs", "kind": lnk.KindFiles, "location": dir}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: domain.NamespacePlatform, Key: domain.SourceKey("acme"), Type: domain.TypeSource,
		Properties: map[string]any{"kind": lnk.KindGraph, "namespace": "alm", "location": "https://acme.example/graph"}}); err != nil {
		t.Fatal(err)
	}

	f, err := g.Resolve(ctx, lnk.MustParse("lnk:docs/File/README.md"))
	if err != nil || f.Kind != lnk.TargetFile || f.Location != filepath.Join(dir, "README.md") {
		t.Fatalf("file = %+v, %v", f, err)
	}
	r, err := g.Resolve(ctx, lnk.MustParse("lnk:acme/Requirement/R-9"))
	if err != nil || r.Type != "alm@Requirement" || asked.Location != "https://acme.example/graph" {
		t.Fatalf("remote = %+v, %v (source %+v)", r, err, asked)
	}

	// a graph source with no location is this graph, under another name
	req, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "alm", Key: "REQ-1", Type: "alm@Requirement"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: domain.NamespacePlatform, Key: domain.SourceKey("req"), Type: domain.TypeSource,
		Properties: map[string]any{"kind": lnk.KindGraph, "namespace": "alm"}}); err != nil {
		t.Fatal(err)
	}
	asked = lnk.Source{}
	if r, err := g.Resolve(ctx, lnk.MustParse("lnk:req/Requirement/REQ-1")); err != nil || r.Node == nil || r.Node.ID != string(req.ID) ||
		r.URL.String() != "lnk:req/Requirement/REQ-1" || asked.Name != "" {
		t.Fatalf("local alias = %+v, %v (remote asked %+v)", r, err, asked)
	}

	// a malformed declaration is an error, not a fallback
	if _, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: domain.NamespacePlatform, Key: domain.SourceKey("broken"), Type: domain.TypeSource,
		Properties: map[string]any{"kind": lnk.KindFiles}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Resolve(ctx, lnk.MustParse("lnk:broken/Requirement/R")); !errors.Is(err, lnk.ErrInvalid) {
		t.Fatalf("broken source: %v", err)
	}

	// a namespace another graph holds (the content graph, ADR 0154 §9) is read there, under its own name
	if _, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: domain.NamespacePlatform, Key: domain.SourceKey("content"), Type: domain.TypeSource,
		Properties: map[string]any{"kind": lnk.KindGraph, "location": "http://content:8080", "namespaces": []any{"code"}}}); err != nil {
		t.Fatal(err)
	}
	asked = lnk.Source{}
	if _, err := g.Resolve(ctx, lnk.MustParse("lnk:code/File/main.go")); err != nil || asked.Location != "http://content:8080" || asked.NamespaceOf() != "code" {
		t.Fatalf("held namespace: %v (source %+v)", err, asked)
	}

	if _, err := graphtest.Edit(ctx, g, docs.ID, map[string]any{"location": filepath.Join(dir, "none")}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Resolve(ctx, lnk.MustParse("lnk:docs/File/README.md")); err == nil {
		t.Fatal("a source follows its declaration: the moved root has no README")
	}
}

// TestProjectSources: a project reads a source of its URLs where its mapping says (ADR 0154 §8), the URL keeping its
// name; a project with no mapping reads the source as named, and a node resolves in the project it belongs to.
func TestProjectSources(t *testing.T) {
	ctx := context.Background()
	var asked lnk.Source
	g := graph.New(graph.NewMemory())
	g.Resolvers = map[string]lnk.Resolver{
		lnk.KindGraph: lnk.ResolverFuncs{ResolveFunc: func(_ context.Context, src lnk.Source, u lnk.URL) (lnk.Target, error) {
			asked = src
			return lnk.Target{Kind: lnk.TargetNode, Type: src.NamespaceOf() + "@" + u.Type, Key: u.Key, URL: lnk.New(src.Name, u.Type, u.Key)}, nil
		}},
	}
	req, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "alm", Key: "REQ-1", Type: "alm@Requirement"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: domain.NamespacePlatform, Key: domain.SourceKey("alm-eu"), Type: domain.TypeSource,
		Properties: map[string]any{"kind": lnk.KindGraph, "namespace": "alm", "location": "https://eu.example/graph"}}); err != nil {
		t.Fatal(err)
	}
	g.ProjectSources = func(_ context.Context, p string) (map[string]string, error) {
		if p == "PROJ-EU" || p == req.Project {
			return map[string]string{"alm": "alm-eu"}, nil
		}
		return nil, nil
	}
	u := lnk.MustParse("lnk:alm/Requirement/R-7")

	got, err := g.ResolveIn(ctx, "PROJ-EU", u)
	if err != nil || asked.Location != "https://eu.example/graph" || got.URL.String() != "lnk:alm/Requirement/R-7" {
		t.Fatalf("mapped = %+v, %v (source %+v)", got, err, asked)
	}
	asked = lnk.Source{}
	if _, err := g.ResolveIn(ctx, "PROJ-US", u); !lnk.IsNotFound(err) || asked.Name != "" {
		t.Fatalf("unmapped = %v (source %+v), want this graph's alm", err, asked)
	}
	if got, err := g.ResolveIn(ctx, "PROJ-US", lnk.MustParse("lnk:alm/Requirement/REQ-1")); err != nil || got.Node == nil || got.Node.ID != string(req.ID) {
		t.Fatalf("unmapped local = %+v, %v", got, err)
	}
	asked = lnk.Source{}
	if _, err := g.ResolveFrom(ctx, req, u); err != nil || asked.Name != "alm-eu" {
		t.Fatalf("from a node of %s: %v (source %+v)", req.Project, err, asked)
	}
}

// TestLinkTargets: a link is written to a logical URL (ADR 0154 step 2). A URL naming a node of this graph is that
// node (the version it names, else the one the change sees); a URL of another source is kept as the link's target,
// committed, read back and checked out again as it is; a node of another graph names its version.
func TestLinkTargets(t *testing.T) {
	run := func(t *testing.T, repo graph.Repo) {
		ctx := context.Background()
		g := graph.New(repo)
		for _, n := range []graphtest.Node{
			{Namespace: domain.NamespacePlatform, Key: domain.SourceKey("acme"), Type: domain.TypeSource,
				Properties: map[string]any{"kind": lnk.KindGraph, "namespace": "alm", "location": "https://acme.example/graph"}},
			{Namespace: domain.NamespacePlatform, Key: domain.SourceKey("docs"), Type: domain.TypeSource,
				Properties: map[string]any{"kind": lnk.KindFiles, "location": t.TempDir()}},
		} {
			if _, err := graphtest.Import(ctx, g, n); err != nil {
				t.Fatal(err)
			}
		}
		req, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "alm", Key: "REQ-1", Type: "alm@Requirement"})
		if err != nil {
			t.Fatal(err)
		}
		remote, doc := lnk.MustParse("lnk:acme/Requirement/R-9?v=2"), lnk.MustParse("lnk:docs/File/specs/login.md")
		spec, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "alm", Key: "SPEC-1", Type: "alm@Specification", Links: []graph.LinkWrite{
			{Type: "alm@refines", Target: lnk.MustParse("lnk:alm/Requirement/REQ-1")},
			{Type: "alm@refines", Target: remote},
			{Type: "alm@documents", Target: doc, Properties: map[string]any{"note": "the login page"}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		check := func(what string, links []domain.Link) {
			t.Helper()
			byURL := map[string]domain.Link{}
			for _, l := range links {
				u, err := g.LinkURL(ctx, l)
				if err != nil {
					t.Fatalf("%s: LinkURL(%+v) = %v", what, l, err)
				}
				byURL[u.Logical().String()] = l
			}
			if l := byURL["lnk:alm/Requirement/REQ-1"]; l.To != req.Ref() || l.External() {
				t.Fatalf("%s: a URL of this graph is its node: %+v", what, links)
			}
			if l := byURL["lnk:acme/Requirement/R-9"]; !l.External() || l.Target != remote {
				t.Fatalf("%s: a node of another graph keeps its URL and version: %+v", what, links)
			}
			if l := byURL["lnk:docs/File/specs/login.md"]; !l.External() || l.Target != doc || l.Properties["note"] != "the login page" {
				t.Fatalf("%s: a file keeps its URL: %+v", what, links)
			}
		}
		links, err := g.OutLinksOf(ctx, spec.Ref())
		if err != nil || len(links) != 3 {
			t.Fatalf("links = %+v, %v", links, err)
		}
		check("committed", links)

		// a new version keeps the links it is checked out with, the ones to other sources included
		v2, err := graphtest.Edit(ctx, g, spec.ID, map[string]any{"title": "Login"})
		if err != nil {
			t.Fatal(err)
		}
		links, err = g.OutLinksOf(ctx, v2.Ref())
		if err != nil || len(links) != 3 {
			t.Fatalf("links of v2 = %+v, %v", links, err)
		}
		check("checked out again", links)

		// a link to another source is removed like any
		head, err := g.BranchHead(ctx, "alm", domain.MainBranch)
		if err != nil {
			t.Fatal(err)
		}
		drop, err := g.CreateChange(ctx, graph.NewChange{Namespace: "alm", Title: "drop the doc", BaselineID: head.ID, OwnBranch: true, ProjectID: g.Structure(domain.StructureProject).Root})
		if err != nil {
			t.Fatal(err)
		}
		cn, err := g.ImpactNodeCheckout(ctx, drop.ID, graph.NodeCheckout{Node: spec.ID, Rationale: "drop the doc"})
		if err != nil {
			t.Fatal(err)
		}
		dv, err := g.ChangeNodeView(ctx, drop.ID, "", *cn.Post)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range dv.Out {
			if l.Target == doc {
				if err := g.ImpactLinkDelete(ctx, drop.ID, l.ID, "", ""); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := graphtest.Accept(ctx, g, drop.ID, cn.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := graphtest.CommitMerge(ctx, g, drop.ID, ""); err != nil {
			t.Fatal(err)
		}
		v3, err := g.NodeByKey(ctx, "alm", "SPEC-1")
		if err != nil {
			t.Fatal(err)
		}
		if links, err = g.OutLinksOf(ctx, v3.Ref()); err != nil || len(links) != 2 || v3.Version != 3 {
			t.Fatalf("links of v%d = %+v, %v", v3.Version, links, err)
		}

		// a node of another graph names its version; a source no project maps and nobody declares is this graph's namespace
		head, err = g.BranchHead(ctx, "alm", domain.MainBranch)
		if err != nil {
			t.Fatal(err)
		}
		c, err := g.CreateChange(ctx, graph.NewChange{Namespace: "alm", Title: "unversioned", BaselineID: head.ID, ProjectID: g.Structure(domain.StructureProject).Root})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.ImpactNodeCreate(ctx, c.ID, graph.NodeCreate{Key: "SPEC-2", Type: "alm@Specification",
			Links: []graph.LinkWrite{{Type: "alm@refines", Target: lnk.MustParse("lnk:acme/Requirement/R-9")}}}); !errors.Is(err, graph.ErrInvalid) {
			t.Fatalf("unversioned link to another graph: %v", err)
		}
		if _, err := g.ImpactNodeCreate(ctx, c.ID, graph.NodeCreate{Key: "SPEC-2", Type: "alm@Specification",
			Links: []graph.LinkWrite{{Type: "alm@refines", Target: lnk.MustParse("lnk:alm/Requirement/REQ-404")}}}); !errors.Is(err, graph.ErrInvalid) {
			t.Fatalf("link to no node of this graph: %v", err)
		}
	}
	bothRepos(t, run)
}

// bothRepos runs f on a fresh in-memory repository and a fresh SQLite one.
func bothRepos(t *testing.T, f func(t *testing.T, repo graph.Repo)) {
	t.Run("memory", func(t *testing.T) { f(t, graph.NewMemory()) })
	t.Run("sqlite", func(t *testing.T) {
		ctx := context.Background()
		db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := platform.MigrateSQLite(ctx, db, "graph", graph.SQLiteMigrations, "migrations_sqlite"); err != nil {
			t.Fatal(err)
		}
		f(t, graph.NewSQLite(db))
	})
}
