package bench

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Outcomes of an operation, the outcome attribute of bench.op.duration.
const (
	OutcomeOK       = "ok"
	OutcomeConflict = "conflict"
	OutcomeError    = "error"
)

// Totals is what the bench wrote so far (atomic counters, read by the gauges and the report).
type Totals struct {
	Versions, Nodes, Links, Changes, DomainVersions, DomainTypes, Errors atomic.Int64
}

// Metrics records every operation: OpenTelemetry instruments (scope github.com/zimwip/goap/bench; through the collector
// of the stack they read goap_bench_* in Prometheus) and the samples of the current window for the CSV report.
type Metrics struct {
	Totals   Totals
	duration metric.Float64Histogram
	size     metric.Int64Histogram

	mu     sync.Mutex
	window map[string]*sample
}

type sample struct {
	durations []float64 // seconds
	errors    int
	bytes     int64
	sized     int
}

// latencyBuckets are the bucket bounds of bench.op.duration, in seconds: 0.5 ms to 2 min.
var latencyBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120}

// sizeBuckets are the bucket bounds of bench.response.size, in bytes: 256 B to 64 MiB.
var sizeBuckets = []float64{256, 1 << 10, 4 << 10, 16 << 10, 64 << 10, 256 << 10, 1 << 20, 4 << 20, 16 << 20, 64 << 20}

// NewMetrics registers the instruments on the global meter provider (a no-op one when telemetry is off).
func NewMetrics() (*Metrics, error) {
	m := &Metrics{window: map[string]*sample{}}
	meter := otel.Meter("github.com/zimwip/goap/bench")
	var err error
	if m.duration, err = meter.Float64Histogram("bench.op.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of an API call made by the bench, as the client sees it"),
		metric.WithExplicitBucketBoundaries(latencyBuckets...)); err != nil {
		return nil, err
	}
	if m.size, err = meter.Int64Histogram("bench.response.size", metric.WithUnit("By"),
		metric.WithDescription("Size of the body of an API answer"),
		metric.WithExplicitBucketBoundaries(sizeBuckets...)); err != nil {
		return nil, err
	}
	gauges := []struct {
		name, desc string
		v          *atomic.Int64
	}{
		{"bench.versions", "Node versions written by the bench (created and modified nodes)", &m.Totals.Versions},
		{"bench.nodes", "Nodes created by the bench", &m.Totals.Nodes},
		{"bench.links", "Links written by the bench", &m.Totals.Links},
		{"bench.changes", "Changes committed by the bench", &m.Totals.Changes},
		{"bench.domain.versions", "Versions of the bench domain published", &m.Totals.DomainVersions},
		{"bench.domain.types", "Node types of the latest bench domain published", &m.Totals.DomainTypes},
		{"bench.errors", "Calls that failed", &m.Totals.Errors},
	}
	for _, g := range gauges {
		v := g.v
		if _, err := meter.Int64ObservableGauge(g.name, metric.WithDescription(g.desc),
			metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error { o.Observe(v.Load()); return nil })); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Observe records one operation.
func (m *Metrics) Observe(ctx context.Context, op string, d time.Duration, outcome string) {
	m.duration.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("op", op), attribute.String("outcome", outcome)))
	if outcome != OutcomeOK {
		m.Totals.Errors.Add(1)
	}
	m.mu.Lock()
	s := m.at(op)
	if outcome == OutcomeOK {
		s.durations = append(s.durations, d.Seconds())
	} else {
		s.errors++
	}
	m.mu.Unlock()
}

// Size records the size of an answer.
func (m *Metrics) Size(ctx context.Context, op string, n int64) {
	m.size.Record(ctx, n, metric.WithAttributes(attribute.String("op", op)))
	m.mu.Lock()
	s := m.at(op)
	s.bytes += n
	s.sized++
	m.mu.Unlock()
}

func (m *Metrics) at(op string) *sample {
	s := m.window[op]
	if s == nil {
		s = &sample{}
		m.window[op] = s
	}
	return s
}

