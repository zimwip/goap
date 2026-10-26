package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The ports of the stack on 127.0.0.1, those of the compose stack on the host.
const (
	portGateway       = 8080
	portGraph         = 8081
	portRegistry      = 8082
	portEngine        = 8083
	portModelGW       = 8084
	portMCP           = 8085
	portIndexer       = 8086
	portPreferences   = 8087
	portCredentials   = 8088
	portEvents        = 8089
	portConversations = 8090
	portJobs          = 8091
	portContent       = 8092
	portLocalFS       = 8093
	portWeb           = 5173 // vite
	// behind goap-proxy, which takes portGraph, the graph services take these
	portGraphGo   = 8094
	portGraphRust = 8095
)

func url(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }

func addr(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }

// graphImpl is one implementation of the graph service.
type graphImpl struct {
	name string // go or rust
	bin  string
	port int
	// db is its SQLite file
	db string
}

// graphs are the graph services of the stack and the URL its clients call: the one of -graph on portGraph, or
// those the proxy mode asks for behind goap-proxy on portGraph.
func (c config) graphs() ([]graphImpl, string) {
	impl := func(name string, port int) graphImpl {
		bin := map[string]string{"go": "graph", "rust": "goap-graph-rs"}[name]
		return graphImpl{name: name, bin: bin, port: port, db: filepath.Join(c.dir, "graph-"+name+".db")}
	}
	switch c.proxy {
	case "":
		return []graphImpl{impl(c.graph, portGraph)}, url(portGraph)
	case "go":
		return []graphImpl{impl("go", portGraphGo)}, url(portGraph)
	case "rust":
		return []graphImpl{impl("rust", portGraphRust)}, url(portGraph)
	case "shadow":
		// one database: the Rust service reads what the Go one wrote
		g, r := impl("go", portGraphGo), impl("rust", portGraphRust)
		r.db = g.db
		return []graphImpl{g, r}, url(portGraph)
	default: // compare: each on its database
		return []graphImpl{impl("go", portGraphGo), impl("rust", portGraphRust)}, url(portGraph)
	}
}

// proxyService is goap-proxy in front of the graph services.
func (c config) proxyService(graphs []graphImpl) service {
	env := map[string]string{
		"GOAP_HTTP_ADDR":        addr(portGraph),
		"GOAP_PROXY_MODE":       c.proxy,
		"GOAP_PROXY_REPORT_DIR": filepath.Join(c.dir, "diff"),
		"GOAP_LOG_FORMAT":       "text",
	}
	for _, g := range graphs {
		env["GOAP_PROXY_"+strings.ToUpper(g.name)] = url(g.port)
	}
	return service{name: "proxy", bin: "goap-proxy", port: portGraph, ready: "/proxy/healthz", env: env}
}

// secrets are the shared secrets of the services, generated once in the data directory (an environment value wins).
type secrets struct{ jwt, jobs, credentials, connectors, modelProxy string }

func (c config) secrets() (secrets, error) {
	get := func(env, file string) (string, error) {
		if v := os.Getenv(env); v != "" {
			return v, nil
		}
		path := filepath.Join(c.dir, "secrets", file)
		if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
			return strings.TrimSpace(string(b)), nil
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		v := hex.EncodeToString(b)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		return v, os.WriteFile(path, []byte(v+"\n"), 0o600)
	}
	var s secrets
	var err error
	for _, x := range []struct {
		to        *string
		env, file string
	}{
		{&s.jwt, "GOAP_JWT_SECRET", "jwt_secret"},
		{&s.jobs, "GOAP_JOB_TOKEN", "job_token"},
		{&s.credentials, "GOAP_CREDENTIALS_TOKEN", "credentials_token"},
		{&s.connectors, "GOAP_CONNECTOR_TOKEN", "connector_token"},
		{&s.modelProxy, "GOAP_MODEL_PROXY_SECRET", "model_proxy_secret"},
	} {
		if *x.to, err = get(x.env, x.file); err != nil {
			return s, err
		}
	}
	return s, nil
}

