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

	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
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
    states: [{name: draft, editable: true}, {name: released}]
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
	req, err := g.CreateNode(ctx, graph.NewNode{Namespace: "docs", Key: "REQ-1", Type: "docs@Req", Properties: map[string]any{"title": "a"}, State: "released"})
	if err != nil {
		t.Fatal(err)
	}
	note, _ := g.CreateNode(ctx, graph.NewNode{Namespace: "docs", Key: "N-1", Type: "docs@Note"})
	base, err := g.BranchHead(ctx, "docs", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	// the bypass is closed for lifecycle types, open for the others
	if _, err := h.CreateNode(ctx, connect.NewRequest(&graphv1.CreateNodeRequest{Namespace: "docs", Key: "REQ-2", Type: "docs@Req"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("CreateNode on a lifecycle type: %v", err)
	}
	if _, err := h.UpdateNode(ctx, connect.NewRequest(&graphv1.UpdateNodeRequest{Base: pbconv.RefToPB(req.Ref()), Props: pbconv.Struct(map[string]any{"title": "b"})})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("UpdateNode on a lifecycle type: %v", err)
	}
	if _, err := h.CreateLink(ctx, connect.NewRequest(&graphv1.CreateLinkRequest{Type: "x", From: pbconv.RefToPB(req.Ref()), To: pbconv.RefToPB(note.Ref())})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("CreateLink from a lifecycle node: %v", err)
	}
	if _, err := h.CreateNode(ctx, connect.NewRequest(&graphv1.CreateNodeRequest{Namespace: "docs", Key: "N-2", Type: "docs@Note"})); err != nil {
		t.Errorf("a type without lifecycle stays writable: %v", err)
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
	ar := connect.NewRequest(&graphv1.AddChangeImpactsRequest{ChangeId: string(c.ID),
		Nodes: []*graphv1.ChangeImpact{{Intent: "modified", Pre: pbconv.RefToPB(ref), Rationale: "release"}}})
	withRoles(ar.Header(), "contributor")
	added, err := h.AddChangeImpacts(ctx, ar)
	if err != nil {
		t.Fatalf("anyone may propose the release: %v", err)
	}
	cnID := added.Msg.Nodes[0].Id
	write := func(props map[string]any, state string) {
		t.Helper()
		wr := connect.NewRequest(&graphv1.WriteChangeImpactRequest{ChangeId: string(c.ID), ChangeImpactId: cnID, Props: pbconv.Struct(props), State: state})
		withRoles(wr.Header(), "contributor")
		if _, err := h.WriteChangeImpact(ctx, wr); err != nil {
			t.Fatalf("a contributor may reopen, edit and propose the release: %v", err)
		}
	}
	write(nil, "draft")
	write(map[string]any{"title": "b"}, "")
	write(nil, "released")
	rv := connect.NewRequest(&graphv1.ReviewChangeImpactRequest{ChangeId: string(c.ID), ChangeImpactId: cnID, Accept: true, Comment: "ok"})
	withRoles(rv.Header(), "contributor")
	if _, err := h.ReviewChangeImpact(ctx, rv); err != nil {
		t.Fatal(err)
	}
	// the transition is authorized for whoever applies the change
	r := connect.NewRequest(&graphv1.ApplyChangeRequest{ChangeId: string(c.ID)})
	withRoles(r.Header(), "contributor")
	if _, err := h.ApplyChange(ctx, r); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a contributor lacks requirement:release: %v", err)
	}
	r = connect.NewRequest(&graphv1.ApplyChangeRequest{ChangeId: string(c.ID)})
	withRoles(r.Header(), "admin")
	if _, err := h.ApplyChange(ctx, r); err != nil {
		t.Fatalf("admin may release: %v", err)
	}
	if n, err := g.NodeByKey(ctx, "docs", "REQ-1"); err != nil || n.State != "released" || n.Version != 4 || n.Properties["title"] != "b" {
		t.Fatalf("REQ-1: %+v %v", n, err)
	}
}
