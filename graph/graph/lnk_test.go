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

	if _, err := graphtest.Edit(ctx, g, docs.ID, map[string]any{"location": filepath.Join(dir, "none")}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Resolve(ctx, lnk.MustParse("lnk:docs/File/README.md")); err == nil {
		t.Fatal("a source follows its declaration: the moved root has no README")
	}
}
