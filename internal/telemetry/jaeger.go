package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/observe"
)

// JaegerTraces reads traces from the Jaeger query API, so that the
// self-observation agent can look for slow spans (GOAP_TRACE_QUERY_URL).
type JaegerTraces struct {
	BaseURL string // e.g. http://jaeger:16686
	Client  *http.Client
}

var _ engine.TraceSource = JaegerTraces{}

// Spans implements engine.TraceSource.
func (j JaegerTraces) Spans(ctx context.Context, traceID string) ([]observe.Span, error) {
	hc := j.Client
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(j.BaseURL, "/")+"/api/traces/"+traceID, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jaeger: %s", resp.Status)
	}
	var body struct {
		Data []struct {
			Spans []struct {
				OperationName string `json:"operationName"`
				Duration      int64  `json:"duration"` // µs
				Tags          []struct {
					Key   string `json:"key"`
					Value any    `json:"value"`
				} `json:"tags"`
			} `json:"spans"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	var out []observe.Span
	for _, t := range body.Data {
		for _, s := range t.Spans {
			sp := observe.Span{Name: s.OperationName, DurationMs: s.Duration / 1000, Attributes: map[string]string{}}
			for _, tag := range s.Tags {
				v := fmt.Sprint(tag.Value)
				sp.Attributes[tag.Key] = v
				if (tag.Key == "error" && v == "true") || (tag.Key == "otel.status_code" && v == "ERROR") {
					sp.Error = true
				}
			}
			out = append(out, sp)
		}
	}
	return out, nil
}

// SelfImprovementFromEnv configures the self-observation builtins: traces
// from GOAP_TRACE_QUERY_URL (Jaeger query API, optional), links to
// GOAP_TRACE_UI_URL (default <query>/trace/).
func SelfImprovementFromEnv(drafts engine.MethodologyDrafts) engine.SelfImprovement {
	cfg := engine.SelfImprovement{Drafts: drafts}
	if u := os.Getenv("GOAP_TRACE_QUERY_URL"); u != "" {
		cfg.Traces = JaegerTraces{BaseURL: u}
		cfg.TraceURL = strings.TrimSuffix(u, "/") + "/trace/"
	}
	if u := os.Getenv("GOAP_TRACE_UI_URL"); u != "" {
		cfg.TraceURL = u
	}
	return cfg
}
