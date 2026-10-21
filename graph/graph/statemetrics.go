package graph

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// What the state of a baseline costs to read (ADR 0143): every read of a state says where it came from (the selection
// of the versions committed: the states are not cached), how many branches of the chain of forks it read and how many
// versions it found.
// Prometheus: goap_graph_state_* (deploy/grafana/dashboards/goap-perf.json).

// stateFromCommits is the source of every state read: the selection of the versions committed (ADR 0143). The
// attribute stays for the dashboards.
const stateFromCommits = "commits"

type stateMetrics struct {
	reads    metric.Int64Counter
	chain    metric.Int64Histogram
	entries  metric.Int64Counter
	duration metric.Float64Histogram
}

var stateMeter = sync.OnceValue(func() *stateMetrics {
	m := otel.Meter("github.com/zimwip/goap/graph/graph")
	s := &stateMetrics{}
	s.reads, _ = m.Int64Counter("graph.state.reads", metric.WithUnit("{read}"),
		metric.WithDescription("Reads of the state of a baseline, by scope (whole, nodes) and by source (cache, commits)"))
	s.chain, _ = m.Int64Histogram("graph.state.chain", metric.WithUnit("{baseline}"),
		metric.WithDescription("Branches of the chain of forks a read of a state selected on"),
		metric.WithExplicitBucketBoundaries(0, 1, 2, 3, 5, 10, 20, 50, 100, 200))
	s.entries, _ = m.Int64Counter("graph.state.entries", metric.WithUnit("{entry}"),
		metric.WithDescription("Versions selected for the states of baselines"))
	s.duration, _ = m.Float64Histogram("graph.state.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of a read of a state, by scope"),
		metric.WithExplicitBucketBoundaries(0.0001, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5))
	return s
})

// stateRead is what one read of a state found, recorded when it ends.
type stateRead struct {
	start     time.Time
	whole     bool
	source    string
	chain     int
	committed int
}

func (r *stateRead) record(ctx context.Context) {
	m := stateMeter()
	scope := attribute.String("scope", "nodes")
	if r.whole {
		scope = attribute.String("scope", "whole")
	}
	m.reads.Add(ctx, 1, metric.WithAttributes(scope, attribute.String("source", r.source)))
	m.chain.Record(ctx, int64(r.chain), metric.WithAttributes(scope))
	if r.committed > 0 {
		m.entries.Add(ctx, int64(r.committed), metric.WithAttributes(attribute.String("source", "committed")))
	}
	m.duration.Record(ctx, time.Since(r.start).Seconds(), metric.WithAttributes(scope))
}
