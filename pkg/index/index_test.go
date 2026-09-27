package index

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zimwip/goap/internal/pgtest"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/domain"
)

func forEachStore(t *testing.T, f func(t *testing.T, s Store)) {
	t.Run("memory", func(t *testing.T) { f(t, NewMemory()) })
	t.Run("sqlite", func(t *testing.T) {
		ctx := context.Background()
		db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := platform.MigrateSQLite(ctx, db, "index", SQLiteMigrations, "migrations_sqlite"); err != nil {
			t.Fatal(err)
		}
		f(t, NewSQLite(db))
	})
	t.Run("postgres", func(t *testing.T) {
		pool := pgtest.Pool(t, Migrations) // skipped without GOAP_TEST_PG_DSN; needs the pgvector extension
		st := NewPostgres(pool)
		if err := st.EnsureVectorIndex(context.Background(), 16); err != nil {
			t.Fatal(err)
		}
		f(t, st)
	})
}

// bagEmbedder is a deterministic embedder: a bag of hashed words over 16 dimensions.
type bagEmbedder struct{}

func (bagEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 16)
		for _, w := range Tokens(t) {
			h := 0
			for _, c := range w {
				h = h*31 + int(c)
			}
			v[(h%16+16)%16]++
		}
		out[i] = v
	}
	return out, nil
}

func node(id string, ver domain.Version, key, typ, title, prio, branch string) domain.NodeEvent {
	return domain.NodeEvent{ID: domain.NodeID(id), Version: ver, Branch: branch, Namespace: "alm", Key: key, Type: typ, State: "draft",
		Text: map[string]string{"title": title}, Facets: map[string]any{"prio": prio}}
}

func TestIndexSearch(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		x := &Indexer{Store: s, Embedder: bagEmbedder{}}
		for _, ev := range []domain.NodeEvent{
			node("n1", 1, "REQ-1", "Requirement", "Users log in with a password", "high", "main"),
			node("n2", 1, "REQ-2", "Requirement", "Export the report as PDF", "low", "main"),
			node("n3", 1, "FN-1", "Function", "Password reset by mail", "high", "main"),
			node("n1", 2, "REQ-1", "Requirement", "Users log in with a passkey", "high", "chg-1"),
		} {
			if err := x.OnNode(ctx, ev); err != nil {
				t.Fatal(err)
			}
		}
		// main head: n1@1, n2@1, n3@1
		if err := x.OnBaseline(ctx, domain.BaselineEvent{Branch: "main", Set: map[domain.NodeID]domain.Version{"n1": 1, "n2": 1, "n3": 1}}); err != nil {
			t.Fatal(err)
		}
		yes := true
		se := &Searcher{Store: s, Embedder: bagEmbedder{}}

		// full text + hybrid, main only
		r, err := se.Search(ctx, Query{Text: "password", Filter: Filter{Main: &yes}, Facets: []string{FacetType, "prio"}})
		if err != nil {
			t.Fatal(err)
		}
		keys := hitKeys(r)
		if !r.Semantic || len(keys) == 0 || !strings.Contains(keys, "REQ-1") || !strings.Contains(keys, "FN-1") {
			t.Fatalf("hits = %s semantic=%v", keys, r.Semantic)
		}
		for _, h := range r.Hits {
			if !h.Main {
				t.Fatalf("non-main hit %+v", h)
			}
		}
		if got := r.Facets[FacetType]; len(got) == 0 {
			t.Fatalf("facets = %v", r.Facets)
		}

		// all branches: both versions of REQ-1 are indexed
		r, err = se.Search(ctx, Query{Text: "log in", Filter: Filter{Type: []string{"Requirement"}}})
		if err != nil {
			t.Fatal(err)
		}
		if k := hitKeys(r); !strings.Contains(k, "REQ-1@1") || !strings.Contains(k, "REQ-1@2") {
			t.Fatalf("hits = %s", k)
		}

		// facet filter without a query, counts
		r, err = se.Search(ctx, Query{Filter: Filter{Main: &yes, Facets: map[string][]string{"prio": {"high"}}}, Facets: []string{"prio", FacetMain}})
		if err != nil {
			t.Fatal(err)
		}
		if r.Total != 2 || r.Facets["prio"][0] != (FacetCount{Value: "high", Count: 2}) {
			t.Fatalf("total %d facets %v", r.Total, r.Facets)
		}

		// a new main head moves the flag from REQ-1@1 to REQ-1@2 and drops FN-1
		if err := x.OnBaseline(ctx, domain.BaselineEvent{Branch: "main", Set: map[domain.NodeID]domain.Version{"n1": 2}, Removed: []domain.NodeID{"n3"}}); err != nil {
			t.Fatal(err)
		}
		r, _ = se.Search(ctx, Query{Filter: Filter{Main: &yes}})
		if k := hitKeys(r); strings.Contains(k, "REQ-1@1") || strings.Contains(k, "FN-1") {
			t.Fatalf("main after head move = %s", k)
		}

		// FTS syntax in the input is inert
		if _, err := se.Search(ctx, Query{Text: `pass" OR (*`}); err != nil {
			t.Fatal(err)
		}

		// authorization runs before counting and paging
		se.Authorize = func(_ context.Context, hits []Hit) ([]Hit, error) {
			var out []Hit
			for _, h := range hits {
				if h.Type == "Function" {
					out = append(out, h)
				}
			}
			return out, nil
		}
		r, _ = se.Search(ctx, Query{Text: "password", Facets: []string{FacetType}})
		if r.Total != 1 || len(r.Facets[FacetType]) != 1 {
			t.Fatalf("authorized total %d facets %v", r.Total, r.Facets)
		}
	})
}

func TestIndexerSkipsUnchangedEmbedding(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		emb := &countEmbedder{}
		x := &Indexer{Store: s, Embedder: emb}
		ev := node("n1", 1, "REQ-1", "Requirement", "same text", "high", "main")
		for i := 0; i < 3; i++ {
			if err := x.OnNode(ctx, ev); err != nil {
				t.Fatal(err)
			}
		}
		if emb.calls != 1 {
			t.Fatalf("embed calls = %d, want 1 (redelivery is idempotent)", emb.calls)
		}
		if err := s.Reset(ctx); err != nil {
			t.Fatal(err)
		}
		if r, _ := (&Searcher{Store: s}).Search(ctx, Query{}); r.Total != 0 {
			t.Fatalf("reset left %d", r.Total)
		}
	})
}

type countEmbedder struct{ calls int }

func (c *countEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	c.calls++
	return bagEmbedder{}.Embed(ctx, texts)
}

func hitKeys(r Result) string {
	var ks []string
	for _, h := range r.Hits {
		ks = append(ks, h.Key+"@"+string(rune('0'+int(h.Version))))
	}
	return strings.Join(ks, ",")
}
