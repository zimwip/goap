// Command mcp is the MCP hub: the registry of connectors (separate services that
// register themselves), the generic MCP definitions, their adapters and the bindings
// of the organizations, and the invocation of tools (ADR 0019). It serves the built-in
// connectors of the platform in-process (ADR 0028).
package main

import (
	"context"
	"maps"
	"os"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/internal/connectors/builtin"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/access"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("mcp")
	defer telemetry.Setup(context.Background(), log, "mcp")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var store mcpsvc.Store = mcpsvc.NewMemoryStore()
	if pool := platform.OptionalPostgres(ctx, log, mcpsvc.Migrations); pool != nil {
		defer pool.Close()
		store = mcpsvc.SQLStore{DB: stdlib.OpenDBFromPool(pool), Dollar: true}
		srv.Readiness(pool.Ping)
	}
	token := os.Getenv("GOAP_CONNECTOR_TOKEN")
	if token == "" {
		log.Warn("GOAP_CONNECTOR_TOKEN not set: no connector can register, only the built-in ones are served")
	}
	hc := platform.H2CClient()
	graphURL := platform.Env("GOAP_GRAPH_URL", "http://localhost:8081")
	// the built-in connectors (ADR 0028) run in the hub; the others register over HTTP
	connectors := map[string]connectorkit.Connector{}
	svc := &mcpsvc.Service{
		Store: store,
		// the MCPs, the adapter definitions, the adapters and the organisation hierarchy are nodes of the graph
		Directory: &mcpsvc.Directory{Graph: graphsvc.NewClient(hc, graphURL)},
		Invoker:   mcpsvc.InprocInvoker{Connectors: connectors, Remote: &mcpsvc.ConnectInvoker{Token: token}},
		Secrets:   mcpsvc.ResolveSecret(platform.NewSecrets()),
		Lease:     platform.EnvDuration("GOAP_CONNECTOR_LEASE", mcpsvc.DefaultLease),
	}
	iam, err := access.NewAuthorizer(&access.Directory{Graph: graphsvc.NewClient(hc, graphURL)})
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	// they call the platform's services for the caller of the tool
	copts := append(telemetry.ClientOptions(), builtin.ForwardIdentity())
	maps.Copy(connectors, builtin.Connectors(builtin.Ports{
		Graph:    graphsvc.NewClient(hc, graphURL, copts...),
		Engine:   enginev1connect.NewEngineServiceClient(hc, platform.Env("GOAP_ENGINE_URL", "http://localhost:8083"), copts...),
		Registry: registrysvc.NewClient(hc, platform.Env("GOAP_REGISTRY_URL", "http://localhost:8082"), copts...),
		Hub:      svc,
		Authz:    iam,
		Floor:    iam.Floor(),
	}))
	svc.KeepRegistered(ctx, connectors)
	srv.Mount(mcpv1connect.NewMcpServiceHandler(&mcpsvc.Handler{Service: svc, Authz: iam, ConnectorToken: token}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
