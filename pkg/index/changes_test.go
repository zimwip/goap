package index

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func nodeIn(id, key, title, project, owner string) domain.NodeEvent {
	ev := node(id, 1, key, "Requirement", title, "high", "main")
	ev.Project, ev.Owner = project, owner
	return ev
}

func changeEv(id, title, intent, project, owner, parent string, status domain.ChangeStatus, impacts ...string) domain.ChangeDocEvent {
	ev := domain.ChangeDocEvent{ID: domain.ChangeID(id), Title: title, Intent: intent, Methodology: "sdlc", Namespace: "alm", Status: status, State: "draft",
		ProjectID: project, OwnerOrg: owner, ParentID: domain.ChangeID(parent)}
	for _, k := range impacts {
		ev.Impacts = append(ev.Impacts, domain.ChangeDocRef{Key: k, Type: "alm@Requirement"})
	}
	return ev
}

func seedDocs(t *testing.T, s Store, emb Embedder) *Indexer {
	t.Helper()
	ctx := context.Background()
	x := &Indexer{Store: s, Embedder: emb}
	for _, ev := range []domain.NodeEvent{
		nodeIn("n1", "REQ-1", "Users reset the password", "P1", "ORG-A"),
		nodeIn("n2", "REQ-2", "Export the report as PDF", "P2", "ORG-B"),
	} {
		if err := x.OnNode(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.OnBaseline(ctx, domain.BaselineEvent{Branch: "main", Set: map[domain.NodeID]domain.Version{"n1": 1, "n2": 1}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ev domain.ChangeDocEvent
		to string
	}{
		{changeEv("C1", "Reset password flow", "Rework how users reset the password by mail", "P1", "ORG-A", "", domain.ChangeActive, "REQ-1"), ""},
		{changeEv("C2", "Export report as PDF", "Add a PDF export of the report", "P2", "ORG-B", "C1", domain.ChangeApplied, "REQ-2"), ""},
		{changeEv("C3", "Draft the password policy", "Private notes on the password policy", "P1", "USR:alice", "", domain.ChangeActive), "alice"},
	} {
		if err := x.OnChange(ctx, c.ev, c.to); err != nil {
			t.Fatal(err)
		}
	}
	return x
}

func keysOf(r Result) []string {
	var out []string
	for _, h := range r.Hits {
		out = append(out, h.Key)
	}
	return out
}

func TestChangeDocumentsAndFilters(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		seedDocs(t, s, bagEmbedder{})
		se := &Searcher{Store: s, Embedder: bagEmbedder{}}
		search := func(q Query) Result {
			t.Helper()
			r, err := se.Search(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
		both := []string{KindNode, KindChange}

		if r := search(Query{}); r.Total != 2 || r.Hits[0].Kind != KindNode {
			t.Fatalf("default kind is node: %+v", r)
		}
		if r := search(Query{Filter: Filter{Kind: []string{KindChange}}}); r.Total != 3 {
			t.Fatalf("changes: %+v", r)
		}
		if r := search(Query{Filter: Filter{Kind: both}}); r.Total != 5 {
			t.Fatalf("mixed: %+v", r)
		}
		// a hit of a change carries its header fields
		r := search(Query{Text: "pdf", Mode: ModeLexical, Filter: Filter{Kind: []string{KindChange}}})
		if r.Total != 1 {
			t.Fatalf("pdf: %+v", r)
		}
		h := r.Hits[0]
		if h.Kind != KindChange || h.ID != "C2" || h.Key != "C2" || h.Title != "Export report as PDF" || h.Status != "applied" || h.Project != "P2" ||
			h.Methodology != "sdlc" || h.Owner != "ORG-B" || h.Parent != "C1" || h.Version != 0 || !h.Main || h.Namespace != "alm" {
			t.Fatalf("hit = %+v", h)
		}
		// the keys of the impacts are searchable
		if r := search(Query{Text: "req", Mode: ModeLexical, Filter: Filter{Kind: []string{KindChange}}}); r.Total != 2 {
			t.Fatalf("impact key: %+v", r)
		}
		// filters
		for name, c := range map[string]struct {
			f    Filter
			want string
		}{
			"project":     {Filter{Kind: both, Project: []string{"P1"}}, "C1,C3,REQ-1"},
			"project 2":   {Filter{Kind: both, Project: []string{"P2"}}, "C2,REQ-2"},
			"owner":       {Filter{Kind: both, Owner: []string{"ORG-A"}}, "C1,REQ-1"},
			"status":      {Filter{Kind: both, Status: []string{"active"}}, "C1,C3"},
			"methodology": {Filter{Kind: both, Methodology: []string{"sdlc"}}, "C1,C2,C3"},
			"roots":       {Filter{Kind: both, RootsOnly: true}, "C1,C3"},
			"main":        {Filter{Kind: both, Main: ptr(true)}, "C1,C2,C3,REQ-1,REQ-2"},
			"not main":    {Filter{Kind: both, Main: ptr(false)}, ""},
			"types":       {Filter{Kind: both, Type: []string{"Requirement"}}, "REQ-1,REQ-2"},
			"namespace":   {Filter{Kind: both, Namespace: []string{"alm"}}, "C1,C2,C3,REQ-1,REQ-2"},
		} {
			got := strings.Join(sortedKeys(search(Query{Filter: c.f, Limit: 50})), ",")
			if got != c.want {
				t.Errorf("%s: got %q want %q", name, got, c.want)
			}
		}
		// facets
		r = search(Query{Filter: Filter{Kind: both}, Facets: []string{FacetKind, FacetProject, FacetStatus, FacetType}})
		if got := r.Facets[FacetProject]; len(got) != 2 || got[0] != (FacetCount{"P1", 3}) {
			t.Fatalf("project facet %v", got)
		}
		if got := r.Facets[FacetKind]; len(got) != 2 || got[0] != (FacetCount{KindChange, 3}) {
			t.Fatalf("kind facet %v", got)
		}
		if got := r.Facets[FacetType]; len(got) != 1 {
			t.Fatalf("a change has no type: %v", got)
		}
		if got := r.Facets[FacetStatus]; len(got) != 2 {
			t.Fatalf("status facet %v", got)
		}
		// the personal subject is stored for the authorizer
		if r := search(Query{Filter: Filter{Kind: []string{KindChange}, Status: []string{"active"}}, Limit: 10}); r.Total != 2 {
			t.Fatal(r)
		} else {
			for _, h := range r.Hits {
				if (h.ID == "C3") != (h.PersonalTo == "alice") {
					t.Fatalf("personalTo %+v", h)
				}
			}
		}
		// paging
		p1 := search(Query{Filter: Filter{Kind: both}, Limit: 2})
		p2 := search(Query{Filter: Filter{Kind: both}, Limit: 2, Offset: 2})
		p3 := search(Query{Filter: Filter{Kind: both}, Limit: 2, Offset: 4})
		if p1.Total != 5 || len(p1.Hits) != 2 || len(p2.Hits) != 2 || len(p3.Hits) != 1 || p1.Hits[0].ID == p2.Hits[0].ID {
			t.Fatalf("paging %d %d %d", len(p1.Hits), len(p2.Hits), len(p3.Hits))
		}
	})
}

func ptr[T any](v T) *T { return &v }

func sortedKeys(r Result) []string {
	ks := keysOf(r)
	for i := range ks {
		for j := i + 1; j < len(ks); j++ {
			if ks[j] < ks[i] {
				ks[i], ks[j] = ks[j], ks[i]
			}
		}
	}
	return ks
}

func TestChangeUpdateMoveAndPurge(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		emb := &countEmbedder{}
		x := &Indexer{Store: s, Embedder: emb}
		ev := changeEv("C1", "Reset password flow", "intent", "P1", "ORG-A", "", domain.ChangeActive, "REQ-1")
		if err := x.OnChange(ctx, ev, ""); err != nil {
			t.Fatal(err)
		}
		// the same text again (a new status, a move to another project): no new embedding
		ev.Status, ev.ProjectID, ev.State = domain.ChangeCommitted, "P9", "review"
		if err := x.OnChange(ctx, ev, ""); err != nil {
			t.Fatal(err)
		}
		if emb.calls != 1 {
			t.Fatalf("embed calls = %d, want 1 (hash skip)", emb.calls)
		}
		se := &Searcher{Store: s, Embedder: emb}
		r, err := se.Search(ctx, Query{Filter: Filter{Kind: []string{KindChange}}})
		if err != nil || r.Total != 1 || r.Hits[0].Project != "P9" || r.Hits[0].Status != "committed" || r.Hits[0].State != "review" {
			t.Fatalf("%v %+v", err, r)
		}
		if r, _ := se.Search(ctx, Query{Filter: Filter{Kind: []string{KindChange}, Project: []string{"P1"}}}); r.Total != 0 {
			t.Fatal("the old project still matches")
		}
		// a reformulated title is a new text: embedded again, and found by its new words
		ev.Title = "Passkeys instead of passwords"
		if err := x.OnChange(ctx, ev, ""); err != nil {
			t.Fatal(err)
		}
		if emb.calls != 2 {
			t.Fatalf("embed calls = %d, want 2", emb.calls)
		}
		if r, _ := se.Search(ctx, Query{Text: "passkeys", Filter: Filter{Kind: []string{KindChange}}}); r.Total != 1 {
			t.Fatalf("reformulated: %+v", r)
		}
		if r, _ := se.Search(ctx, Query{Text: "reset", Filter: Filter{Kind: []string{KindChange}}, Mode: ModeLexical}); r.Total != 0 {
			t.Fatalf("the old title is gone from the text: %+v", r)
		}
		// a node with the same id is another document
		if err := x.OnNode(ctx, domain.NodeEvent{ID: "C1", Version: 1, Namespace: "alm", Type: "T", Key: "K", Branch: "main"}); err != nil {
			t.Fatal(err)
		}
		if err := x.OnChange(ctx, domain.ChangeDocEvent{ID: "C1", Deleted: true}, ""); err != nil {
			t.Fatal(err)
		}
		if r, _ := se.Search(ctx, Query{Filter: Filter{Kind: []string{KindChange, KindNode}}}); r.Total != 1 || r.Hits[0].Kind != KindNode {
			t.Fatalf("purge: %+v", r)
		}
	})
}

func TestSearchModes(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		seedDocs(t, s, bagEmbedder{})
		change := Filter{Kind: []string{KindChange}}
		with := &Searcher{Store: s, Embedder: bagEmbedder{}}
		without := &Searcher{Store: s}

		// semantic requires the embedder
		if _, err := without.Search(ctx, Query{Text: "password", Mode: ModeSemantic, Filter: change}); !errors.Is(err, ErrSemanticUnavailable) {
			t.Fatalf("no embedder: %v", err)
		}
		if _, err := (&Searcher{Store: s, Embedder: failing{}}).Search(ctx, Query{Text: "password", Mode: ModeSemantic, Filter: change}); !errors.Is(err, ErrSemanticUnavailable) {
			t.Fatalf("failing embedder: %v", err)
		}
		if _, err := with.Search(ctx, Query{Mode: ModeSemantic, Filter: change}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("semantic without text: %v", err)
		}
		if _, err := with.Search(ctx, Query{Text: "x", Mode: "fuzzy"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unknown mode: %v", err)
		}
		// hybrid degrades
		for name, se := range map[string]*Searcher{"no embedder": without, "failing": {Store: s, Embedder: failing{}}} {
			r, err := se.Search(ctx, Query{Text: "password", Filter: change})
			if err != nil || r.Semantic || r.Total != 2 {
				t.Fatalf("hybrid with %s: %v %+v", name, err, r)
			}
		}
		// each mode
		r, err := with.Search(ctx, Query{Text: "password", Mode: ModeLexical, Filter: change})
		if err != nil || r.Semantic || r.Total != 2 {
			t.Fatalf("lexical: %v %+v", err, r)
		}
		r, err = with.Search(ctx, Query{Text: "password", Mode: ModeSemantic, Filter: change, MinSimilarity: ptr(0.01)})
		if err != nil || !r.Semantic || r.Total == 0 || r.Hits[0].Similarity <= 0 || r.Hits[0].Score != r.Hits[0].Similarity {
			t.Fatalf("semantic: %v %+v", err, r)
		}
		r, err = with.Search(ctx, Query{Text: "password", Filter: change, MinSimilarity: ptr(0.0)})
		if err != nil || !r.Semantic || r.Hits[0].Similarity == 0 {
			t.Fatalf("hybrid: %v %+v", err, r)
		}
		// the floor of the request replaces the global one
		all, _ := with.Search(ctx, Query{Text: "password", Mode: ModeSemantic, Filter: change, MinSimilarity: ptr(0.0)})
		none, _ := with.Search(ctx, Query{Text: "password", Mode: ModeSemantic, Filter: change, MinSimilarity: ptr(0.999)})
		if all.Total != 3 || none.Total != 0 {
			t.Fatalf("min similarity: %d %d", all.Total, none.Total)
		}
		strict := &Searcher{Store: s, Embedder: bagEmbedder{}, MinSimilarity: 0.999}
		if r, _ := strict.Search(ctx, Query{Text: "password", Mode: ModeSemantic, Filter: change}); r.Total != 0 {
			t.Fatal("global floor")
		}
		if r, _ := strict.Search(ctx, Query{Text: "password", Mode: ModeSemantic, Filter: change, MinSimilarity: ptr(0.0)}); r.Total != 3 {
			t.Fatal("the request overrides the global floor")
		}
		for _, bad := range []float64{-0.1, 1.1} {
			if _, err := with.Search(ctx, Query{Text: "x", MinSimilarity: ptr(bad)}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("floor %v: %v", bad, err)
			}
		}
	})
}

