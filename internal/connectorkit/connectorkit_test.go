package connectorkit_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/internal/connectors/localfs"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/mcp"
)

// A connector started later registers by itself; a call from an organization goes
// through binding, adapter and connector, over real HTTP.
func TestAutoRegistrationAndCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const token = "s3cret"

	hub := &mcpsvc.Service{Store: mcpsvc.NewMemoryStore(), Invoker: &mcpsvc.ConnectInvoker{Token: token, HTTP: http.DefaultClient}, Lease: 3 * time.Second}
	if err := mcpsvc.Seed(ctx, hub.Store); err != nil {
		t.Fatal(err)
	}
	dev := authz.Principal{Subject: "u", Org: "acme", Roles: []string{"admin"}}
	mux := http.NewServeMux()
	mux.Handle(mcpv1connect.NewMcpServiceHandler(&mcpsvc.Handler{Service: hub, Identity: identity.Extractor{Default: &dev}, ConnectorToken: token}))
	hubSrv := httptest.NewServer(mux)
	defer hubSrv.Close()

	dir := t.TempDir()
	cmux := http.NewServeMux()
	cmux.Handle(connectorkit.Handler(localfs.Connector{}, token))
	connSrv := httptest.NewServer(cmux)
	defer connSrv.Close()

	// the hub knows nothing about the connector until it registers itself
	if cs, _ := hub.Connectors(ctx); len(cs) != 0 {
		t.Fatalf("connectors before start = %v", cs)
	}
	go connectorkit.Register(ctx, slog.Default(), http.DefaultClient, hubSrv.URL, connSrv.URL, token, localfs.Connector{}.Info())
	deadline := time.Now().Add(5 * time.Second)
	for {
		if cs, _ := hub.Connectors(ctx); len(cs) == 1 && cs[0].Live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connector did not register")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := hub.Bind(ctx, mcp.Binding{OrgID: "acme", MCP: "document-repository", Connector: "localfs", Config: map[string]any{"root": dir}}); err != nil {
		t.Fatal(err)
	}
	client := mcpsvc.NewClient(http.DefaultClient, hubSrv.URL)
	cctx := authz.With(ctx, dev)
	if _, err := client.CallTool(cctx, "acme", "document-repository/write", map[string]any{"path": "n.txt", "content": "hi"}); err != nil {
		t.Fatal(err)
	}
	out, err := client.CallTool(cctx, "acme", "document-repository/read", map[string]any{"path": "n.txt"})
	if err != nil || out.(map[string]any)["text"] != "hi" {
		t.Fatalf("read = %v, %v", out, err)
	}
	if _, err := client.CallTool(cctx, "acme", "document-repository/read", map[string]any{"path": "../x"}); err == nil {
		t.Fatal("escape not reported")
	}
	if _, err := client.CallTool(cctx, "globex", "document-repository/read", map[string]any{"path": "n.txt"}); err == nil {
		t.Fatal("unbound organization can call the tool")
	}
	tools, mcps, err := client.Tools(cctx, "acme")
	if err != nil || len(tools) != 3 || len(mcps) != 1 {
		t.Fatalf("tools = %v %v %v", tools, mcps, err)
	}

	// the connector refuses calls that do not carry the token
	c := connectorv1connectClient(connSrv.URL)
	_, err = c(ctx)
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodePermissionDenied {
		t.Fatalf("call without token = %v", err)
	}
}
