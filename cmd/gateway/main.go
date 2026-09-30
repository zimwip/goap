// Command gateway is the single entry point of the platform.
package main

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	credentialsv1 "github.com/zimwip/goap/gen/goap/credentials/v1"
	"github.com/zimwip/goap/gen/goap/credentials/v1/credentialsv1connect"
	"github.com/zimwip/goap/internal/gateway"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/access"
)

// credentialsClient adapts the credentials Connect client to gateway.Credentials.
type credentialsClient struct {
	rpc credentialsv1connect.CredentialsServiceClient
}

func (c credentialsClient) Register(ctx context.Context, subject, password string) error {
	_, err := c.rpc.Register(ctx, connect.NewRequest(&credentialsv1.RegisterRequest{Subject: subject, Password: password}))
	return err
}

func (c credentialsClient) Verify(ctx context.Context, subject, password string) (bool, error) {
	r, err := c.rpc.Verify(ctx, connect.NewRequest(&credentialsv1.VerifyRequest{Subject: subject, Password: password}))
	if err != nil {
		return false, err
	}
	return r.Msg.GetOk(), nil
}

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
			{Prefix: "/goap.preferences.v1.PreferencesService/", Upstream: platform.Env("GOAP_PREFERENCES_URL", "http://localhost:8087")},
			{Prefix: "/goap.index.v1.IndexService/", Upstream: platform.Env("GOAP_INDEXER_URL", "http://localhost:8086")},
			{Prefix: "/goap.mcp.v1.McpService/", Upstream: platform.Env("GOAP_MCP_URL", "http://localhost:8085")},
		},
	}
	// who a caller is comes from the User nodes of the graph as well as from its token
	cfg.Enrich = (&access.Directory{Graph: graphsvc.NewClient(platform.H2CClient(), platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), telemetry.ClientOptions()...)}).Enrich
	if origins := platform.Env("GOAP_CORS_ORIGINS", ""); origins != "" {
		cfg.AllowOrigins = strings.Split(origins, ",")
	}
	if cfg.AuthMode == "hs256" || cfg.AuthMode == "local" {
		secret, err := secrets.Get(ctx, "goap/gateway#jwt_secret", "GOAP_JWT_SECRET")
		if err != nil {
			platform.Fatal(log, "jwt secret", err)
		}
		cfg.JWTSecret = []byte(secret)
	}
	if cfg.AuthMode == "local" {
		cfg.Credentials = credentialsClient{rpc: credentialsv1connect.NewCredentialsServiceClient(platform.H2CClient(),
			platform.Env("GOAP_CREDENTIALS_URL", "http://localhost:8088"), telemetry.ClientOptions()...)}
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
