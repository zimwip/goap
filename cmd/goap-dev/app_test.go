package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
)

// smokeApp builds the whole platform in one process on a temporary SQLite database, with no sign-in, and serves
// it through httptest (no port to pick, nothing calls os.Exit).
func smokeApp(t *testing.T) (*app, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GOAP_STORE", "sqlite")
	t.Setenv("GOAP_SQLITE_PATH", filepath.Join(dir, "goap.db"))
	t.Setenv("GOAP_AUTH_MODE", "none")
	t.Setenv("GOAP_SANDBOX", "inproc")
	t.Setenv("GOAP_CONNECTOR_TOKEN", "smoke-token")
	cfg := loadConfig()
	cfg.DomainsDir = filepath.Join("..", "..", "domains")
	cfg.MethodologiesDir = filepath.Join("..", "..", "methodologies")
	cfg.WebDir = filepath.Join(dir, "no-web")
	a, err := newApp(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	ts := httptest.NewServer(a.Server.Echo)
	t.Cleanup(func() {
		ts.Close()
		a.Close()
	})
	return a, ts
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

// The composition starts, answers its health, status, identity and graph calls, and stops.
func TestSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the whole platform; skipped with -short")
	}
	_, ts := smokeApp(t)

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ready" {
		t.Fatalf("/readyz = %d %q", resp.StatusCode, body)
	}

	code, status := getJSON(t, ts.URL+"/api/status")
	services, _ := status["services"].([]any)
	if code != http.StatusOK || status["status"] != "ok" || len(services) != 1 {
		t.Fatalf("/api/status = %d %v", code, status)
	}
	if svc, _ := services[0].(map[string]any); svc["name"] != "goap-dev" || svc["status"] != "up" {
		t.Fatalf("/api/status services = %v", services)
	}

	code, who := getJSON(t, ts.URL+"/api/whoami")
	if code != http.StatusOK || who["subject"] != "dev" {
		t.Fatalf("/api/whoami = %d %v", code, who)
	}

	// a graph call through the Connect handler: the bootstrap and the domains left their namespaces
	graph := graphv1connect.NewGraphServiceClient(ts.Client(), ts.URL)
	ns, err := graph.ListNamespaces(context.Background(), connect.NewRequest(&graphv1.ListNamespacesRequest{}))
	if err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	found := map[string]bool{}
	for _, n := range ns.Msg.Namespaces {
		found[n] = true
	}
	if !found["organisation"] || !found["platform"] {
		t.Fatalf("namespaces = %v", ns.Msg.Namespaces)
	}
}

// Close stops the background loops: a second app on the same process starts and stops cleanly, and a context ended
// by its parent ends the server.
func TestRunStopsWithItsContext(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the whole platform; skipped with -short")
	}
	dir := t.TempDir()
	t.Setenv("GOAP_STORE", "sqlite")
	t.Setenv("GOAP_SQLITE_PATH", filepath.Join(dir, "goap.db"))
	t.Setenv("GOAP_AUTH_MODE", "none")
	t.Setenv("GOAP_CONNECTOR_TOKEN", "smoke-token")
	cfg := loadConfig()
	cfg.DomainsDir = filepath.Join("..", "..", "domains")
	cfg.MethodologiesDir = filepath.Join("..", "..", "methodologies")
	cfg.WebDir = filepath.Join(dir, "no-web")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no local port to listen on: %v", err)
	}
	cfg.Addr = l.Addr().String()
	l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg) }()
	ready := false
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline) && !ready; time.Sleep(50 * time.Millisecond) {
		if resp, err := http.Get("http://" + cfg.Addr + "/readyz"); err == nil {
			resp.Body.Close()
			ready = resp.StatusCode == http.StatusOK
		}
	}
	if !ready {
		t.Fatal("run never became ready")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("run did not stop with its context")
	}
}
