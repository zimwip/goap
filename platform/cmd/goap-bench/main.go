// Command goap-bench is the load generator of the performance stack (make perf): it writes nodes and versions through
// the graph service's API on the two axes, the domain (a generated domain published, then grown, by the domain
// registry) and the change (changes landing versions of its nodes), probes the reads as the graph grows, and measures
// every call (OpenTelemetry metrics when OTEL_EXPORTER_OTLP_ENDPOINT is set, drawn by the Grafana dashboard "GOAP
// performance", and a CSV report).
//
//	goap-bench [-graph http://localhost:8080] [-versions 100000] [-workers 4] [-edits 50] [-csv bench.csv] ...
//
// It talks to the services behind the gateway with the platform's identity headers (a system principal holding the
// admin role), or through the gateway with -token.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
	"github.com/zimwip/goap/platform/bench"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("goap-bench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg bench.Config
	graphURL := fs.String("graph", platform.Env("GOAP_GRAPH_URL", "http://localhost:8080"), "base URL of the graph service (or goap-dev)")
	domainURL := fs.String("domains", platform.Env("GOAP_DOMAINS_URL", ""), "base URL of the domain registry (default: the graph's)")
	token := fs.String("token", os.Getenv("GOAP_TOKEN"), "bearer token, to go through the gateway (default: identity headers of a system principal)")
	csvPath := fs.String("csv", "", "write the report (one row per operation and window) to this CSV file; a directory (ending with /): <run>.csv in it")
	wait := fs.Duration("wait", 0, "wait up to this long for the graph service to answer before starting")
	fs.StringVar(&cfg.Run, "run", "", "prefix of the keys created by this run (default: generated)")
	fs.IntVar(&cfg.Domain.Types, "types", 10, "node types of the generated domain (domain axis)")
	fs.IntVar(&cfg.Domain.Attributes, "attributes", 4, "extra attributes per node type")
	fs.Int64Var(&cfg.DomainEvery, "domain-every", 0, "publish a new domain version (one more type) every that many versions written (0: once at the start)")
	fs.IntVar(&cfg.Workers, "workers", 4, "concurrent writers")
	fs.IntVar(&cfg.Edits, "edits", 50, "nodes per change (change axis)")
	fs.Float64Var(&cfg.CreateRatio, "create", 0.3, "share of the edits that create a node, the others modify one (0..1)")
	fs.IntVar(&cfg.Links, "links", 2, "links of a created node to earlier nodes")
	fs.Float64Var(&cfg.AddLinks, "add-links", 0.3, "share of the modifications that also add a link to another node (0..1)")
	fs.Float64Var(&cfg.Spread, "spread", 0.5, "variability of each change around -edits, -create, -links and -add-links: uniform within ±spread of the value, the mean staying it (0..1; 0: every change alike)")
	fs.IntVar(&cfg.HotNodes, "hot", 20, "nodes per writer that take -hot-ratio of the modifications (deep histories)")
	fs.Float64Var(&cfg.HotRatio, "hot-ratio", 0.2, "share of the modifications on the hot nodes")
	fs.IntVar(&cfg.TextBytes, "text", 200, "bytes of the description of a created node")
	fs.StringVar(&cfg.Mode, "mode", bench.ModeCommit, "commit (one CommitEdits per change) or lifecycle (the change API call by call)")
	fs.Int64Var(&cfg.TargetVersions, "versions", 10000, "stop once this many versions are written (0: no bound)")
	fs.DurationVar(&cfg.Duration, "duration", 0, "stop after this long (0: no bound)")
	fs.Int64Var(&cfg.MaxChanges, "changes", 0, "stop after this many changes (0: no bound)")
	fs.DurationVar(&cfg.ProbeEvery, "probe", 5*time.Second, "measure the reads every that long (0: never)")
	fs.DurationVar(&cfg.ReportEvery, "report", 10*time.Second, "window of the report and the progress lines")
	fs.Uint64Var(&cfg.Seed, "seed", 0, "seed of the random choices (default: the time)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if cfg.Mode != bench.ModeCommit && cfg.Mode != bench.ModeLifecycle {
		fmt.Fprintln(stderr, "goap-bench: -mode is commit or lifecycle")
		return 2
	}
	if *domainURL == "" {
		*domainURL = *graphURL
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := platform.Logger("goap-bench")
	shutdown := telemetry.Setup(ctx, log, "goap-bench")
	defer func() {
		// the last values reach the collector before the process ends
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = shutdown(c)
	}()
	m, err := bench.NewMetrics()
	if err != nil {
		fmt.Fprintln(stderr, "goap-bench:", err)
		return 2
	}
	r := &bench.Runner{Config: cfg, Metrics: m, Log: stdout,
		Clients:  bench.NewClients(*graphURL, *domainURL, authz.System("bench", access.RoleAdmin), *token, m),
		Reviewer: bench.NewClients(*graphURL, *domainURL, authz.System("bench-reviewer", access.RoleAdmin), *token, m)}
	cfg.Defaults() // the run is named before its report file
	r.Config = cfg
	if *csvPath != "" {
		if strings.HasSuffix(*csvPath, "/") {
			if err := os.MkdirAll(*csvPath, 0o755); err != nil {
				fmt.Fprintln(stderr, "goap-bench:", err)
				return 2
			}
			*csvPath = filepath.Join(*csvPath, cfg.Run+".csv")
		}
		fmt.Fprintln(stdout, "report:", *csvPath)
		f, err := os.Create(*csvPath)
		if err != nil {
			fmt.Fprintln(stderr, "goap-bench:", err)
			return 2
		}
		defer f.Close()
		if r.CSV, err = bench.NewCSVWriter(f); err != nil {
			fmt.Fprintln(stderr, "goap-bench:", err)
			return 2
		}
	}
	if *wait > 0 {
		if err := r.WaitReady(ctx, *wait); err != nil {
			fmt.Fprintln(stderr, "goap-bench:", err)
			return 1
		}
	}
	if err := r.Run(ctx); err != nil {
		fmt.Fprintln(stderr, "goap-bench:", err)
		return 1
	}
	return 0
}
