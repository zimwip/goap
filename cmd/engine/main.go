// Command engine runs agent processes.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	triggerevents "github.com/zimwip/goap/pkg/events"
	"os"
	"sync/atomic"
	"time"

	"github.com/zimwip/goap/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/gen/goap/runtime/v1/runtimev1connect"
	"github.com/zimwip/goap/internal/enginesvc"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/internal/sandbox"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/risk"
	"github.com/zimwip/goap/pkg/selfimprove"
	"github.com/zimwip/goap/pkg/typecat"
	"github.com/zimwip/goap/pkg/verify"
)

func main() {
	ctx := context.Background()
	// the facts of the risk register are items of a change (ADR 0065)
	risk.Register()
	verify.Register()
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
	authorizer, err := access.NewAuthorizer(&access.Directory{Graph: graphsvc.NewClient(hc, platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), copts...)})
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	registry := registrysvc.NewClient(hc, platform.Env("GOAP_REGISTRY_URL", "http://localhost:8082"), copts...)
	// the type catalogue (ADR 0012): the ancestors behind x.types, from the registry, reloaded on its domain events
	types := typecat.NewLive(registry.Domains)
	go func() {
		for delay := time.Second; types.Reload(ctx) != nil; delay = min(2*delay, time.Minute) {
			time.Sleep(delay)
		}
	}()
	builtins := engine.DefaultBuiltins()
	e := &engine.Engine{
		Graph:         graphsvc.NewClient(hc, platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), copts...),
		Methodologies: registry,
		Executors: map[string]engine.Executor{
			methodology.KindLLM:     engine.LLMExecutor{Client: models},
			methodology.KindScript:  engine.ScriptExecutor{Sandboxes: sandboxes},
			methodology.KindHuman:   engine.HumanExecutor{},
			methodology.KindBuiltin: builtins,
			methodology.KindTool:    engine.ToolExecutor{},
		},
		Intent:    intent.Resolver{Ranker: ranker},
		Store:     engine.NewMemoryStore(), // PostgreSQL store: milestone M1
		Events:    publisher,
		Scope:     engine.AuthzScope{Authz: authorizer, Hub: mcpsvc.NewClient(hc, platform.Env("GOAP_MCP_URL", "http://localhost:8085"), copts...)},
		LLM:       models,
		Sandboxes: sandboxes,
		Tracer:    telemetry.NewEngineTracer(),
		Log:       log,
		MaxSteps:  platform.EnvInt("GOAP_MAX_STEPS", 50),
		Types:     func() def.TypeSet { return types.Get() },
	}
	// self-observation (methodology-improvement): journal, traces, drafts
	selfimprove.Register(builtins, e, telemetry.SelfImprovementFromEnv(registry))
	for _, subject := range []string{"goap.registry.domain.published", "goap.registry.domain.deleted"} {
		if err := events.Subscribe(subject, func([]byte) {
			if err := types.Reload(context.Background()); err != nil {
				log.Error("type catalogue", "err", err)
			}
		}); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	// triggers: agents run automatically on events and schedules
	var triggers *engine.TriggerManager
	if platform.Env("GOAP_TRIGGERS", "on") == "on" {
		triggers = &engine.TriggerManager{Engine: e, Log: log}
		if events != nil {
			// several replicas may run this binary; only one may fire schedule
			// (cron) triggers at a time (event triggers are instead deduplicated
			// below by binding every replica to the same durable consumer name).
			host, _ := os.Hostname()
			holder := fmt.Sprintf("%s-%d", host, os.Getpid())
			if lease, err := events.NewLease(ctx, "goap-leases"); err != nil {
				log.Warn("trigger leadership lease", "err", err)
			} else {
				const ttl = 30 * time.Second
				var leader atomic.Bool
				renew := func() {
					held, err := lease.Acquire(ctx, "trigger-manager", holder, ttl)
					if err != nil {
						log.Warn("trigger leadership", "err", err)
						return
					}
					leader.Store(held)
				}
				renew()
				go func() {
					tick := time.NewTicker(ttl / 3)
					defer tick.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-tick.C:
							renew()
						}
					}
				}()
				triggers.Leader = leader.Load
			}
			go func() {
				handle := func(ctx context.Context, subject string, data []byte) error {
					if subject == "goap.registry.methodology.published" {
						var m struct{ Name, Version string }
						_ = json.Unmarshal(data, &m)
						triggers.Handle(ctx, engine.TriggerEvent{Type: triggerevents.MethodologyPublished, Methodology: m.Name, Version: m.Version})
						return nil
					}
					var ev domain.ChangeEvent
					if json.Unmarshal(data, &ev) == nil && ev.Type != "" {
						triggers.Handle(ctx, engine.TriggerEventOf(ev))
					}
					return nil
				}
				if err := events.ConsumeDurable(ctx, log, "trigger-manager", []string{"goap.change.>", "goap.registry.methodology.published"}, handle); err != nil {
					log.Error("trigger events consumer", "err", err)
				}
			}()
		}
		triggers.Start(ctx)
		go triggers.WatchProcesses(ctx, broker)
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
