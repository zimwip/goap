package main

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v5"

	"github.com/zimwip/goap/gen/goap/assistant/v1/assistantv1connect"
	"github.com/zimwip/goap/gen/goap/change/v1/changev1connect"
	"github.com/zimwip/goap/gen/goap/conversations/v1/conversationsv1connect"
	"github.com/zimwip/goap/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/gen/goap/events/v1/eventsv1connect"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/gen/goap/index/v1/indexv1connect"
	"github.com/zimwip/goap/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/gen/goap/model/v1/modelv1connect"
	"github.com/zimwip/goap/gen/goap/preferences/v1/preferencesv1connect"
	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/gen/goap/runtime/v1/runtimev1connect"
	"github.com/zimwip/goap/internal/assistantsvc"
	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/internal/credsvc"
	"github.com/zimwip/goap/internal/eventsvc"
	"github.com/zimwip/goap/internal/gateway"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/indexersvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/prefssvc"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/engine"
)

// buildServer creates the HTTP server: sign-in (the auth middleware of the gateway's mode), the Connect handlers of
// every service of the process, the event stream, the status and the built IDE. Nothing listens until it runs.
func buildServer(e *env, st stores, gp *graphPart, rp *registryPart, pp *platformPart, ep *enginePart) (*platform.Server, error) {
	srv := platform.NewServer(e.log, e.cfg.Addr)
	ident := identity.Extractor{Default: e.dev}
	authorizer := gp.authorizer

	// Sign-in (ADR 0040, 0042): local by default (GOAP_AUTH_MODE unset) — this single process is its own
	// identity provider (register/login/logout, no external IdP), the same way cmd/gateway's AuthMode does
	// for the distributed platform, reusing its authenticator and auth endpoints. GOAP_AUTH_MODE=none turns it
	// off for development: every request then acts as the fixed GOAP_DEV_SUBJECT/ORG/ROLES principal. Applied
	// per-route (not e.Use), so the built IDE's static assets and /api/status/health stay reachable with no
	// token.
	authMW, err := buildAuth(e, st, gp, srv)
	if err != nil {
		return nil, err
	}
	mount := func(path string, h http.Handler) {
		if authMW != nil {
			srv.Echo.Any(path+"*", echo.WrapHandler(h), authMW)
			return
		}
		srv.Mount(path, h)
	}

	graphHandler := &graphsvc.Handler{Graph: gp.g, Events: engine.Publishers{changePublisher(e.triggers.onChange), pp.indexSink, rp.bus}, Authz: authorizer, Floor: authorizer.Floor(), Identity: ident}
	graphOpts := append(telemetry.HandlerOptions(), connect.WithInterceptors(graphHandler.Identify(), eventsvc.CommandInterceptor(), graphHandler.PersonalScope(), graphHandler.EnsureCaller()))
	mount(graphv1connect.NewGraphServiceHandler(graphHandler, graphOpts...))
	mount(changev1connect.NewChangeServiceHandler(graphHandler, graphOpts...))
	mount(registryv1connect.NewRegistryServiceHandler(&registrysvc.Handler{Service: rp.reg, Identity: ident}, append(telemetry.HandlerOptions(), connect.WithInterceptors(eventsvc.CommandInterceptor()))...))
	whoami := func(ctx context.Context, p authz.Principal) (any, error) { return authorizer.Session(ctx, p) }
	if authMW != nil {
		srv.Echo.GET("/api/whoami", identity.WhoAmI(identity.Extractor{}, whoami), authMW)
	} else {
		srv.Echo.GET("/api/whoami", identity.WhoAmI(ident, whoami))
	}
	mount(mcpv1connect.NewMcpServiceHandler(&mcpsvc.Handler{Service: pp.hub, Authz: authorizer, Identity: ident, ConnectorToken: pp.connToken}, telemetry.HandlerOptions()...))
	mount(modelv1connect.NewModelServiceHandler(&modelgw.Handler{Service: pp.gw, Identity: ident, Authz: authorizer}, telemetry.HandlerOptions()...))
	mount(preferencesv1connect.NewPreferencesServiceHandler(&prefssvc.Handler{Service: &prefssvc.Service{Store: st.prefs}, Identity: ident}, telemetry.HandlerOptions()...))
	convs := &convsvc.Service{Store: st.convs}
	mount(conversationsv1connect.NewConversationServiceHandler(&convsvc.Handler{Service: convs, Identity: ident}, telemetry.HandlerOptions()...))
	// the assistant (ADR 0087) in process: the real graph (a change it creates starts its triggers like any other), the
	// gateway, the registry, the access directory and the conversation service, all acting for the caller
	assistant := &assistantsvc.Service{Convs: convs, Model: pp.gw, Graph: engine.EventingGraph{GraphPort: gp.g, OnEvent: e.triggers.onChange},
		Methodologies: rp.reg, Projects: assistantsvc.Directory{Directory: gp.directory}, Engine: assistantsvc.EngineClient{API: ep.handler}, Log: e.log}
	mount(assistantv1connect.NewAssistantServiceHandler(&assistantsvc.Handler{Service: assistant, Identity: ident}, telemetry.HandlerOptions()...))
	mount(indexv1connect.NewIndexServiceHandler(&indexersvc.Handler{Service: pp.indexer, Identity: ident, Authz: authorizer}, telemetry.HandlerOptions()...))
	mount(enginev1connect.NewEngineServiceHandler(ep.handler, telemetry.HandlerOptions()...))
	rp.bus.Authz = authorizer
	rp.bus.Lookup = func(ctx context.Context, id string) (engine.Process, bool) {
		p, err := ep.e.Store.Get(ctx, id)
		if err != nil || p == nil {
			return engine.Process{}, false
		}
		return *p, true
	}
	mount(eventsv1connect.NewEventServiceHandler(&eventsvc.Handler{Hub: rp.bus, Identity: ident}, telemetry.HandlerOptions()...))
	// single process: the platform is up when this answers (the gateway serves it otherwise)
	srv.Echo.GET("/api/status", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC(),
			"services": []map[string]any{{"name": "goap-dev", "status": "up", "latencyMs": 0}}})
	})
	serveWeb(e.log, srv.Echo, e.cfg.WebDir)
	if ep.runtime != nil {
		mount(runtimev1connect.NewRuntimeServiceHandler(ep.runtime, telemetry.HandlerOptions()...))
	}
	return srv, nil
}

