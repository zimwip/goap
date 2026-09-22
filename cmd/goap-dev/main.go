// Command goap-dev runs graph, registry, model gateway and engine in a single
// process with in-memory storage, behind the gateway routes, for local
// development without containers.
package main

import (
	"context"

	"github.com/zimwip/goap/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/gen/goap/model/v1/modelv1connect"
	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/internal/enginesvc"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/methodology"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("goap-dev")
	secrets := platform.NewSecrets()

	g := graph.New(graph.NewMemory())
	if _, err := graphsvc.SeedDemo(ctx, g); err != nil {
		platform.Fatal(log, "seed", err)
	}
	reg := &registrysvc.Handler{Store: registrysvc.NewMemoryStore()}
	if _, err := reg.LoadDir(ctx, platform.Env("GOAP_METHODOLOGIES_DIR", "methodologies")); err != nil {
		platform.Fatal(log, "methodologies", err)
	}
	key, _ := secrets.Get(ctx, "", "ANTHROPIC_API_KEY")
	router, err := modelgw.Build(ctx, modelgw.DefaultConfig(key != ""), secrets.Get)
	if err != nil {
		platform.Fatal(log, "models", err)
	}
	e := &engine.Engine{
		Graph:         g,
		Methodologies: devMethodologies{reg},
		Executors: map[string]engine.Executor{
			methodology.KindLLM:     engine.LLMExecutor{Client: router},
			methodology.KindHuman:   engine.HumanExecutor{},
			methodology.KindBuiltin: engine.DefaultBuiltins(),
		},
		Intent: intent.Resolver{Ranker: intent.Lexical{}},
		Store:  engine.NewMemoryStore(),
		Events: engine.NopPublisher{},
		Log:    log,
	}
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	srv.Mount(graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Graph: g}))
	srv.Mount(registryv1connect.NewRegistryServiceHandler(reg))
	srv.Mount(modelv1connect.NewModelServiceHandler(&modelgw.Handler{Router: router}))
	srv.Mount(enginev1connect.NewEngineServiceHandler(&enginesvc.Handler{Engine: e, Log: log}))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}

// devMethodologies compiles methodologies straight from the in-process registry.
type devMethodologies struct{ reg *registrysvc.Handler }

func (d devMethodologies) Methodology(ctx context.Context, name string) (*methodology.Compiled, error) {
	r, err := d.reg.Store.Get(ctx, name, "")
	if err != nil {
		return nil, engine.ErrUnknownMethodology{Name: name}
	}
	m, err := methodology.Parse([]byte(r.Source))
	if err != nil {
		return nil, err
	}
	return m.Compile()
}
