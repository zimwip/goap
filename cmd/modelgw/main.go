// Command modelgw is the LLM model gateway.
package main

import (
	"context"

	"github.com/zimwip/goap/gen/goap/model/v1/modelv1connect"
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
	router, err := modelgw.Build(ctx, cfg, secrets.Get)
	if err != nil {
		platform.Fatal(log, "models", err)
	}
	router.Instrument = telemetry.NewGenAI().Instrument
	names, targets, providers := router.Aliases()
	for _, n := range names {
		log.Info("model alias", "alias", n, "provider", targets[n].Provider, "model", targets[n].Model)
	}
	log.Info("providers", "providers", providers)

	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	srv.Mount(modelv1connect.NewModelServiceHandler(&modelgw.Handler{Router: router}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
