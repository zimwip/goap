package indexersvc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"testing"

	"connectrpc.com/connect"

	indexv1 "github.com/zimwip/goap/gen/goap/index/v1"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/graph/graphtest"
	"github.com/zimwip/goap/pkg/index"
	"github.com/zimwip/goap/pkg/typecat"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

var changes = index.Filter{Kind: []string{index.KindChange}}

// The documents of the changes follow the graph through the in-process sink (the path of goap-dev): a created change,
// a reformulated one, the impacts it gains, a move to another project, a purge, and a republish after a reset.
func TestChangesFollowTheGraph(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := New(index.NewMemory(), &bagEmbedder{}, nil, quietLog())
	d, err := def.ParseDomain([]byte(`
name: docs
version: 1.0.0
nodeTypes:
  - {name: Requirement, attributes: [title], search: [{property: title, text: true}]}
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
	g.Observe(NewSink(ctx, svc))
	root := g.Structure(domain.StructureProject).Root
	head, err := g.BranchHead(ctx, "docs", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Namespace: "docs", Title: "Reset the password", Intent: "Users reset it by mail", Methodology: "sdlc",
		BaselineID: head.ID, OwnBranch: true, ProjectID: root})
	if err != nil {
		t.Fatal(err)
	}
	q := func(text string, f index.Filter) index.Query {
		f.Kind = []string{index.KindChange}
		return index.Query{Text: text, Mode: index.ModeLexical, Filter: f}
	}
	res := waitFor(t, svc, ctx, q("password", index.Filter{}), 1)
	h := res.Hits[0]
	if h.ID != domain.NodeID(c.ID) || h.Title != "Reset the password" || h.Project != root || h.Methodology != "sdlc" || h.Status != string(c.Status) || h.Namespace != "docs" {
		t.Fatalf("hit %+v", h)
	}
	if h.Owner == "" {
		t.Fatalf("the holding unit is indexed: %+v", h)
	}

	// reformulated
	title := "Passkeys everywhere"
	if _, err := g.UpdateChange(ctx, c.ID, graph.ChangePatch{Title: &title}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, svc, ctx, q("passkeys", index.Filter{}), 1)
	waitFor(t, svc, ctx, q("reset", index.Filter{}), 1) // the intent still says reset

	// the keys of its impacts, published without the header (the impacts alone were written)
	if _, err := g.ImpactNodeCreate(ctx, c.ID, graph.NodeCreate{Key: "ALPHAKEY", Type: "docs@Requirement", Properties: map[string]any{"title": "x"}, Rationale: "r"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, svc, ctx, q("alphakey", index.Filter{}), 1)

	// moved to another project (ADR 0091)
	if _, err := graphtest.Project(ctx, g, "P-NEW", "New project"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.MoveChange(ctx, c.ID, "P-NEW"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, svc, ctx, q("passkeys", index.Filter{Project: []string{"P-NEW"}}), 1)
	waitFor(t, svc, ctx, q("passkeys", index.Filter{Project: []string{root}}), 0)

	// a reindex publishes the headers again
	svc.Republish = func(ctx context.Context) (int, error) { return g.Republish(ctx, NewSink(ctx, svc)) }
	n, err := svc.Reindex(ctx)
	if err != nil || n == 0 {
		t.Fatalf("reindex %d %v", n, err)
	}
	waitFor(t, svc, ctx, q("passkeys", index.Filter{Project: []string{"P-NEW"}}), 1)
	waitFor(t, svc, ctx, q("alphakey", index.Filter{}), 1)
	if _, _, _, _, nc, _ := svc.Status(); nc == 0 {
		t.Fatal("status counts the changes")
	}

	// a change that landed nothing is purged with its document
	other, err := g.CreateChange(ctx, graph.NewChange{Namespace: "docs", Title: "Scratch", Intent: "scratch", BaselineID: head.ID, ProjectID: root})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, svc, ctx, q("scratch", index.Filter{}), 1)
	if _, err := g.PurgeChange(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, svc, ctx, q("scratch", index.Filter{}), 0)
}

type fakeAccess struct {
	projects map[string][]string // subject -> projects (and below) it holds; "*": every project
	subs     map[string][]string
}

func (f fakeAccess) MayAccessProject(_ context.Context, p authz.Principal, project string) (bool, error) {
	l := f.projects[p.Subject]
	return slices.Contains(l, "*") || slices.Contains(l, project), nil
}

func (f fakeAccess) SubProjects(_ context.Context, project string) ([]string, error) {
	if s, ok := f.subs[project]; ok {
		return s, nil
	}
	return []string{project}, nil
}

func publish(t *testing.T, svc *Service, subject string, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	if err := svc.Handle(context.Background(), subject, data); err != nil {
		t.Fatal(err)
	}
}

func seedAccess(t *testing.T) *Service {
	t.Helper()
	svc := New(index.NewMemory(), &bagEmbedder{}, denyType{typ: "docs@Secret"}, quietLog())
	svc.Access = fakeAccess{
		projects: map[string][]string{"admin": {"*"}, "alice": {"P1", "P1.1"}, "bob": {"P2"}, "carol": {}},
		subs:     map[string][]string{"P1": {"P1", "P1.1"}},
	}
	for _, n := range []domain.NodeEvent{
		{ID: "n1", Version: 1, Namespace: "docs", Type: "docs@Requirement", Key: "REQ-1", Branch: "main", Project: "P1", Text: map[string]string{"title": "password reset"}},
		{ID: "n2", Version: 1, Namespace: "docs", Type: "docs@Requirement", Key: "REQ-2", Branch: "main", Project: "P2", Text: map[string]string{"title": "password policy"}},
		{ID: "n3", Version: 1, Namespace: "docs", Type: "docs@Requirement", Key: "REQ-3", Branch: "main", Project: "P1.1", Text: map[string]string{"title": "password vault"}},
		{ID: "n4", Version: 1, Namespace: "docs", Type: "docs@Secret", Key: "SEC-1", Branch: "main", Project: "P1", Text: map[string]string{"title": "password"}},
		{ID: "n5", Version: 1, Namespace: "docs", Type: "docs@Requirement", Key: "REQ-5", Branch: "main", Text: map[string]string{"title": "password no project"}},
	} {
		publish(t, svc, fmt.Sprintf(domain.SubjectNodeWritten, n.Namespace, n.Type, n.ID), n)
	}
	for _, c := range []domain.ChangeDocEvent{
		{ID: "C1", Title: "password change", ProjectID: "P1", OwnerOrg: "ORG-A", Status: domain.ChangeActive, Namespace: "docs"},
		{ID: "C2", Title: "password change", ProjectID: "P2", OwnerOrg: "ORG-B", Status: domain.ChangeActive, Namespace: "docs"},
		{ID: "C3", Title: "password change", ProjectID: "P1", OwnerOrg: "USR:alice", Status: domain.ChangeDraft, Namespace: "docs"},
	} {
		publish(t, svc, fmt.Sprintf(domain.SubjectChangeIndexed, c.ID), c)
	}
	return svc
}

func as(subject string) context.Context {
	return authz.With(context.Background(), authz.Principal{Subject: subject, Roles: []string{"contributor"}})
}

func TestResultsAreAuthorized(t *testing.T) {
	svc := seedAccess(t)
	all := index.Filter{Kind: []string{index.KindNode, index.KindChange}}
	facets := []string{index.FacetProject, index.FacetKind, index.FacetStatus}
	ids := func(who context.Context) (string, index.Result) {
		r, err := svc.Searcher.Search(who, index.Query{Text: "password", Mode: index.ModeLexical, Filter: all, Facets: facets, Limit: 50, Snippet: true})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range r.Hits {
			out = append(out, string(h.ID))
		}
		slices.Sort(out)
		return fmt.Sprint(out), r
	}
	for who, want := range map[string]string{
		// an internal caller has no identity and sees all
		"":      "[C1 C2 C3 n1 n2 n3 n4 n5]",
		"admin": "[C1 C2 n1 n2 n3 n5]", // administrators see every project, not the personal changes of others, and not what the policy denies
		"alice": "[C1 C3 n1 n3 n5]",    // P1 and P1.1, her personal change
		"bob":   "[C2 n2 n5]",
		"carol": "[n5]", // no project: only what no project holds
	} {
		ctx := context.Background()
		if who != "" {
			ctx = as(who)
		}
		got, r := ids(ctx)
		if got != want {
			t.Errorf("%q: got %s want %s", who, got, want)
		}
		if r.Total != len(r.Hits) {
			t.Errorf("%q: total %d for %d hits", who, r.Total, len(r.Hits))
		}
		// nothing is counted or quoted for a document the caller cannot see
		sum := 0
		for _, c := range r.Facets[index.FacetKind] {
			sum += c.Count
		}
		if sum != r.Total {
			t.Errorf("%q: kind facet counts %d for %d hits", who, sum, r.Total)
		}
		for _, c := range r.Facets[index.FacetProject] {
			switch who {
			case "bob":
				if c.Value != "P2" {
					t.Errorf("bob sees the facet %v", c)
				}
			case "carol":
				t.Errorf("carol sees the facet %v", c)
			}
		}
	}
	// similar_to does not reveal what the caller cannot see
	if _, err := svc.Searcher.Search(as("bob"), index.Query{SimilarTo: &index.Ref{Kind: index.KindChange, ID: "C3"}, Filter: all}); err == nil {
		t.Fatal("bob must not find the personal change of alice")
	}
}

func TestSearchHandler(t *testing.T) {
	svc := seedAccess(t)
	h := &Handler{Service: svc, Identity: identity.Extractor{}}
	call := func(subject string, m *indexv1.SearchRequest) (*indexv1.SearchResponse, error) {
		req := connect.NewRequest(m)
		if subject != "" {
			req.Header().Set(identity.HeaderSubject, subject)
		}
		r, err := h.Search(context.Background(), req)
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	// a project and its sub-projects, resolved by the server
	r, err := call("alice", &indexv1.SearchRequest{Text: "password", Mode: "lexical", Kinds: []string{"node"}, Projects: []string{"P1"}, IncludeSubprojects: true})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, x := range r.Hits {
		got = append(got, x.Key)
	}
	slices.Sort(got)
	if fmt.Sprint(got) != "[REQ-1 REQ-3]" {
		t.Fatalf("with sub-projects: %v", got)
	}
	r, _ = call("alice", &indexv1.SearchRequest{Text: "password", Mode: "lexical", Kinds: []string{"node"}, Projects: []string{"P1"}})
	if r.Total != 1 || r.Hits[0].Key != "REQ-1" || r.Hits[0].Project != "P1" {
		t.Fatalf("exact project: %+v", r)
	}
	// the hit of a change
	r, err = call("bob", &indexv1.SearchRequest{Text: "password", Mode: "lexical", Kinds: []string{"change"}, Statuses: []string{"active"}, RootsOnly: true, Snippet: true})
	if err != nil || r.Total != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	x := r.Hits[0]
	if x.Kind != "change" || x.Change == nil || x.Change.ChangeId != "C2" || x.Change.Title != "password change" || x.Change.Status != "active" || x.Change.ProjectId != "P2" ||
		x.Change.OwnerOrg != "ORG-B" || x.Snippet == "" {
		t.Fatalf("hit %+v", x)
	}
	// errors are codes
	for name, c := range map[string]struct {
		m    *indexv1.SearchRequest
		code connect.Code
	}{
		"kind":        {&indexv1.SearchRequest{Kinds: []string{"file"}}, connect.CodeInvalidArgument},
		"mode":        {&indexv1.SearchRequest{Text: "x", Mode: "fuzzy"}, connect.CodeInvalidArgument},
		"floor":       {&indexv1.SearchRequest{Text: "x", MinSimilarity: ptr(2.0)}, connect.CodeInvalidArgument},
		"similar_id":  {&indexv1.SearchRequest{SimilarTo: &indexv1.DocumentRef{Kind: "node"}}, connect.CodeInvalidArgument},
		"unknown doc": {&indexv1.SearchRequest{SimilarTo: &indexv1.DocumentRef{Kind: "node", Id: "nope"}}, connect.CodeNotFound},
	} {
		if _, err := call("alice", c.m); connect.CodeOf(err) != c.code {
			t.Errorf("%s: %v", name, err)
		}
	}
	// semantic search without an embedding model is a failed precondition; hybrid still answers
	text := New(index.NewMemory(), nil, nil, quietLog())
	th := &Handler{Service: text, Identity: identity.Extractor{}}
	if _, err := th.Search(context.Background(), connect.NewRequest(&indexv1.SearchRequest{Text: "x", Mode: "semantic"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("semantic: %v", err)
	}
	res, err := th.Search(context.Background(), connect.NewRequest(&indexv1.SearchRequest{Text: "x"}))
	if err != nil || res.Msg.Semantic {
		t.Fatalf("hybrid degrades: %v %+v", err, res)
	}
	// similar_to by the document of a node
	sim, err := call("", &indexv1.SearchRequest{SimilarTo: &indexv1.DocumentRef{Kind: "node", Id: "n1"}, Kinds: []string{"node", "change"}, MinSimilarity: ptr(0.0)})
	if err != nil || len(sim.Hits) == 0 {
		t.Fatalf("%v %+v", err, sim)
	}
	for _, x := range sim.Hits {
		if x.Id == "n1" && x.Kind == "node" {
			t.Fatal("excludes itself")
		}
	}
}

func ptr[T any](v T) *T { return &v }
