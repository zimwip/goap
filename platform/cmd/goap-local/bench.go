package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// runBench runs the performance test of the local machine, the one of make perf: the graph service(s) alone on
// SQLite, bootstrapping themselves, behind goap-proxy in its mode (none with -proxy ""), loaded by goap-bench with
// args; then, in modes compare and shadow, the proxy's report and, in mode compare, the comparison of the two
// databases.
func runBench(ctx context.Context, log *slog.Logger, cfg config, args []string) error {
	if err := cfg.prepare(log); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	sup := &supervisor{cfg: cfg, log: log}
	defer sup.wait()
	defer cancel()

	graphs, graphURL := cfg.graphs()
	for i, g := range graphs {
		env := map[string]string{
			"GOAP_HTTP_ADDR":   addr(g.port),
			"GOAP_SQLITE_PATH": g.db,
			"GOAP_DOMAINS_DIR": filepath.Join(cfg.root, "domains"),
			"GOAP_LOG_FORMAT":  "text",
			// no bootstrap command in this stack: the service bootstraps the platform itself (shadow: the Go one)
			"GOAP_GRAPH_BOOTSTRAP": "1",
		}
		if cfg.proxy == "shadow" && i > 0 {
			env["GOAP_GRAPH_BOOTSTRAP"] = "0"
		}
		if err := sup.start(ctx, service{name: "graph-" + g.name, bin: g.bin, port: g.port, ready: "/readyz", env: env}); err != nil {
			return err
		}
	}
	if cfg.proxy != "" {
		if err := sup.start(ctx, cfg.proxyService(graphs)); err != nil {
			return err
		}
	}
	results := filepath.Join(cfg.dir, "bench") + string(os.PathSeparator)
	bench := []string{"-wait", "3m", "-hold=0", "-csv", results}
	if cfg.proxy == "compare" || cfg.proxy == "shadow" {
		// a comparison pairs the calls: one writer, the same keys at each run (later arguments win)
		bench = append(bench, "-workers", "1", "-seed", "1")
	}
	bench = append(bench, args...)
	if !slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(strings.TrimLeft(a, "-"), "versions") }) {
		bench = append(bench, "-versions", "20000")
	}
	if err := sup.start(ctx, service{name: "bench", bin: "goap-bench", args: bench, env: map[string]string{"GOAP_GRAPH_URL": graphURL}, once: true}); err != nil {
		return err
	}
	log.Info("bench done", "csv", results)
	if cfg.proxy != "compare" && cfg.proxy != "shadow" {
		return nil // the proxy compares nothing in the other modes: no report
	}
	report, err := get(ctx, url(portGraph)+"/proxy/report")
	if err != nil {
		return err
	}
	fmt.Printf("proxy report (%s mode):\n%s\n", cfg.proxy, report)
	if cfg.proxy == "compare" {
		return sup.start(ctx, service{name: "diff-db", bin: "goap-proxy", once: true, args: []string{"diff-db",
			"--go", "sqlite://" + graphs[0].db, "--rust", "sqlite://" + graphs[1].db,
			"--ids", filepath.Join(cfg.dir, "diff", "ids.json")}})
	}
	return nil
}
