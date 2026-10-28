package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// runBench runs the performance test of the local machine, the one of make perf: the graph service alone on SQLite
// (GOAP_DB_DSN: on PostgreSQL), bootstrapping itself, loaded by goap-bench with args.
func runBench(ctx context.Context, log *slog.Logger, cfg config, args []string) error {
	if err := cfg.prepare(log); err != nil {
		return err
	}
	if err := checkPorts([]portUse{{portDomains, "domains"}, {portGraph, "graph"}}); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	sup := &supervisor{cfg: cfg, log: log}
	defer sup.wait()
	defer cancel()

	// the domain registry first: the graph waits for its domains (ADR 0167)
	if err := sup.start(ctx, cfg.domainsService(map[string]string{"GOAP_GRAPH_URL": url(portGraph)})); err != nil {
		return err
	}
	env := map[string]string{
		"GOAP_HTTP_ADDR":   addr(portGraph),
		"GOAP_SQLITE_PATH": filepath.Join(cfg.dir, "graph.db"),
		"GOAP_DOMAINS_URL": url(portDomains),
		"GOAP_LOG_FORMAT":  "text",
		// no bootstrap command in this stack: the service bootstraps the platform itself
		"GOAP_GRAPH_BOOTSTRAP": "1",
	}
	if err := sup.start(ctx, service{name: "graph", bin: "graph", port: portGraph, ready: "/readyz", env: env}); err != nil {
		return err
	}
	results := filepath.Join(cfg.dir, "bench") + string(os.PathSeparator)
	bench := append([]string{"-wait", "3m", "-hold=0", "-csv", results}, args...)
	if !slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(strings.TrimLeft(a, "-"), "versions") }) {
		bench = append(bench, "-versions", "20000")
	}
	if err := sup.start(ctx, service{name: "bench", bin: "goap-bench", args: bench, env: map[string]string{"GOAP_GRAPH_URL": url(portGraph), "GOAP_DOMAINS_URL": url(portDomains)}, once: true}); err != nil {
		return err
	}
	log.Info("bench done", "csv", results)
	return nil
}
