package graphsvc_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

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

// createObject creates a node of a type in a change of its own, as a caller with roles ("-": anonymous).
func createObject(h *graphsvc.Handler, roles, typ, key string) (*graphv1.ChangeImpact, error) {
	ctx := context.Background()
	head, err := h.Graph.BranchHead(ctx, "alm", domain.MainBranch)
	if err != nil {
		return nil, err
	}
	c, err := h.Graph.CreateChange(ctx, graph.NewChange{Namespace: "alm", Title: "create " + key, BaselineID: head.ID})
	if err != nil {
		return nil, err
	}
	req := connect.NewRequest(&graphv1.ImpactNodeCreateRequest{ChangeId: string(c.ID), Type: typ, Key: key, Rationale: "new"})
	if roles != "-" {
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
	}
	out, err := h.ImpactNodeCreate(ctx, req)
	if err != nil {
		return nil, err
	}
	return out.Msg.Node, nil
}

// Creating a node in a change asks object:create on the project of the change (ADR 0076: no write outside a change).
func TestImpactNodeCreateIsRoleGated(t *testing.T) {
	ds, err := def.LoadDomains("../../domains")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
	if _, err := g.BranchHead(context.Background(), "alm", domain.MainBranch); err != nil {
		t.Fatal(err)
	}
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g, Authz: authorizer}

	for _, tc := range []struct {
		name, roles, key string
		want             connect.Code
	}{
		{"anonymous", "-", "REQ-1", connect.CodePermissionDenied},
		{"not on the project", "", "REQ-2", connect.CodePermissionDenied},
		{"member of the project", "developer", "REQ-3", 0},
		{"another role on the project", "tester", "REQ-4", 0},
		{"admin", "admin", "REQ-5", 0},
	} {
		cn, err := createObject(h, tc.roles, "alm@Need", tc.key)
		if got := connect.CodeOf(err); err != nil && got != tc.want || err == nil && tc.want != 0 {
			t.Errorf("%s: err=%v, want code %v", tc.name, err, tc.want)
		}
		if err == nil && (cn.Key != tc.key || cn.Type != "alm@Need" || cn.Post == nil) {
			t.Errorf("%s: the node is created checked out in the change: %+v", tc.name, cn)
		}
	}
	// a denied caller created nothing
	if _, err := g.NodeByKey(context.Background(), "alm", "REQ-2"); err == nil {
		t.Error("REQ-2 must not exist")
	}
	// the type catalogue judges it
	if _, err := createObject(h, "admin", "alm@Nope", "X-1"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown type: %v", err)
	}
}

func TestAccessNodesAreGatedByTheFloor(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	floor, err := authz.NewCasbinWith(authz.FloorPolicies)
	if err != nil {
		t.Fatal(err)
	}
	// the overall authorizer lets methodologists do everything on policies, the floor only administrators
	h := &graphsvc.Handler{Graph: g, Authz: authorizer, Floor: floor}
	base, err := g.BranchHead(ctx, "", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	add := func(roles string) error {
		c, err := g.CreateChange(ctx, graph.NewChange{Title: "t", BaselineID: base.ID})
		if err != nil {
			t.Fatal(err)
		}
		req := connect.NewRequest(&graphv1.ImpactNodeCreateRequest{ChangeId: string(c.ID), Key: "USR:x", Type: access.NodeTypeUser, Rationale: "why"})
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
		_, err = h.ImpactNodeCreate(ctx, req)
		return err
	}
	if err := add("methodologist"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("only administrators may change users: %v", err)
	}
	if err := add("admin"); err != nil {
		t.Errorf("admin must: %v", err)
	}
	// creating an access node in a change: administrators only
	orgBase, err := g.BranchHead(ctx, "organisation", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Namespace: "organisation", Title: "policy", BaselineID: orgBase.ID})
	if err != nil {
		t.Fatal(err)
	}
	req := connect.NewRequest(&graphv1.ImpactNodeCreateRequest{ChangeId: string(c.ID), Key: "POL:x", Type: access.NodeTypePolicy, Rationale: "why"})
	req.Header().Set(identity.HeaderSubject, "u")
	req.Header().Set(identity.HeaderOrg, "acme")
	req.Header().Set(identity.HeaderRoles, "methodologist")
	if _, err := h.ImpactNodeCreate(ctx, req); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a Policy node created by a methodologist: %v", err)
	}
}

