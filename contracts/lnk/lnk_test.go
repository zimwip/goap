package lnk

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		text string
		want URL
	}{
		{"lnk:alm/Requirement/REQ-1", URL{Source: "alm", Type: "Requirement", Key: "REQ-1"}},
		{"lnk:docs/File/adr/0153.md", URL{Source: "docs", Type: "File", Key: "adr/0153.md"}},
		{"lnk:code/Method/repo/a.go%23method:T.Run", URL{Source: "code", Type: "Method", Key: "repo/a.go#method:T.Run"}},
		{"lnk:alm/Requirement/a%20b%3Fc?v=3", URL{Source: "alm", Type: "Requirement", Key: "a b?c", Version: 3}},
		{"lnk:org/OrgUnit/USR:alice@example.com", URL{Source: "org", Type: "OrgUnit", Key: "USR:alice@example.com"}},
	} {
		u, err := Parse(tc.text)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.text, err)
		}
		if u != tc.want {
			t.Fatalf("Parse(%q) = %+v, want %+v", tc.text, u, tc.want)
		}
		if got := u.String(); got != tc.text {
			t.Fatalf("String() = %q, want %q", got, tc.text)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for _, s := range []string{
		"", "alm/Requirement/X", "lnk:alm/Requirement", "lnk:alm//X", "lnk:Alm/Requirement/X", "lnk:alm/Requirement/",
		"lnk:alm/Requirement/X?v=0", "lnk:alm/Requirement/X?w=1", "lnk:alm/Re@q/X",
	} {
		if _, err := Parse(s); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Parse(%q) = %v, want ErrInvalid", s, err)
		}
	}
}

func TestURLJSON(t *testing.T) {
	type wrap struct {
		To URL `json:"to"`
	}
	b, err := json.Marshal(wrap{To: New("alm", "Requirement", "REQ-1")})
	if err != nil || string(b) != `{"to":"lnk:alm/Requirement/REQ-1"}` {
		t.Fatalf("marshal = %s, %v", b, err)
	}
	var w wrap
	if err := json.Unmarshal(b, &w); err != nil || w.To.Key != "REQ-1" {
		t.Fatalf("unmarshal = %+v, %v", w, err)
	}
}

// local is a graph resolver over a map of namespaces: a source with no location only.
func local(nodes map[string]map[string]string) Resolver {
	return ResolverFuncs{ResolveFunc: func(_ context.Context, src Source, u URL) (Target, error) {
		if src.Location != "" {
			return Target{}, ErrNoResolver
		}
		ns, ok := nodes[src.NamespaceOf()]
		if !ok {
			return Target{}, ErrUnknownSource
		}
		typ, ok := ns[u.Key]
		if !ok || BareType(typ) != u.Type {
			return Target{}, ErrNotFound
		}
		return Target{Kind: TargetNode, Type: typ, Key: u.Key, Node: &NodeRef{ID: "id-" + u.Key}}, nil
	}}
}

func newRegistry(nodes map[string]map[string]string) *Registry {
	r := NewRegistry()
	r.Register(KindGraph, local(nodes))
	return r
}

func TestRegistryGraphFallback(t *testing.T) {
	ctx := context.Background()
	r := newRegistry(map[string]map[string]string{"alm": {"REQ-1": "alm@Requirement"}})
	got, err := r.Resolve(ctx, New("alm", "Requirement", "REQ-1"))
	if err != nil || got.Node == nil || got.Node.ID != "id-REQ-1" || got.URL.String() != "lnk:alm/Requirement/REQ-1" {
		t.Fatalf("Resolve = %+v, %v", got, err)
	}
	if _, err := r.Resolve(ctx, New("alm", "Requirement", "REQ-2")); !IsNotFound(err) {
		t.Fatalf("missing key: %v", err)
	}
	if _, err := r.Resolve(ctx, New("nowhere", "Requirement", "REQ-1")); !IsNotFound(err) {
		t.Fatalf("unknown source: %v", err)
	}
	u, err := r.URLOf(ctx, "alm", Target{Type: "alm@Requirement", Key: "REQ-1"})
	if err != nil || u.String() != "lnk:alm/Requirement/REQ-1" {
		t.Fatalf("URLOf = %s, %v", u, err)
	}
}

