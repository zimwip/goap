// Command modelgw is the LLM model gateway.
package main

import (
	"context"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/gen/goap/model/v1/modelv1connect"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/llmcfg"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("modelgw")
	defer telemetry.Setup(context.Background(), log, "modelgw")(context.Background())
	secrets := platform.NewSecrets()
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var store modelgw.Store = modelgw.NewMemoryStore()
	if pool := platform.OptionalPostgres(ctx, log, modelgw.Migrations); pool != nil {
		defer pool.Close()
		store = modelgw.SQLStore{DB: stdlib.OpenDBFromPool(pool), Dollar: true}
		srv.Readiness(pool.Ping)
	}
	hc := platform.H2CClient()
	graphURL := platform.Env("GOAP_GRAPH_URL", "http://localhost:8081")
	// providers, models and aliases are nodes of the graph; API keys are references resolved here
	svc := modelgw.NewService(&llmcfg.Directory{Graph: graphsvc.NewClient(hc, graphURL, telemetry.ClientOptions()...)}, store, secrets.Resolve, log)
	svc.Router.Instrument = telemetry.NewGenAI().Instrument
	if err := svc.Reload(ctx); err != nil {
		log.Warn("model configuration not read yet", "err", err)
	}
	names, targets, providers := svc.Router.Aliases()
	for _, n := range names {
		log.Info("model alias", "alias", n, "provider", targets[n].Provider, "model", targets[n].Model)
	}
	log.Info("providers", "providers", providers)

	iam, err := access.NewAuthorizer(&access.Directory{Graph: graphsvc.NewClient(hc, graphURL)})
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	srv.Mount(modelv1connect.NewModelServiceHandler(&modelgw.Handler{Service: svc, Authz: iam}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
