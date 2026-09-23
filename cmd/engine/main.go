// Command engine runs agent processes.
package main

import (
	"context"
	"encoding/json"
	"maps"
	"time"

	"github.com/zimwip/goap/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/gen/goap/runtime/v1/runtimev1connect"
	"github.com/zimwip/goap/internal/enginesvc"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/iamsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/internal/sandbox"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("engine")
	defer telemetry.Setup(ctx, log, "engine")(ctx)
	hc := platform.H2CClient()
	copts := telemetry.ClientOptions()
	var models llm.Client = telemetry.LLMClient{Next: modelgw.NewClient(hc, platform.Env("GOAP_MODELGW_URL", "http://localhost:8084"), copts...)}
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()

	// live events: fed by NATS when available (several replicas), locally otherwise
	broker := engine.NewBroker()
	var publisher engine.Publisher = broker
	if events != nil {
		publisher = events
		if err := events.Subscribe("goap.process.>", func(data []byte) {
			var ev engine.ProcessEvent
			if json.Unmarshal(data, &ev) == nil {
				broker.Deliver(ev)
			}
		}); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}

	sandboxes, runtime, pool, err := sandbox.FromEnv(log, hc, copts)
	if err != nil {
		platform.Fatal(log, "sandbox", err)
	}
	if pool != nil {
		defer pool.Close()
		go func() {
			for range time.Tick(time.Minute) {
				pool.ReapIdle()
			}
		}()
	}

	var ranker intent.Ranker = intent.Lexical{}
	if platform.Env("GOAP_INTENT_RANKER", "lexical") == "llm" {
		ranker = intent.LLMRanker{Client: models, Model: platform.Env("GOAP_INTENT_MODEL", "fast")}
	}
	authorizer := iamsvc.NewClient(hc, platform.Env("GOAP_IAM_URL", "http://localhost:8086"), copts...)
	registry := registrysvc.NewClient(hc, platform.Env("GOAP_REGISTRY_URL", "http://localhost:8082"), copts...)
	builtins := engine.DefaultBuiltins()
	e := &engine.Engine{
		Graph:         graphsvc.NewClient(hc, platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), copts...),
		Methodologies: registry,
		Executors: map[string]engine.Executor{
			methodology.KindLLM:     engine.LLMExecutor{Client: models},
			methodology.KindScript:  engine.ScriptExecutor{Sandboxes: sandboxes},
			methodology.KindHuman:   engine.HumanExecutor{},
			methodology.KindBuiltin: builtins,
			// methodology.KindTool: MCP connector (milestone M3)
		},
		Intent:    intent.Resolver{Ranker: ranker},
		Store:     engine.NewMemoryStore(), // PostgreSQL store: milestone M1
		Events:    publisher,
		Authz:     authorizer,
		LLM:       models,
		Sandboxes: sandboxes,
		Tracer:    telemetry.NewEngineTracer(),
		Log:       log,
		MaxSteps:  platform.EnvInt("GOAP_MAX_STEPS", 50),
	}
	// self-observation (methodology-improvement): journal, traces, drafts
	maps.Copy(builtins, e.SelfImprovementBuiltins(telemetry.SelfImprovementFromEnv(registry)))
	// triggers: agents run automatically on events and schedules
	var triggers *engine.TriggerManager
	if platform.Env("GOAP_TRIGGERS", "on") == "on" {
		triggers = &engine.TriggerManager{Engine: e, Log: log}
		triggers.Start(ctx)
		go triggers.WatchProcesses(ctx, broker)
		if err := events.Subscribe("goap.change.>", func(data []byte) {
			var ev domain.ChangeEvent
			if json.Unmarshal(data, &ev) == nil && ev.Type != "" {
				triggers.Handle(context.Background(), engine.TriggerEventOf(ev))
			}
		}); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
		if err := events.Subscribe("goap.registry.methodology.published", func(data []byte) {
			var m struct{ Name, Version string }
			_ = json.Unmarshal(data, &m)
			triggers.Handle(context.Background(), engine.TriggerEvent{Type: "methodology.published", Methodology: m.Name, Version: m.Version})
		}); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	srv.Readiness(events.Ready)
	srv.Mount(enginev1connect.NewEngineServiceHandler(&enginesvc.Handler{Engine: e, Log: log, Authz: authorizer, Broker: broker, Triggers: triggers}, telemetry.HandlerOptions()...))
	if runtime != nil {
		// sandboxes call back the engine here (job token authentication)
		srv.Mount(runtimev1connect.NewRuntimeServiceHandler(runtime, telemetry.HandlerOptions()...))
	}
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
