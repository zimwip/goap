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
	req := connect.NewRequest(&graphv1.CreateNodeRequest{ChangeId: string(c.ID), Type: typ, Key: key, Rationale: "new"})
	if roles != "-" {
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
	}
	out, err := h.CreateNode(ctx, req)
	if err != nil {
		return nil, err
	}
	return out.Msg.Node, nil
}

// Creating a node in a change asks object:create on the project of the change (ADR 0076: no write outside a change).
func TestCreateNodeIsRoleGated(t *testing.T) {
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
		req := connect.NewRequest(&graphv1.AddChangeImpactsRequest{ChangeId: string(c.ID),
			Nodes: []*graphv1.ChangeImpact{{Intent: "created", Key: "USR:x", Type: access.NodeTypeUser, Rationale: "why"}}})
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
		_, err = h.AddChangeImpacts(ctx, req)
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
	req := connect.NewRequest(&graphv1.CreateNodeRequest{ChangeId: string(c.ID), Key: "POL:x", Type: access.NodeTypePolicy, Rationale: "why"})
	req.Header().Set(identity.HeaderSubject, "u")
	req.Header().Set(identity.HeaderOrg, "acme")
	req.Header().Set(identity.HeaderRoles, "methodologist")
	if _, err := h.CreateNode(ctx, req); connect.CodeOf(err) != connect.CodePermissionDenied {
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
		req := connect.NewRequest(&graphv1.CreateNodeRequest{ChangeId: string(c.ID), Key: "S-" + roles, Type: "vault@Secret", Rationale: "why"})
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
		if _, err := h.CreateNode(ctx, req); (err == nil) != (want == 0) || err != nil && connect.CodeOf(err) != want {
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
		req := connect.NewRequest(&graphv1.AddChangeImpactsRequest{ChangeId: string(c.ID),
			Nodes: []*graphv1.ChangeImpact{{Intent: "modified", Pre: pbconv.RefToPB(pre), Rationale: "why"}}})
		as(roles, req)
		out, err := h.AddChangeImpacts(ctx, req)
		if err != nil {
			return nil, err
		}
		return out.Msg.Nodes[0], nil
	}

	cn, err := add("contributor", req1.Ref())
	if err != nil {
		t.Fatal(err)
	}
	co := connect.NewRequest(&graphv1.CheckoutNodeRequest{ChangeId: string(c.ID), ChangeImpactId: cn.Id})
	as("contributor", co)
	if out, err := h.CheckoutNode(ctx, co); err != nil || out.Msg.Node.Post == nil || out.Msg.Node.Post.Version != 2 {
		t.Fatalf("checkout: %v %v", out, err)
	}
	props, _ := structpb.NewStruct(map[string]any{"title": "two"})
	up := connect.NewRequest(&graphv1.UpdateNodeRequest{ChangeId: string(c.ID), ChangeImpactId: cn.Id, Props: props})
	as("contributor", up)
	if out, err := h.UpdateNode(ctx, up); err != nil || out.Msg.Node.Post == nil || out.Msg.Node.Post.Version != 2 {
		t.Fatalf("update in place: %v %v", out, err)
	}
	rv := connect.NewRequest(&graphv1.ReviewChangeImpactRequest{ChangeId: string(c.ID), ChangeImpactId: cn.Id, Accept: true})
	as("contributor", rv)
	if _, err := h.ReviewChangeImpact(ctx, rv); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a review without comment must be refused: %v", err)
	}
	rv.Msg.Comment = "checked"
	if out, err := h.ReviewChangeImpact(ctx, rv); err != nil || out.Msg.Node.Review != "accepted" || out.Msg.Node.Reviews[0].By != "u" {
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
	ci := connect.NewRequest(&graphv1.CheckinNodeRequest{ChangeId: string(c.ID), ChangeImpactId: cn.Id})
	as("contributor", ci)
	if _, err := h.CheckinNode(ctx, ci); err != nil {
		t.Fatalf("check-in: %v", err)
	}
	log, err = h.ListChangeEvents(ctx, connect.NewRequest(&graphv1.ListChangeEventsRequest{ChangeId: string(c.ID)}))
	if err != nil {
		t.Fatal(err)
	}
	ops = ops[:0]
	for _, e := range log.Msg.Events {
		ops = append(ops, e.Op)
	}
	if strings.Join(ops, ",") != "declared,written,updated,reviewed,checkedIn" {
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
