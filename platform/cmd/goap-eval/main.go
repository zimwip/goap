// Command goap-eval runs offline evaluation cases (pkg/eval, ADR 0099) against the model gateway, as the user of the
// token given: every call goes through the gateway's admission and ledger (source "eval").
//
//	goap-eval [-url http://localhost:8080] [-token $GOAP_TOKEN] [-model alias] [-runs n] [-json report.json] [-v] <file or dir>...
//
// The exit status is 1 when a case does not reach its pass rate, 2 on a usage or loading error.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/zimwip/goap/engine/eval"
	"github.com/zimwip/goap/platform/modelgw"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("goap-eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", envOr("GOAP_EVAL_URL", "http://localhost:8080"), "base URL of the model gateway (goap-dev, the gateway or modelgw)")
	token := fs.String("token", os.Getenv("GOAP_TOKEN"), "bearer token of the user the calls are made as (GOAP_TOKEN)")
	model := fs.String("model", "", "alias or provider/model overriding the model of every case")
	runs := fs.Int("runs", 0, "runs of every case, overriding theirs")
	jsonOut := fs.String("json", "", "write the full report as JSON to this file")
	verbose := fs.Bool("v", false, "print every check and the answers of failed runs")
	timeout := fs.Duration("timeout", 30*time.Minute, "bound of the whole evaluation")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: goap-eval [flags] <case file or directory>...")
		fs.PrintDefaults()
		return 2
	}
	cases, err := eval.Load(fs.Args()...)
	if err != nil {
		fmt.Fprintln(stderr, "goap-eval:", err)
		return 2
	}
	hc := &http.Client{Timeout: 10 * time.Minute, Transport: bearer{token: *token, next: http.DefaultTransport}}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	rep := eval.Runner{Client: modelgw.NewClient(hc, strings.TrimRight(*url, "/")), Model: *model, Runs: *runs}.RunAll(ctx, cases)
	print(stdout, rep, *verbose)
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(*jsonOut, b, 0o644); err != nil {
			fmt.Fprintln(stderr, "goap-eval:", err)
			return 2
		}
	}
	if rep.Failed > 0 {
		return 1
	}
	return 0
}

func print(w io.Writer, rep eval.Report, verbose bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CASE\tMODEL\tPASS\tRATE\tIN TOK\tOUT TOK\tTOOLS\tMS")
	for _, c := range rep.Cases {
		mark := "ok"
		if !c.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%.0f%%\t%.0f\t%.0f\t%.1f\t%.0f\n", c.Name, c.Model, mark, 100*c.PassRate, c.InputTokens, c.OutputTokens, c.ToolCalls, c.DurationMs)
	}
	_ = tw.Flush()
	for _, c := range rep.Cases {
		for i, r := range c.Runs {
			if r.Passed && !verbose {
				continue
			}
			fmt.Fprintf(w, "\n%s, run %d: passed=%v trajectory=%v\n", c.Name, i+1, r.Passed, r.Trajectory)
			for _, ch := range r.Checks {
				if !ch.Passed || verbose {
					fmt.Fprintf(w, "  [%s] %s %s\n", mark(ch.Passed), ch.Name, ch.Detail)
				}
			}
			if r.Error != "" {
				fmt.Fprintf(w, "  error: %s\n", r.Error)
			}
			if !r.Passed {
				fmt.Fprintf(w, "  answer: %.600s\n", r.Answer)
			}
		}
	}
	fmt.Fprintf(w, "\n%d passed, %d failed\n", rep.Passed, rep.Failed)
}

func mark(ok bool) string {
	if ok {
		return "ok"
	}
	return "x"
}

// bearer adds the token to every request.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.token != "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.next.RoundTrip(r)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
