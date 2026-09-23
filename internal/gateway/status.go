package gateway

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

// ServiceStatus is the health of one upstream service.
type ServiceStatus struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // up | down
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

// PlatformStatus aggregates the services health.
type PlatformStatus struct {
	Status   string          `json:"status"` // ok | degraded | down
	Services []ServiceStatus `json:"services"`
	Time     time.Time       `json:"time"`
}

// serviceName derives "graph" from "/goap.graph.v1.GraphService/".
func serviceName(prefix string) string {
	parts := strings.Split(strings.Trim(prefix, "/"), ".")
	if len(parts) >= 2 {
		return parts[1]
	}
	return prefix
}

// checkStatus probes the readiness endpoint of every upstream in parallel.
func checkStatus(ctx context.Context, routes []Route, hc *http.Client) PlatformStatus {
	out := PlatformStatus{Time: time.Now().UTC(), Services: make([]ServiceStatus, len(routes))}
	var wg sync.WaitGroup
	for i, r := range routes {
		wg.Add(1)
		go func(i int, r Route) {
			defer wg.Done()
			s := ServiceStatus{Name: serviceName(r.Prefix), Status: "down"}
			start := time.Now()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(r.Upstream, "/")+"/readyz", nil)
			resp, err := hc.Do(req)
			s.LatencyMs = time.Since(start).Milliseconds()
			switch {
			case err != nil:
				s.Error = "unreachable"
			case resp.StatusCode != http.StatusOK:
				s.Error = resp.Status
			default:
				s.Status = "up"
			}
			if resp != nil {
				resp.Body.Close()
			}
			out.Services[i] = s
		}(i, r)
	}
	wg.Wait()
	sort.Slice(out.Services, func(i, j int) bool { return out.Services[i].Name < out.Services[j].Name })
	up := 0
	for _, s := range out.Services {
		if s.Status == "up" {
			up++
		}
	}
	switch {
	case up == len(out.Services):
		out.Status = "ok"
	case up == 0:
		out.Status = "down"
	default:
		out.Status = "degraded"
	}
	return out
}

// statusHandler serves GET /api/status (cached a few seconds). Errors are
// kept generic: the endpoint does not reveal internal addresses.
func statusHandler(routes []Route) echo.HandlerFunc {
	hc := &http.Client{Timeout: 2 * time.Second}
	var mu sync.Mutex
	var cached PlatformStatus
	return func(c echo.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(cached.Time) > 3*time.Second {
			cached = checkStatus(c.Request().Context(), routes, hc)
		}
		return c.JSON(http.StatusOK, cached)
	}
}
