package graphsvc_test

import (
	"context"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
)

// A service acting for a person reads as that person; a person, or a service acting by itself, is itself; the reach
// of a person is its projects and the open namespaces, a service by itself and an in-process call read everything
// (ADR 0161 §5).
func TestReachOf(t *testing.T) {
	agent := authz.System("engine").For("ada")
	if r := graphsvc.Reader(agent); r.Subject != "ada" || r.System() {
		t.Fatalf("a service acting for ada reads as %+v", r)
	}
	if r := graphsvc.Reader(authz.System("indexer")); r.Subject != "system:indexer" {
		t.Fatalf("a service by itself reads as %+v", r)
	}
	asked := ""
	reachOf := graphsvc.ReachOf(func(_ context.Context, p authz.Principal) ([]string, error) {
		asked = p.Subject
		return []string{"PROJ-A"}, nil
	})
	r, err := reachOf(authz.With(context.Background(), agent))
	if err != nil {
		t.Fatal(err)
	}
	if asked != "ada" || r == nil || !r.Projects["PROJ-A"] || len(r.Projects) != 1 {
		t.Fatalf("reach for ada: %+v (asked for %q)", r, asked)
	}
	org := domain.Node{Namespace: access.NamespaceOrganisation}
	if !r.Sees(org) || r.Sees(domain.Node{Namespace: "alm"}) {
		t.Fatal("the organisation is open, a node of no project of the content is not")
	}
	for _, ctx := range []context.Context{authz.With(context.Background(), authz.System("indexer")), context.Background()} {
		if r, err := reachOf(ctx); r != nil || err != nil {
			t.Fatalf("a service by itself or an in-process call: %+v, %v, want everything", r, err)
		}
	}
	if graphsvc.OwnedWrites(access.NamespaceOrganisation) || graphsvc.OwnedWrites(domain.NamespacePlatform) || !graphsvc.OwnedWrites("alm") {
		t.Fatal("OwnedWrites: the open namespaces are administered by the platform, the others owned by their projects")
	}
}