// Row is one line of the CSV report: an operation over a window, with the totals at its end.
type Row struct {
	Elapsed                                  time.Duration
	Versions, Nodes, Links, Changes, Domains int64
	Op                                       string
	Count, Errors                            int
	Rate                                     float64 // calls per second over the window
	P50, P95, P99, Max                       time.Duration
	MeanBytes                                int64
}

// Flush ends the window: one row per operation seen in it, sorted by name.
func (m *Metrics) Flush(elapsed, window time.Duration) []Row {
	m.mu.Lock()
	w := m.window
	m.window = map[string]*sample{}
	m.mu.Unlock()
	ops := make([]string, 0, len(w))
	for op := range w {
		ops = append(ops, op)
	}
	slices.Sort(ops)
	rows := make([]Row, 0, len(ops))
	for _, op := range ops {
		s := w[op]
		slices.Sort(s.durations)
		r := Row{Elapsed: elapsed, Versions: m.Totals.Versions.Load(), Nodes: m.Totals.Nodes.Load(), Links: m.Totals.Links.Load(),
			Changes: m.Totals.Changes.Load(), Domains: m.Totals.DomainVersions.Load(), Op: op, Count: len(s.durations), Errors: s.errors,
			P50: quantile(s.durations, 0.5), P95: quantile(s.durations, 0.95), P99: quantile(s.durations, 0.99), Max: quantile(s.durations, 1)}
		if window > 0 {
			r.Rate = float64(len(s.durations)) / window.Seconds()
		}
		if s.sized > 0 {
			r.MeanBytes = s.bytes / int64(s.sized)
		}
		rows = append(rows, r)
	}
	return rows
}

// quantile of sorted samples (nearest rank), as a duration.
func quantile(sorted []float64, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q*float64(len(sorted))+0.5) - 1
	i = max(0, min(i, len(sorted)-1))
	return time.Duration(sorted[i] * float64(time.Second))
}

// CSVHeader is the first line of the report.
var CSVHeader = []string{"elapsed_s", "versions", "nodes", "links", "changes", "domain_versions", "op", "count", "errors", "rate_per_s", "p50_ms", "p95_ms", "p99_ms", "max_ms", "mean_bytes"}

// CSVWriter writes the rows of the report.
type CSVWriter struct{ w *csv.Writer }

// NewCSVWriter writes the header and returns the writer.
func NewCSVWriter(w io.Writer) (*CSVWriter, error) {
	c := &CSVWriter{w: csv.NewWriter(w)}
	if err := c.w.Write(CSVHeader); err != nil {
		return nil, err
	}
	c.w.Flush()
	return c, c.w.Error()
}

// Write appends rows and flushes them.
func (c *CSVWriter) Write(rows []Row) error {
	ms := func(d time.Duration) string {
		return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 3, 64)
	}
	for _, r := range rows {
		if err := c.w.Write([]string{strconv.FormatFloat(r.Elapsed.Seconds(), 'f', 1, 64), itoa(r.Versions), itoa(r.Nodes), itoa(r.Links), itoa(r.Changes), itoa(r.Domains),
			r.Op, strconv.Itoa(r.Count), strconv.Itoa(r.Errors), strconv.FormatFloat(r.Rate, 'f', 2, 64), ms(r.P50), ms(r.P95), ms(r.P99), ms(r.Max), itoa(r.MeanBytes)}); err != nil {
			return err
		}
	}
	c.w.Flush()
	return c.w.Error()
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// Summary is a one-line progress report of a window.
func Summary(rows []Row, window time.Duration) string {
	if len(rows) == 0 {
		return "no call"
	}
	r := rows[0]
	s := fmt.Sprintf("t=%5.0fs versions=%d nodes=%d changes=%d domains=%d", r.Elapsed.Seconds(), r.Versions, r.Nodes, r.Changes, r.Domains)
	for _, r := range rows {
		if r.Count == 0 && r.Errors == 0 {
			continue
		}
		s += fmt.Sprintf(" | %s n=%d p50=%s p95=%s", r.Op, r.Count, r.P50.Round(time.Microsecond*100), r.P95.Round(time.Microsecond*100))
		if r.Errors > 0 {
			s += fmt.Sprintf(" err=%d", r.Errors)
		}
	}
	return s
}
