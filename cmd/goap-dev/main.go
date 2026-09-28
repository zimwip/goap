// Command goap-dev runs graph, registry, model gateway and engine in a
// single process, for local development without containers. Storage is
// in-memory (GOAP_STORE=memory, default) or a local SQLite file
// (GOAP_STORE=sqlite, GOAP_SQLITE_PATH) that keeps the graph, methodologies,
// access policies (graph nodes) and processes across restarts. When GOAP_WEB_DIR (default
// web/dist) holds a built IDE, it is served too. Callers act as the principal
// GOAP_DEV_SUBJECT / GOAP_DEV_ROLES unless the request carries X-Goap-*
// identity headers.
package main

import (
	"context"
	"maps"
	"net/http"
	"os"
	"time"

	"github.com/labstack/echo/v5"
	"strings"

	"github.com/zimwip/goap/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/gen/goap/index/v1/indexv1connect"
	"github.com/zimwip/goap/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/gen/goap/model/v1/modelv1connect"
	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/gen/goap/runtime/v1/runtimev1connect"
	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/internal/connectors/builtin"
	"github.com/zimwip/goap/internal/connectors/localfs"
	"github.com/zimwip/goap/internal/enginesvc"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/indexersvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/internal/sandbox"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/llmcfg"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("goap-dev")
	defer telemetry.Setup(context.Background(), log, "goap-dev")(context.Background())
	secrets := platform.NewSecrets()
	dev := authz.Principal{
		Subject: platform.Env("GOAP_DEV_SUBJECT", "dev"),
		Org:     platform.Env("GOAP_DEV_ORG", "dev"),
		Roles:   strings.Split(platform.Env("GOAP_DEV_ROLES", "admin"), ","),
	}
	ident := identity.Extractor{Default: &dev}

	st, err := openStores(ctx, log)
	if err != nil {
		platform.Fatal(log, "store", err)
	}
	defer st.close()
	g := graph.New(st.graph)
	directory := &access.Directory{Graph: g}
	authorizer, err := access.NewAuthorizer(directory)
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	g.Authorizer = graphsvc.TransitionAuthorizer(authorizer)
	var triggers *engine.TriggerManager
	// methodologies and domains are nodes of the graph: the registry needs no database
	// the scope of the MCPs (ADR 0028) is checked where methodologies declare them
	reg := &registrysvc.Service{Store: registrysvc.NewGraphStore(g), DomainStore: st.domains, Authz: authorizer,
		MCPScopes: (&mcpsvc.Directory{Graph: g}).Scopes}
	// the graph judges nodes by the types of the published domains (ADR 0012): its catalogue follows the registry
	types := typecat.NewLive(reg.Domains)
	g.Types = func() graph.TypeCatalog { return types.Get() }
	// publications reload the triggers and the type catalogue
	reg.Events = registryEvents{
		methodology: func(ctx context.Context, name, version string) {
			if triggers != nil {
				triggers.Handle(ctx, engine.TriggerEvent{Type: "methodology.published", Methodology: name, Version: version})
			}
		},
		domain: func(ctx context.Context, name, version string) {
			if err := types.Reload(ctx); err != nil {
				log.Error("type catalogue", "domain", name, "err", err)
				return
			}
			log.Info("domain published: type catalogue reloaded", "domain", name, "version", version)
		},
	}
	system := authz.With(ctx, authz.Principal{Subject: "system:registry", Roles: []string{"admin"}})
	if _, err := reg.SeedDomains(system, platform.Env("GOAP_DOMAINS_DIR", "domains")); err != nil {
		platform.Fatal(log, "domains", err)
	}
	if err := types.Reload(ctx); err != nil {
		platform.Fatal(log, "type catalogue", err)
	}
	// the gateway configuration (providers, models, aliases) must exist before methodologies are seeded below:
	// publishing a methodology stubs any alias it references that the platform namespace doesn't have yet
	// (registrysvc.ensureAliasStubs), and that stub would otherwise collide with the alias this seeds.
	if cfg, err := modelgw.InitialConfig(ctx, platform.Env("GOAP_MODELS_CONFIG", ""), secrets); err != nil {
		platform.Fatal(log, "models config", err)
	} else if provs, models, aliases, err := cfg.Objects(); err != nil {
		platform.Fatal(log, "models config", err)
	} else if _, err := graphsvc.SeedModels(ctx, g, provs, models, aliases); err != nil {
		platform.Fatal(log, "seed models", err)
	}
	if _, err := reg.Seed(system, platform.Env("GOAP_METHODOLOGIES_DIR", "methodologies")); err != nil {
		platform.Fatal(log, "methodologies", err)
	}
	if _, err := graphsvc.SeedDemo(ctx, g); err != nil {
		platform.Fatal(log, "seed", err)
	}
	if _, err := graphsvc.SeedAccess(ctx, g); err != nil {
		platform.Fatal(log, "seed access", err)
	}
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		platform.Fatal(log, "seed defaults", err)
	}
	if _, err := graphsvc.SeedBuiltins(ctx, g); err != nil {
		platform.Fatal(log, "seed built-in MCPs", err)
	}
	gw := modelgw.NewService(&llmcfg.Directory{Graph: g}, st.models, secrets.Resolve, log)
	gw.Router.Instrument = telemetry.NewGenAI().Instrument
	if err := gw.Reload(ctx); err != nil {
		platform.Fatal(log, "models", err)
	}
	// the node index follows the graph in-process (ADR 0026); embeddings go through the gateway, semantic search
	// needs an "embed" alias. The graph is published again at start: an index kept in SQLite catches up, a new one fills.
	indexer := indexersvc.New(st.index, gw, authorizer, log)
	indexSink := indexersvc.NewSink(ctx, indexer)
	indexer.Republish = func(ctx context.Context) (int, error) { return g.Republish(ctx, indexSink) }
	g.Observe(indexSink)
	go func() {
		if n, err := indexer.Republish(ctx); err != nil {
			log.Error("index: initial publication", "err", err)
		} else {
			log.Info("node index: graph published", "versions", n)
		}
	}()
	// the engine calls the gateway in-process, without an identity: trusted
	models := telemetry.LLMClient{Next: llm.ClientFunc(gw.Complete)}
	// scripts run in-process unless GOAP_SANDBOX selects a provisioner
	sandboxes, runtime, _, err := sandbox.FromEnv(log, platform.H2CClient(), telemetry.ClientOptions())
	if err != nil {
		platform.Fatal(log, "sandbox", err)
	}
	broker := engine.NewBroker()
	onChange := func(ctx context.Context, ev domain.ChangeEvent) {
		if triggers != nil {
			triggers.Handle(ctx, engine.TriggerEventOf(ev))
		}
	}
	// MCP hub in-process; the local file system connector and the built-in connectors (ADR 0028,
	// added once the engine exists) run inside too, other connectors register over HTTP like in
	// the distributed platform
	connectors := map[string]connectorkit.Connector{"localfs": localfs.Connector{}}
	connectorToken := os.Getenv("GOAP_CONNECTOR_TOKEN")
	hub := &mcpsvc.Service{
		Store:     st.mcp,
		Directory: &mcpsvc.Directory{Graph: g},
		Invoker:   mcpsvc.InprocInvoker{Connectors: connectors, Remote: &mcpsvc.ConnectInvoker{Token: connectorToken}},
		Secrets:   mcpsvc.ResolveSecret(secrets),
		Lease:     platform.EnvDuration("GOAP_CONNECTOR_LEASE", mcpsvc.DefaultLease),
	}
	if root := os.Getenv("GOAP_DEV_FS_ROOT"); root != "" {
		// demo: the default organisation implements document-repository with a directory
		if snap, err := hub.Directory.Snapshot(ctx); err != nil {
			platform.Fatal(log, "mcp", err)
		} else if _, _, ok := snap.Resolve(domain.DefaultOrg, "document-repository"); !ok {
			if err := graphsvc.SeedAdapter(ctx, g, graphsvc.LocalFSAdapter(domain.DefaultOrg, root)); err != nil {
				platform.Fatal(log, "mcp adapter", err)
			}
		}
	}
	builtins := engine.DefaultBuiltins()
	e := &engine.Engine{
		Graph:         engine.EventingGraph{GraphPort: g, OnEvent: onChange},
		Methodologies: reg,
		Executors: map[string]engine.Executor{
			methodology.KindLLM:     engine.LLMExecutor{Client: models},
			methodology.KindScript:  engine.ScriptExecutor{Sandboxes: sandboxes},
			methodology.KindHuman:   engine.HumanExecutor{},
			methodology.KindBuiltin: builtins,
			methodology.KindTool:    engine.ToolExecutor{},
		},
		Tools:     mcpsvc.HubPort{Service: hub},
		Intent:    intent.Resolver{Ranker: intent.Lexical{}},
		Store:     st.processes,
		Events:    broker,
		Authz:     authorizer,
		LLM:       models,
		Sandboxes: sandboxes,
		Tracer:    telemetry.NewEngineTracer(),
		Log:       log,
		Types:     func() methodology.TypeSet { return types.Get() },
	}
	// self-observation (methodology-improvement): journal, traces, drafts
	maps.Copy(builtins, e.SelfImprovementBuiltins(telemetry.SelfImprovementFromEnv(registrysvc.Drafts{Service: reg})))
	triggers = &engine.TriggerManager{Engine: e, Log: log}
	triggers.Start(ctx)
	go triggers.WatchProcesses(ctx, broker)
	engineHandler := &enginesvc.Handler{Engine: e, Log: log, DefaultPrincipal: &dev, Authz: authorizer, Broker: broker, Triggers: triggers}
	// the built-in connectors call the platform in-process, for the caller of the tool
	maps.Copy(connectors, builtin.Connectors(builtin.Ports{Graph: engine.EventingGraph{GraphPort: g, OnEvent: onChange}, Engine: engineHandler,
		Hub: hub, Registry: reg, Authz: authorizer, Floor: authorizer.Floor()}))
	hub.KeepRegistered(ctx, connectors)
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	srv.Mount(graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Graph: g, Events: engine.Publishers{changePublisher(onChange), indexSink}, Authz: authorizer, Floor: authorizer.Floor(), Identity: ident}, telemetry.HandlerOptions()...))
	srv.Mount(registryv1connect.NewRegistryServiceHandler(&registrysvc.Handler{Service: reg, Identity: ident}, telemetry.HandlerOptions()...))
	srv.Echo.GET("/api/whoami", identity.WhoAmI(ident, directory.Enrich))
	srv.Mount(mcpv1connect.NewMcpServiceHandler(&mcpsvc.Handler{Service: hub, Authz: authorizer, Identity: ident, ConnectorToken: connectorToken}, telemetry.HandlerOptions()...))
	srv.Mount(modelv1connect.NewModelServiceHandler(&modelgw.Handler{Service: gw, Identity: ident, Authz: authorizer}, telemetry.HandlerOptions()...))
	srv.Mount(indexv1connect.NewIndexServiceHandler(&indexersvc.Handler{Service: indexer, Identity: ident, Authz: authorizer}, telemetry.HandlerOptions()...))
	srv.Mount(enginev1connect.NewEngineServiceHandler(engineHandler, telemetry.HandlerOptions()...))
	// single process: the platform is up when this answers (the gateway serves it otherwise)
	srv.Echo.GET("/api/status", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC(),
			"services": []map[string]any{{"name": "goap-dev", "status": "up", "latencyMs": 0}}})
	})
	serveWeb(log, srv.Echo, platform.Env("GOAP_WEB_DIR", "web/dist"))
	if runtime != nil {
		srv.Mount(runtimev1connect.NewRuntimeServiceHandler(runtime, telemetry.HandlerOptions()...))
	}
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}

// changePublisher forwards the change events of the graph handler (calls from
// the IDE) to the triggers.
type changePublisher func(ctx context.Context, ev domain.ChangeEvent)

func (f changePublisher) Publish(ctx context.Context, _ string, v any) error {
	if ev, ok := v.(domain.ChangeEvent); ok {
		f(ctx, ev)
	}
	return nil
}