type failing struct{}

func (failing) Embed(context.Context, []string) ([][]float32, error) { return nil, errors.New("down") }

func TestSimilarTo(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		x := seedDocs(t, s, bagEmbedder{})
		// a change nearly the same as C1, and an unembedded one
		if err := x.OnChange(ctx, changeEv("C4", "Reset password flow again", "Rework how users reset the password by mail", "P1", "ORG-A", "", domain.ChangeActive, "REQ-1"), ""); err != nil {
			t.Fatal(err)
		}
		bare := &Indexer{Store: s}
		if err := bare.OnChange(ctx, changeEv("C5", "No embedding", "none", "P1", "ORG-A", "", domain.ChangeActive), ""); err != nil {
			t.Fatal(err)
		}
		calls := &countEmbedder{}
		se := &Searcher{Store: s, Embedder: calls, MinSimilarity: 0.01}
		change := Filter{Kind: []string{KindChange}}

		r, err := se.Search(ctx, Query{SimilarTo: &Ref{Kind: KindChange, ID: "C1"}, Filter: change})
		if err != nil || !r.Semantic || len(r.Hits) == 0 {
			t.Fatalf("%v %+v", err, r)
		}
		if calls.calls != 0 {
			t.Fatal("similar_to embeds nothing")
		}
		for _, h := range r.Hits {
			if h.ID == "C1" {
				t.Fatal("excludes itself")
			}
		}
		if r.Hits[0].ID != "C4" {
			t.Fatalf("nearest = %s", r.Hits[0].ID)
		}
		// the filters hold
		r, err = se.Search(ctx, Query{SimilarTo: &Ref{Kind: KindChange, ID: "C1"}, Filter: Filter{Kind: []string{KindChange}, Project: []string{"P2"}}})
		if err != nil || r.Total != 1 || r.Hits[0].ID != "C2" {
			t.Fatalf("filtered: %v %+v", err, r)
		}
		// a node to documents of both kinds; itself excluded whatever its version
		r, err = se.Search(ctx, Query{SimilarTo: &Ref{ID: "n1"}, Filter: Filter{Kind: []string{KindNode, KindChange}}})
		if err != nil || len(r.Hits) == 0 {
			t.Fatalf("node: %v %+v", err, r)
		}
		for _, h := range r.Hits {
			if h.ID == "n1" && h.Kind == KindNode {
				t.Fatal("excludes itself")
			}
		}
		if r.Hits[0].ID != "C1" && r.Hits[0].ID != "C4" {
			t.Fatalf("nearest change = %+v", r.Hits[0])
		}
		// the same id of another kind is another document
		if _, err := se.Search(ctx, Query{SimilarTo: &Ref{Kind: KindChange, ID: "n1"}, Filter: change}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("not found: %v", err)
		}
		if _, err := se.Search(ctx, Query{SimilarTo: &Ref{Kind: KindChange, ID: "C5"}, Filter: change}); !errors.Is(err, ErrSemanticUnavailable) {
			t.Fatalf("not embedded: %v", err)
		}
		// the source is authorized too: a document the caller cannot see is not found
		se.Authorize = func(_ context.Context, hits []Hit) ([]Hit, error) {
			var out []Hit
			for _, h := range hits {
				if h.ID != "C1" {
					out = append(out, h)
				}
			}
			return out, nil
		}
		if _, err := se.Search(ctx, Query{SimilarTo: &Ref{Kind: KindChange, ID: "C1"}, Filter: change}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("hidden source: %v", err)
		}
		if r, err := se.Search(ctx, Query{SimilarTo: &Ref{Kind: KindChange, ID: "C4"}, Filter: change}); err != nil || r.Total == 0 {
			t.Fatalf("%v %+v", err, r)
		} else {
			for _, h := range r.Hits {
				if h.ID == "C1" {
					t.Fatal("hidden hit")
				}
			}
		}
	})
}

