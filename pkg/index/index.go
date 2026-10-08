// Package index is the node index (ADR 0026): a read model of the graph that answers full-text,
// semantic (embedding) and faceted searches. It is fed by the node and baseline events of the graph
// and knows nothing else of the platform.
package index

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

// Kinds of documents (ADR 0095): the versions of the nodes of the graph and the changes. One table, one text index and
// one vector index hold both, told apart by the kind: the filters, the facets, the ranking, the authorization and the
// reindex are the same code, and a search can mix them.
const (
	KindNode   = "node"
	KindChange = "change"
	// KindRequest is a request (ADR 0098): Owner holds its requester, Project its project (empty until triaged).
	KindRequest = "request"
)

// Built-in facets, always present besides the ones the node type declares.
const (
	FacetKind        = "kind"
	FacetNamespace   = "namespace"
	FacetType        = "type"
	FacetState       = "state"
	FacetBranch      = "branch"
	FacetMain        = "main" // "true" when the version is the head of main (a change document always is)
	FacetProject     = "project"
	FacetOwner       = "owner"
	FacetStatus      = "status" // of a change
	FacetMethodology = "methodology"
)

// Search modes.
const (
	ModeHybrid   = "hybrid"
	ModeLexical  = "lexical"
	ModeSemantic = "semantic"
)

// SnippetMax bounds the passage returned with a hit, in runes.
const SnippetMax = 240

// Errors of a search.
var (
	// ErrSemanticUnavailable: semantic search was asked and no embedding is available (no embedder, the embedding of
	// the query failed, or the document to compare with has no embedding).
	ErrSemanticUnavailable = errors.New("semantic search is unavailable: no embedding model answers")
	// ErrInvalid: the request is not well formed.
	ErrInvalid = errors.New("invalid search")
	// ErrNotFound: the document of a similar_to does not exist, or the caller may not see it.
	ErrNotFound = errors.New("document not found")
)

// PoolSize bounds the candidates a search ranks, authorizes and counts facets on.
const PoolSize = 500

// Ref identifies a document: a node version, or a change (ID is the change id, Version 0).
type Ref struct {
	Kind    string
	ID      domain.NodeID
	Version domain.Version
}

// Doc is one indexed document: a node version or a change (ADR 0095).
type Doc struct {
	Kind      string // KindNode when empty
	ID        domain.NodeID
	Version   domain.Version
	Namespace string
	Type      string // empty for a change
	Key       string // the change id for a change
	State     string // lifecycle state of a node; of a change
	Branch    string
	Main      bool
	Deleted   bool
	// Project and Owner are keys: the project and the unit owning the node version; for a change its project and its
	// holding unit.
	Project string
	Owner   string
	// Change documents only.
	Status      string
	Methodology string
	Parent      string // parent change, empty for a root change
	PersonalTo  string // subject a personal change belongs to
	Title       string
	Facets      map[string]string // declared facets, values as text
	Text        string            // what full text and the embedding are built from
	Hash        string            // of Text, to skip the embedding call when unchanged
	Embedding   []float32         // nil: not embedded
	Time        time.Time
}

// Hit is a search result.
type Hit struct {
	Kind      string            `json:"kind"`
	ID        domain.NodeID     `json:"id"`
	Version   domain.Version    `json:"version"`
	Namespace string            `json:"namespace"`
	Type      string            `json:"type,omitempty"`
	Key       string            `json:"key"`
	State     string            `json:"state,omitempty"`
	Branch    string            `json:"branch"`
	Main      bool              `json:"main"`
	Deleted   bool              `json:"deleted,omitempty"`
	Project   string            `json:"project,omitempty"`
	Owner     string            `json:"owner,omitempty"`
	Facets    map[string]string `json:"facets,omitempty"`
	// Change documents only.
	Status      string `json:"status,omitempty"`
	Methodology string `json:"methodology,omitempty"`
	Parent      string `json:"parent,omitempty"`
	PersonalTo  string `json:"personalTo,omitempty"`
	Title       string `json:"title,omitempty"`
	// Score ranks the hit (the fused rank in a hybrid search, the cosine similarity in a semantic one); Similarity is
	// the cosine similarity when the hit came out of the vector side, else 0.
	Score      float64 `json:"score"`
	Similarity float64 `json:"similarity,omitempty"`
	Snippet    string  `json:"snippet,omitempty"`
}

// Ref identifies the document of the hit.
func (h Hit) Ref() Ref { return Ref{Kind: kindOf(h.Kind), ID: h.ID, Version: h.Version} }

func kindOf(k string) string {
	if k == "" {
		return KindNode
	}
	return k
}

