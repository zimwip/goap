// Command goap-dev runs graph, registry, iam, model gateway and engine in a
// single process with in-memory storage, for local development without
// containers. Callers act as the principal GOAP_DEV_SUBJECT / GOAP_DEV_ROLES
// unless the request carries X-Goap-* identity headers.
package main

import (
	"context"
	"strings"

	"github.com/zimwip/goap/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/gen/goap/iam/v1/iamv1connect"
	"github.com/zimwip/goap/gen/goap/model/v1/modelv1connect"
	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/internal/enginesvc"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/iamsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/methodology"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("goap-dev")
	secrets := platform.NewSecrets()
	dev := authz.Principal{
		Subject: platform.Env("GOAP_DEV_SUBJECT", "dev"),
		Org:     platform.Env("GOAP_DEV_ORG", "dev"),
		Roles:   strings.Split(platform.Env("GOAP_DEV_ROLES", "admin"), ","),
	}
	ident := identity.Extractor{Default: &dev}

	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		platform.Fatal(log, "casbin", err)
	}
	g := graph.New(graph.NewMemory())
	if _, err := graphsvc.SeedDemo(ctx, g); err != nil {
		platform.Fatal(log, "seed", err)
	}
	reg := &registrysvc.Service{Store: registrysvc.NewMemoryStore(), Authz: authorizer}
	system := authz.With(ctx, authz.Principal{Subject: "system:registry", Roles: []string{"admin"}})
	if _, err := reg.Seed(system, platform.Env("GOAP_METHODOLOGIES_DIR", "methodologies")); err != nil {
		platform.Fatal(log, "methodologies", err)
	}
	key, _ := secrets.Get(ctx, "", "ANTHROPIC_API_KEY")
	router, err := modelgw.Build(ctx, modelgw.DefaultConfig(key != ""), secrets.Get)
	if err != nil {
		platform.Fatal(log, "models", err)
	}
	e := &engine.Engine{
		Graph:         g,
		Methodologies: reg,
		Executors: map[string]engine.Executor{
			methodology.KindLLM:     engine.LLMExecutor{Client: router},
			methodology.KindHuman:   engine.HumanExecutor{},
			methodology.KindBuiltin: engine.DefaultBuiltins(),
		},
		Intent: intent.Resolver{Ranker: intent.Lexical{}},
		Store:  engine.NewMemoryStore(),
		Events: engine.NopPublisher{},
		Authz:  authorizer,
		Log:    log,
	}
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	srv.Mount(graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Graph: g}))
	srv.Mount(registryv1connect.NewRegistryServiceHandler(&registrysvc.Handler{Service: reg, Identity: ident}))
	srv.Mount(iamv1connect.NewIamServiceHandler(&iamsvc.Handler{Enforcer: authorizer, Identity: ident}))
	srv.Mount(modelv1connect.NewModelServiceHandler(&modelgw.Handler{Router: router}))
	srv.Mount(enginev1connect.NewEngineServiceHandler(&enginesvc.Handler{Engine: e, Log: log, DefaultPrincipal: &dev, Authz: authorizer}))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
