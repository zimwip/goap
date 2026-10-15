// The connector kit against a hub over a real graph (ADR 0125).
package connectorkite2e

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/contracts/mcp"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/mcpbuiltin"
	"github.com/zimwip/goap/mcp/connectorkit"
	"github.com/zimwip/goap/mcp/connectors/localfs"
	"github.com/zimwip/goap/mcp/connectors/workspace"
	"github.com/zimwip/goap/mcp/mcpsvc"
	"github.com/zimwip/goap/platform/devseed"
)

// A connector started later registers by itself; a call from an organization goes
// through binding, adapter and connector, over real HTTP.
func TestAutoRegistrationAndCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const token = "s3cret"

	dir := t.TempDir()
	g := graph.New(graph.NewMemory())
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := devseed.DocumentRepository(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedAdapter(ctx, g, devseed.LocalFSAdapter(access.DefaultOrg, dir)); err != nil {
		t.Fatal(err)
	}
	hub := &mcpsvc.Service{Store: mcpsvc.NewMemoryStore(), Directory: &mcpsvc.Directory{Graph: g},
		Invoker: &mcpsvc.ConnectInvoker{Token: token, HTTP: http.DefaultClient}, Lease: 3 * time.Second}
	dev := authz.Principal{Subject: "u", Org: "acme", Roles: []string{"admin"}}
	mux := http.NewServeMux()
	mux.Handle(mcpv1connect.NewMcpServiceHandler(&mcpsvc.Handler{Service: hub, Identity: identity.Extractor{Default: &dev}, ConnectorToken: token}))
	hubSrv := httptest.NewServer(mux)
	defer hubSrv.Close()

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
	if _, err := client.CallTool(cctx, "acme", "ticketing/create", nil); err == nil {
		t.Fatal("a tool of an MCP nobody implements can be called")
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

// The change a tool call runs for reaches a remote connector (ADR 0118): the workspace of a change is found from it,
// through the engine's client, the hub and the connector over HTTP.
func TestCallContextReachesTheConnector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const token = "s3cret"
	root := t.TempDir()
	g := graph.New(graph.NewMemory())
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := graphsvc.SeedBuiltins(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedAdapter(ctx, g, mcpbuiltin.WorkspaceAdapter(access.DefaultOrg, root)); err != nil {
		t.Fatal(err)
	}
	hub := &mcpsvc.Service{Store: mcpsvc.NewMemoryStore(), Directory: &mcpsvc.Directory{Graph: g},
		Invoker: &mcpsvc.ConnectInvoker{Token: token, HTTP: http.DefaultClient}, Lease: 3 * time.Second}
	dev := authz.Principal{Subject: "u", Org: "acme", Roles: []string{"admin"}}
	mux := http.NewServeMux()
	mux.Handle(mcpv1connect.NewMcpServiceHandler(&mcpsvc.Handler{Service: hub, Identity: identity.Extractor{Default: &dev}, ConnectorToken: token}))
	hubSrv := httptest.NewServer(mux)
	defer hubSrv.Close()
	conn := workspace.New()
	cmux := http.NewServeMux()
	cmux.Handle(connectorkit.Handler(conn, token))
	connSrv := httptest.NewServer(cmux)
	defer connSrv.Close()
	if _, err := hub.RegisterConnector(ctx, conn.Info(), connSrv.URL); err != nil {
		t.Fatal(err)
	}

	client := mcpsvc.NewClient(http.DefaultClient, hubSrv.URL)
	cctx := mcp.WithCall(authz.With(ctx, dev), mcp.CallContext{Change: "CHG-42", Process: "P-1"})
	if _, err := client.CallTool(cctx, "acme", "workspace/write", map[string]any{"path": "a.txt", "content": "hi"}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "CHG-42", "a.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("workspace of CHG-42: %q %v", b, err)
	}
	if _, err := client.CallTool(authz.With(ctx, dev), "acme", "workspace/ls", nil); err == nil || !strings.Contains(err.Error(), "runs for none") {
		t.Fatalf("a call for no change: %v", err)
	}
}
