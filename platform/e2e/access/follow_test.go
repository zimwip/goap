package accesse2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/graphsnap"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/typecat"
)

// TestPoliciesFollowThePlatformEvents (ADR 0154 §9): a following directory keeps the policies it read until a commit
// event of the organisation says they moved; the graph's own events, delivered by its outbox, refresh it at the commit.
func TestPoliciesFollowThePlatformEvents(t *testing.T) {
	ctx := context.Background()
	ds, err := def.LoadDomains("../../../domains")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
	dir := &access.Directory{Graph: g, Follow: &graphsnap.Heads{}}
	a, err := access.NewAuthorizer(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graphsvc.SeedAccess(ctx, g); err != nil {
		t.Fatal(err)
	}
	contrib := authz.Principal{Subject: "c", Org: "o", Roles: []string{"contributor"}}
	if can(t, a, contrib, "report", "read-all") {
		t.Fatal("unexpected access")
	}
	if err := graphsvc.SeedPolicy(ctx, g, authz.Policy{Rule: `hasRole(r.sub, "contributor")`, Resource: "report", Action: "read-all", Effect: "allow"}); err != nil {
		t.Fatal(err)
	}
	if can(t, a, contrib, "report", "read-all") {
		t.Fatal("no event yet: the policies read are kept")
	}
	moved := func(ns string) []byte {
		data, _ := json.Marshal(domain.CommitEvent{ID: "b", Namespace: ns, Branch: domain.MainBranch})
		return data
	}
	dir.Moved(moved("alm"))
	if can(t, a, contrib, "report", "read-all") {
		t.Fatal("another namespace moved: the policies read are kept")
	}
	dir.Moved(moved(access.NamespaceOrganisation))
	if !can(t, a, contrib, "report", "read-all") {
		t.Fatal("the organisation moved: the new policy applies")
	}

	// delivered by the graph's outbox, as an in-process composition does: the next commit refreshes it
	subject := fmt.Sprintf(domain.SubjectCommitAdvanced, domain.MainBranch)
	g.Observe(outbox.SinkFunc(func(_ context.Context, m outbox.Message) error {
		if m.Subject == subject {
			dir.Moved(m.Data)
		}
		return nil
	}))
	if err := graphsvc.SeedPolicy(ctx, g, authz.Policy{Rule: `hasRole(r.sub, "contributor")`, Resource: "report", Action: "export", Effect: "allow"}); err != nil {
		t.Fatal(err)
	}
	if !can(t, a, contrib, "report", "export") {
		t.Fatal("the event of the commit refreshed the policies")
	}
}
