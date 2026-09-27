package graphsvc_test

import (
	"context"
	"net/http"
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
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

func createObject(h *graphsvc.Handler, roles, key string) error {
	req := connect.NewRequest(&graphv1.CreateObjectRequest{Methodology: "test-design", NodeType: "alm@Need", Key: key})
	if roles != "" {
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
	}
	_, err := h.CreateObject(context.Background(), req)
	return err
}

func TestCreateObjectIsRoleGated(t *testing.T) {
	ds, err := methodology.LoadDomains("../../domains")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
	if _, err := g.CreateBaseline(context.Background(), "B0", nil); err != nil {
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
		{"anonymous", "", "REQ-1", connect.CodePermissionDenied},
		{"no role", "viewer", "REQ-2", connect.CodePermissionDenied},
		{"contributor", "contributor", "REQ-3", 0},
		{"methodologist", "methodologist", "REQ-4", 0},
		{"admin", "admin", "REQ-5", 0},
	} {
		err := createObject(h, tc.roles, tc.key)
		if got := connect.CodeOf(err); err != nil && got != tc.want || err == nil && tc.want != 0 {
			t.Errorf("%s: err=%v, want code %v", tc.name, err, tc.want)
		}
	}
	// a denied caller created nothing; the object lives in the namespace of its type
	if _, err := g.NodeByKey(context.Background(), "alm", "REQ-2"); err == nil {
		t.Error("REQ-2 must not exist")
	}
	if n, err := g.NodeByKey(context.Background(), "alm", "REQ-3"); err != nil || n.Type != "alm@Need" {
		t.Errorf("REQ-3 must exist: %+v %v", n, err)
	}
	// the type catalogue judges it
	if err := createObjectOf(h, "alm@Nope", "X-1"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown type: %v", err)
	}
}

func createObjectOf(h *graphsvc.Handler, typ, key string) error {
	req := connect.NewRequest(&graphv1.CreateObjectRequest{NodeType: typ, Key: key})
	req.Header().Set(identity.HeaderSubject, "u")
	req.Header().Set(identity.HeaderOrg, "acme")
	req.Header().Set(identity.HeaderRoles, "admin")
	_, err := h.CreateObject(context.Background(), req)
	return err
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
	base, err := g.CreateBaseline(ctx, "Repository", nil)
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
	// direct writes of access nodes are refused: they go through changes
	req := connect.NewRequest(&graphv1.CreateNodeRequest{Namespace: "organisation", Key: "POL:x", Type: access.NodeTypePolicy})
	if _, err := h.CreateNode(ctx, req); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("direct write of a Policy node: %v", err)
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
	req1, err := g.CreateNode(ctx, graph.NewNode{Key: "REQ-1", Type: "Requirement", Properties: map[string]any{"title": "one"}})
	if err != nil {
		t.Fatal(err)
	}
	base, err := g.CreateBaseline(ctx, "B1", []domain.NodeRef{req1.Ref()})
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
	props, _ := structpb.NewStruct(map[string]any{"title": "two"})
	wr := connect.NewRequest(&graphv1.WriteChangeImpactRequest{ChangeId: string(c.ID), ChangeImpactId: cn.Id, Props: props})
	as("contributor", wr)
	if out, err := h.WriteChangeImpact(ctx, wr); err != nil || out.Msg.Node.Post == nil || out.Msg.Node.Post.Version != 2 {
		t.Fatalf("write: %v %v", out, err)
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
}

func TestCommitEditsGatesAccessNodes(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g, Authz: authorizer}
	base, err := g.CreateBaseline(ctx, "Repository", nil)
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
}
