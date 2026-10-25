// Command goap-local runs the platform on one machine without Docker, one process per binary as the compose stack
// runs them: an embedded NATS server (JetStream), the graph (Rust by default, or Go) on a SQLite file, and every
// service on a SQLite file of its own, the processes restarted when they stop. `goap-local bench` runs the graph alone,
// behind goap-proxy, and loads it with goap-bench: the performance test of the local machine. `goap-local test` runs
// the platform on fresh data and the end-to-end test of platform/e2e/stack against its gateway (go test, from -root).
//
//	goap-local [flags]                 the platform, until interrupted
//	goap-local test [flags] -- args    the platform, then go test ./e2e/stack/ with args, then stop
//	goap-local bench [flags] -- args   the graph(s) behind goap-proxy, goap-bench with args, the report, then stop
//
// The binaries are read from -bin (make local builds them in .goap/bin), the data kept in -dir (.goap/local), each
// process's output in <dir>/logs/<name>.log and on the standard output, prefixed by its name.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/zimwip/goap/lib/platform"
)

// config is what the command line and the environment set.
type config struct {
	bin, dir, root string
	// graph is the implementation of the graph service: rust or go.
	graph string
	// content runs the content graph next to the platform graph (ADR 0154 §9).
	content bool
	// proxy puts goap-proxy in front of the graph in that mode (go, rust, compare, shadow), empty: none.
	proxy string
	// skip names the services not started.
	skip map[string]bool
	// natsPort is the client port of the embedded NATS server, natsMonitor its monitoring port (0: none).
	natsPort, natsMonitor int
	reset                 bool
}

func main() {
	log := platform.Logger("goap-local")
	args := os.Args[1:]
	mode := ""
	if len(args) > 0 && (args[0] == "bench" || args[0] == "test") {
		mode, args = args[0], args[1:]
	}
	cfg, rest, err := parse(args, mode)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "goap-local:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch mode {
	case "bench":
		err = runBench(ctx, log, cfg, rest)
	case "test":
		err = runPlatform(ctx, log, cfg, func(ctx context.Context) error { return stackTest(ctx, cfg, rest) })
	default:
		err = runPlatform(ctx, log, cfg, nil)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("goap-local", "err", err)
		os.Exit(1)
	}
}

func parse(args []string, mode string) (config, []string, error) {
	bench := mode == "bench"
	name := "goap-local"
	if mode != "" {
		name += " " + mode
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	cfg := config{}
	fs.StringVar(&cfg.bin, "bin", platform.Env("GOAP_LOCAL_BIN", filepath.Join(".goap", "bin")), "directory of the binaries (make local-build)")
	fs.StringVar(&cfg.dir, "dir", platform.Env("GOAP_LOCAL_DIR", filepath.Join(".goap", "local")), "directory of the data, the secrets and the logs")
	fs.StringVar(&cfg.root, "root", ".", "root of the repository: its domains/ and methodologies/")
	fs.StringVar(&cfg.graph, "graph", platform.Env("GOAP_LOCAL_GRAPH", "rust"), "graph service: rust or go")
	fs.BoolVar(&cfg.content, "content", true, "run the content graph next to the platform graph")
	proxy := ""
	if bench {
		proxy = "rust"
	}
	fs.StringVar(&cfg.proxy, "proxy", platform.Env("GOAP_LOCAL_PROXY", proxy), "goap-proxy in front of the graph in this mode: go, rust, compare or shadow (empty: none)")
	skip := fs.String("skip", "", "services not started, comma separated (e.g. importer-git,connector-localfs)")
	fs.IntVar(&cfg.natsPort, "nats-port", 4222, "client port of the embedded NATS server")
	fs.IntVar(&cfg.natsMonitor, "nats-monitor", 8222, "monitoring port of the embedded NATS server (0: none)")
	fs.BoolVar(&cfg.reset, "reset", mode != "", "drop the data of the previous run before starting")
	if err := fs.Parse(args); err != nil {
		return cfg, nil, err
	}
	switch cfg.graph {
	case "rust", "go":
	default:
		return cfg, nil, fmt.Errorf("-graph must be rust or go, got %q", cfg.graph)
	}
	switch cfg.proxy {
	case "", "go", "rust", "compare", "shadow":
	default:
		return cfg, nil, fmt.Errorf("-proxy must be go, rust, compare or shadow, got %q", cfg.proxy)
	}
	cfg.skip = map[string]bool{}
	for _, s := range strings.Split(*skip, ",") {
		if s = strings.TrimSpace(s); s != "" {
			cfg.skip[s] = true
		}
	}
	var err error
	for _, p := range []*string{&cfg.bin, &cfg.dir, &cfg.root} {
		if *p, err = filepath.Abs(*p); err != nil {
			return cfg, nil, err
		}
	}
	return cfg, fs.Args(), nil
}

// stackTest runs the end-to-end test of a running platform (platform/e2e/stack) against the gateway of the stack: the
// person it registers is the first of the fresh data, an administrator, so it tests the writes too.
func stackTest(ctx context.Context, cfg config, args []string) error {
	cmd := exec.CommandContext(ctx, "go", append([]string{"test", "./e2e/stack/", "-run", "TestStack", "-count=1", "-v"}, args...)...)
	cmd.Dir = filepath.Join(cfg.root, "platform")
	cmd.Env = environ(map[string]string{"GOAP_STACK_URL": url(portGateway)})
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("stack test: %w", err)
	}
	return nil
}
