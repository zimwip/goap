package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/enginesvc"
	"github.com/zimwip/goap/gateway/credsvc"
	"github.com/zimwip/goap/graph/domainsvc"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/index"
	"github.com/zimwip/goap/graph/smarttagsvc"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/mcp/mcpsvc"
	"github.com/zimwip/goap/platform/convsvc"
	"github.com/zimwip/goap/platform/eventsvc"
	"github.com/zimwip/goap/platform/jobsvc"
	"github.com/zimwip/goap/platform/modelgw"
	"github.com/zimwip/goap/platform/prefssvc"
)

// stores are the storage backends of the single-process platform.
type stores struct {
	graph     graph.Repo
	processes engine.Store
	models    modelgw.Store
	prefs     prefssvc.Store
	convs     convsvc.Store
	creds     credsvc.Store
	mcp       mcpsvc.Store
	index     index.Store
	domains   domainsvc.Store
	smartTags smarttagsvc.Store
	jobs      jobsvc.Store
	// dir is the directory of the local database (sqlite), empty in memory: where the generated JWT
	// secret of local sign-in is kept (localJWTSecret).
	dir   string
	close func()
}

// openStores selects the storage (GOAP_STORE): memory, or sqlite for the
// local mode (one SQLite file, GOAP_SQLITE_PATH, default .goap/goap.db).
func openStores(ctx context.Context, log *slog.Logger) (stores, error) {
	switch kind := platform.Env("GOAP_STORE", "memory"); kind {
	case "memory":
		return stores{graph: graph.NewMemory(), processes: engine.NewMemoryStore(), models: modelgw.NewMemoryStore(), prefs: prefssvc.NewMemoryStore(), convs: convsvc.NewMemoryStore(), creds: credsvc.NewMemoryStore(), mcp: mcpsvc.NewMemoryStore(), index: index.NewMemory(), domains: domainsvc.NewMemoryStore(), smartTags: smarttagsvc.NewMemoryStore(), jobs: jobsvc.NewMemoryStore(), close: func() {}}, nil
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
			{"modelgw", modelgw.SQLiteMigrations}, {"preferences", prefssvc.SQLiteMigrations}, {"conversations", convsvc.SQLiteMigrations}, {"credentials", credsvc.SQLiteMigrations},
			{"mcp", mcpsvc.SQLiteMigrations}, {"index", index.SQLiteMigrations},
			// the domain registry (ADR 0125 §3), under the component name it had in the registry: a local database keeps its table
			{"registry", domainsvc.SQLiteMigrations},
			{"smarttags", smarttagsvc.SQLiteMigrations},
			{"jobs", jobsvc.SQLiteMigrations},
		} {
			if err := platform.MigrateSQLite(ctx, db, m.component, m.fs, "migrations_sqlite"); err != nil {
				db.Close()
				return stores{}, err
			}
		}
		// the local mode is the one process on its file: the locks the previous start left are dropped, so that
		// its runs are resumed at once (ADR 0124)
		processes := enginesvc.SQLStore{DB: db, Holder: enginesvc.NewHolder()}
		if err := processes.ReleaseAll(ctx); err != nil {
			db.Close()
			return stores{}, err
		}
		abs, _ := filepath.Abs(path)
		log.Info("local storage", "sqlite", abs)
		return stores{graph: graph.NewSQLite(db),
			processes: processes, models: modelgw.SQLStore{DB: db}, prefs: prefssvc.SQLStore{DB: db}, convs: convsvc.SQLStore{DB: db}, creds: credsvc.SQLStore{DB: db}, mcp: mcpsvc.SQLStore{DB: db}, index: index.NewSQLite(db), domains: domainsvc.SQLStore{DB: db}, smartTags: smarttagsvc.SQLStore{DB: db}, jobs: jobsvc.SQLStore{DB: db}, dir: filepath.Dir(path), close: func() { closeDB(log, db) }}, nil
	default:
		return stores{}, fmt.Errorf("GOAP_STORE must be memory or sqlite, got %q", kind)
	}
}

// localJWTSecret is the secret local sign-in (ADR 0042, on by default) signs its tokens with when none is
// configured (GOAP_JWT_SECRET, Vault): generated once and kept next to the local database (dir/jwt_secret),
// so a restart does not sign everyone out; in memory (dir empty) the accounts do not survive a restart
// either, so a fresh secret per process is right.
func localJWTSecret(dir string) (string, error) { return localSecret(dir, "jwt_secret") }

// localSecret is a random secret kept in the file name of dir (generated once; a fresh one per process when dir
// is empty).
func localSecret(dir, name string) (string, error) {
	gen := func() (string, error) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return hex.EncodeToString(b), nil
	}
	if dir == "" {
		return gen()
	}
	path := filepath.Join(dir, name)
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b)), nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	secret, err := gen()
	if err != nil {
		return "", err
	}
	return secret, os.WriteFile(path, []byte(secret+"\n"), 0o600)
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

// registryEvents receives the methodology registry events in-process (no NATS here) and reacts to publications of
// methodologies (the domain registry calls its Changed hook in process).
// localURL is the base URL of this process for a listening address (":8080" is http://localhost:8080).
func localURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}

// emit runs the relay of a service's outbox (ADR 0136) for the life of the process.
func (e *env) emit(r *outbox.Relay) { outbox.Start(e.ctx, r, e.log) }

// busSink delivers the events of an outbox to the event stream of the web, which keeps what is for it.
func busSink(bus *eventsvc.Hub) outbox.Sink {
	return outbox.SinkFunc(func(ctx context.Context, m outbox.Message) error {
		bus.Ingest(m.Subject, m.Data, platform.StampOf(ctx))
		return nil
	})
}
