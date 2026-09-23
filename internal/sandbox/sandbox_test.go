package sandbox

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zimwip/goap/gen/goap/runtime/v1/runtimev1connect"
	"github.com/zimwip/goap/pkg/dsl"
)

type recordingHost struct {
	mu    sync.Mutex
	calls []string
}

func (h *recordingHost) add(s string) { h.mu.Lock(); h.calls = append(h.calls, s); h.mu.Unlock() }
func (h *recordingHost) Complete(_ context.Context, r dsl.CompleteRequest) (dsl.CompleteResult, error) {
	h.add("llm:" + r.Prompt)
	return dsl.CompleteResult{Text: "summary", InputTokens: 5, OutputTokens: 2}, nil
}
func (h *recordingHost) RunAgent(_ context.Context, name, _ string) (dsl.AgentResult, error) {
	h.add("agent:" + name)
	return dsl.AgentResult{Status: "waiting"}, dsl.ErrSuspended
}
func (h *recordingHost) CallTool(context.Context, string, map[string]any) (any, error) {
	return nil, nil
}
func (h *recordingHost) Node(_ context.Context, key string) (dsl.Node, error) {
	h.add("node:" + key)
	return dsl.Node{Key: key, Type: "Requirement"}, nil
}
func (h *recordingHost) Nodes(context.Context, string) ([]dsl.Node, error) { return nil, nil }
func (h *recordingHost) Links(context.Context, string, string, string) ([]dsl.Link, error) {
	return nil, nil
}

// loopback starts runners as in-process HTTP servers.
type loopback struct {
	mu   sync.Mutex
	srvs map[string]*httptest.Server
}

