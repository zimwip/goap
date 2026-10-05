// Command goap-dev runs graph, registry, model gateway and engine in a
// single process, for local development without containers. Storage is
// in-memory (GOAP_STORE=memory, default) or a local SQLite file
// (GOAP_STORE=sqlite, GOAP_SQLITE_PATH) that keeps the graph, methodologies,
// access policies (graph nodes) and processes across restarts. When GOAP_WEB_DIR (default
// web/dist) holds a built IDE, it is served too. By default (ADR 0040, 0042) users sign in
// (register/login/logout, internal/credsvc), the same auth code cmd/gateway uses; with
// GOAP_AUTH_MODE=none, callers act as the principal GOAP_DEV_SUBJECT / GOAP_DEV_ROLES unless the
// request carries X-Goap-* identity headers.
//
// The wiring is one builder per concern (ADR 0073), in the order the composition needs: stores
// (local.go), graph (graph.go), registry and the platform bootstrap (registry.go), model gateway,
// node index and MCP hub (platform.go), engine and triggers (engine.go), HTTP server (server.go).
// newApp (app.go) strings them; the smoke test (app_test.go) builds it without a process.
package main

import (
	"context"
	"strings"

	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/authz"
)

// config is what the environment says about this composition; the other variables are read where they apply
// (storage in openStores, sandboxes in sandbox.FromEnv, token lifetimes in buildServer).
type config struct {
	Addr             string // GOAP_HTTP_ADDR
	AuthMode         string // GOAP_AUTH_MODE
	WebDir           string // GOAP_WEB_DIR
	DomainsDir       string // GOAP_DOMAINS_DIR
	MethodologiesDir string // GOAP_METHODOLOGIES_DIR
	ModelsConfig     string // GOAP_MODELS_CONFIG
	DevFSRoot        string // GOAP_DEV_FS_ROOT
	ConnectorToken   string // GOAP_CONNECTOR_TOKEN (generated when empty)
	Dev              authz.Principal
}

func loadConfig() config {
	return config{
		Addr:             platform.Env("GOAP_HTTP_ADDR", ":8080"),
		AuthMode:         platform.Env("GOAP_AUTH_MODE", defaultAuthMode),
		WebDir:           platform.Env("GOAP_WEB_DIR", "web/dist"),
		DomainsDir:       platform.Env("GOAP_DOMAINS_DIR", "domains"),
		MethodologiesDir: platform.Env("GOAP_METHODOLOGIES_DIR", "methodologies"),
		ModelsConfig:     platform.Env("GOAP_MODELS_CONFIG", ""),
		DevFSRoot:        platform.Env("GOAP_DEV_FS_ROOT", ""),
		ConnectorToken:   platform.Env("GOAP_CONNECTOR_TOKEN", ""),
		Dev: authz.Principal{
			Subject: platform.Env("GOAP_DEV_SUBJECT", "dev"),
			Org:     platform.Env("GOAP_DEV_ORG", "dev"),
			Project: platform.Env("GOAP_DEV_PROJECT", ""),
			Roles:   strings.Split(platform.Env("GOAP_DEV_ROLES", "admin"), ","),
		},
	}
}

func main() {
	log := platform.Logger("goap-dev")
	defer telemetry.Setup(context.Background(), log, "goap-dev")(context.Background())
	if err := run(context.Background(), loadConfig()); err != nil {
		platform.Fatal(log, "goap-dev", err)
	}
}

// run builds the platform and serves it until ctx ends or the process is signalled.
func run(ctx context.Context, cfg config) error {
	a, err := newApp(ctx, cfg, platform.Logger("goap-dev"))
	if err != nil {
		return err
	}
	defer a.Close()
	return a.Server.RunContext(a.ctx)
}
