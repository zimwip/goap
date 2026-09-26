package connectorkit_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/internal/connectors/localfs"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

// A connector started later registers by itself; a call from an organization goes
// through binding, adapter and connector, over real HTTP.
func TestAutoRegistrationAndCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const token = "s3cret"

	dir := t.TempDir()
	g := graph.New(graph.NewMemory())
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedAdapter(ctx, g, graphsvc.LocalFSAdapter(domain.DefaultOrg, dir)); err != nil {
		t.Fatal(err)
	}
	hub := &mcpsvc.Service{Store: mcpsvc.NewMemoryStore(), Directory: &mcpsvc.Directory{Graph: g}, Library: loadPlatformDomain(t),
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

// platformDomain serves the algorithms of domains/platform.yaml, the adapter library of the platform.
type platformDomain struct{ algos map[string]algo.Algorithm }

func (l platformDomain) Algorithm(_ context.Context, _, _, name string) (algo.Algorithm, string, error) {
	a, ok := l.algos[name]
	if !ok {
		return a, "", errors.New("not in the library")
	}
	return a, "1.0.0", nil
}

func loadPlatformDomain(t *testing.T) platformDomain {
	t.Helper()
	raw, err := os.ReadFile("../../domains/platform.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := methodology.ParseDomain(raw)
	if err != nil {
		t.Fatal(err)
	}
	l := platformDomain{algos: map[string]algo.Algorithm{}}
	for _, a := range d.Algorithms {
		l.algos[a.Name] = a
	}
	return l
}