func (l *loopback) Name() string { return "loopback" }
func (l *loopback) Start(_ context.Context, spec Spec) (Instance, error) {
	mux := http.NewServeMux()
	mux.Handle(runtimev1connect.NewSandboxServiceHandler(&Runner{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	s := httptest.NewServer(mux)
	l.mu.Lock()
	if l.srvs == nil {
		l.srvs = map[string]*httptest.Server{}
	}
	l.srvs[spec.ID] = s
	l.mu.Unlock()
	return Instance{ID: spec.ID, Endpoint: s.URL}, nil
}
func (l *loopback) Stop(_ context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok := l.srvs[id]; ok {
		s.Close()
		delete(l.srvs, id)
	}
	return nil
}

func runtimeServer(t *testing.T) (*Runtime, string) {
	rt := NewRuntime()
	mux := http.NewServeMux()
	mux.Handle(runtimev1connect.NewRuntimeServiceHandler(rt))
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return rt, s.URL
}

const script = `
const n = ctx.node("REQ-1");
const r = ctx.complete({ prompt: "summarize " + n.key });
ctx.addArtifact("summary", { text: r.text, tokens: r.inputTokens });
ctx.log("done " + n.type);
`

func checkResult(t *testing.T, res dsl.Result, h *recordingHost) {
	t.Helper()
	if len(res.Items) != 1 || res.Items[0]["type"] != "summary" || res.Items[0]["data"].(map[string]any)["text"] != "summary" {
		t.Fatalf("unexpected items %+v", res.Items)
	}
	if len(res.Logs) != 1 || res.Logs[0].Message != "done Requirement" {
		t.Fatalf("logs %+v", res.Logs)
	}
	if strings.Join(h.calls, ",") != "node:REQ-1,llm:summarize REQ-1" {
		t.Fatalf("host calls %v", h.calls)
	}
}

func TestRemoteExecution(t *testing.T) {
	rt, url := runtimeServer(t)
	pool := &Pool{Provisioner: &loopback{}, Runtime: rt, RuntimeURL: url}
	ctx := context.Background()
	sb, err := pool.Acquire(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	h := &recordingHost{}
	res, err := sb.Execute(ctx, dsl.Job{Language: "javascript", Code: script}, h)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, res, h)
	// suspension crosses the sandbox boundary
	res, err = sb.Execute(ctx, dsl.Job{Language: "javascript", Code: `ctx.addImpact("X", "y"); ctx.runAgent("child", "go")`}, h)
	if err != nil || !res.Suspended || len(res.Items) != 0 {
		t.Fatalf("expected suspension: %+v %v", res, err)
	}
	// errors are reported, tokens of finished jobs are revoked
	if _, err := sb.Execute(ctx, dsl.Job{Language: "javascript", Code: `throw new Error("boom")`}, h); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected script error, got %v", err)
	}
	if len(rt.jobs) != 0 {
		t.Fatalf("job tokens must be revoked: %d left", len(rt.jobs))
	}
	pool.Release(ctx, "p1")
}

func TestRuntimeRejectsBadToken(t *testing.T) {
	rt, url := runtimeServer(t)
	_, done := rt.Register("job", &recordingHost{})
	defer done()
	h := RemoteHost{Client: runtimev1connect.NewRuntimeServiceClient(http.DefaultClient, url), JobID: "job", Token: "forged"}
	if _, err := h.Node(context.Background(), "REQ-1"); err == nil {
		t.Fatal("forged token accepted")
	}
}

func TestProcessProvisioner(t *testing.T) {
	if testing.Short() {
		t.Skip("builds goap-runner")
	}
	bin := filepath.Join(t.TempDir(), "goap-runner")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/goap-runner")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build runner: %v\n%s", err, out)
	}
	t.Setenv("GOAP_SECRET_FOR_TEST", "must-not-leak")
	rt, url := runtimeServer(t)
	prov := &ProcessProvisioner{Binary: bin}
	pool := &Pool{Provisioner: prov, Runtime: rt, RuntimeURL: url}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sb, err := pool.Acquire(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	h := &recordingHost{}
	res, err := sb.Execute(ctx, dsl.Job{Language: "javascript", Code: script}, h)
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, res, h)
	// the Go interpreter cannot read the environment (no os package)
	_, err = sb.Execute(ctx, dsl.Job{Language: "go", Code: "package action\nimport (\"os\"\n\"github.com/zimwip/goap/pkg/dsl\")\nfunc Run(ctx *dsl.Ctx) error { ctx.Log(os.Getenv(\"GOAP_SECRET_FOR_TEST\")); return nil }"}, h)
	if err == nil {
		t.Fatal("os import must be rejected")
	}
	endpoint := sb.(*remote).box.inst.Endpoint
	pool.Release(ctx, "p1")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := http.Get(endpoint + "/healthz"); err != nil {
			return // stopped
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("sandbox process still running after release")
}

func TestDockerProvisionerRequests(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	var created map[string]any
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/containers/create") {
			_ = json.NewDecoder(r.Body).Decode(&created)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"abc"}`)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		mu.Unlock()
	})}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()

	d := &DockerProvisioner{Host: "unix://" + sock, Network: "goap-sandbox", Runtime: "runsc"}
	inst, err := d.Start(context.Background(), Spec{ID: "goap-sbx-1", ProcessID: "p1", Image: "goap/runner", MemoryMB: 256, CPUs: 0.5, PIDs: 64})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Endpoint != "http://goap-sbx-1:8080" {
		t.Fatalf("endpoint %s", inst.Endpoint)
	}
	if err := d.Stop(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	host := created["HostConfig"].(map[string]any)
	if host["ReadonlyRootfs"] != true || host["NetworkMode"] != "goap-sandbox" || host["Runtime"] != "runsc" ||
		host["Memory"].(float64) != 256*1024*1024 || host["PidsLimit"].(float64) != 64 {
		t.Fatalf("container not hardened: %+v", host)
	}
	if caps := host["CapDrop"].([]any); len(caps) != 1 || caps[0] != "ALL" {
		t.Fatalf("capabilities: %v", caps)
	}
	if created["User"] != "65532:65532" {
		t.Fatalf("user %v", created["User"])
	}
	want := "POST /v1.43/containers/create,POST /v1.43/containers/abc/start,DELETE /v1.43/containers/goap-sbx-1"
	if strings.Join(seen, ",") != want {
		t.Fatalf("calls %v", seen)
	}
}

func TestKubernetesProvisionerRequests(t *testing.T) {
	var pod map[string]any
	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces/goap/pods":
			_ = json.NewDecoder(r.Body).Decode(&pod)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodGet:
			gets++
			if gets < 2 {
				_, _ = io.WriteString(w, `{"status":{"phase":"Pending"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"status":{"phase":"Running","podIP":"10.1.2.3"}}`)
		case r.Method == http.MethodDelete:
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	k := &KubernetesProvisioner{APIServer: srv.URL, Token: "tok", Namespace: "goap", RuntimeClass: "gvisor", HTTP: srv.Client()}
	inst, err := k.Start(context.Background(), Spec{ID: "goap-sbx-2", ProcessID: "p2", Image: "goap/runner", MemoryMB: 128})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Endpoint != "http://10.1.2.3:8080" {
		t.Fatalf("endpoint %s", inst.Endpoint)
	}
	spec := pod["spec"].(map[string]any)
	if spec["automountServiceAccountToken"] != false || spec["runtimeClassName"] != "gvisor" {
		t.Fatalf("pod not hardened: %+v", spec)
	}
	c := spec["containers"].([]any)[0].(map[string]any)["securityContext"].(map[string]any)
	if c["readOnlyRootFilesystem"] != true || c["allowPrivilegeEscalation"] != false {
		t.Fatalf("container security context: %+v", c)
	}
	if err := k.Stop(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	_ = os.Getenv
}
