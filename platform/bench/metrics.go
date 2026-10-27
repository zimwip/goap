package bench

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
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
	Totals Totals
	// Speed follows the write speed as the data grows (bench.write.speed, bench.write.drift)
	Speed    Speed
	duration metric.Float64Histogram
	size     metric.Int64Histogram
	// shaped is the duration of the calls whose cost follows the size of what they handle, by that size (LinkClass):
	// the links the node of a node call holds, the links the nodes of a change hold for a change call.
	shaped      metric.Float64Histogram
	changeEdits metric.Int64Histogram
	changeLinks metric.Int64Histogram

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
	if m.shaped, err = meter.Float64Histogram("bench.op.shaped.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of a call by the size of what it handles: links held by its node, or by the nodes of its change (attribute links)"),
		metric.WithExplicitBucketBoundaries(latencyBuckets...)); err != nil {
		return nil, err
	}
	if m.changeEdits, err = meter.Int64Histogram("bench.change.edits", metric.WithUnit("{node}"),
		metric.WithDescription("Nodes a change writes"),
		metric.WithExplicitBucketBoundaries(1, 2, 5, 10, 20, 50, 100, 200, 500)); err != nil {
		return nil, err
	}
	if m.changeLinks, err = meter.Int64Histogram("bench.change.links", metric.WithUnit("{link}"),
		metric.WithDescription("Links the nodes a change writes hold, once written"),
		metric.WithExplicitBucketBoundaries(10, 100, 1000, 10000, 100000)); err != nil {
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
	speeds := []struct {
		name, desc string
		v          func() (float64, bool)
	}{
		{"bench.write.speed", "Versions written per second over the last tenth of the versions written", m.Speed.Rate},
		{"bench.write.drift", "Relative change of the write speed when the volume doubles: the speed over the last tenth of the versions written against the speed over the same tenth of half of them, minus 1 (0: steady; -0.25: a quarter slower at twice the data)", m.Speed.Drift},
	}
	for _, g := range speeds {
		v := g.v
		if _, err := meter.Float64ObservableGauge(g.name, metric.WithDescription(g.desc),
			metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
				if x, ok := v(); ok {
					o.Observe(x)
				}
				return nil
			})); err != nil {
			return nil, err
		}
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

// LinkClass is the size class of a number of links held, the links attribute of bench.op.shaped.duration: an order of
// magnitude, so a dashboard draws one line per class.
func LinkClass(n int) string {
	switch {
	case n < 10:
		return "0-9"
	case n < 100:
		return "10-99"
	case n < 1000:
		return "100-999"
	case n < 10000:
		return "1000-9999"
	}
	return "10000+"
}

// Shaped records a call by the size of what it handled (links held, LinkClass); Observe records it too.
func (m *Metrics) Shaped(ctx context.Context, op string, d time.Duration, links int) {
	m.shaped.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("op", op), attribute.String("links", LinkClass(links))))
}

// Change records the shape of a change written: the nodes it writes and the links they hold.
func (m *Metrics) Change(ctx context.Context, edits, links int) {
	m.changeEdits.Record(ctx, int64(edits))
	m.changeLinks.Record(ctx, int64(links))
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
	// WriteSpeed and WriteDrift are Speed.Rate and Speed.Drift at the end of the window (NaN: not measured yet)
	WriteSpeed, WriteDrift float64
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
	speed, drift := math.NaN(), math.NaN()
	if x, ok := m.Speed.Rate(); ok {
		speed = x
	}
	if x, ok := m.Speed.Drift(); ok {
		drift = x
	}
	rows := make([]Row, 0, len(ops))
	for _, op := range ops {
		s := w[op]
		slices.Sort(s.durations)
		r := Row{Elapsed: elapsed, Versions: m.Totals.Versions.Load(), Nodes: m.Totals.Nodes.Load(), Links: m.Totals.Links.Load(),
			Changes: m.Totals.Changes.Load(), Domains: m.Totals.DomainVersions.Load(), Op: op, Count: len(s.durations), Errors: s.errors,
			P50: quantile(s.durations, 0.5), P95: quantile(s.durations, 0.95), P99: quantile(s.durations, 0.99), Max: quantile(s.durations, 1),
			WriteSpeed: speed, WriteDrift: drift}
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

// SpeedSummary tells the write speed and its drift when measured: " write=120/s drift=-12%/x2".
func SpeedSummary(speed, drift float64) string {
	s := ""
	if !math.IsNaN(speed) {
		s += fmt.Sprintf(" write=%.0f/s", speed)
	}
	if !math.IsNaN(drift) {
		s += fmt.Sprintf(" drift=%+.0f%%/x2", 100*drift)
	}
	return s
}

// float formats a measure of the report, empty when not measured (NaN).
func float(x float64, prec int) string {
	if math.IsNaN(x) {
		return ""
	}
	return strconv.FormatFloat(x, 'f', prec, 64)
}

// CSVHeader is the first line of the report.
var CSVHeader = []string{"elapsed_s", "versions", "nodes", "links", "changes", "domain_versions", "op", "count", "errors", "rate_per_s", "p50_ms", "p95_ms", "p99_ms", "max_ms", "mean_bytes", "write_per_s", "write_drift"}

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
			r.Op, strconv.Itoa(r.Count), strconv.Itoa(r.Errors), strconv.FormatFloat(r.Rate, 'f', 2, 64), ms(r.P50), ms(r.P95), ms(r.P99), ms(r.Max), itoa(r.MeanBytes),
			float(r.WriteSpeed, 1), float(r.WriteDrift, 3)}); err != nil {
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
	s += SpeedSummary(r.WriteSpeed, r.WriteDrift)
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