func TestSnippets(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		long := strings.Repeat("filler words go on and on. ", 40) + "The quarantine rule is the one that matters. " + strings.Repeat("more filler. ", 40)
		x := &Indexer{Store: s}
		if err := x.OnChange(ctx, changeEv("C1", "Title", long, "P1", "ORG-A", "", domain.ChangeActive), ""); err != nil {
			t.Fatal(err)
		}
		se := &Searcher{Store: s}
		r, err := se.Search(ctx, Query{Text: "quarantine", Filter: Filter{Kind: []string{KindChange}}, Snippet: true})
		if err != nil || r.Total != 1 {
			t.Fatalf("%v %+v", err, r)
		}
		sn := r.Hits[0].Snippet
		if !strings.Contains(sn, "quarantine") || len([]rune(sn)) > SnippetMax {
			t.Fatalf("snippet (%d): %q", len([]rune(sn)), sn)
		}
		r, _ = se.Search(ctx, Query{Text: "quarantine", Filter: Filter{Kind: []string{KindChange}}})
		if r.Hits[0].Snippet != "" {
			t.Fatal("no snippet unless asked")
		}
		// no overlap with the text (a semantic match): the beginning of the document, bounded
		r, _ = se.Search(ctx, Query{Filter: Filter{Kind: []string{KindChange}}, Snippet: true})
		if sn := r.Hits[0].Snippet; sn == "" || len([]rune(sn)) > SnippetMax {
			t.Fatalf("snippet %q", sn)
		}
	})
}