// prepare makes the data directory, dropping the previous one with -reset.
func (c config) prepare(log *slog.Logger) error {
	if c.reset {
		for _, name := range []string{"nats", "diff", "localfs"} {
			if err := os.RemoveAll(filepath.Join(c.dir, name)); err != nil {
				return err
			}
		}
		dbs, _ := filepath.Glob(filepath.Join(c.dir, "*.db*"))
		for _, f := range dbs {
			if err := os.Remove(f); err != nil {
				return err
			}
		}
		log.Info("data of the previous run dropped", "dir", c.dir)
	}
	return os.MkdirAll(c.dir, 0o755)
}

// runPlatform runs the whole platform until the context is done.
func runPlatform(ctx context.Context, log *slog.Logger, cfg config, ready func(context.Context) error) error {
	if err := cfg.prepare(log); err != nil {
		return err
	}
	sec, err := cfg.secrets()
	if err != nil {
		return err
	}
	graphs, graphURL := cfg.graphs()
	common := func(port int, extra map[string]string) map[string]string {
		env := map[string]string{
			"GOAP_NATS_URL":   cfg.natsURL(),
			"GOAP_LOG_FORMAT": "text",
		}
		if port > 0 {
			env["GOAP_HTTP_ADDR"] = addr(port)
		}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	sources := ""
	if cfg.content && !cfg.skip["graph-content"] {
		sources = "content=alm,code@" + url(portContent)
	}
	platformGraph := func(g graphImpl) map[string]string {
		return common(g.port, map[string]string{
			"GOAP_SQLITE_PATH":   g.db,
			"GOAP_DOMAINS_DIR":   filepath.Join(cfg.root, "domains"),
			"GOAP_GRAPH_SEED":    envOr("GOAP_GRAPH_SEED", "dev"),
			"GOAP_GRAPH_SOURCES": sources,
			"GOAP_JOBS_URL":      url(portJobs),
			"GOAP_JOB_TOKEN":     sec.jobs,
			"GOAP_REGISTRY_URL":  url(portRegistry),
		})
	}
	// the graphs, then everything else: started in the order of their dependencies
	var all []service
	for _, g := range graphs {
		// the registry, the jobs and the content graph are called once serving; the registry calls the graph at start
		all = append(all, service{name: "graph-" + g.name, bin: g.bin, port: g.port, env: platformGraph(g),
			lazy: []string{"registry", "jobs", "graph-content"}})
	}
	if cfg.proxy != "" {
		all = append(all, cfg.proxyService(graphs))
	}
	if sources != "" {
		all = append(all, service{name: "graph-content", bin: graphs[0].bin, port: portContent, env: common(portContent, map[string]string{
			"GOAP_GRAPH_SPACE":        "content",
			"GOAP_PLATFORM_GRAPH_URL": graphURL,
			"GOAP_REGISTRY_URL":       url(portRegistry),
			"GOAP_JOBS_URL":           url(portJobs),
			"GOAP_JOB_TOKEN":          sec.jobs,
			"GOAP_SQLITE_PATH":        filepath.Join(cfg.dir, "content-"+graphs[0].name+".db"),
		}), lazy: []string{"registry", "jobs"}})
		if cfg.graph != graphs[0].name {
			log.Info("the content graph follows the proxied implementation", "graph", graphs[0].name)
		}
	}
	db := func(name string) string { return filepath.Join(cfg.dir, name+".db") }
	home, _ := os.UserHomeDir()
	all = append(all,
		service{name: "jobs", bin: "jobs", port: portJobs, env: common(portJobs, map[string]string{
			"GOAP_SQLITE_PATH": db("jobs"), "GOAP_JOB_TOKEN": sec.jobs})},
		service{name: "registry", bin: "registry", port: portRegistry, env: common(portRegistry, map[string]string{
			"GOAP_METHODOLOGIES_DIR": filepath.Join(cfg.root, "methodologies"), "GOAP_GRAPH_URL": graphURL})},
		service{name: "modelgw", bin: "modelgw", port: portModelGW, env: common(portModelGW, map[string]string{
			"GOAP_SQLITE_PATH": db("modelgw"), "GOAP_GRAPH_URL": graphURL, "GOAP_MODEL_PROXY_SECRET": sec.modelProxy})},
		service{name: "preferences", bin: "preferences", port: portPreferences, env: common(portPreferences, map[string]string{
			"GOAP_SQLITE_PATH": db("preferences")})},
		service{name: "credentials", bin: "credentials", port: portCredentials, env: common(portCredentials, map[string]string{
			"GOAP_SQLITE_PATH": db("credentials"), "GOAP_CREDENTIALS_TOKEN": sec.credentials})},
		service{name: "conversations", bin: "conversations", port: portConversations, env: common(portConversations, map[string]string{
			"GOAP_SQLITE_PATH": db("conversations"), "GOAP_GRAPH_URL": graphURL, "GOAP_REGISTRY_URL": url(portRegistry),
			"GOAP_MODELGW_URL": url(portModelGW), "GOAP_ENGINE_URL": url(portEngine)})},
		service{name: "indexer", bin: "indexer", port: portIndexer, env: common(portIndexer, map[string]string{
			"GOAP_SQLITE_PATH": db("index"), "GOAP_GRAPH_URL": graphURL, "GOAP_MODELGW_URL": url(portModelGW)})},
		service{name: "events", bin: "events", port: portEvents, env: common(portEvents, map[string]string{
			"GOAP_GRAPH_URL": graphURL})},
		service{name: "mcp", bin: "mcp", port: portMCP, env: common(portMCP, map[string]string{
			"GOAP_SQLITE_PATH": db("mcp"), "GOAP_GRAPH_URL": graphURL, "GOAP_ENGINE_URL": url(portEngine),
			"GOAP_REGISTRY_URL": url(portRegistry), "GOAP_CONNECTOR_TOKEN": sec.connectors,
			"GOAP_MODEL_PROXY_SECRET": sec.modelProxy, "GOAP_MODEL_PROXY_URL": url(portModelGW) + "/anthropic"}),
			lazy: []string{"engine"}}, // the engine calls the hub at start, the hub calls the engine back when asked
		service{name: "engine", bin: "engine", port: portEngine, env: common(portEngine, map[string]string{
			"GOAP_SQLITE_PATH": db("engine"), "GOAP_GRAPH_URL": graphURL, "GOAP_REGISTRY_URL": url(portRegistry),
			"GOAP_MODELGW_URL": url(portModelGW), "GOAP_MCP_URL": url(portMCP), "GOAP_RUNTIME_URL": url(portEngine)})},
		service{name: "connector-localfs", bin: "connector-localfs", port: portLocalFS, env: common(portLocalFS, map[string]string{
			"GOAP_MCP_URL": url(portMCP), "GOAP_CONNECTOR_URL": url(portLocalFS), "GOAP_CONNECTOR_TOKEN": sec.connectors,
			"GOAP_LOCALFS_ROOTS": filepath.Join(cfg.dir, "localfs")})},
		service{name: "importer-git", bin: "importer-git", env: common(0, map[string]string{
			"GOAP_GRAPH_URL": graphURL, "GOAP_JOBS_URL": url(portJobs), "GOAP_JOB_TOKEN": sec.jobs,
			"GOAP_IMPORT_GIT_ROOTS": home, "GOAP_IMPORT_GIT_CLONES": filepath.Join(cfg.dir, "clones")})},
		service{name: "gateway", bin: "gateway", port: portGateway, env: common(portGateway, map[string]string{
			"GOAP_AUTH_MODE":         envOr("GOAP_AUTH_MODE", "local"),
			"GOAP_DEV_TOKENS":        "true",
			"GOAP_CORS_ORIGINS":      "http://localhost:5173",
			"GOAP_JWT_SECRET":        sec.jwt,
			"GOAP_GRAPH_URL":         graphURL,
			"GOAP_REGISTRY_URL":      url(portRegistry),
			"GOAP_ENGINE_URL":        url(portEngine),
			"GOAP_MODELGW_URL":       url(portModelGW),
			"GOAP_PREFERENCES_URL":   url(portPreferences),
			"GOAP_CONVERSATIONS_URL": url(portConversations),
			"GOAP_INDEXER_URL":       url(portIndexer),
			"GOAP_MCP_URL":           url(portMCP),
			"GOAP_CREDENTIALS_URL":   url(portCredentials),
			"GOAP_CREDENTIALS_TOKEN": sec.credentials,
			"GOAP_EVENTS_URL":        url(portEvents),
			"GOAP_JOBS_URL":          url(portJobs),
		})},
	)
	web := filepath.Join(cfg.root, "web")
	if cfg.web {
		// --strictPort: on a port taken vite would take the next one, and the IDE on 5173 be a previous run's
		all = append(all, service{name: "web", path: "npm", dir: web, port: portWeb, ready: "/",
			args: []string{"run", "dev", "--", "--port", strconv.Itoa(portWeb), "--strictPort"},
			env:  map[string]string{"GOAP_GATEWAY_URL": url(portGateway)}})
	}
	var started []service
	for _, svc := range all {
		if cfg.skip[svc.name] {
			log.Info("skipped", "service", svc.name)
			continue
		}
		if svc.port > 0 && svc.ready == "" {
			svc.ready = "/readyz"
		}
		started = append(started, svc)
	}
	if err := dependencies(started); err != nil {
		return err
	}
	uses := append(ports(started), portUse{cfg.natsPort, "nats"}, portUse{cfg.natsMonitor, "nats monitoring"})
	if err := checkPorts(uses); err != nil {
		return err
	}

	ns, err := startNATS(cfg)
	if err != nil {
		return err
	}
	defer ns.Shutdown()
	log.Info("nats", "url", cfg.natsURL())

	ctx, cancel := context.WithCancel(ctx)
	sup := &supervisor{cfg: cfg, log: log}
	defer sup.wait()
	defer cancel() // first: the processes stop, then they are waited for

	if cfg.web {
		if err := sup.start(ctx, service{name: "npm-install", path: "npm", dir: web, once: true,
			args: []string{"install", "--no-audit", "--no-fund"}}); err != nil {
			// an install that failed late (a package's own download) may still leave what vite needs
			if _, serr := os.Stat(filepath.Join(web, "node_modules", ".bin", "vite")); serr != nil || ctx.Err() != nil {
				return err
			}
			log.Warn("npm install failed, the web runs on the modules in place", "err", err)
		}
	}
	// the platform bootstrap, once per database (shadow: the Go service's)
	booted := map[string]bool{}
	for _, g := range graphs {
		if booted[g.db] {
			continue
		}
		booted[g.db] = true
		if err := sup.start(ctx, service{name: "bootstrap-" + g.name, bin: g.bin, args: []string{"bootstrap"}, env: platformGraph(g), once: true}); err != nil {
			return err
		}
	}
	if err := sup.startAll(ctx, started); err != nil {
		if ready != nil || ctx.Err() != nil {
			return err
		}
		log.Warn("not every service is ready", "err", err)
	} else {
		web := "make dev-web (http://localhost:5173)"
		if cfg.web {
			web = "http://localhost:5173"
		}
		log.Info("platform ready", "gateway", url(portGateway), "web", web)
	}
	if cfg.watch {
		sup.wg.Add(1)
		go func() {
			defer sup.wg.Done()
			watch(ctx, cfg, sup, log, started)
		}()
	}
	if ready != nil {
		// the platform runs while ready does (goap-local test), then stops
		return ready(ctx)
	}
	<-ctx.Done()
	log.Info("stopping")
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// get fetches a URL's body.
func get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err == nil && resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("%s: %s", u, resp.Status)
	}
	return b, err
}