// Filter restricts a search. Values of one field are alternatives, fields are combined with AND: a field a kind of
// document does not have (the type of a change, the status of a node) keeps no document of that kind once set.
type Filter struct {
	Kind                           []string // empty: every kind (Searcher.Search defaults to nodes)
	Namespace, Type, State, Branch []string
	Main                           *bool
	Project, Owner                 []string
	Status, Methodology            []string            // of changes
	RootsOnly                      bool                // changes with no parent (a node is no root change)
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
	// Mode is ModeHybrid (default), ModeLexical or ModeSemantic. Semantic requires an embedding and fails with
	// ErrSemanticUnavailable without one; hybrid degrades to lexical and reports Result.Semantic false.
	Mode string
	// MinSimilarity, when set, replaces the searcher's floor for this request (cosine, 0..1).
	MinSimilarity *float64
	// Snippet asks for the best matching passage of each returned hit.
	Snippet bool
	// SimilarTo ranks the documents nearest to an existing one by its stored embedding (no text, no embedding call). It
	// excludes the document itself (every version of a node) and honours the filter.
	SimilarTo *Ref
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
	// Hash returns the text hash and the embedding presence of a stored document.
	Hash(ctx context.Context, kind string, id domain.NodeID, v domain.Version) (hash string, embedded, found bool, err error)
	// Delete removes every version of a document (a purged change).
	Delete(ctx context.Context, kind string, id domain.NodeID) error
	// Texts returns the text of the documents (for snippets).
	Texts(ctx context.Context, refs []Ref) (map[Ref]string, error)
	// Vector returns the document of a node (its head of main, else its latest version) or of a change, with its
	// embedding; found is false when it does not exist, the vector is nil when it is not embedded.
	Vector(ctx context.Context, kind string, id domain.NodeID) (hit Hit, vec []float32, found bool, err error)
	// SetMain marks the given versions as the head of main and clears the flag of the other versions
	// of the same nodes (documents of kind node); removed nodes lose the flag.
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

// Search ranks the candidates (full text and nearest vector merged by reciprocal rank fusion), authorizes them, counts
// the facets on the authorized set and returns the requested page. Nothing is counted, totalled or quoted before the
// authorization: a document the caller may not see leaves no trace in the answer.
func (s *Searcher) Search(ctx context.Context, q Query) (Result, error) {
	var res Result
	if len(q.Filter.Kind) == 0 {
		q.Filter.Kind = []string{KindNode}
	}
	if q.Mode == "" {
		q.Mode = ModeHybrid
	}
	if q.Mode != ModeHybrid && q.Mode != ModeLexical && q.Mode != ModeSemantic {
		return res, fmt.Errorf("%w: unknown mode %q", ErrInvalid, q.Mode)
	}
	floor := s.MinSimilarity
	if floor == 0 {
		floor = DefaultMinSimilarity
	}
	if q.MinSimilarity != nil {
		if *q.MinSimilarity < 0 || *q.MinSimilarity > 1 {
			return res, fmt.Errorf("%w: min similarity %v is not in 0..1", ErrInvalid, *q.MinSimilarity)
		}
		floor = *q.MinSimilarity
	}
	keep := func(near []Hit) []Hit {
		kept := near[:0:0]
		for _, h := range near {
			if h.Score >= floor {
				h.Similarity = h.Score
				kept = append(kept, h)
			}
		}
		return kept
	}

	var lists [][]Hit
	var self *Ref
	switch {
	case q.SimilarTo != nil:
		hits, err := s.similar(ctx, q, keep)
		if err != nil {
			return res, err
		}
		lists, res.Semantic, self = [][]Hit{hits}, true, q.SimilarTo
	case q.Text == "":
		if q.Mode == ModeSemantic {
			return res, fmt.Errorf("%w: a semantic search needs a text", ErrInvalid)
		}
		hits, err := s.Store.List(ctx, q.Filter, PoolSize+1)
		if err != nil {
			return res, err
		}
		lists = append(lists, hits)
	default:
		if q.Mode != ModeSemantic {
			ft, err := s.Store.FullText(ctx, q.Text, q.Filter, PoolSize+1)
			if err != nil {
				return res, err
			}
			lists = append(lists, ft)
		}
		if q.Mode != ModeLexical && s.Embedder != nil {
			vecs, err := s.Embedder.Embed(ctx, []string{q.Text})
			if err == nil && len(vecs) == 1 {
				near, err := s.Store.Nearest(ctx, vecs[0], q.Filter, PoolSize+1)
				if err != nil {
					return res, err
				}
				lists, res.Semantic = append(lists, keep(near)), true
			} else if q.Mode == ModeSemantic {
				return res, fmt.Errorf("%w: %v", ErrSemanticUnavailable, err)
			}
			// in hybrid mode an embedding failure degrades the search to full text
		} else if q.Mode == ModeSemantic {
			return res, ErrSemanticUnavailable
		}
	}
	hits := fuse(lists)
	if self != nil {
		hits = slices.DeleteFunc(hits, func(h Hit) bool { return kindOf(h.Kind) == kindOf(self.Kind) && h.ID == self.ID })
	}
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
	if q.Snippet && len(res.Hits) > 0 {
		if err := s.snippets(ctx, res.Hits, q.Text); err != nil {
			return res, err
		}
	}
	return res, nil
}

// similar ranks the documents nearest to the stored embedding of an existing one. The source goes through the
// authorization too: a document the caller cannot see is as good as absent.
func (s *Searcher) similar(ctx context.Context, q Query, keep func([]Hit) []Hit) ([]Hit, error) {
	src, vec, found, err := s.Store.Vector(ctx, kindOf(q.SimilarTo.Kind), q.SimilarTo.ID)
	if err != nil {
		return nil, err
	}
	if found && s.Authorize != nil {
		seen, err := s.Authorize(ctx, []Hit{src})
		if err != nil {
			return nil, err
		}
		found = len(seen) == 1
	}
	if !found {
		return nil, fmt.Errorf("%w: %s %s", ErrNotFound, kindOf(q.SimilarTo.Kind), q.SimilarTo.ID)
	}
	if len(vec) == 0 {
		return nil, fmt.Errorf("%w: %s %s has no embedding (yet)", ErrSemanticUnavailable, kindOf(q.SimilarTo.Kind), q.SimilarTo.ID)
	}
	near, err := s.Store.Nearest(ctx, vec, q.Filter, PoolSize+2)
	if err != nil {
		return nil, err
	}
	return keep(near), nil
}

// snippets fills the passage of each hit that best matches the query (the beginning of the text when nothing does).
func (s *Searcher) snippets(ctx context.Context, hits []Hit, text string) error {
	refs := make([]Ref, len(hits))
	for i, h := range hits {
		refs[i] = h.Ref()
	}
	texts, err := s.Store.Texts(ctx, refs)
	if err != nil {
		return err
	}
	for i := range hits {
		hits[i].Snippet = Snippet(texts[refs[i]], text, SnippetMax)
	}
	return nil
}

// Snippet returns the passage of a text that holds most terms of the query, at most limit runes (an ellipsis marks a
// cut). The passages are the lines of the text, long ones cut at the sentences.
func Snippet(text, query string, limit int) string {
	terms := Tokens(query)
	var best string
	bestScore := -1
	for _, line := range strings.Split(text, "\n") {
		for _, p := range sentences(line) {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			score := 0
			toks := Tokens(p)
			for _, t := range terms {
				for _, x := range toks {
					if strings.HasPrefix(x, t) {
						score++
						break
					}
				}
			}
			if score > bestScore {
				best, bestScore = p, score
			}
		}
	}
	return clip(best, limit, terms)
}

func sentences(line string) []string {
	if len([]rune(line)) <= SnippetMax {
		return []string{line}
	}
	return strings.SplitAfter(line, ". ")
}

// clip cuts s to max runes at most (ellipses included), around the first term it holds when it is long.
func clip(s string, limit int, terms []string) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	start := 0
	low := []rune(strings.ToLower(s))
	if len(low) == len(r) { // lowering keeps the length but for rare scripts: then the window starts at the beginning
		for _, t := range terms {
			if i := strings.Index(string(low), t); i >= 0 {
				start = len([]rune(string(low)[:i])) - limit/4
				break
			}
		}
	}
	start = min(max(start, 0), len(r)-1)
	room := limit
	if start > 0 {
		room--
	}
	end := start + room
	if end < len(r) {
		room--
		end = start + room
	}
	end = min(end, len(r))
	out := string(r[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(r) {
		out += "…"
	}
	return out
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
	by := map[Ref]*entry{}
	var order []*entry
	for _, l := range lists {
		for rank, h := range l {
			e, ok := by[h.Ref()]
			if !ok {
				e = &entry{hit: h}
				by[h.Ref()], order = e, append(order, e)
			}
			if h.Similarity > e.hit.Similarity {
				e.hit.Similarity = h.Similarity
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
	case FacetKind:
		return kindOf(h.Kind), true
	case FacetNamespace:
		return h.Namespace, true
	case FacetType:
		return h.Type, h.Type != ""
	case FacetProject:
		return h.Project, h.Project != ""
	case FacetOwner:
		return h.Owner, h.Owner != ""
	case FacetStatus:
		return h.Status, h.Status != ""
	case FacetMethodology:
		return h.Methodology, h.Methodology != ""
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
	if !in(f.Kind, kindOf(h.Kind)) || !in(f.Namespace, h.Namespace) || !in(f.Type, h.Type) || !in(f.State, h.State) || !in(f.Branch, h.Branch) ||
		!in(f.Project, h.Project) || !in(f.Owner, h.Owner) || !in(f.Status, h.Status) || !in(f.Methodology, h.Methodology) {
		return false
	}
	if f.RootsOnly && (kindOf(h.Kind) != KindChange || h.Parent != "") {
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
