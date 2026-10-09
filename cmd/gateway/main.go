// Command gateway is the single entry point of the platform.
package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"

	credentialsv1 "github.com/zimwip/goap/gen/goap/credentials/v1"
	"github.com/zimwip/goap/gen/goap/credentials/v1/credentialsv1connect"
	"github.com/zimwip/goap/internal/credsvc"
	"github.com/zimwip/goap/internal/gateway"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
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

func (c credentialsClient) StartSession(ctx context.Context, subject string, maxAge time.Duration) (string, error) {
	r, err := c.rpc.StartSession(ctx, connect.NewRequest(&credentialsv1.StartSessionRequest{Subject: subject, MaxAgeSeconds: int64(maxAge / time.Second)}))
	if err != nil {
		return "", err
	}
	return r.Msg.GetId(), nil
}

func (c credentialsClient) SessionActive(ctx context.Context, id, subject string) (bool, error) {
	r, err := c.rpc.CheckSession(ctx, connect.NewRequest(&credentialsv1.CheckSessionRequest{Id: id, Subject: subject}))
	if err != nil {
		return false, err
	}
	return r.Msg.GetActive(), nil
}

func (c credentialsClient) EndSession(ctx context.Context, id string) error {
	_, err := c.rpc.EndSession(ctx, connect.NewRequest(&credentialsv1.EndSessionRequest{Id: id}))
	return err
}

func (c credentialsClient) EndSessions(ctx context.Context, subject string) error {
	_, err := c.rpc.EndSessions(ctx, connect.NewRequest(&credentialsv1.EndSessionsRequest{Subject: subject}))
	return err
}

func main() {
	ctx := context.Background()
	log := platform.Logger("gateway")
	defer telemetry.Setup(context.Background(), log, "gateway")(context.Background())
	secrets := platform.NewSecrets()
	cfg := gateway.Config{
		AuthMode:  platform.Env("GOAP_AUTH_MODE", gateway.DefaultAuthMode),
		DevTokens: platform.Env("GOAP_DEV_TOKENS", "") == "true",
		// a token lives GOAP_TOKEN_TTL and is refreshed by the web up to GOAP_SESSION_MAX after the sign-in
		TokenTTL:   platform.EnvDuration("GOAP_TOKEN_TTL", gateway.DefaultTokenTTL),
		MaxSession: platform.EnvDuration("GOAP_SESSION_MAX", gateway.DefaultMaxSession),
		Routes: []gateway.Route{
			{Prefix: "/goap.graph.v1.GraphService/", Upstream: platform.Env("GOAP_GRAPH_URL", "http://localhost:8081")},
			{Prefix: "/goap.change.v1.ChangeService/", Upstream: platform.Env("GOAP_GRAPH_URL", "http://localhost:8081")},
			{Prefix: "/goap.registry.v1.RegistryService/", Upstream: platform.Env("GOAP_REGISTRY_URL", "http://localhost:8082")},
			{Prefix: "/goap.engine.v1.EngineService/", Upstream: platform.Env("GOAP_ENGINE_URL", "http://localhost:8083")},
			{Prefix: "/goap.model.v1.ModelService/", Upstream: platform.Env("GOAP_MODELGW_URL", "http://localhost:8084")},
			{Prefix: "/goap.preferences.v1.PreferencesService/", Upstream: platform.Env("GOAP_PREFERENCES_URL", "http://localhost:8087")},
			{Prefix: "/goap.conversations.v1.ConversationService/", Upstream: platform.Env("GOAP_CONVERSATIONS_URL", "http://localhost:8090")},
			{Prefix: "/goap.assistant.v1.AssistantService/", Upstream: platform.Env("GOAP_CONVERSATIONS_URL", "http://localhost:8090")},
			{Prefix: "/goap.index.v1.IndexService/", Upstream: platform.Env("GOAP_INDEXER_URL", "http://localhost:8086")},
			{Prefix: "/goap.events.v1.EventService/", Upstream: platform.Env("GOAP_EVENTS_URL", "http://localhost:8089")},
			{Prefix: "/goap.mcp.v1.McpService/", Upstream: platform.Env("GOAP_MCP_URL", "http://localhost:8085")},
		},
	}
	// who a caller is comes from the User nodes of the graph as well as from its token
	graphClient := graphsvc.NewClient(platform.H2CClient(), platform.Env("GOAP_GRAPH_URL", "http://localhost:8081"), telemetry.ClientOptions()...)
	directory := &access.Directory{Graph: graphClient}
	cfg.Enrich = directory.Enrich
	authorizer, err := access.NewAuthorizer(directory)
	if err != nil {
		platform.Fatal(log, "authorizer", err)
	}
	cfg.Session = func(ctx context.Context, p authz.Principal) (any, error) { return authorizer.Session(ctx, p) }
	cfg.ProjectAccess = directory.MayAccessProject
	if origins := platform.Env("GOAP_CORS_ORIGINS", ""); origins != "" {
		cfg.AllowOrigins = strings.Split(origins, ",")
	}
	mode, err := gateway.LookupMode(cfg.AuthMode)
	if err != nil {
		platform.Fatal(log, "auth mode", err)
	}
	if mode.VerifiesTokens() {
		secret, err := secrets.Get(ctx, "goap/gateway#jwt_secret", "GOAP_JWT_SECRET")
		if err != nil {
			platform.Fatal(log, "jwt secret", err)
		}
		cfg.JWTSecret = []byte(secret)
	}
	if mode.Sessions() {
		// the credentials service answers the gateway only, which presents its shared service credential
		credToken, err := secrets.Get(ctx, "goap/credentials#service_token", "GOAP_CREDENTIALS_TOKEN")
		if err != nil || credToken == "" {
			platform.Fatal(log, "credentials service token (GOAP_CREDENTIALS_TOKEN)", errors.Join(err, errors.New("not set")))
		}
		cfg.Credentials = credentialsClient{rpc: credentialsv1connect.NewCredentialsServiceClient(platform.H2CClient(),
			platform.Env("GOAP_CREDENTIALS_URL", "http://localhost:8088"), append(telemetry.ClientOptions(), credsvc.ClientToken(credToken))...)}
		// the user exists in the graph from their first sign-in (ADR 0042), and their first calls see it
		cfg.OnSignIn = func(ctx context.Context, subject string) error {
			if err := graphClient.DeclareUser(ctx, subject); err != nil {
				return err
			}
			return directory.Refresh(ctx)
		}
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