// A type a domain flags `adminOnly` is gated by the handler with no code naming it (ADR 0068): creating one needs the
// floor, and the RPC the connectors of a distributed deployment ask answers.
func TestAdminOnlyTypeOfADomainIsGated(t *testing.T) {
	ctx := context.Background()
	d, err := def.ParseDomain([]byte("name: vault\nversion: 1.0.0\nnodeTypes:\n  - {name: Secret, adminOnly: true, changeControlled: false}\n  - {name: Note, changeControlled: false}\n"))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(d)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
	floor, err := authz.NewCasbinWith(authz.FloorPolicies)
	if err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g, Floor: floor}
	for typ, want := range map[string]bool{"vault@Secret": true, "vault@Note": false} {
		r, err := h.IsAdminOnlyType(ctx, connect.NewRequest(&graphv1.IsAdminOnlyTypeRequest{Type: typ}))
		if err != nil || r.Msg.AdminOnly != want {
			t.Errorf("IsAdminOnlyType(%s) = %v, %v; want %v", typ, r, err, want)
		}
	}
	base, err := g.BranchHead(ctx, "vault", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Namespace: "vault", Title: "secret", BaselineID: base.ID})
	if err != nil {
		t.Fatal(err)
	}
	for roles, want := range map[string]connect.Code{"contributor": connect.CodePermissionDenied, "admin": 0} {
		req := connect.NewRequest(&graphv1.ImpactNodeCreateRequest{ChangeId: string(c.ID), Key: "S-" + roles, Type: "vault@Secret", Rationale: "why"})
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
		if _, err := h.ImpactNodeCreate(ctx, req); (err == nil) != (want == 0) || err != nil && connect.CodeOf(err) != want {
			t.Errorf("%s creating a flagged type: %v", roles, err)
		}
	}
}

