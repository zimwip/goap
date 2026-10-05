package mcpsvc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	mcpv1 "github.com/zimwip/goap/gen/goap/mcp/v1"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
)

// A caller reaches the adapters of its own unit and of the units below it, never those of another unit: the
// unit of a request is checked against the caller's. Administrators and platform services name any unit.
func TestCallToolStaysWithinTheCallersUnit(t *testing.T) {
	inv := &fakeInvoker{resp: &connectorv1.InvokeResponse{Result: pbconv.Struct(map[string]any{"text": "hi"})}}
	h := &mcpsvc.Handler{Service: newHub(t, world(t), inv), ConnectorToken: "t"}
	call := func(who authz.Principal, unit string) (string, error) {
		req := connect.NewRequest(&mcpv1.CallToolRequest{Unit: unit, Name: "document-repository/read", Arguments: pbconv.Struct(map[string]any{"path": "n.txt"})})
		identity.SetHeaders(who, req.Header())
		_, err := h.CallTool(context.Background(), req)
		root := ""
		if inv.last != nil {
			root, _ = pbconv.Map(inv.last.Config)["root"].(string)
		}
		return root, err
	}
	a := authz.Principal{Subject: "alice", Org: "ORG-A"}
	if root, err := call(a, "ORG-A1"); err != nil || root != "/a" {
		t.Fatalf("a sub-unit: %q %v", root, err)
	}
	if root, err := call(a, ""); err != nil || root != "/a" { // no unit: the caller's own
		t.Fatalf("no unit: %q %v", root, err)
	}
	inv.last = nil
	for _, unit := range []string{"ORG-B", domain.DefaultOrg} { // a sibling tree, and the unit above
		if _, err := call(a, unit); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("unit %s: %v", unit, err)
		}
	}
	if inv.last != nil {
		t.Fatalf("the connector was called for a unit the caller is not in: %v", inv.last)
	}
	if _, err := call(authz.Principal{Subject: "root", Org: "ORG-A", Roles: []string{"admin"}}, "ORG-B"); err != nil {
		t.Fatalf("administrator: %v", err)
	}
	if _, err := call(authz.Principal{Subject: "system:trigger:x", Org: "system"}, "ORG-B"); err != nil {
		t.Fatalf("platform service: %v", err)
	}
	// the effective adapters follow the same rule
	req := connect.NewRequest(&mcpv1.ListEffectiveRequest{Unit: "ORG-B"})
	identity.SetHeaders(a, req.Header())
	if _, err := h.ListEffective(context.Background(), req); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ListEffective of another unit: %v", err)
	}
}

// With no connector token configured no connector can register; with one, only a request carrying it can.
func TestConnectorRegistrationNeedsAToken(t *testing.T) {
	register := func(h *mcpsvc.Handler, token string) error {
		req := connect.NewRequest(&mcpv1.RegisterConnectorRequest{Info: localfsInfo(), Endpoint: "http://localfs:8080"})
		if token != "" {
			req.Header().Set(mcpsvc.ConnectorTokenHeader, token)
		}
		_, err := h.RegisterConnector(context.Background(), req)
		return err
	}
	svc := newHub(t, world(t), &fakeInvoker{})
	if err := register(&mcpsvc.Handler{Service: svc}, ""); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("no token configured: %v", err)
	}
	h := &mcpsvc.Handler{Service: svc, ConnectorToken: "s3cret"}
	if err := register(h, "wrong"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("wrong token: %v", err)
	}
	if err := register(h, "s3cret"); err != nil {
		t.Fatalf("right token: %v", err)
	}
}
