// Package index is the node index (ADR 0026): a read model of the graph that answers full-text,
// semantic (embedding) and faceted searches. It is fed by the node and baseline events of the graph
// and knows nothing else of the platform.
package index

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

// Built-in facets, always present besides the ones the node type declares.
const (
	FacetNamespace = "namespace"
	FacetType      = "type"
	FacetState     = "state"
	FacetBranch    = "branch"
	FacetMain      = "main" // "true" when the version is the head of main
)

// PoolSize bounds the candidates a search ranks, authorizes and counts facets on.
const PoolSize = 500

// Doc is one indexed node version.
type Doc struct {
	ID        domain.NodeID
	Version   domain.Version
	Namespace string
	Type      string
	Key       string
	State     string
	Branch    string
	Main      bool
	Deleted   bool
	Facets    map[string]string // declared facets, values as text
	Text      string            // what full text and the embedding are built from
	Hash      string            // of Text, to skip the embedding call when unchanged
	Embedding []float32         // nil: not embedded
	Time      time.Time
}

// Hit is a search result.
type Hit struct {
	ID        domain.NodeID     `json:"id"`
	Version   domain.Version    `json:"version"`
	Namespace string            `json:"namespace"`
	Type      string            `json:"type"`
	Key       string            `json:"key"`
	State     string            `json:"state,omitempty"`
	Branch    string            `json:"branch"`
	Main      bool              `json:"main"`
	Facets    map[string]string `json:"facets,omitempty"`
	Score     float64           `json:"score"`
}

// Ref is the node version of the hit.
func (h Hit) Ref() domain.NodeRef { return domain.NodeRef{ID: h.ID, Version: h.Version} }

// Filter restricts a search. Values of one field are alternatives, fields are combined with AND.
type Filter struct {
	Namespace, Type, State, Branch []string
	Main                           *bool
	Facets                         map[string][]string // declared facets
}

// Query is a search request.
type Query struct {
	Text   string
	Filter Filter
	// Facets names the facets to count (built-in or declared).
	Facets []string
	Limit  int
	Offset int
}

// FacetCount is one value of a facet and the number of hits holding it.
type FacetCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// Result is the answer of a search.
type Result struct {
	Hits   []Hit                   `json:"hits"`
	Total  int                     `json:"total"`
	Facets map[string][]FacetCount `json:"facets,omitempty"`
	// Semantic tells whether the embedding side took part in the ranking.
	Semantic bool `json:"semantic"`
	// Truncated: more than PoolSize candidates matched, the total and facets cover the best ones.
	Truncated bool `json:"truncated,omitempty"`
}

// Store is the persistence of the index. Implementations: memory, SQLite (FTS5 + exact cosine scan),
// PostgreSQL (tsvector + pgvector).
type Store interface {
	// Upsert inserts or replaces the version (idempotent on ID + Version). The main flag of an
	// existing version is kept unless the document says Main, and a nil embedding keeps the stored
	// one when the text hash is unchanged.
	Upsert(ctx context.Context, d Doc) error
	// Hash returns the text hash and the embedding presence of a stored version.
	Hash(ctx context.Context, id domain.NodeID, v domain.Version) (hash string, embedded, found bool, err error)
	// SetMain marks the given versions as the head of main and clears the flag of the other versions
	// of the same nodes; removed nodes lose the flag.
	SetMain(ctx context.Context, set map[domain.NodeID]domain.Version, removed []domain.NodeID) error
	// List returns the versions matching the filter, by key.
	List(ctx context.Context, f Filter, limit int) ([]Hit, error)
	// FullText returns the best full-text matches, best first.
	FullText(ctx context.Context, text string, f Filter, limit int) ([]Hit, error)
	// Nearest returns the versions nearest to the vector, best first, with the cosine similarity as Score.
	Nearest(ctx context.Context, vec []float32, f Filter, limit int) ([]Hit, error)
	// Reset empties the index (before a full rebuild).
	Reset(ctx context.Context) error
}

// Embedder turns texts into vectors (the model gateway). Nil means text-only indexing.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Authorizer keeps the hits the caller may see (ABAC), preserving order.
type Authorizer func(ctx context.Context, hits []Hit) ([]Hit, error)

// DefaultMinSimilarity is the cosine similarity below which a vector match is dropped: nearest-neighbour
// search always returns something, however unrelated.
const DefaultMinSimilarity = 0.3

// Searcher runs hybrid searches over a store.
type Searcher struct {
	Store     Store
	Embedder  Embedder   // nil: full text only
	Authorize Authorizer // nil: no filtering
	// MinSimilarity drops vector matches less similar than this (cosine, 0 means DefaultMinSimilarity,
	// negative keeps everything).
	MinSimilarity float64
}

