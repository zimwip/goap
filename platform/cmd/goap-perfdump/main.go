// Command goap-perfdump writes a text summary of the last run of the performance stack (make perf-dump): what its
// Prometheus holds of the last run of goap-bench, laid out by the volume written, with the date and the commit, printed
// and kept in .goap/perf/dumps/<date>-<commit>.md.
//
//	goap-perfdump [-prometheus http://localhost:9390] [-since 12h] [-bins 10] [-out .goap/perf/dumps] [-args "..."]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zimwip/goap/platform/perfdump"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("goap-perfdump", flag.ContinueOnError)
	fs.SetOutput(stderr)
	prom := fs.String("prometheus", envOr("GOAP_PERF_PROMETHEUS", "http://localhost:9390"), "Prometheus of the performance stack")
	since := fs.Duration("since", 12*time.Hour, "how far back to look for the last run")
	bins := fs.Int("bins", 10, "rows of the summary, by volume written")
	out := fs.String("out", ".goap/perf/dumps", "directory the summary is kept in (empty: not kept)")
	benchArgs := fs.String("args", os.Getenv("PERF_ARGS"), "the arguments of the run, written in the header")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	now := time.Now()
	step := max(*since/2000, 15*time.Second)
	samples := perfdump.Samples{}
	for _, m := range perfdump.Metrics {
		pts, err := queryRange(ctx, *prom, m.Query(), now.Add(-*since), now, step)
		if err != nil {
			fmt.Fprintf(stderr, "goap-perfdump: %s: %v\n", m.Name, err)
			if m.Name == perfdump.Volume {
				return 1
			}
			continue
		}
		samples[m.Name] = pts
	}
	from, to, ok := perfdump.LastRun(samples)
	if !ok {
		fmt.Fprintln(stderr, "goap-perfdump: no run of goap-bench in Prometheus (make perf first)")
		return 1
	}
	h := perfdump.Header{At: now, Commit: git("rev-parse", "--short", "HEAD"), Branch: git("rev-parse", "--abbrev-ref", "HEAD"), Args: *benchArgs, From: from, To: to}
	if git("status", "--porcelain", "--untracked-files=no") != "" {
		h.Commit += " (uncommitted changes)"
	}
	var b strings.Builder
	if err := perfdump.Write(&b, h, samples, perfdump.Rows(samples, from, to, *bins)); err != nil {
		fmt.Fprintln(stderr, "goap-perfdump:", err)
		return 1
	}
	fmt.Fprint(stdout, b.String())
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			fmt.Fprintln(stderr, "goap-perfdump:", err)
			return 1
		}
		name := now.Format("20060102-150405") + "-" + strings.Fields(h.Commit + " unknown")[0] + ".md"
		path := filepath.Join(*out, name)
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			fmt.Fprintln(stderr, "goap-perfdump:", err)
			return 1
		}
		fmt.Fprintln(stderr, "kept in", path)
	}
	return 0
}

// queryRange asks Prometheus for expr between from and to: the points of its first series.
func queryRange(ctx context.Context, base, expr string, from, to time.Time, step time.Duration) ([]perfdump.Point, error) {
	q := url.Values{"query": {expr}, "start": {strconv.FormatInt(from.Unix(), 10)}, "end": {strconv.FormatInt(to.Unix(), 10)}, "step": {strconv.Itoa(int(step.Seconds()))}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/v1/query_range?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Values [][2]any `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("prometheus answer: %w", err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", body.Error)
	}
	if len(body.Data.Result) == 0 {
		return nil, nil
	}
	var out []perfdump.Point
	for _, v := range body.Data.Result[0].Values {
		t, _ := v[0].(float64)
		s, _ := v[1].(string)
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		out = append(out, perfdump.Point{T: time.Unix(int64(t), 0), V: f})
	}
	return out, nil
}

func git(args ...string) string {
	b, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
