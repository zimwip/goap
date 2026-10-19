package graph

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// What the state of a baseline costs to read, and what the checkpoints are worth (ADR 0140): every read of a state
// says where it stopped (the cache, a stored baseline, or the landings down to the empty state), how many baselines it
// walked and how many entries it read from each source; every checkpoint stored says how long it took and how many
// entries it wrote. Prometheus: goap_graph_state_*, goap_graph_checkpoint_* (deploy/grafana/dashboards/goap-perf.json).

// Sources of a state read: where the walk down the chain of baselines stopped.
const (
	stateFromCache    = "cache"    // a state computed before (the state cache)
	stateFromStored   = "stored"   // a baseline storing its entries: a checkpoint, or a snapshot, a rebase, a revert
	stateFromLandings = "landings" // the versions landed, down to the empty state: no stored baseline on the way
)

type stateMetrics struct {
	reads      metric.Int64Counter
	chain      metric.Int64Histogram
	entries    metric.Int64Counter
	duration   metric.Float64Histogram
	cpStored   metric.Int64Counter
	cpEntries  metric.Int64Histogram
	cpDuration metric.Float64Histogram
	cpPending  metric.Int64UpDownCounter
}

var stateMeter = sync.OnceValue(func() *stateMetrics {
	m := otel.Meter("github.com/zimwip/goap/graph/graph")
	s := &stateMetrics{}
	s.reads, _ = m.Int64Counter("graph.state.reads", metric.WithUnit("{read}"),
		metric.WithDescription("Reads of the state of a baseline, by scope (whole, nodes) and by where the walk down its chain stopped (cache, stored, landings)"))
	s.chain, _ = m.Int64Histogram("graph.state.chain", metric.WithUnit("{baseline}"),
		metric.WithDescription("Baselines walked by a read of a state before it stopped"),
		metric.WithExplicitBucketBoundaries(0, 1, 2, 3, 5, 10, 20, 50, 100, 200))
	s.entries, _ = m.Int64Counter("graph.state.entries", metric.WithUnit("{entry}"),
		metric.WithDescription("Entries read for the states of baselines, by source (stored: entries of a stored baseline, landed: versions landed)"))
	s.duration, _ = m.Float64Histogram("graph.state.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of a read of a state, by scope"),
		metric.WithExplicitBucketBoundaries(0.0001, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5))
	s.cpStored, _ = m.Int64Counter("graph.checkpoint.stored", metric.WithUnit("{checkpoint}"),
		metric.WithDescription("Checkpoints stored: whole states of baselines, by outcome"))
	s.cpEntries, _ = m.Int64Histogram("graph.checkpoint.entries", metric.WithUnit("{entry}"),
		metric.WithDescription("Entries a checkpoint stores: the nodes of its state"),
		metric.WithExplicitBucketBoundaries(100, 1000, 10_000, 50_000, 100_000, 250_000, 500_000, 1_000_000, 2_000_000))
	s.cpDuration, _ = m.Float64Histogram("graph.checkpoint.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of the storing of a checkpoint"),
		metric.WithExplicitBucketBoundaries(0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60))
	s.cpPending, _ = m.Int64UpDownCounter("graph.checkpoint.pending", metric.WithUnit("{checkpoint}"),
		metric.WithDescription("Checkpoints queued and not stored yet"))
	return s
})

// stateRead is what one read of a state found, recorded when it ends.
type stateRead struct {
	start          time.Time
	whole          bool
	source         string
	chain          int
	stored, landed int
}

func (r *stateRead) record(ctx context.Context) {
	m := stateMeter()
	scope := attribute.String("scope", "nodes")
	if r.whole {
		scope = attribute.String("scope", "whole")
	}
	m.reads.Add(ctx, 1, metric.WithAttributes(scope, attribute.String("source", r.source)))
	m.chain.Record(ctx, int64(r.chain), metric.WithAttributes(scope))
	if r.stored > 0 {
		m.entries.Add(ctx, int64(r.stored), metric.WithAttributes(attribute.String("source", "stored")))
	}
	if r.landed > 0 {
		m.entries.Add(ctx, int64(r.landed), metric.WithAttributes(attribute.String("source", "landed")))
	}
	m.duration.Record(ctx, time.Since(r.start).Seconds(), metric.WithAttributes(scope))
}

func recordCheckpoint(ctx context.Context, start time.Time, entries int, err error) {
	m := stateMeter()
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	m.cpStored.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	m.cpDuration.Record(ctx, time.Since(start).Seconds())
	if err == nil && entries > 0 {
		m.cpEntries.Record(ctx, int64(entries))
	}
}