func TestChangeTextIsBounded(t *testing.T) {
	ev := changeEv("C1", strings.Repeat("t", 5000), strings.Repeat("i", 5000), "P", "O", "", domain.ChangeActive)
	for i := 0; i < 300; i++ {
		ev.Impacts = append(ev.Impacts, domain.ChangeDocRef{Key: strings.Repeat("k", 30), Type: "alm@X"})
	}
	if n := len([]rune(ChangeText(ev))); n > MaxChangeText {
		t.Fatalf("text of %d runes", n)
	}
	txt := ChangeText(changeEv("C1", "T", "I", "P", "O", "", domain.ChangeActive, "REQ-1"))
	for _, want := range []string{"title: T", "intent: I", "methodology: sdlc", "namespace: alm", "nodes: REQ-1 (alm@Requirement)"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("%q misses %q", txt, want)
		}
	}
}

func TestNodeProjectAndOwnerFacets(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		seedDocs(t, s, nil)
		r, err := (&Searcher{Store: s}).Search(ctx, Query{Facets: []string{FacetProject, FacetOwner}, Filter: Filter{Project: []string{"P1"}}})
		if err != nil || r.Total != 1 || r.Hits[0].Project != "P1" || r.Hits[0].Owner != "ORG-A" || r.Facets[FacetProject][0] != (FacetCount{"P1", 1}) {
			t.Fatalf("%v %+v", err, r)
		}
	})
}
