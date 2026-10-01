// Command modelgw is the LLM model gateway.
package main

import (
	"context"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/gen/goap/model/v1/modelv1connect"
	"github.com/zimwip/goap/internal/iamsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("modelgw")
	defer telemetry.Setup(context.Background(), log, "modelgw")(context.Background())
	secrets := platform.NewSecrets()
	var cfg modelgw.Config
	if path := platform.Env("GOAP_MODELS_CONFIG", ""); path != "" {
		c, err := modelgw.LoadConfig(path)
		if err != nil {
			platform.Fatal(log, "models config", err)
		}
		cfg = c
	} else {
		key, _ := secrets.Get(ctx, "goap/modelgw#anthropic_api_key", "ANTHROPIC_API_KEY")
		cfg = modelgw.DefaultConfig(key != "")
	}
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var store modelgw.Store = modelgw.NewMemoryStore()
	if pool := platform.OptionalPostgres(ctx, log, modelgw.Migrations); pool != nil {
		defer pool.Close()
		store = modelgw.SQLStore{DB: stdlib.OpenDBFromPool(pool), Dollar: true}
		srv.Readiness(pool.Ping)
	}
	secret, _ := secrets.Get(ctx, "goap/modelgw#encryption_key", "GOAP_SECRET_KEY")
	if secret == "" {
		log.Warn("GOAP_SECRET_KEY not set: provider API keys are encrypted with a well-known development key")
		secret = modelgw.DevSecret
	}
	svc := modelgw.NewService(store, modelgw.NewBox(secret), log)
	svc.Router.Instrument = telemetry.NewGenAI().Instrument
	if err := svc.Bootstrap(ctx, cfg, secrets.Get); err != nil {
		platform.Fatal(log, "models", err)
	}
	names, targets, providers := svc.Router.Aliases()
	for _, n := range names {
		log.Info("model alias", "alias", n, "provider", targets[n].Provider, "model", targets[n].Model)
	}
	log.Info("providers", "providers", providers)

	iam := iamsvc.NewClient(platform.H2CClient(), platform.Env("GOAP_IAM_URL", "http://localhost:8086"))
	srv.Mount(modelv1connect.NewModelServiceHandler(&modelgw.Handler{Service: svc, Authz: iam}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
