package main

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/zimwip/goap/internal/credsvc"
	"github.com/zimwip/goap/internal/enginesvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/prefssvc"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/index"
)

// stores are the storage backends of the single-process platform.
type stores struct {
	graph      graph.Repo
	processes  engine.Store
	models     modelgw.Store
	prefs      prefssvc.Store
	creds      credsvc.Store
	mcp        mcpsvc.Store
	index      index.Store
	domains    registrysvc.DomainStore
	algorithms registrysvc.AlgorithmStore
	close      func()
}

// openStores selects the storage (GOAP_STORE): memory, or sqlite for the
// local mode (one SQLite file, GOAP_SQLITE_PATH, default .goap/goap.db).
func openStores(ctx context.Context, log *slog.Logger) (stores, error) {
	switch kind := platform.Env("GOAP_STORE", "memory"); kind {
	case "memory":
		reg := registrysvc.NewMemoryStore()
		return stores{graph: graph.NewMemory(), processes: engine.NewMemoryStore(), models: modelgw.NewMemoryStore(), prefs: prefssvc.NewMemoryStore(), creds: credsvc.NewMemoryStore(), mcp: mcpsvc.NewMemoryStore(), index: index.NewMemory(), domains: reg, algorithms: reg, close: func() {}}, nil
	case "sqlite":
		path := platform.Env("GOAP_SQLITE_PATH", filepath.Join(".goap", "goap.db"))
		db, err := platform.OpenSQLite(ctx, path)
		if err != nil {
			return stores{}, err
		}
		for _, m := range []struct {
			component string
			fs        fs.FS
		}{
			{"graph", graph.SQLiteMigrations},
			{"engine", enginesvc.SQLiteMigrations},
			{"modelgw", modelgw.SQLiteMigrations}, {"preferences", prefssvc.SQLiteMigrations}, {"credentials", credsvc.SQLiteMigrations},
			{"mcp", mcpsvc.SQLiteMigrations}, {"index", index.SQLiteMigrations},
			{"registry", registrysvc.SQLiteMigrations},
		} {
			if err := platform.MigrateSQLite(ctx, db, m.component, m.fs, "migrations_sqlite"); err != nil {
				db.Close()
				return stores{}, err
			}
		}
		processes := enginesvc.SQLiteStore{DB: db}
		if n, err := processes.Interrupted(ctx); err != nil {
			db.Close()
			return stores{}, err
		} else if n > 0 {
			log.Warn("processes interrupted by the previous shutdown marked failed", "count", n)
		}
		abs, _ := filepath.Abs(path)
		log.Info("local storage", "sqlite", abs)
		return stores{graph: graph.NewSQLite(db),
			processes: processes, models: modelgw.SQLStore{DB: db}, prefs: prefssvc.SQLStore{DB: db}, creds: credsvc.SQLStore{DB: db}, mcp: mcpsvc.SQLStore{DB: db}, index: index.NewSQLite(db),
			domains: registrysvc.SQLDomainStore{DB: db}, algorithms: registrysvc.SQLAlgorithmStore{DB: db}, close: func() { closeDB(log, db) }}, nil
	default:
		return stores{}, fmt.Errorf("GOAP_STORE must be memory or sqlite, got %q", kind)
	}
}

func closeDB(log *slog.Logger, db *sql.DB) {
	if err := db.Close(); err != nil {
		log.Error("sqlite close", "err", err)
	}
}

// serveWeb serves a built IDE (npm run build) with a single-page fallback,
// leaving the Connect RPCs (/goap.*) and HTTP endpoints (/api, health) alone.
func serveWeb(log *slog.Logger, e *echo.Echo, dir string) {
	if st, err := os.Stat(filepath.Join(dir, "index.html")); err != nil || st.IsDir() {
		log.Info("no built IDE to serve (cd web && npm run build, or npm run dev)", "dir", dir)
		return
	}
	e.Use(middleware.StaticWithConfig(middleware.StaticConfig{
		Filesystem: os.DirFS(dir),
		Root:       ".",
		HTML5:      true,
		Skipper: func(c *echo.Context) bool {
			p := c.Request().URL.Path
			return strings.HasPrefix(p, "/goap.") || strings.HasPrefix(p, "/api/") || p == "/healthz" || p == "/readyz"
		},
	}))
	log.Info("serving the IDE", "dir", dir)
}

// registryEvents receives the registry events in-process (no NATS here) and
// reacts to publications of methodologies and domains.
type registryEvents struct {
	methodology func(ctx context.Context, name, version string)
	domain      func(ctx context.Context, name, version string)
}

func (f registryEvents) Publish(ctx context.Context, subject string, v any) error {
	ev, ok := v.(map[string]string)
	if !ok {
		return nil
	}
	switch subject {
	case "goap.registry.methodology.published":
		f.methodology(context.WithoutCancel(ctx), ev["name"], ev["version"])
	case "goap.registry.domain.published", "goap.registry.domain.deleted":
		f.domain(context.WithoutCancel(ctx), ev["name"], ev["version"])
	}
	return nil
}
