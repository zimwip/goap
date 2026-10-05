// Command goap-dev runs graph, registry, model gateway and engine in a
// single process, for local development without containers. Storage is
// in-memory (GOAP_STORE=memory, default) or a local SQLite file
// (GOAP_STORE=sqlite, GOAP_SQLITE_PATH) that keeps the graph, methodologies,
// access policies (graph nodes) and processes across restarts. When GOAP_WEB_DIR (default
// web/dist) holds a built IDE, it is served too. By default (ADR 0040, 0042) users sign in
// (register/login/logout, internal/credsvc), the same auth code cmd/gateway uses; with
// GOAP_AUTH_MODE=none, callers act as the principal GOAP_DEV_SUBJECT / GOAP_DEV_ROLES unless the
// request carries X-Goap-* identity headers.
package main

import (
	"connectrpc.com/connect"
	"github.com/zimwip/goap/pkg/decision"
	"github.com/zimwip/goap/pkg/events"
	"github.com/zimwip/goap/pkg/risk"

	"context"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/labstack/echo/v5"
	"strings"

	"github.com/zimwip/goap/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/gen/goap/events/v1/eventsv1connect"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/gen/goap/index/v1/indexv1connect"
	"github.com/zimwip/goap/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/gen/goap/model/v1/modelv1connect"
	"github.com/zimwip/goap/gen/goap/preferences/v1/preferencesv1connect"
	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/gen/goap/runtime/v1/runtimev1connect"
	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/internal/connectors/builtin"
	"github.com/zimwip/goap/internal/connectors/localfs"
	"github.com/zimwip/goap/internal/credsvc"
	"github.com/zimwip/goap/internal/enginesvc"
	"github.com/zimwip/goap/internal/eventsvc"
	"github.com/zimwip/goap/internal/gateway"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/indexersvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/prefssvc"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/internal/sandbox"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/llmcfg"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/selfimprove"
	"github.com/zimwip/goap/pkg/typecat"
)

