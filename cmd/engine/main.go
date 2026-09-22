// Command engine runs agent processes.
package main

import (
	"context"

	"github.com/zimwip/goap/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/internal/enginesvc"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/methodology"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("engine")
	hc := platform.H2CClient()
	models := modelgw.NewClient(hc, platform.Env("GOAP_MODELGW_URL", "http://localhost:8084"))
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()

	var ranker intent.Ranker = intent.Lexical{}
	if platform.Env("GOAP_INTENT_RANKER", "lexical") == "llm" {
		ranker = intent.LLMRanker{Client: models, Model: platform.Env("GOAP_INTENT_MODEL", "fast")}
	}
	e := &engine.Engine{
		Graph:         graphsvc.NewClient(hc, platform.Env("GOAP_GRAPH_URL", "http://localhost:8081")),
		Methodologies: registrysvc.NewClient(hc, platform.Env("GOAP_REGISTRY_URL", "http://localhost:8082")),
		Executors: map[string]engine.Executor{
			methodology.KindLLM:     engine.LLMExecutor{Client: models},
			methodology.KindHuman:   engine.HumanExecutor{},
			methodology.KindBuiltin: engine.DefaultBuiltins(),
			// methodology.KindTool: MCP connector (milestone M3)
		},
		Intent:   intent.Resolver{Ranker: ranker},
		Store:    engine.NewMemoryStore(), // PostgreSQL store: milestone M1
		Events:   events,
		Log:      log,
		MaxSteps: platform.EnvInt("GOAP_MAX_STEPS", 50),
	}
	if events == nil {
		e.Events = engine.NopPublisher{}
	}
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	srv.Readiness(events.Ready)
	srv.Mount(enginev1connect.NewEngineServiceHandler(&enginesvc.Handler{Engine: e, Log: log}))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
