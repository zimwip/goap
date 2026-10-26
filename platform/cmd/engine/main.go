// Command engine runs agent processes.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	triggerevents "github.com/zimwip/goap/contracts/events"

	"github.com/zimwip/goap/contracts/assess"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/contracts/gen/goap/runtime/v1/runtimev1connect"
	"github.com/zimwip/goap/contracts/llm"
	"github.com/zimwip/goap/contracts/risk"
	"github.com/zimwip/goap/contracts/verify"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/enginesvc"
	"github.com/zimwip/goap/engine/enginetel"
	"github.com/zimwip/goap/engine/intent"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/engine/sandbox"
	"github.com/zimwip/goap/engine/selfimprove"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/domainclient"
	"github.com/zimwip/goap/lib/graphclient"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/lib/typecat"
	"github.com/zimwip/goap/mcp/mcpsvc"
	"github.com/zimwip/goap/platform/modelgw"
	"github.com/zimwip/goap/platform/registrysvc"
)

func main() {
	ctx := context.Background()
	// the facts of the risk register are items of a change (ADR 0065)
	risk.Register()
	assess.Register()
	verify.Register()
	log := platform.Logger("engine")
	defer telemetry.Setup(ctx, log, "engine")(ctx)
	hc := platform.H2CClient()
	copts := telemetry.ClientOptions()
	var models llm.Client = enginetel.LLMClient{Next: modelgw.NewClient(hc, platform.Env("GOAP_MODELGW_URL", "http://localhost:8084"), append(slices.Clone(copts), identity.Forward())...)}
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
	directory := &access.Directory{Graph: graphclient.NewClient(hc, platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), copts...)}
	// the policies, the organisation and the projects the platform graph delivers, read again when its baseline
	// events say the organisation moved (ADR 0154 §9)
	if events != nil {
		if err := directory.FollowEvents(events); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	authorizer, err := access.NewAuthorizer(directory)
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	registry := registrysvc.NewClient(hc, platform.Env("GOAP_REGISTRY_URL", "http://localhost:8082"), copts...)
	// the type catalogue (ADR 0012): the ancestors behind x.types, from the domain registry (ADR 0125 §3, ADR 0167)
	types := typecat.NewLive(domainclient.NewClient(hc, platform.Env("GOAP_DOMAINS_URL", "http://localhost:8096"), copts...).Domains)
	go func() {
		// tried again when the domain registry announces it serves (ADR 0169)
		_ = events.Await(ctx, []string{platform.ReadyDomains}, types.Reload, nil)
	}()
	// processes: in memory (one replica), in PostgreSQL (GOAP_DB_DSN), shared by the replicas, which serialize
	// their work on a process through its locks and resume the runs a stopped replica left (ADR 0124), or in a SQLite
	// file (GOAP_SQLITE_PATH, the local stack: one replica, which drops the locks of its previous start to resume its
	// runs at once)
	var store engine.Store = engine.NewMemoryStore()
	var locks engine.Locker
	if db := platform.OptionalSQL(ctx, log, "engine", enginesvc.Migrations, enginesvc.SQLiteMigrations); db != nil {
		defer db.Close()
		shared := enginesvc.SQLStore{DB: db.DB, Dollar: db.Dollar, Holder: enginesvc.NewHolder()}
		if db.SQLite() {
			if err := shared.ReleaseAll(ctx); err != nil {
				platform.Fatal(log, "engine locks", err)
			}
		}
		store, locks = shared, shared
	}
	builtins := engine.DefaultBuiltins()
	e := &engine.Engine{
		Graph:         graphclient.NewClient(hc, platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), copts...),
		Methodologies: registry,
		Executors: map[string]engine.Executor{
			methodology.KindLLM:     engine.LLMExecutor{Client: models},
			methodology.KindScript:  engine.ScriptExecutor{Sandboxes: sandboxes},
			methodology.KindHuman:   engine.HumanExecutor{},
			methodology.KindBuiltin: builtins,
			methodology.KindTool:    engine.ToolExecutor{},
		},
		Intent:                   intent.Resolver{Ranker: ranker},
		Store:                    store,
		Locks:                    locks,
		LockTTL:                  platform.EnvDuration("GOAP_ENGINE_LOCK_TTL", engine.DefaultLockTTL),
		MaxRuns:                  platform.EnvInt("GOAP_ENGINE_MAX_RUNS", 0),
		Events:                   publisher,
		Scope:                    engine.AuthzScope{Authz: authorizer, Hub: mcpsvc.NewClient(hc, platform.Env("GOAP_MCP_URL", "http://localhost:8085"), copts...)},
		LLM:                      models,
		Sandboxes:                sandboxes,
		Tracer:                   enginetel.NewEngineTracer(),
		Log:                      log,
		MaxSteps:                 platform.EnvInt("GOAP_MAX_STEPS", 50),
		MaxParallel:              platform.EnvInt("GOAP_MAX_PARALLEL", 4),
		PersonReviewsModelOutput: platform.EnvBool("GOAP_PERSON_REVIEWS_MODEL_OUTPUT", true), // the oversight floor (EU AI Act Art. 14)
		Types:                    func() def.TypeSet { return types.Get() },
	}
	// the events of the processes go through the outbox of the store (ADR 0136): to the bus, where every replica's
	// broker reads them and one replica's triggers (their durable consumer, below); without a bus, in process
	var local atomic.Pointer[engine.TriggerManager]
	sink := outbox.NATS(events)
	if events == nil {
		sink = outbox.Sinks{
			engine.ProcessEvents(func(_ context.Context, ev engine.ProcessEvent) { broker.Deliver(ev) }),
			engine.ProcessEvents(func(ctx context.Context, ev engine.ProcessEvent) {
				if t := local.Load(); t != nil {
					t.OnProcess(ctx, ev)
				}
			}),
		}
	}
	outbox.Start(ctx, e.Emit(sink), log)
	if locks != nil {
		if events != nil {
			// wake-ups and interruptions reach the other replicas at once (ADR 0124)
			signals := enginesvc.NATSSignals{Bus: events}
			if err := signals.Listen(e); err != nil {
				platform.Fatal(log, "subscribe", err)
			}
			e.Signals = signals
		}
		go e.Recover(ctx, 0)
	}
	// self-observation (methodology-improvement): journal, traces, drafts
	selfimprove.Register(builtins, e, enginetel.SelfImprovementFromEnv(registry))
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
					if strings.HasPrefix(subject, "goap.process.") {
						var ev engine.ProcessEvent
						if json.Unmarshal(data, &ev) == nil && ev.Event != "log" {
							triggers.OnProcess(ctx, ev)
						}
						return nil
					}
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
				if err := events.ConsumeDurable(ctx, log, "trigger-manager", []string{"goap.change.>", "goap.process.>", "goap.registry.methodology.published"}, handle); err != nil {
					log.Error("trigger events consumer", "err", err)
				}
			}()
		}
		triggers.Start(ctx)
	}
	local.Store(triggers)
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
