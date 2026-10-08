package indexersvc

import (
	"context"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/index"
)

// The documents of the requests follow the graph (ADR 0098): created, triaged by a link (they take the project of the
// change), delivered when their change is applied; an untriaged request is found by its requester and the triagers only.
func TestRequestsFollowTheGraph(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(index.NewMemory(), &bagEmbedder{}, authorizer, quietLog())
	g := graph.New(graph.NewMemory())
	g.Observe(NewSink(ctx, svc))
	root := g.Structure(domain.StructureProject).Root
	r, err := g.CreateRequest(ctx, graph.NewRequest{Title: "Faster checkout", Text: "the payment page is slow", Requester: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	q := func(text string, f index.Filter) index.Query {
		f.Kind = []string{index.KindRequest}
		return index.Query{Text: text, Mode: index.ModeLexical, Filter: f}
	}
	h := waitFor(t, svc, ctx, q("payment", index.Filter{}), 1).Hits[0]
	if h.ID != domain.NodeID(r.ID) || h.Title != "Faster checkout" || h.Owner != "alice" || h.Status != string(domain.RequestOpen) || h.Project != "" {
		t.Fatalf("hit %+v", h)
	}
	as := func(subject string, roles ...string) context.Context {
		return authz.With(ctx, authz.Principal{Subject: subject, Org: "acme", Roles: roles})
	}
	search := func(c context.Context) int {
		res, err := svc.Searcher.Search(c, q("payment", index.Filter{}))
		if err != nil {
			t.Fatal(err)
		}
		return res.Total
	}
	if search(as("alice")) != 1 || search(as("bob", "developer")) != 0 || search(as("tom", "triage")) != 1 {
		t.Fatal("an untriaged request is found by its requester and the triagers only")
	}

	// linked: triaged in the project of the change
	head, err := g.BranchHead(ctx, "", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "c", BaselineID: head.ID, ProjectID: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.LinkRequest(ctx, r.ID, c.ID, domain.LinkOrigin); err != nil {
		t.Fatal(err)
	}
	waitFor(t, svc, ctx, q("payment", index.Filter{Project: []string{root}, Status: []string{string(domain.RequestTriaged)}}), 1)
	if search(as("bob", "developer")) != 1 {
		t.Fatal("a member of the project finds a triaged request")
	}
	// delivered with its change
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, svc, ctx, q("payment", index.Filter{Status: []string{string(domain.RequestDelivered)}}), 1)

	// a reindex publishes the requests again
	svc.Republish = func(ctx context.Context) (int, error) { return g.Republish(ctx, NewSink(ctx, svc)) }
	if _, err := svc.Reindex(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, svc, ctx, q("payment", index.Filter{}), 1)
}