// Search ranks the candidates (full text and nearest vector merged by reciprocal rank fusion),
// authorizes them, counts the facets on the authorized set and returns the requested page.
func (s *Searcher) Search(ctx context.Context, q Query) (Result, error) {
	var lists [][]Hit
	var res Result
	if q.Text == "" {
		hits, err := s.Store.List(ctx, q.Filter, PoolSize+1)
		if err != nil {
			return res, err
		}
		lists = append(lists, hits)
	} else {
		ft, err := s.Store.FullText(ctx, q.Text, q.Filter, PoolSize+1)
		if err != nil {
			return res, err
		}
		lists = append(lists, ft)
		if s.Embedder != nil {
			vecs, err := s.Embedder.Embed(ctx, []string{q.Text})
			if err == nil && len(vecs) == 1 {
				near, err := s.Store.Nearest(ctx, vecs[0], q.Filter, PoolSize+1)
				if err != nil {
					return res, err
				}
				floor := s.MinSimilarity
				if floor == 0 {
					floor = DefaultMinSimilarity
				}
				kept := near[:0:0]
				for _, h := range near {
					if h.Score >= floor {
						kept = append(kept, h)
					}
				}
				lists, res.Semantic = append(lists, kept), true
			}
			// an embedding failure degrades the search to full text
		}
	}
	hits := fuse(lists)
	if len(hits) > PoolSize {
		hits, res.Truncated = hits[:PoolSize], true
	}
	if s.Authorize != nil {
		var err error
		if hits, err = s.Authorize(ctx, hits); err != nil {
			return res, err
		}
	}
	res.Total, res.Facets = len(hits), countFacets(hits, q.Facets)
	if q.Limit <= 0 {
		q.Limit = 20
	}
	lo := min(q.Offset, len(hits))
	res.Hits = hits[lo:min(lo+q.Limit, len(hits))]
	return res, nil
}

// fuse merges ranked lists by reciprocal rank fusion (k = 60); a single list keeps its order.
func fuse(lists [][]Hit) []Hit {
	if len(lists) == 1 {
		return lists[0]
	}
	type entry struct {
		hit   Hit
		score float64
	}
	by := map[domain.NodeRef]*entry{}
	var order []*entry
	for _, l := range lists {
		for rank, h := range l {
			e, ok := by[h.Ref()]
			if !ok {
				e = &entry{hit: h}
				by[h.Ref()], order = e, append(order, e)
			}
			e.score += 1 / float64(60+rank+1)
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].score > order[j].score })
	out := make([]Hit, len(order))
	for i, e := range order {
		out[i], out[i].Score = e.hit, e.score
	}
	return out
}

func countFacets(hits []Hit, names []string) map[string][]FacetCount {
	if len(names) == 0 {
		return nil
	}
	out := map[string][]FacetCount{}
	for _, name := range names {
		counts := map[string]int{}
		for _, h := range hits {
			if v, ok := h.facet(name); ok {
				counts[v]++
			}
		}
		fc := make([]FacetCount, 0, len(counts))
		for v, c := range counts {
			fc = append(fc, FacetCount{Value: v, Count: c})
		}
		sort.Slice(fc, func(i, j int) bool {
			if fc[i].Count != fc[j].Count {
				return fc[i].Count > fc[j].Count
			}
			return fc[i].Value < fc[j].Value
		})
		out[name] = fc
	}
	return out
}

func (h Hit) facet(name string) (string, bool) {
	switch name {
	case FacetNamespace:
		return h.Namespace, true
	case FacetType:
		return h.Type, true
	case FacetState:
		return h.State, h.State != ""
	case FacetBranch:
		return h.Branch, true
	case FacetMain:
		if h.Main {
			return "true", true
		}
		return "false", true
	}
	v, ok := h.Facets[name]
	return v, ok
}

// matches tells whether a document passes the filter (memory store, tests).
func (f Filter) matches(h Hit) bool {
	in := func(vals []string, v string) bool {
		if len(vals) == 0 {
			return true
		}
		for _, x := range vals {
			if x == v {
				return true
			}
		}
		return false
	}
	if !in(f.Namespace, h.Namespace) || !in(f.Type, h.Type) || !in(f.State, h.State) || !in(f.Branch, h.Branch) {
		return false
	}
	if f.Main != nil && *f.Main != h.Main {
		return false
	}
	for name, vals := range f.Facets {
		if v, ok := h.Facets[name]; !ok || !in(vals, v) {
			return false
		}
	}
	return true
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
