package graphsvc_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/graph/graphtest"
	"github.com/zimwip/goap/pkg/typecat"
)

// A User's deactivate/reactivate transitions (ADR 0048) require the admin role, like any other
// permission-gated transition: the floor policy (hasRole(admin) -> "*","*") grants it, nobody else gets it by
// default.
func TestUserDeactivateReactivateRequireAdmin(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	g.Authorizer = graphsvc.TransitionAuthorizer(authorizer)

	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.EnsureUser(ctx, g, "carol"); err != nil {
		t.Fatal(err)
	}
	move := func(principal authz.Principal, title, state string) error {
		t.Helper()
		head, err := g.BranchHead(ctx, access.NamespaceOrganisation, domain.MainBranch)
		if err != nil {
			t.Fatal(err)
		}
		carol, err := g.NodeByKey(ctx, access.NamespaceOrganisation, access.UserKey("carol"))
		if err != nil {
			t.Fatal(err)
		}
		ref := carol.Ref()
		_, err = g.Commit(authz.With(ctx, principal), graph.Commit{Namespace: access.NamespaceOrganisation, Title: title, Baseline: head.ID,
			Edits: []graph.NodeEdit{{Pre: &ref, State: state, Rationale: "test"}}})
		return err
	}
	if err := move(authz.Principal{Subject: "mallory"}, "deactivate carol", "deactivated"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("a non-admin must not deactivate a user: %v", err)
	}
	if err := move(authz.Principal{Subject: "admin1", Roles: []string{"admin"}}, "deactivate carol", "deactivated"); err != nil {
		t.Fatalf("an admin may deactivate a user: %v", err)
	}
	if n, err := g.NodeByKey(ctx, access.NamespaceOrganisation, access.UserKey("carol")); err != nil || n.State != "deactivated" {
		t.Fatalf("carol should be deactivated: %+v %v", n, err)
	}
	if err := move(authz.Principal{Subject: "mallory"}, "reactivate carol", "active"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("a non-admin must not reactivate a user: %v", err)
	}
	if err := move(authz.Principal{Subject: "admin1", Roles: []string{"admin"}}, "reactivate carol", "active"); err != nil {
		t.Fatalf("an admin may reactivate a user: %v", err)
	}
	if n, err := g.NodeByKey(ctx, access.NamespaceOrganisation, access.UserKey("carol")); err != nil || n.State != "active" {
		t.Fatalf("carol should be active again: %+v %v", n, err)
	}
}

func TestLifecycleIsEnforcedByTheService(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	g.Authorizer = graphsvc.TransitionAuthorizer(authorizer)
	h := &graphsvc.Handler{Graph: g, Authz: authorizer}

	d, err := def.ParseDomain([]byte(`
name: docs
version: 1.0.0
lifecycles:
  - name: req
    initial: released
    states: [{name: draft, notLandable: true}, {name: released}]
    transitions:
      - {name: reopen, from: released, to: draft}
      - {name: release, from: draft, to: released, permission: "requirement:release"}
nodeTypes:
  - {name: Req, lifecycle: req, properties: [title]}
  - {name: Note}
`))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(d)
	if err != nil {
		t.Fatal(err)
	}
	g.Types = func() graph.TypeCatalog { return cat }
	req, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "docs", Key: "REQ-1", Type: "docs@Req", Properties: map[string]any{"title": "a"}, State: "released"})
	if err != nil {
		t.Fatal(err)
	}
	base, err := g.BranchHead(ctx, "docs", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	// reopen, edit, release: "release" needs requirement:release
	c, err := g.CreateChange(ctx, graph.NewChange{Namespace: "docs", Title: "t", BaselineID: base.ID})
	if err != nil {
		t.Fatal(err)
	}
	ref := req.Ref()
	withRoles := func(hdr interface{ Set(k, v string) }, roles string) {
		hdr.Set(identity.HeaderSubject, "u")
		hdr.Set(identity.HeaderOrg, "acme")
		hdr.Set(identity.HeaderRoles, roles)
	}
	ar := connect.NewRequest(&graphv1.ProposeImpactRequest{ChangeId: string(c.ID),
		Nodes: []*graphv1.ChangeImpact{{Intent: "modified", Pre: pbconv.RefToPB(ref), Rationale: "release"}}})
	withRoles(ar.Header(), "contributor")
	added, err := h.ProposeImpact(ctx, ar)
	if err != nil {
		t.Fatalf("anyone may propose the release: %v", err)
	}
	cnID := added.Msg.Nodes[0].Id
	move := func(roles, state string) error {
		r := connect.NewRequest(&graphv1.ImpactNodeTransitionRequest{ChangeId: string(c.ID), ChangeImpactId: cnID, State: state})
		withRoles(r.Header(), roles)
		_, err := h.ImpactNodeTransition(ctx, r)
		return err
	}
	// a transition is authorized for whoever takes it, when it is taken (ADR 0076)
	if err := move("contributor", "draft"); err != nil {
		t.Fatalf("a contributor may reopen: %v", err)
	}
	co := connect.NewRequest(&graphv1.ImpactNodeCheckoutRequest{ChangeId: string(c.ID), ChangeImpactId: cnID})
	withRoles(co.Header(), "contributor")
	if _, err := h.ImpactNodeCheckout(ctx, co); err != nil {
		t.Fatal(err)
	}
	up := connect.NewRequest(&graphv1.ImpactNodeUpdateRequest{ChangeId: string(c.ID), ChangeImpactId: cnID, Props: pbconv.Struct(map[string]any{"title": "b"})})
	withRoles(up.Header(), "contributor")
	if _, err := h.ImpactNodeUpdate(ctx, up); err != nil {
		t.Fatalf("a contributor may edit: %v", err)
	}
	rv := connect.NewRequest(&graphv1.ImpactNodeReviewRequest{ChangeId: string(c.ID), ChangeImpactId: cnID, Accept: true, Comment: "ok"})
	withRoles(rv.Header(), "contributor")
	if _, err := h.ImpactNodeReview(ctx, rv); err != nil {
		t.Fatal(err)
	}
	ci := connect.NewRequest(&graphv1.ImpactNodeCheckinRequest{ChangeId: string(c.ID), ChangeImpactId: cnID})
	withRoles(ci.Header(), "contributor")
	if _, err := h.ImpactNodeCheckin(ctx, ci); err != nil {
		t.Fatal(err)
	}
	if err := move("contributor", "released"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a contributor lacks requirement:release: %v", err)
	}
	if err := move("admin", "released"); err != nil {
		t.Fatalf("admin may release: %v", err)
	}
	r := connect.NewRequest(&graphv1.ApplyChangeRequest{ChangeId: string(c.ID)})
	withRoles(r.Header(), "contributor")
	if _, err := h.ApplyChange(ctx, r); err != nil {
		t.Fatal(err)
	}
	if n, err := g.NodeByKey(ctx, "docs", "REQ-1"); err != nil || n.State != "released" || n.Version != 4 || n.Properties["title"] != "b" {
		t.Fatalf("REQ-1: %+v %v", n, err)
	}
}
