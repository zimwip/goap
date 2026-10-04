package indexersvc

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/index"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

type bagEmbedder struct{ fail bool }

func (b *bagEmbedder) Embed(_ context.Context, req llm.EmbedRequest) (llm.EmbedResponse, error) {
	if b.fail {
		return llm.EmbedResponse{}, io.ErrUnexpectedEOF
	}
	out := llm.EmbedResponse{}
	for _, t := range req.Texts {
		v := make([]float32, 256)
		for _, w := range index.Tokens(t) {
			h := 0
			for _, c := range w {
				h = h*31 + int(c)
			}
			v[(h%256+256)%256]++
		}
		out.Vectors = append(out.Vectors, v)
	}
	return out, nil
}

type denyType struct{ typ string }

func (d denyType) Authorize(_ context.Context, r authz.Request) (bool, error) {
	return r.Resource.Type != d.typ, nil
}

// waitFor polls until the search returns want hits (the sink is asynchronous).
func waitFor(t *testing.T, svc *Service, ctx context.Context, q index.Query, want int) index.Result {
	t.Helper()
	var res index.Result
	for i := 0; i < 200; i++ {
		var err error
		if res, err = svc.Searcher.Search(ctx, q); err != nil {
			t.Fatal(err)
		}
		if res.Total == want {
			return res
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("want %d hits, got %d (%+v)", want, res.Total, res.Hits)
	return res
}

func TestGraphToSearch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(index.NewMemory(), &bagEmbedder{}, denyType{typ: "docs@Secret"}, log)

	d, err := methodology.ParseDomain([]byte(`
name: docs
version: 1.0.0
nodeTypes:
  - {name: Requirement, properties: [title, priority], search: [{property: title, text: true}, {property: priority, facet: true}]}
  - {name: Secret, properties: [title], search: [{property: title, text: true}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(d)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
	mk := func(key, typ string, props map[string]any) domain.Node {
		n, err := g.CreateNode(ctx, graph.NewNode{Namespace: "docs", Key: key, Type: typ, Properties: props})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := g.BranchHead(ctx, "docs", domain.MainBranch); err != nil {
		t.Fatal(err)
	}

	g.Observe(NewSink(ctx, svc))
	r1 := mk("REQ-1", "docs@Requirement", map[string]any{"title": "Users reset their password", "priority": "high"})
	mk("REQ-2", "docs@Requirement", map[string]any{"title": "Export a report", "priority": "low"})
	mk("SEC-1", "docs@Secret", map[string]any{"title": "password vault"})
	if _, err := g.BranchHead(ctx, "docs", domain.MainBranch); err != nil {
		t.Fatal(err)
	}

	user := authz.With(ctx, authz.Principal{Subject: "u", Roles: []string{"contributor"}})
	yes := true
	q := index.Query{Text: "password", Filter: index.Filter{Main: &yes}, Facets: []string{"priority", index.FacetType}}
	// the secret is filtered by ABAC, before counting
	res := waitFor(t, svc, user, q, 1)
	if res.Hits[0].Key != "REQ-1" || !res.Semantic || res.Facets[index.FacetType][0].Value != "docs@Requirement" {
		t.Fatalf("res = %+v", res)
	}
	// an internal caller (no identity) sees both
	waitFor(t, svc, ctx, q, 2)

	// a new version on main moves the flag; the old version stays searchable off main
	upd, err := g.UpdateNode(ctx, r1.Ref(), map[string]any{"title": "Users reset their passkey", "priority": "high"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.BranchHead(ctx, "docs", domain.MainBranch); err != nil {
		t.Fatal(err)
	}
	waitFor(t, svc, ctx, index.Query{Text: "passkey", Filter: index.Filter{Main: &yes}}, 1)
	waitFor(t, svc, ctx, index.Query{Text: "password", Filter: index.Filter{Main: &yes}}, 1) // SEC-1 only
	all := waitFor(t, svc, ctx, index.Query{Text: "reset", Filter: index.Filter{Type: []string{"docs@Requirement"}}}, 2)
	if !strings.Contains(all.Hits[0].Key, "REQ-1") || all.Hits[0].Version+all.Hits[1].Version != 3 {
		t.Fatalf("both versions of REQ-1 expected: %+v", all.Hits)
	}
	_ = upd

	// reindex: the graph publishes again, the index converges to the same content
	svc.Republish = func(ctx context.Context) (int, error) { return g.Republish(ctx, NewSink(ctx, svc)) }
	n, err := svc.Reindex(ctx)
	if err != nil || n == 0 {
		t.Fatalf("reindex: %d %v", n, err)
	}
	waitFor(t, svc, ctx, index.Query{Text: "reset", Filter: index.Filter{Type: []string{"docs@Requirement"}}}, 2)
	waitFor(t, svc, ctx, index.Query{Text: "passkey", Filter: index.Filter{Main: &yes}}, 1)
}

func TestEmbeddingUnavailableDegradesToText(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(index.NewMemory(), &bagEmbedder{fail: true}, nil, log)
	for i, title := range []string{"alpha beta", "gamma"} {
		ev := domain.NodeEvent{ID: domain.NodeID(rune('a' + i)), Version: 1, Namespace: "x", Key: title, Type: "T", Branch: "main", Text: map[string]string{"title": title}}
		if err := svc.Indexer.OnNode(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	res, err := svc.Searcher.Search(ctx, index.Query{Text: "beta"})
	if err != nil || res.Total != 1 || res.Semantic {
		t.Fatalf("text-only search: %v %+v", err, res)
	}
	if semantic, _, _, _, _ := svc.Status(); semantic {
		t.Fatal("status must report no semantic search")
	}
}
