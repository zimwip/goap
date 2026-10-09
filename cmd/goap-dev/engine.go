package main

import (
	"maps"

	"github.com/zimwip/goap/internal/connectors/builtin"
	"github.com/zimwip/goap/internal/enginesvc"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/internal/sandbox"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/selfimprove"
)

// enginePart is the engine, its triggers and the handler the IDE and the built-in connectors call it through.
type enginePart struct {
	e       *engine.Engine
	handler *enginesvc.Handler
	runtime *sandbox.Runtime // nil unless GOAP_SANDBOX runs scripts out of process
}

// buildEngine creates the engine, registers the builtins and starts the triggers (the late binding of env.triggers
// is set here), then adds the built-in connectors to the hub, which keeps them registered.
func buildEngine(e *env, st stores, gp *graphPart, rp *registryPart, pp *platformPart) (*enginePart, error) {
	g := gp.g
	// the engine calls the gateway in-process, without an identity: trusted
	models := telemetry.LLMClient{Next: llm.ClientFunc(pp.gw.Complete)}
	// scripts run in-process unless GOAP_SANDBOX selects a provisioner
	sandboxes, runtime, _, err := sandbox.FromEnv(e.log, platform.H2CClient(), telemetry.ClientOptions())
	if err != nil {
		return nil, wrap("sandbox", err)
	}
	broker := engine.NewBroker()
	builtins := engine.DefaultBuiltins()
	eg := &engine.Engine{
		Graph:         engine.EventingGraph{GraphPort: g, OnEvent: e.triggers.onChange},
		Methodologies: rp.reg,
		Executors: map[string]engine.Executor{
			methodology.KindLLM:     engine.LLMExecutor{Client: models},
			methodology.KindScript:  engine.ScriptExecutor{Sandboxes: sandboxes},
			methodology.KindHuman:   engine.HumanExecutor{},
			methodology.KindBuiltin: builtins,
			methodology.KindTool:    engine.ToolExecutor{},
		},
		Intent:    intent.Resolver{Ranker: intent.Lexical{}},
		Store:     st.processes,
		Events:    engine.Publishers{broker, rp.bus},
		Scope:     engine.AuthzScope{Authz: gp.authorizer, Hub: mcpsvc.HubPort{Service: pp.hub}},
		LLM:       models,
		Sandboxes: sandboxes,
		Tracer:    telemetry.NewEngineTracer(),
		Log:       e.log,
		Types:     func() def.TypeSet { return rp.types.Get() },
		// the lifecycle of a change is its methodology's, run by the engine (ADR 0058, 0098)
		Lifecycles:           rp.reg,
		TransitionAuthorizer: graphsvc.ChangeTransitionAuthorizer(gp.authorizer),
	}
	e.guardian.set(engine.Guardian{Engine: eg, Next: registrysvc.Guardian{Service: rp.reg, Directory: gp.directory}})
	// self-observation (methodology-improvement): journal, traces, drafts
	selfimprove.Register(builtins, eg, telemetry.SelfImprovementFromEnv(registrysvc.Drafts{Service: rp.reg}))
	triggers := &engine.TriggerManager{Engine: eg, Log: e.log}
	e.triggers.set(triggers)
	triggers.Start(e.ctx)
	go triggers.WatchProcesses(e.ctx, broker)
	handler := &enginesvc.Handler{Engine: eg, Log: e.log, DefaultPrincipal: e.dev, Authz: gp.authorizer, Broker: broker, Triggers: triggers}
	// the built-in connectors call the platform in-process, for the caller of the tool
	maps.Copy(pp.connectors, builtin.Connectors(builtin.Ports{Graph: engine.EventingGraph{GraphPort: g, OnEvent: e.triggers.onChange}, Engine: handler,
		Hub: pp.hub, Registry: rp.reg, Authz: gp.authorizer, Floor: gp.authorizer.Floor()}))
	pp.hub.KeepRegistered(e.ctx, pp.connectors)
	return &enginePart{e: eg, handler: handler, runtime: runtime}, nil
}
