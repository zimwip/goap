// Command goap-dev runs graph, registry, iam, model gateway and engine in a
// single process, for local development without containers. Storage is
// in-memory (GOAP_STORE=memory, default) or a local SQLite file
// (GOAP_STORE=sqlite, GOAP_SQLITE_PATH) that keeps the graph, methodologies,
// policies and processes across restarts. When GOAP_WEB_DIR (default
// web/dist) holds a built IDE, it is served too. Callers act as the principal
// GOAP_DEV_SUBJECT / GOAP_DEV_ROLES unless the request carries X-Goap-*
// identity headers.
package main

import (
	"context"
	"maps"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"strings"

	"github.com/zimwip/goap/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/gen/goap/iam/v1/iamv1connect"
	"github.com/zimwip/goap/gen/goap/model/v1/modelv1connect"
	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/gen/goap/runtime/v1/runtimev1connect"
	"github.com/zimwip/goap/internal/enginesvc"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/iamsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/internal/sandbox"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/metamodel"
	"github.com/zimwip/goap/pkg/methodology"
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
	authorizer, err := authz.NewCasbin(st.policies)
	if err != nil {
		platform.Fatal(log, "casbin", err)
	}
	g := graph.New(st.graph)
	if _, err := graphsvc.SeedDemo(ctx, g); err != nil {
		platform.Fatal(log, "seed", err)
	}
	var triggers *engine.TriggerManager
	reg := &registrysvc.Service{Store: st.methodologies, Authz: authorizer}
	// publications are projected onto the domain graph (the methodology as
	// versioned elements) and reload the triggers
	reg.Events = registryEvents(func(ctx context.Context, name, version string) {
		r, err := st.methodologies.Get(ctx, name, version)
		if err != nil {
			log.Error("published methodology", "name", name, "err", err)
			return
		}
		if res, err := metamodel.Sync(ctx, g, &r.Methodology); err != nil {
			log.Error("methodology projection", "name", name, "err", err)
		} else if res.Changed() {
			log.Info("methodology projected onto the graph", "name", name, "version", version, "change", res.Change)
		}
		if triggers != nil {
			triggers.Handle(ctx, engine.TriggerEvent{Type: "methodology.published", Methodology: name, Version: version})
		}
	})
	system := authz.With(ctx, authz.Principal{Subject: "system:registry", Roles: []string{"admin"}})
	if _, err := reg.Seed(system, platform.Env("GOAP_METHODOLOGIES_DIR", "methodologies")); err != nil {
		platform.Fatal(log, "methodologies", err)
	}
	if _, err := metamodel.SyncAll(ctx, g, reg); err != nil {
		platform.Fatal(log, "methodology projection", err)
	}
	key, _ := secrets.Get(ctx, "", "ANTHROPIC_API_KEY")
	router, err := modelgw.Build(ctx, modelgw.DefaultConfig(key != ""), secrets.Get)
	if err != nil {
		platform.Fatal(log, "models", err)
	}
	router.Instrument = telemetry.NewGenAI().Instrument
	models := telemetry.LLMClient{Next: router}
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
	builtins := engine.DefaultBuiltins()
	e := &engine.Engine{
		Graph:         engine.EventingGraph{GraphPort: g, OnEvent: onChange},
		Methodologies: reg,
		Executors: map[string]engine.Executor{
			methodology.KindLLM:     engine.LLMExecutor{Client: models},
			methodology.KindScript:  engine.ScriptExecutor{Sandboxes: sandboxes},
			methodology.KindHuman:   engine.HumanExecutor{},
			methodology.KindBuiltin: builtins,
		},
		Intent:    intent.Resolver{Ranker: intent.Lexical{}},
		Store:     st.processes,
		Events:    broker,
		Authz:     authorizer,
		LLM:       models,
		Sandboxes: sandboxes,
		Tracer:    telemetry.NewEngineTracer(),
		Log:       log,
	}
	// self-observation (methodology-improvement): journal, traces, drafts
	maps.Copy(builtins, e.SelfImprovementBuiltins(telemetry.SelfImprovementFromEnv(registrysvc.Drafts{Service: reg})))
	triggers = &engine.TriggerManager{Engine: e, Log: log}
	triggers.Start(ctx)
	go triggers.WatchProcesses(ctx, broker)
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	srv.Mount(graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Graph: g, Events: changePublisher(onChange)}, telemetry.HandlerOptions()...))
	srv.Mount(registryv1connect.NewRegistryServiceHandler(&registrysvc.Handler{Service: reg, Identity: ident}, telemetry.HandlerOptions()...))
	srv.Mount(iamv1connect.NewIamServiceHandler(&iamsvc.Handler{Enforcer: authorizer, Identity: ident}, telemetry.HandlerOptions()...))
	srv.Mount(modelv1connect.NewModelServiceHandler(&modelgw.Handler{Router: router}, telemetry.HandlerOptions()...))
	srv.Mount(enginev1connect.NewEngineServiceHandler(&enginesvc.Handler{Engine: e, Log: log, DefaultPrincipal: &dev, Authz: authorizer, Broker: broker, Triggers: triggers}, telemetry.HandlerOptions()...))
	// single process: the platform is up when this answers (the gateway serves it otherwise)
	srv.Echo.GET("/api/status", func(c echo.Context) error {
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