func TestRegistryDeclaredSources(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry()
	// a declared graph source elsewhere, under another name than its namespace
	if err := r.Declare(Source{Name: "docs", Kind: KindFiles}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("files source without location: %v", err)
	}
	if err := r.Declare(Source{Name: "acme", Kind: KindGraph, Location: "https://acme.example/graph", Namespace: "alm"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, New("acme", "Requirement", "R")); !errors.Is(err, ErrNoResolver) {
		t.Fatalf("no resolver for graph: %v", err)
	}
	var seen Source
	r.Register(KindGraph, ResolverFuncs{ResolveFunc: func(_ context.Context, src Source, u URL) (Target, error) {
		seen = src
		return Target{Kind: TargetNode, Type: src.NamespaceOf() + "@" + u.Type, Key: u.Key}, nil
	}})
	got, err := r.Resolve(ctx, New("acme", "Requirement", "R"))
	if err != nil || got.Type != "alm@Requirement" || seen.Location != "https://acme.example/graph" {
		t.Fatalf("Resolve = %+v, %v (source %+v)", got, err, seen)
	}
	// Lookup finds the sources the graph declares
	r.Lookup = func(_ context.Context, name string) (Source, bool, error) {
		if name == "docs" {
			return Source{Name: "docs", Kind: KindFiles, Location: "/srv/docs"}, true, nil
		}
		return Source{}, false, nil
	}
	if src, err := r.Source(ctx, "docs"); err != nil || src.Kind != KindFiles {
		t.Fatalf("Source(docs) = %+v, %v", src, err)
	}
	if src, err := r.Source(ctx, "alm"); err != nil || src.Kind != KindGraph || src.Location != "" || src.NamespaceOf() != "alm" {
		t.Fatalf("Source(alm) = %+v, %v", src, err)
	}
}

func TestFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "adr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "adr", "0153.md"), []byte("# lnk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := newRegistry(nil)
	r.Register(KindFiles, Files{})
	if err := r.Declare(Source{Name: "docs", Kind: KindFiles, Location: dir}); err != nil {
		t.Fatal(err)
	}
	got, err := r.ResolveString(ctx, "lnk:docs/File/adr/0153.md")
	if err != nil || got.Kind != TargetFile || got.Properties["size"] != int64(5) || got.Location != filepath.Join(dir, "adr", "0153.md") {
		t.Fatalf("Resolve file = %+v, %v", got, err)
	}
	if got, err := r.ResolveString(ctx, "lnk:docs/Directory/adr"); err != nil || got.Kind != TargetDirectory {
		t.Fatalf("Resolve directory = %+v, %v", got, err)
	}
	if _, err := r.ResolveString(ctx, "lnk:docs/File/adr"); !IsNotFound(err) {
		t.Fatalf("a directory is no file: %v", err)
	}
	if _, err := r.ResolveString(ctx, "lnk:docs/File/adr/none.md"); !IsNotFound(err) {
		t.Fatalf("missing file: %v", err)
	}
	for _, s := range []string{"lnk:docs/File/../outside.md", "lnk:docs/File/adr/../../outside.md", "lnk:docs/File//etc/passwd"} {
		if _, err := r.ResolveString(ctx, s); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v, want ErrInvalid", s, err)
		}
	}
	u, err := r.URLOf(ctx, "docs", Target{Location: filepath.Join(dir, "adr", "0153.md")})
	if err != nil || u.String() != "lnk:docs/File/adr/0153.md" {
		t.Fatalf("URLOf = %s, %v", u, err)
	}
	if _, err := r.URLOf(ctx, "docs", Target{Location: filepath.Join(filepath.Dir(dir), "outside.md")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("URLOf outside the root: %v", err)
	}
}

func TestResolveIn(t *testing.T) {
	ctx := context.Background()
	r := newRegistry(map[string]map[string]string{"alm": {"REQ-1": "alm@Requirement"}, "alm2": {"REQ-2": "alm2@Requirement"}})
	if err := r.Declare(Source{Name: "alm-archive", Kind: KindGraph, Namespace: "alm2"}); err != nil {
		t.Fatal(err)
	}
	m := Mapping(func(name string) (string, bool) {
		if name == "alm" {
			return "alm-archive", true
		}
		return "", false
	})
	// the mapping says where the source is stored; the target keeps the URL's own source
	got, err := r.ResolveIn(ctx, m, New("alm", "Requirement", "REQ-2"))
	if err != nil || got.URL.String() != "lnk:alm/Requirement/REQ-2" {
		t.Fatalf("mapped = %+v, %v", got, err)
	}
	if _, err := r.ResolveIn(ctx, m, New("alm", "Requirement", "REQ-1")); !IsNotFound(err) {
		t.Fatalf("mapped away: %v", err)
	}
	if _, err := r.ResolveIn(ctx, nil, New("alm", "Requirement", "REQ-1")); err != nil {
		t.Fatalf("no mapping: %v", err)
	}
}
