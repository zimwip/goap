package graphsvc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// The criticality of a change header: anyone may raise it, lowering it asks change:lower-criticality (administrators), a
// value that is no level is refused (ADR 0075 §3).
func TestChangeCriticalityHeader(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g, Authz: authorizer}
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "t", Methodology: "m", Data: map[string]any{domain.DataCriticality: "C2"}})
	if err != nil {
		t.Fatal(err)
	}
	set := func(roles, level string) error {
		req := connect.NewRequest(&graphv1.UpdateChangeRequest{Id: string(c.ID), Data: pbconv.Struct(map[string]any{domain.DataCriticality: level})})
		req.Header().Set(identity.HeaderSubject, "u")
		req.Header().Set(identity.HeaderRoles, roles)
		_, err := h.UpdateChange(ctx, req)
		return err
	}
	for _, tc := range []struct {
		name, roles, level string
		want               connect.Code
	}{
		{"raise", "developer", "C3", 0},
		{"same level", "developer", "C3", 0},
		{"lowering needs the permission", "developer", "C2", connect.CodePermissionDenied},
		{"lowering by an administrator", "admin", "C1", 0},
		{"not a level", "admin", "urgent", connect.CodeInvalidArgument},
	} {
		err := set(tc.roles, tc.level)
		if err == nil && tc.want != 0 || err != nil && connect.CodeOf(err) != tc.want {
			t.Errorf("%s: %v, want code %v", tc.name, err, tc.want)
		}
	}
	got, _ := g.Change(ctx, c.ID)
	if got.Data[domain.DataCriticality] != "C1" {
		t.Fatalf("criticality %v", got.Data)
	}
	// a change created with a value that is no level is refused too
	req := connect.NewRequest(&graphv1.CreateChangeRequest{Title: "x", Methodology: "m", Data: pbconv.Struct(map[string]any{domain.DataCriticality: "C7"})})
	if _, err := h.CreateChange(ctx, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("create: %v", err)
	}
}