func TestChangeImpactRPCs(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	floor, err := authz.NewCasbinWith(authz.FloorPolicies)
	if err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g, Authz: authorizer, Floor: floor}
	g.Caller = graphsvc.Caller
	req1, err := graphtest.Import(ctx, g, graphtest.Node{Key: "REQ-1", Type: "Requirement", Properties: map[string]any{"title": "one"}})
	if err != nil {
		t.Fatal(err)
	}
	base, err := g.BranchHead(ctx, "", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "t", BaselineID: base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	as := func(roles string, r interface{ Header() http.Header }) {
		r.Header().Set(identity.HeaderSubject, "u")
		r.Header().Set(identity.HeaderOrg, "acme")
		r.Header().Set(identity.HeaderRoles, roles)
	}
	add := func(roles string, pre domain.NodeRef) (*graphv1.ChangeImpact, error) {
		req := connect.NewRequest(&graphv1.ProposeImpactRequest{ChangeId: string(c.ID),
			Nodes: []*graphv1.ChangeImpact{{Intent: "modified", Pre: pbconv.RefToPB(pre), Rationale: "why"}}})
		as(roles, req)
		out, err := h.ProposeImpact(ctx, req)
		if err != nil {
			return nil, err
		}
		return out.Msg.Nodes[0], nil
	}

	cn, err := add("contributor", req1.Ref())
	if err != nil {
		t.Fatal(err)
	}
	co := connect.NewRequest(&graphv1.ImpactNodeCheckoutRequest{ChangeId: string(c.ID), ChangeImpactId: cn.Id})
	as("contributor", co)
	if out, err := h.ImpactNodeCheckout(ctx, co); err != nil || out.Msg.Node.Post == nil || out.Msg.Node.Post.Version != 0 {
		t.Fatalf("checkout: %v %v", out, err)
	}
	props, _ := structpb.NewStruct(map[string]any{"title": "two"})
	up := connect.NewRequest(&graphv1.ImpactNodeUpdateRequest{ChangeId: string(c.ID), ChangeImpactId: cn.Id, Props: props})
	as("contributor", up)
	if out, err := h.ImpactNodeUpdate(ctx, up); err != nil || out.Msg.Node.Post == nil || out.Msg.Node.Post.Version != 0 {
		t.Fatalf("update the draft: %v %v", out, err)
	}
	rv := connect.NewRequest(&graphv1.ImpactNodeReviewRequest{ChangeId: string(c.ID), ChangeImpactId: cn.Id, Accept: true})
	as("contributor", rv)
	if _, err := h.ImpactNodeReview(ctx, rv); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a review without comment must be refused: %v", err)
	}
	rv.Msg.Comment = "checked"
	if out, err := h.ImpactNodeReview(ctx, rv); err != nil || out.Msg.Node.Review != "accepted" || out.Msg.Node.Reviews[0].By != "u" {
		t.Fatalf("review: %v %v", out, err)
	}
	get, err := h.GetChange(ctx, connect.NewRequest(&graphv1.GetChangeRequest{Id: string(c.ID)}))
	if err != nil || len(get.Msg.Change.Nodes) != 1 || get.Msg.Change.Nodes[0].Review != "accepted" {
		t.Fatalf("GetChange carries the change impacts: %v %v", get, err)
	}
	// every operation is an event of the impact log, with its caller (ADR 0029)
	log, err := h.ListChangeEvents(ctx, connect.NewRequest(&graphv1.ListChangeEventsRequest{ChangeId: string(c.ID)}))
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, e := range log.Msg.Events {
		ops = append(ops, e.Op)
		if e.By != "u" || e.ImpactId != cn.Id {
			t.Fatalf("event %s: by %q, impact %s", e.Op, e.By, e.ImpactId)
		}
	}
	vs, verr := g.Versions(ctx, req1.ID)
	if n, err := g.ChangeNode(ctx, c.ID, "", domain.NodeRef{ID: req1.ID}); err != nil || !n.IsDraft() || verr != nil || len(vs) != 1 {
		t.Fatalf("the accepted review leaves a draft, no version, until the change lands: %+v %v %v", n, err, verr)
	}
	log, err = h.ListChangeEvents(ctx, connect.NewRequest(&graphv1.ListChangeEventsRequest{ChangeId: string(c.ID)}))
	if err != nil {
		t.Fatal(err)
	}
	ops = ops[:0]
	for _, e := range log.Msg.Events {
		ops = append(ops, e.Op)
	}
	if strings.Join(ops, ",") != "proposed,checkedOut,updated,reviewed" {
		t.Fatalf("impact log = %v", ops)
	}
}

func TestCommitEditsGatesAccessNodes(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g, Authz: authorizer}
	base, err := g.BranchHead(ctx, "", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	commit := func(roles, typ string) (*graphv1.CommitEditsResponse, error) {
		req := connect.NewRequest(&graphv1.CommitEditsRequest{Title: "t", BaselineId: string(base.ID),
			Edits: []*graphv1.NodeEdit{{Key: "K-" + roles, Type: typ, Rationale: "why"}}})
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
		out, err := h.CommitEdits(ctx, req)
		if err != nil {
			return nil, err
		}
		return out.Msg, nil
	}
	if _, err := commit("contributor", access.NodeTypePolicy); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a contributor must not commit policies: %v", err)
	}
	out, err := commit("admin", access.NodeTypePolicy)
	if err != nil || out.ChangeId == "" || out.Baseline.GetId() == "" {
		t.Fatalf("an administrator may: %v %v", out, err)
	}
	if _, err := commit("contributor", "alm@Need"); err != nil {
		t.Fatalf("ordinary nodes are not gated: %v", err)
	}
	if n, err := g.NodeByKey(ctx, "", "K-admin"); err != nil || n.ChangeID != domain.ChangeID(out.ChangeId) || n.Comment != "why" {
		t.Fatalf("committed node: %+v %v", n, err)
	}
	// regression: organisation/project structure and Assignment must be gated too (a platform Assignment
	// grants the "admin" platform role, ADR 0046/0047 — an ungated Assignment write is privilege escalation
	// to full administrator, not just a stray node)
	for _, typ := range []string{access.NodeTypeAssignment, access.NodeTypeProjectUnit, access.NodeTypeOrgUnit, domain.TypeAdapter} {
		if _, err := commit("contributor", typ); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("a contributor must not commit a %s node: %v", typ, err)
		}
	}
}