// buildAuth mounts the sign-in endpoints and returns the authentication middleware of the mode (nil when the mode
// is open: every request then acts as the dev principal).
func buildAuth(e *env, st stores, gp *graphPart, srv *platform.Server) (echo.MiddlewareFunc, error) {
	mode, err := gateway.LookupMode(e.cfg.AuthMode)
	if err != nil {
		return nil, wrap("auth mode", err)
	}
	if mode.Open() {
		return nil, nil
	}
	secret, err := e.secrets.Get(e.ctx, "goap/gateway#jwt_secret", "GOAP_JWT_SECRET")
	if err == nil && secret == "" && mode.Sessions() {
		secret, err = localJWTSecret(st.dir)
	}
	if err != nil {
		return nil, wrap("jwt secret", err)
	}
	authCfg := gateway.Config{AuthMode: e.cfg.AuthMode, JWTSecret: []byte(secret), DevTokens: platform.Env("GOAP_DEV_TOKENS", "") == "true",
		TokenTTL: platform.EnvDuration("GOAP_TOKEN_TTL", gateway.DefaultTokenTTL), MaxSession: platform.EnvDuration("GOAP_SESSION_MAX", gateway.DefaultMaxSession),
		Credentials: &credsvc.Service{Store: st.creds}, Enrich: gp.directory.Enrich, ProjectAccess: gp.directory.MayAccessProject,
		// the user exists in the graph from their first sign-in, member of the unit new users join (ADR 0042)
		OnSignIn: func(ctx context.Context, subject string) error {
			if err := graphsvc.EnsureUser(ctx, gp.g, subject); err != nil {
				return err
			}
			return gp.directory.Refresh(ctx)
		}}
	// one state of the sessions for the endpoints and the authenticator: a sign-out is refused at once (ADR 0045)
	authCfg = gateway.Prepare(authCfg)
	if err := gateway.MountAuthEndpoints(srv.Echo, authCfg); err != nil {
		return nil, wrap("auth", err)
	}
	mw, err := gateway.Authenticator(authCfg)
	if err != nil {
		return nil, wrap("auth", err)
	}
	e.log.Info("local sign-in enabled", "mode", e.cfg.AuthMode)
	return mw, nil
}
