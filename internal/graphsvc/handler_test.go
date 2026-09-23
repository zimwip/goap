package graphsvc_test

import (
	"context"
	"os"
	"testing"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/metamodel"
	"github.com/zimwip/goap/pkg/methodology"
)

func createObject(h *graphsvc.Handler, roles, key string) error {
	req := connect.NewRequest(&graphv1.CreateObjectRequest{Methodology: "test-design", NodeType: "Requirement", Key: key})
	if roles != "" {
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
	}
	_, err := h.CreateObject(context.Background(), req)
	return err
}

func TestCreateObjectIsRoleGated(t *testing.T) {
	data, err := os.ReadFile("../../methodologies/test-design.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := methodology.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	if _, err := metamodel.Sync(context.Background(), g, m); err != nil {
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
	// a denied caller created nothing
	if _, err := g.NodeByKey(context.Background(), "REQ-2"); err == nil {
		t.Error("REQ-2 must not exist")
	}
	if _, err := g.NodeByKey(context.Background(), "REQ-3"); err != nil {
		t.Errorf("REQ-3 must exist: %v", err)
	}
}

func TestNodeTypeWritesAreRoleGated(t *testing.T) {
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
	add := func(roles string) error {
		c, err := g.CreateChange(ctx, graph.NewChange{Title: "t", BaselineID: base.ID})
		if err != nil {
			t.Fatal(err)
		}
		item := domain.ChangeItem{Kind: domain.KindProposal, Proposal: &domain.Proposal{Op: domain.OpCreateNode,
			Node: &domain.NodeDraft{Key: "M:x/nodetype/T", Type: metamodel.TypeNodeType}}}
		req := connect.NewRequest(&graphv1.AddItemsRequest{ChangeId: string(c.ID), Items: pbconv.ItemsToPB([]domain.ChangeItem{item})})
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderOrg, "acme")
		req.Header().Set(identity.HeaderRoles, roles)
		_, err = h.AddItems(ctx, req)
		return err
	}
	if err := add("contributor"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("contributor must not author node types: %v", err)
	}
	if err := add("methodologist"); err != nil {
		t.Errorf("methodologist must: %v", err)
	}
}