// A merge modifies the parents it discovers: the gate of the access nodes stands in front of them too (ADR 0077).
func TestMergeIsGatedOnTheParents(t *testing.T) {
	ctx := context.Background()
	d, err := def.ParseDomain([]byte(`
name: vault
version: 1.0.0
nodeTypes:
  - {name: Safe, adminOnly: true, changeControlled: false}
  - {name: Entry, changeControlled: false}
linkTypes:
  - {name: holds, from: Safe, to: Entry, compose: true}
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
	var links []graph.LinkWrite
	for _, k := range []string{"E1", "E2"} {
		n, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "vault", Key: k, Type: "vault@Entry"})
		if err != nil {
			t.Fatal(err)
		}
		links = append(links, graph.LinkWrite{Type: "vault@holds", To: n.Ref()})
	}
	if _, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "vault", Key: "S", Type: "vault@Safe", Links: links}); err != nil {
		t.Fatal(err)
	}
	floor, err := authz.NewCasbinWith(authz.FloorPolicies)
	if err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g, Floor: floor}
	base, err := g.BranchHead(ctx, "vault", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	for roles, want := range map[string]connect.Code{"contributor": connect.CodePermissionDenied, "admin": 0} {
		c, err := g.CreateChange(ctx, graph.NewChange{Namespace: "vault", Title: "merge " + roles, BaselineID: base.ID, OwnBranch: true})
		if err != nil {
			t.Fatal(err)
		}
		req := connect.NewRequest(&graphv1.ImpactNodeMergeRequest{ChangeId: string(c.ID), Sources: []*graphv1.NodeName{{Key: "E1"}, {Key: "E2"}},
			Into: &graphv1.NodeCreateSpec{Key: "E-" + roles, Type: "vault@Entry", Rationale: "one entry"}})
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
		out, err := h.ImpactNodeMerge(ctx, req)
		if (err == nil) != (want == 0) || err != nil && connect.CodeOf(err) != want {
			t.Fatalf("%s merging the entries of a safe: %v", roles, err)
		}
		if err == nil && (len(out.Msg.Parents) != 1 || out.Msg.Parents[0].Key != "S" || len(out.Msg.Successors) != 1 || len(out.Msg.Sources) != 2) {
			t.Errorf("result: %+v", out.Msg)
		}
		if err != nil {
			if got, _ := g.ListChangeImpacts(ctx, c.ID); len(got) != 0 {
				t.Errorf("a refused merge left %d impacts", len(got))
			}
		}
	}
	// nothing derives from E1 until the change lands: the successor is a draft (ADR 0079)
	r, err := h.DerivedNodes(ctx, connect.NewRequest(&graphv1.DerivedNodesRequest{Ref: &graphv1.NodeRef{Id: string(must(t, g, "E1").ID)}}))
	if err != nil || len(r.Msg.Nodes) != 0 {
		t.Errorf("DerivedNodes: %v, %v", r, err)
	}
}

func must(t *testing.T, g *graph.Graph, key string) domain.Node {
	t.Helper()
	n, err := g.NodeByKey(context.Background(), "vault", key)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
