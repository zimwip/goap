// Command gateway is the single entry point of the platform.
package main

import (
	"context"
	"strings"

	"github.com/zimwip/goap/internal/gateway"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
)

func main() {
	ctx := context.Background()
	log := platform.Logger("gateway")
	defer telemetry.Setup(context.Background(), log, "gateway")(context.Background())
	secrets := platform.NewSecrets()
	cfg := gateway.Config{
		AuthMode:  platform.Env("GOAP_AUTH_MODE", "none"),
		DevTokens: platform.Env("GOAP_DEV_TOKENS", "") == "true",
		Routes: []gateway.Route{
			{Prefix: "/goap.graph.v1.GraphService/", Upstream: platform.Env("GOAP_GRAPH_URL", "http://localhost:8081")},
			{Prefix: "/goap.registry.v1.RegistryService/", Upstream: platform.Env("GOAP_REGISTRY_URL", "http://localhost:8082")},
			{Prefix: "/goap.engine.v1.EngineService/", Upstream: platform.Env("GOAP_ENGINE_URL", "http://localhost:8083")},
			{Prefix: "/goap.model.v1.ModelService/", Upstream: platform.Env("GOAP_MODELGW_URL", "http://localhost:8084")},
			{Prefix: "/goap.mcp.v1.McpService/", Upstream: platform.Env("GOAP_MCP_URL", "http://localhost:8085")},
			{Prefix: "/goap.iam.v1.IamService/", Upstream: platform.Env("GOAP_IAM_URL", "http://localhost:8086")},
		},
	}
	if origins := platform.Env("GOAP_CORS_ORIGINS", ""); origins != "" {
		cfg.AllowOrigins = strings.Split(origins, ",")
	}
	if cfg.AuthMode == "hs256" {
		secret, err := secrets.Get(ctx, "goap/gateway#jwt_secret", "GOAP_JWT_SECRET")
		if err != nil {
			platform.Fatal(log, "jwt secret", err)
		}
		cfg.JWTSecret = []byte(secret)
	}
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	if err := gateway.Mount(srv.Echo, cfg); err != nil {
		platform.Fatal(log, "gateway", err)
	}
	log.Info("auth", "mode", cfg.AuthMode)
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
