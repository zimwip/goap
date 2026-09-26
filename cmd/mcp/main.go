// Command mcp is the MCP hub: the registry of connectors (separate services that
// register themselves), the generic MCP definitions, their adapters and the bindings
// of the organizations, and the invocation of tools (ADR 0019).
package main

import (
	"context"
	"os"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/iamsvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
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
		log.Warn("GOAP_CONNECTOR_TOKEN not set: any service can register as a connector")
	}
	hc := platform.H2CClient()
	svc := &mcpsvc.Service{
		Store: store,
		// the MCPs, the adapters and the organisation hierarchy are nodes of the graph
		Directory: &mcpsvc.Directory{Graph: graphsvc.NewClient(hc, platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"))},
		Invoker:   &mcpsvc.ConnectInvoker{Token: token},
		Secrets:   mcpsvc.ResolveSecret(platform.NewSecrets()),
		Lease:     platform.EnvDuration("GOAP_CONNECTOR_LEASE", mcpsvc.DefaultLease),
	}
	iam := iamsvc.NewClient(hc, platform.Env("GOAP_IAM_URL", "http://localhost:8086"))
	srv.Mount(mcpv1connect.NewMcpServiceHandler(&mcpsvc.Handler{Service: svc, Authz: iam, ConnectorToken: token}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
