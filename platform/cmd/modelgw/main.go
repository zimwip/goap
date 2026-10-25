// Command modelgw is the LLM model gateway.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/zimwip/goap/contracts/gen/goap/model/v1/modelv1connect"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/graphclient"
	"github.com/zimwip/goap/lib/graphsnap"
	"github.com/zimwip/goap/lib/llmcfg"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/platform/modelgw"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("modelgw")
	defer telemetry.Setup(context.Background(), log, "modelgw")(context.Background())
	secrets := platform.NewSecrets()
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	var store modelgw.Store = modelgw.NewMemoryStore()
	if db := platform.OptionalSQL(ctx, log, "modelgw", modelgw.Migrations, modelgw.SQLiteMigrations); db != nil {
		defer db.Close()
		store = modelgw.SQLStore{DB: db.DB, Dollar: db.Dollar}
		srv.Readiness(db.Ping)
	}
	hc := platform.H2CClient()
	graphURL := platform.Env("GOAP_GRAPH_URL", "http://localhost:8081")
	// providers, models and aliases are nodes of the graph; API keys are references resolved here; one reader of the
	// heads for the models and the access (ADR 0145)
	graphClient := graphclient.NewClient(hc, graphURL, telemetry.ClientOptions()...)
	reader := graphsnap.NewReader(graphClient)
	svc := modelgw.NewService(&llmcfg.Directory{Graph: graphClient, Reader: reader}, store, secrets.Resolve, log)
	svc.Vault = secrets.Put
	genai := telemetry.NewGenAI()
	svc.Router.Instrument, svc.Router.InstrumentEmbed = genai.Instrument, genai.InstrumentEmbed
	if err := svc.Reload(ctx); err != nil {
		log.Warn("model configuration not read yet", "err", err)
	}
	names, targets, providers := svc.Router.Aliases()
	for _, n := range names {
		log.Info("model alias", "alias", n, "provider", targets[n].Provider, "model", targets[n].Model)
	}
	log.Info("providers", "providers", providers)
	// the providers whose well-known API key variable is set (ANTHROPIC_API_KEY, ...) are signed on with no model (the
	// administrator picks the models in the settings), once the graph has seeded the model configuration
	go signOn(ctx, log, graphClient, svc)

	directory := &access.Directory{Graph: graphClient, Reader: reader}
	iam, err := access.NewAuthorizer(directory)
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	svc.CostTTL = time.Duration(platform.EnvInt("GOAP_LLM_BEHAVIOR_COST_TTL_DAYS", int(modelgw.DefaultCostTTL/(24*time.Hour)))) * 24 * time.Hour // a measured behaviour cost is measured again after this (ADR 0093)
	svc.CallPrompts = platform.EnvBool("GOAP_LLM_CALL_PROMPTS", true)                                                                            // the exchange of the calls no change log keeps (ADR 0089)
	go svc.KeepCalls(ctx, platform.EnvInt("GOAP_LLM_CALL_RETENTION_DAYS", modelgw.DefaultCallRetentionDays))                                     // the ledger of calls (ADR 0089)
	svc.Authz = iam                                                                                                                              // the roles a model requires are held on the caller's project (ADR 0043)
	// its events (a call recorded in the ledger, a quota exceeded) go through its outbox to the bus (ADR 0136)
	events := platform.OptionalEvents(ctx, log)
	defer events.Close()
	// the policies, the organisation and the projects the platform graph delivers, read again when its baseline
	// events say the organisation moved (ADR 0154 §9)
	if events != nil {
		if err := directory.FollowEvents(events); err != nil {
			platform.Fatal(log, "subscribe", err)
		}
	}
	outbox.Start(ctx, svc.Emit(outbox.NATS(events)), log)
	srv.Mount(modelv1connect.NewModelServiceHandler(&modelgw.Handler{Service: svc, Authz: iam}, telemetry.HandlerOptions()...))
	// the Anthropic endpoint where the Claude coding agent's sessions call the model, each call a row of the ledger
	// (ADR 0123 §6); its tokens are minted by the MCP hub with the same secret
	if secret := platform.Env("GOAP_MODEL_PROXY_SECRET", ""); secret != "" {
		srv.Mount((&modelgw.AnthropicProxy{Service: svc, Secret: []byte(secret)}).Route())
	} else {
		log.Info("GOAP_MODEL_PROXY_SECRET not set: no Anthropic endpoint, a connector calling a model by itself stays outside the ledger")
	}
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}

// signOn signs on to the providers of the environment, retrying while the graph is unreachable or not booted yet.
func signOn(ctx context.Context, log *slog.Logger, g modelgw.SignOnGraph, svc *modelgw.Service) {
	detected := modelgw.DetectProviders(os.LookupEnv)
	if len(detected) == 0 {
		return
	}
	for wait := time.Second; ; wait = min(2*wait, time.Minute) {
		names, err := modelgw.SignOn(ctx, g, detected)
		if err == nil {
			if len(names) > 0 {
				log.Info("providers signed on from the environment", "providers", names)
				if err := svc.Reload(ctx); err != nil {
					log.Warn("model configuration not read", "err", err)
				}
			}
			return
		}
		log.Debug("sign on to the providers of the environment: retrying", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
