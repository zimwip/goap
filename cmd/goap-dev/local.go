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

	"github.com/casbin/casbin/v2/persist"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/zimwip/goap/internal/enginesvc"
	"github.com/zimwip/goap/internal/iamsvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
)

// stores are the storage backends of the single-process platform.
type stores struct {
	graph         graph.Repo
	methodologies registrysvc.Store
	policies      persist.Adapter // nil: in-memory policies
	processes     engine.Store
	models        modelgw.Store
	mcp           mcpsvc.Store
	close         func()
}

// openStores selects the storage (GOAP_STORE): memory, or sqlite for the
// local mode (one SQLite file, GOAP_SQLITE_PATH, default .goap/goap.db).
func openStores(ctx context.Context, log *slog.Logger) (stores, error) {
	switch kind := platform.Env("GOAP_STORE", "memory"); kind {
	case "memory":
		return stores{graph: graph.NewMemory(), methodologies: registrysvc.NewMemoryStore(), processes: engine.NewMemoryStore(), models: modelgw.NewMemoryStore(), mcp: mcpsvc.NewMemoryStore(), close: func() {}}, nil
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
			{"graph", graph.SQLiteMigrations}, {"registry", registrysvc.SQLiteMigrations},
			{"iam", iamsvc.SQLiteMigrations}, {"engine", enginesvc.SQLiteMigrations},
			{"modelgw", modelgw.SQLiteMigrations}, {"mcp", mcpsvc.SQLiteMigrations},
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
		return stores{graph: graph.NewSQLite(db), methodologies: registrysvc.SQLiteStore{DB: db},
			policies: &iamsvc.SQLiteAdapter{DB: db}, processes: processes, models: modelgw.SQLStore{DB: db}, mcp: mcpsvc.SQLStore{DB: db}, close: func() { closeDB(log, db) }}, nil
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
		Root:  dir,
		HTML5: true,
		Skipper: func(c echo.Context) bool {
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
	case "goap.registry.domain.published":
		f.domain(context.WithoutCancel(ctx), ev["name"], ev["version"])
	}
	return nil
}
