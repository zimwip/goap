// Package perfdump writes a text summary of a run of the performance stack (make perf-dump): what its Prometheus holds
// of the last run of goap-bench, laid out by the volume written, so a run can be kept and compared with another as text.
package perfdump

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

// Metric is one value of the summary: its column, its PromQL expression (one series; {{w}} the rate window), how it is
// written and whether it is an average over a bin of volume (else its last value in the bin).
type Metric struct {
	Name   string
	Expr   string
	Format string // fmt verb of a value
	Scale  float64
}

// rateWindow is the window of the rates of the expressions.
const rateWindow = "1m"

func bench(m string) string { return m + `{service_name="goap-bench"}` }

func quantile(q, ops string) string {
	return fmt.Sprintf(`histogram_quantile(%s, sum by (le) (rate(goap_bench_op_duration_seconds_bucket{service_name="goap-bench", outcome="ok", op=~%q}[{{w}}])))`, q, ops)
}

func cpu(container string) string {
	return fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{container_label_com_docker_compose_project="goap-perf", container_label_com_docker_compose_service=%q}[{{w}}])) or sum(rate(podman_container_cpu_seconds_total[{{w}}]) * on (id) group_left (name) podman_container_info{name="goap-perf-%s-1"})`, container, container)
}

func hit(cache string) string {
	return fmt.Sprintf(`sum(rate(goap_cache_reads_total{service_name="graph", cache=%q, result="hit"}[{{w}}])) / sum(rate(goap_cache_reads_total{service_name="graph", cache=%q}[{{w}}]))`, cache, cache)
}

// Volume is the metric the rows are laid out by.
const Volume = "versions"

// Metrics are the values of the summary, the volume first.
var Metrics = []Metric{
	{Name: Volume, Expr: "max(" + bench("goap_bench_versions") + ")", Format: "%.0f"},
	{Name: "nodes", Expr: "max(" + bench("goap_bench_nodes") + ")", Format: "%.0f"},
	{Name: "changes", Expr: "max(" + bench("goap_bench_changes") + ")", Format: "%.0f"},
	{Name: "links", Expr: "max(" + bench("goap_bench_links") + ")", Format: "%.0f"},
	{Name: "errors", Expr: "max(" + bench("goap_bench_errors") + ")", Format: "%.0f"},
	{Name: "versions/s", Expr: "sum(deriv(" + bench("goap_bench_versions") + "[2m]))", Format: "%.0f"},
	{Name: "changes/s", Expr: "sum(deriv(" + bench("goap_bench_changes") + "[2m]))", Format: "%.1f"},
	{Name: "commit p50 ms", Expr: quantile("0.5", "CommitEdits|CommitChange"), Format: "%.1f", Scale: 1000},
	{Name: "commit p95 ms", Expr: quantile("0.95", "CommitEdits|CommitChange"), Format: "%.1f", Scale: 1000},
	{Name: "close p50 ms", Expr: quantile("0.5", "CloseChange|MergeChange"), Format: "%.1f", Scale: 1000},
	{Name: "checkout p50 ms", Expr: quantile("0.5", "ImpactNodeCheckout|ImpactNodeCheckoutBatch"), Format: "%.1f", Scale: 1000},
	{Name: "checkout p95 ms", Expr: quantile("0.95", "ImpactNodeCheckout|ImpactNodeCheckoutBatch"), Format: "%.1f", Scale: 1000},
	{Name: "update p50 ms", Expr: quantile("0.5", "ImpactNodeUpdate"), Format: "%.1f", Scale: 1000},
	{Name: "links/change", Expr: `sum(rate(goap_bench_change_links_sum{service_name="goap-bench"}[{{w}}])) / sum(rate(goap_bench_change_links_count{service_name="goap-bench"}[{{w}}]))`, Format: "%.0f"},
	{Name: "queries/RPC", Expr: `sum(rate(goap_db_client_operation_duration_seconds_count{service_name="graph", pgx_operation_type="query"}[{{w}}])) / sum(rate(goap_rpc_server_duration_milliseconds_count{service_name="graph"}[{{w}}]))`, Format: "%.2f"},
	{Name: "cpu graph", Expr: cpu("graph"), Format: "%.2f"},
	{Name: "cpu postgres", Expr: cpu("postgres"), Format: "%.2f"},
	{Name: "drafts hit %", Expr: hit("graph.drafts"), Format: "%.1f", Scale: 100},
	{Name: "versions hit %", Expr: hit("graph.versions"), Format: "%.1f", Scale: 100},
	{Name: "go heap MiB", Expr: `sum(goap_go_memory_used_bytes{service_name="graph"})`, Format: "%.0f", Scale: 1.0 / (1 << 20)},
	{Name: "db MiB", Expr: `max(pg_database_size_bytes{datname="goap"})`, Format: "%.0f", Scale: 1.0 / (1 << 20)},
}

// Query is the expression of m, its rate window set.
func (m Metric) Query() string { return strings.ReplaceAll(m.Expr, "{{w}}", rateWindow) }

// Point is a value at a time.
type Point struct {
	T time.Time
	V float64
}

// Samples are the points of each metric by name.
type Samples map[string][]Point

// LastRun keeps the points of the last run of goap-bench: from the last time its volume fell (a new run starts from
// zero), up to the last point. ok false: no volume at all.
func LastRun(s Samples) (from, to time.Time, ok bool) {
	vol := s[Volume]
	if len(vol) == 0 {
		return
	}
	start := 0
	for i := 1; i < len(vol); i++ {
		if vol[i].V < vol[i-1].V || vol[i].T.Sub(vol[i-1].T) > 10*time.Minute {
			start = i
		}
	}
	return vol[start].T, vol[len(vol)-1].T, true
}

// Row is a bin of volume: the volume it reaches and the value of each metric.
type Row struct {
	Volume float64
	Values map[string]float64
}

// Rows lays the points between from and to out in bins of volume (bins of them, by the volume at the end): the mean of
// each metric over the points of the bin, the counters (volume, nodes...) at its end.
func Rows(s Samples, from, to time.Time, bins int) []Row {
	vol := within(s[Volume], from, to)
	if len(vol) == 0 || bins <= 0 {
		return nil
	}
	top := vol[len(vol)-1].V
	step := niceStep(top / float64(bins))
	var rows []Row
	for lo := 0.0; lo < top; lo += step {
		hi := lo + step
		var at []time.Time
		for _, p := range vol {
			if p.V > lo && p.V <= hi {
				at = append(at, p.T)
			}
		}
		if len(at) == 0 {
			continue
		}
		row := Row{Volume: math.Min(hi, top), Values: map[string]float64{}}
		for _, m := range Metrics {
			pts := within(s[m.Name], at[0], at[len(at)-1])
			if len(pts) == 0 {
				continue
			}
			if counter(m.Name) {
				row.Values[m.Name] = pts[len(pts)-1].V
				continue
			}
			sum, n := 0.0, 0
			for _, p := range pts {
				if !math.IsNaN(p.V) && !math.IsInf(p.V, 0) {
					sum, n = sum+p.V, n+1
				}
			}
			if n > 0 {
				row.Values[m.Name] = sum / float64(n)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func counter(name string) bool {
	switch name {
	case Volume, "nodes", "changes", "links", "errors", "db MiB":
		return true
	}
	return false
}

func within(ps []Point, from, to time.Time) []Point {
	i := sort.Search(len(ps), func(i int) bool { return !ps[i].T.Before(from) })
	j := sort.Search(len(ps), func(i int) bool { return ps[i].T.After(to) })
	return ps[i:j]
}

// niceStep rounds x up to 1, 2 or 5 times a power of ten.
func niceStep(x float64) float64 {
	if x <= 0 {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(x)))
	for _, k := range []float64{1, 2, 5, 10} {
		if k*p >= x {
			return k * p
		}
	}
	return 10 * p
}

// Header says what was measured: when, which code, which run.
type Header struct {
	At       time.Time
	Commit   string
	Branch   string
	Args     string
	From, To time.Time
}

// Write writes the summary as Markdown: the header, the totals of the run, then one row per bin of volume.
func Write(w io.Writer, h Header, s Samples, rows []Row) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# GOAP perf dump %s\n\n", h.At.UTC().Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(&b, "- commit: %s", h.Commit)
	if h.Branch != "" {
		fmt.Fprintf(&b, " (%s)", h.Branch)
	}
	fmt.Fprintf(&b, "\n- bench args: %s\n", orNone(h.Args))
	d := h.To.Sub(h.From).Round(time.Second)
	fmt.Fprintf(&b, "- run: %s → %s (%s)\n", h.From.UTC().Format("2006-01-02 15:04:05"), h.To.UTC().Format("15:04:05"), d)
	if n := len(rows); n > 0 {
		last := rows[n-1].Values
		fmt.Fprintf(&b, "- reached: %.0f versions, %.0f nodes, %.0f changes, %.0f links, %.0f errors, %.0f MiB in PostgreSQL\n",
			last[Volume], last["nodes"], last["changes"], last["links"], last["errors"], last["db MiB"]/(1<<20))
		if secs := d.Seconds(); secs > 0 && last[Volume] > 0 {
			fmt.Fprintf(&b, "- mean: %.0f versions/s, %.1f changes/s", last[Volume]/secs, last["changes"]/secs)
			if g, p := mean(s["cpu graph"], h.From, h.To), mean(s["cpu postgres"], h.From, h.To); g > 0 || p > 0 {
				per := secs * 1000 / last[Volume]
				fmt.Fprintf(&b, "; CPU per 1000 versions: graph %.2f s, postgres %.2f s", g*per, p*per)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	var cols []Metric
	for _, m := range Metrics {
		if m.Name == "nodes" || m.Name == "links" || m.Name == "errors" {
			continue
		}
		cols = append(cols, m)
	}
	for i, m := range cols {
		if i > 0 {
			b.WriteString(" | ")
		} else {
			b.WriteString("| ")
		}
		b.WriteString(m.Name)
	}
	b.WriteString(" |\n|")
	for range cols {
		b.WriteString("---:|")
	}
	b.WriteString("\n")
	for _, r := range rows {
		b.WriteString("|")
		for _, m := range cols {
			v, ok := r.Values[m.Name]
			if !ok {
				b.WriteString(" – |")
				continue
			}
			if m.Scale != 0 {
				v *= m.Scale
			}
			fmt.Fprintf(&b, " "+m.Format+" |", v)
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(not given)"
	}
	return s
}

func mean(ps []Point, from, to time.Time) float64 {
	pts := within(ps, from, to)
	sum, n := 0.0, 0
	for _, p := range pts {
		if !math.IsNaN(p.V) {
			sum, n = sum+p.V, n+1
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