func main() {
	ctx := context.Background()
	// the facts of the risk register are items of a change (ADR 0065)
	risk.Register()
	log := platform.Logger("goap-dev")
	defer telemetry.Setup(context.Background(), log, "goap-dev")(context.Background())
	secrets := platform.NewSecrets()
	dev := authz.Principal{
		Subject: platform.Env("GOAP_DEV_SUBJECT", "dev"),
		Org:     platform.Env("GOAP_DEV_ORG", "dev"),
		Project: platform.Env("GOAP_DEV_PROJECT", ""),
		Roles:   strings.Split(platform.Env("GOAP_DEV_ROLES", "admin"), ","),
	}
	ident := identity.Extractor{Default: &dev}

	st, err := openStores(ctx, log)
	if err != nil {
		platform.Fatal(log, "store", err)
	}
	defer st.close()
	g := graph.New(st.graph)
	g.Caller = graphsvc.Caller           // the principal behind each event of the impact logs (ADR 0029)
	g.DecisionPolicy = decision.Policy{} // confidence, rounds and deadline settle the decision points (ADR 0067)
	// baselines written whole before they were stored as deltas are compacted, once, in the background (ADR 0032)
	go func() {
		if n, err := g.CompactBaselines(ctx); err != nil {
			log.Error("compact baselines", "err", err)
		} else if n > 0 {
			log.Info("baselines compacted", "rewritten", n)
		}
	}()
	directory := &access.Directory{Graph: g}
	authorizer, err := access.NewAuthorizer(directory)
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	g.Authorizer = graphsvc.TransitionAuthorizer(authorizer)
	g.ChangeAuthorizer = graphsvc.ChangeTransitionAuthorizer(authorizer)
	g.Validators = []graph.NodeValidator{access.AdminFloorValidator{}}
	var triggers *engine.TriggerManager
	// methodologies and domains are nodes of the graph: the registry needs no database
	// the scope of the MCPs (ADR 0028) is checked where methodologies declare them
	reg := &registrysvc.Service{Store: registrysvc.NewGraphStore(g), DomainStore: st.domains, Authz: authorizer,
		MCPScopes: (&mcpsvc.Directory{Graph: g}).Scopes}
	// the graph judges nodes by the types of the published domains (ADR 0012): its catalogue follows the registry
	types := typecat.NewLive(reg.Domains)
	g.Types = func() graph.TypeCatalog { return types.Get() }
	// a change scoped to an Activity is gated by its own goal condition at Apply, not the node-type lifecycle's
	// Editable floor (architecture plan "Activity concept")
	g.LandingGate = reg.LandingGate
	g.SubChangeValidator = reg.SubChangeValidator
	// a change follows the lifecycle its methodology names, its gates read the conditions of the methodology (ADR 0058)
	g.Lifecycles = reg
	// the one event stream of the web (ADR 0053): every publication of the platform also feeds it
	bus := eventsvc.NewHub()
	go bus.Run(ctx)
	// publications reload the triggers and the type catalogue
	reg.Events = engine.Publishers{bus, registryEvents{
		methodology: func(ctx context.Context, name, version string) {
			if triggers != nil {
				triggers.Handle(ctx, engine.TriggerEvent{Type: events.MethodologyPublished, Methodology: name, Version: version})
			}
		},
		domain: func(ctx context.Context, name, version string) {
			if err := types.Reload(ctx); err != nil {
				log.Error("type catalogue", "domain", name, "err", err)
				return
			}
			log.Info("domain published: type catalogue reloaded", "domain", name, "version", version)
		},
	}}
	system := authz.With(ctx, authz.Principal{Subject: "system:registry", Roles: []string{"admin"}})
	if _, err := reg.SeedDomains(system, platform.Env("GOAP_DOMAINS_DIR", "domains")); err != nil {
		platform.Fatal(log, "domains", err)
	}
	if err := types.Reload(ctx); err != nil {
		platform.Fatal(log, "type catalogue", err)
	}
	// the roots of the organisation and of the projects, before any change (ADR 0054)
	if err := g.Bootstrap(ctx); err != nil {
		platform.Fatal(log, "bootstrap", err)
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
	if _, err := graphsvc.SeedAccess(ctx, g); err != nil {
		platform.Fatal(log, "seed access", err)
	}
	if _, err := graphsvc.SeedDefaults(ctx, g); err != nil {
		platform.Fatal(log, "seed defaults", err)
	}
	if _, err := graphsvc.SeedDemo(ctx, g); err != nil {
		platform.Fatal(log, "seed", err)
	}
	if _, err := graphsvc.SeedBuiltins(ctx, g); err != nil {
		platform.Fatal(log, "seed built-in MCPs", err)
	}
	gw := modelgw.NewService(&llmcfg.Directory{Graph: g}, st.models, secrets.Resolve, log)
	gw.Router.Instrument = telemetry.NewGenAI().Instrument
	gw.Authz = authorizer // the roles a model requires are held on the caller's project (ADR 0043)
	if err := gw.Reload(ctx); err != nil {
		platform.Fatal(log, "models", err)
	}
	// the node index follows the graph in-process (ADR 0026); embeddings go through the gateway, semantic search
	// needs an "embed" alias. The graph is published again at start: an index kept in SQLite catches up, a new one fills.
	indexer := indexersvc.New(st.index, gw, authorizer, log)
	indexSink := indexersvc.NewSink(ctx, indexer)
	indexer.Republish = func(ctx context.Context) (int, error) { return g.Republish(ctx, indexSink) }
	g.Observe(engine.Publishers{indexSink, bus})
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
	// connectors register with a shared token (a service without one cannot register): generated like the JWT
	// secret when none is configured, and shown so that a connector started by hand can use it
	connectorToken := os.Getenv("GOAP_CONNECTOR_TOKEN")
	if connectorToken == "" {
		var err error
		if connectorToken, err = localSecret(st.dir, "connector_token"); err != nil {
			platform.Fatal(log, "connector token", err)
		}
		log.Info("connector token generated: start connectors with GOAP_CONNECTOR_TOKEN set to it", "file", filepath.Join(st.dir, "connector_token"))
	}
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
		} else if _, _, ok := snap.Resolve(g.Structure(domain.StructureOrganisation).Root, "document-repository"); !ok {
			if err := graphsvc.SeedAdapter(ctx, g, graphsvc.LocalFSAdapter(g.Structure(domain.StructureOrganisation).Root, root)); err != nil {
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
		Intent:    intent.Resolver{Ranker: intent.Lexical{}},
		Store:     st.processes,
		Events:    engine.Publishers{broker, bus},
		Scope:     engine.AuthzScope{Authz: authorizer, Hub: mcpsvc.HubPort{Service: hub}},
		LLM:       models,
		Sandboxes: sandboxes,
		Tracer:    telemetry.NewEngineTracer(),
		Log:       log,
		Types:     func() def.TypeSet { return types.Get() },
	}
	// self-observation (methodology-improvement): journal, traces, drafts
	selfimprove.Register(builtins, e, telemetry.SelfImprovementFromEnv(registrysvc.Drafts{Service: reg}))
	triggers = &engine.TriggerManager{Engine: e, Log: log}
	triggers.Start(ctx)
	go triggers.WatchProcesses(ctx, broker)
	engineHandler := &enginesvc.Handler{Engine: e, Log: log, DefaultPrincipal: &dev, Authz: authorizer, Broker: broker, Triggers: triggers}
	// the built-in connectors call the platform in-process, for the caller of the tool
	maps.Copy(connectors, builtin.Connectors(builtin.Ports{Graph: engine.EventingGraph{GraphPort: g, OnEvent: onChange}, Engine: engineHandler,
		Hub: hub, Registry: reg, Authz: authorizer, Floor: authorizer.Floor()}))
	hub.KeepRegistered(ctx, connectors)
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))

	// Sign-in (ADR 0040, 0042): local by default (GOAP_AUTH_MODE unset) — this single process is its own
	// identity provider (register/login/logout, no external IdP), the same way cmd/gateway's AuthMode does
	// for the distributed platform, reusing its authenticator and auth endpoints. GOAP_AUTH_MODE=none turns it
	// off for development: every request then acts as the fixed GOAP_DEV_SUBJECT/ORG/ROLES principal. Applied
	// per-route (not e.Use), so the built IDE's static assets and /api/status/health stay reachable with no
	// token.
	var authMW echo.MiddlewareFunc
	authMode := platform.Env("GOAP_AUTH_MODE", gateway.DefaultAuthMode)
	mode, err := gateway.LookupMode(authMode)
	if err != nil {
		platform.Fatal(log, "auth mode", err)
	}
	if !mode.Open() {
		secret, err := secrets.Get(ctx, "goap/gateway#jwt_secret", "GOAP_JWT_SECRET")
		if err == nil && secret == "" && mode.Sessions() {
			secret, err = localJWTSecret(st.dir)
		}
		if err != nil {
			platform.Fatal(log, "jwt secret", err)
		}
		authCfg := gateway.Config{AuthMode: authMode, JWTSecret: []byte(secret), DevTokens: platform.Env("GOAP_DEV_TOKENS", "") == "true",
			TokenTTL: platform.EnvDuration("GOAP_TOKEN_TTL", gateway.DefaultTokenTTL), MaxSession: platform.EnvDuration("GOAP_SESSION_MAX", gateway.DefaultMaxSession),
			Credentials: &credsvc.Service{Store: st.creds}, Enrich: directory.Enrich, ProjectAccess: directory.MayAccessProject,
			// the user exists in the graph from their first sign-in, member of the unit new users join (ADR 0042)
			OnSignIn: func(ctx context.Context, subject string) error {
				if err := graphsvc.EnsureUser(ctx, g, subject); err != nil {
					return err
				}
				return directory.Refresh(ctx)
			}}
		// one state of the sessions for the endpoints and the authenticator: a sign-out is refused at once (ADR 0045)
		authCfg = gateway.Prepare(authCfg)
		if err := gateway.MountAuthEndpoints(srv.Echo, authCfg); err != nil {
			platform.Fatal(log, "auth", err)
		}
		if authMW, err = gateway.Authenticator(authCfg); err != nil {
			platform.Fatal(log, "auth", err)
		}
		log.Info("local sign-in enabled", "mode", authMode)
	}
	mount := func(path string, h http.Handler) {
		if authMW != nil {
			srv.Echo.Any(path+"*", echo.WrapHandler(h), authMW)
			return
		}
		srv.Mount(path, h)
	}

	graphHandler := &graphsvc.Handler{Graph: g, Events: engine.Publishers{changePublisher(onChange), indexSink, bus}, Authz: authorizer, Floor: authorizer.Floor(), Identity: ident}
	mount(graphv1connect.NewGraphServiceHandler(graphHandler, append(telemetry.HandlerOptions(), connect.WithInterceptors(graphHandler.Identify(), eventsvc.CommandInterceptor(), graphHandler.PersonalScope(), graphHandler.EnsureCaller()))...))
	mount(registryv1connect.NewRegistryServiceHandler(&registrysvc.Handler{Service: reg, Identity: ident}, append(telemetry.HandlerOptions(), connect.WithInterceptors(eventsvc.CommandInterceptor()))...))
	whoami := func(ctx context.Context, p authz.Principal) (any, error) { return authorizer.Session(ctx, p) }
	if authMW != nil {
		srv.Echo.GET("/api/whoami", identity.WhoAmI(identity.Extractor{}, whoami), authMW)
	} else {
		srv.Echo.GET("/api/whoami", identity.WhoAmI(ident, whoami))
	}
	mount(mcpv1connect.NewMcpServiceHandler(&mcpsvc.Handler{Service: hub, Authz: authorizer, Identity: ident, ConnectorToken: connectorToken}, telemetry.HandlerOptions()...))
	mount(modelv1connect.NewModelServiceHandler(&modelgw.Handler{Service: gw, Identity: ident, Authz: authorizer}, telemetry.HandlerOptions()...))
	mount(preferencesv1connect.NewPreferencesServiceHandler(&prefssvc.Handler{Service: &prefssvc.Service{Store: st.prefs}, Identity: ident}, telemetry.HandlerOptions()...))
	mount(indexv1connect.NewIndexServiceHandler(&indexersvc.Handler{Service: indexer, Identity: ident, Authz: authorizer}, telemetry.HandlerOptions()...))
	mount(enginev1connect.NewEngineServiceHandler(engineHandler, telemetry.HandlerOptions()...))
	bus.Authz = authorizer
	bus.Lookup = func(ctx context.Context, id string) (engine.Process, bool) {
		p, err := e.Store.Get(ctx, id)
		if err != nil || p == nil {
			return engine.Process{}, false
		}
		return *p, true
	}
	mount(eventsv1connect.NewEventServiceHandler(&eventsvc.Handler{Hub: bus, Identity: ident}, telemetry.HandlerOptions()...))
	// single process: the platform is up when this answers (the gateway serves it otherwise)
	srv.Echo.GET("/api/status", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC(),
			"services": []map[string]any{{"name": "goap-dev", "status": "up", "latencyMs": 0}}})
	})
	serveWeb(log, srv.Echo, platform.Env("GOAP_WEB_DIR", "web/dist"))
	if runtime != nil {
		mount(runtimev1connect.NewRuntimeServiceHandler(runtime, telemetry.HandlerOptions()...))
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
