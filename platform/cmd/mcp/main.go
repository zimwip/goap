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

	"github.com/zimwip/goap/contracts/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/contracts/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/domainclient"
	"github.com/zimwip/goap/lib/graphclient"
	"github.com/zimwip/goap/lib/graphsnap"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/mcp/connectorkit"
	"github.com/zimwip/goap/mcp/connectors/builtin"
	"github.com/zimwip/goap/mcp/mcpsvc"
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
	// one reader of the graph for the hub's views and the access (ADR 0145)
	graphClient := graphclient.NewClient(hc, graphURL)
	reader := graphsnap.NewReader(graphClient)
	svc := &mcpsvc.Service{
		Store: store,
		// the MCPs, the adapter definitions, the adapters and the organisation hierarchy are nodes of the graph
		Directory: &mcpsvc.Directory{Graph: graphClient, Reader: reader},
		Invoker:   mcpsvc.InprocInvoker{Connectors: connectors, Remote: &mcpsvc.ConnectInvoker{Token: token}},
		Secrets:   mcpsvc.ResolveSecret(platform.NewSecrets()),
		Lease:     platform.EnvDuration("GOAP_CONNECTOR_LEASE", mcpsvc.DefaultLease),
	}
	// a connector calling a model by itself (the Claude coding agent) gets the gateway's Anthropic endpoint and a token
	// of each call, so its calls are rows of the ledger (ADR 0123 §6)
	if secret, u := platform.Env("GOAP_MODEL_PROXY_SECRET", ""), platform.Env("GOAP_MODEL_PROXY_URL", ""); secret != "" && u != "" {
		svc.Models = &mcpsvc.ModelEndpoint{URL: u, Secret: []byte(secret)}
	}
	directory := &access.Directory{Graph: graphClient, Reader: reader}
	iam, err := access.NewAuthorizer(directory)
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	// they call the platform's services for the caller of the tool; the domains are the graph's, the methodologies the
	// methodology registry's (ADR 0125 §3), both read through the contract
	copts := append(telemetry.ClientOptions(), builtin.ForwardIdentity())
	maps.Copy(connectors, builtin.Connectors(builtin.Ports{
		Graph:         graphclient.NewClient(hc, graphURL, copts...),
		Engine:        enginev1connect.NewEngineServiceClient(hc, platform.Env("GOAP_ENGINE_URL", "http://localhost:8083"), copts...),
		Domains:       domainclient.NewClient(hc, graphURL, copts...),
		Methodologies: registryv1connect.NewRegistryServiceClient(hc, platform.Env("GOAP_REGISTRY_URL", "http://localhost:8082"), copts...),
		Hub:           svc,
		Authz:         iam,
		Floor:         iam.Floor(),
	}))
	// its events (a connector registered, lost, back) go through its outbox to the bus (ADR 0136); Watch tells the
	// connectors whose lease ran out
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()
	// the policies, the organisation and the projects the platform graph delivers, read again when its baseline
	// events say the organisation moved (ADR 0154 §9)
	if events != nil {
		if err := directory.FollowEvents(events); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	outbox.Start(ctx, svc.Emit(outbox.NATS(events)), log)
	go svc.Watch(ctx, 0)
	svc.KeepRegistered(ctx, connectors)
	srv.Mount(mcpv1connect.NewMcpServiceHandler(&mcpsvc.Handler{Service: svc, Authz: iam, ConnectorToken: token}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
