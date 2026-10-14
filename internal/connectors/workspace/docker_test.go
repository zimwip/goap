package workspace

import (
	"context"
	bin "encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/mcp"
)

// fakeDocker is the part of the Docker Engine API the connector uses, its execs run on the host in the directory
// mounted at /workspace (what a container of an image with a shell would do).
type fakeDocker struct {
	t       *testing.T
	tmp     string // the /tmp of the container
	mu      sync.Mutex
	created []map[string]any
	running map[string]bool   // container name -> running
	bind    map[string]string // container name -> host directory of /workspace
	execs   map[string]*fakeExec
	removed []string
	seq     int
}

type fakeExec struct {
	container string
	spec      map[string]any
	running   bool
	exit      int
}

type frameWriter struct {
	mu     *sync.Mutex
	w      http.ResponseWriter
	stream byte
}

func (f frameWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hdr := make([]byte, 8)
	hdr[0] = f.stream
	bin.BigEndian.PutUint32(hdr[4:], uint32(len(p)))
	if _, err := f.w.Write(append(hdr, p...)); err != nil {
		return 0, err
	}
	f.w.(http.Flusher).Flush()
	return len(p), nil
}

func (d *fakeDocker) hostPath(container, p string) string {
	if rest, ok := strings.CutPrefix(p, ContainerDir); ok {
		return d.bind[container] + rest
	}
	return p
}

func (d *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/v1.43")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	d.mu.Lock()
	switch {
	case r.Method == http.MethodPost && p == "/containers/create":
		name := r.URL.Query().Get("name")
		d.created = append(d.created, body)
		binds := body["HostConfig"].(map[string]any)["Binds"].([]any)
		d.bind[name] = strings.TrimSuffix(binds[0].(string), ":"+ContainerDir)
		d.running[name] = false
		d.mu.Unlock()
		reply(map[string]any{"Id": name})
		return
	case parts[0] == "containers" && len(parts) == 3 && parts[2] == "json":
		running, ok := d.running[parts[1]]
		d.mu.Unlock()
		if !ok {
			http.Error(w, "no such container", http.StatusNotFound)
			return
		}
		status := "exited"
		if running {
			status = "running"
		}
		reply(map[string]any{"Id": parts[1], "State": map[string]any{"Running": running, "Status": status}, "Config": map[string]any{"Image": "goap/builder:1"}})
		return
	case parts[0] == "containers" && len(parts) == 3 && parts[2] == "start":
		d.running[parts[1]] = true
		d.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	case r.Method == http.MethodDelete && parts[0] == "containers":
		_, ok := d.running[parts[1]]
		delete(d.running, parts[1])
		d.removed = append(d.removed, parts[1])
		d.mu.Unlock()
		if !ok {
			http.Error(w, "no such container", http.StatusNotFound)
		}
		return
	case parts[0] == "containers" && len(parts) == 3 && parts[2] == "exec":
		d.seq++
		id := "exec-" + string(rune('a'+d.seq))
		d.execs[id] = &fakeExec{container: parts[1], spec: body}
		d.mu.Unlock()
		reply(map[string]any{"Id": id})
		return
	case parts[0] == "exec" && len(parts) == 3 && parts[2] == "json":
		x := d.execs[parts[1]]
		d.mu.Unlock()
		reply(map[string]any{"Running": x.running, "ExitCode": x.exit})
		return
	case parts[0] == "exec" && len(parts) == 3 && parts[2] == "start":
		x := d.execs[parts[1]]
		x.running = true
		var args []string
		for _, a := range x.spec["Cmd"].([]any) {
			args = append(args, strings.ReplaceAll(a.(string), "/tmp/", d.tmp+"/"))
		}
		env := []string{"PATH=" + os.Getenv("PATH")}
		if e, ok := x.spec["Env"].([]any); ok {
			for _, v := range e {
				env = append(env, strings.ReplaceAll(v.(string), "/tmp/", d.tmp+"/"))
			}
		}
		dir, _ := x.spec["WorkingDir"].(string)
		dir = d.hostPath(x.container, dir)
		d.mu.Unlock()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir, cmd.Env = dir, env
		detach, _ := body["Detach"].(bool)
		if detach {
			_ = cmd.Run()
			d.mu.Lock()
			x.running = false
			d.mu.Unlock()
			return
		}
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		var mu sync.Mutex
		cmd.Stdout, cmd.Stderr = frameWriter{&mu, w, 1}, frameWriter{&mu, w, 2}
		err := cmd.Run()
		d.mu.Lock()
		x.running, x.exit = false, 0
		if ee, ok := err.(*exec.ExitError); ok {
			x.exit = ee.ExitCode()
		}
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	d.t.Errorf("unexpected docker call %s %s", r.Method, r.URL)
	http.Error(w, "unexpected", http.StatusBadRequest)
}

func newDockerFixture(t *testing.T) (*fixture, *fakeDocker) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake docker runs the execs with a POSIX shell")
	}
	d := &fakeDocker{t: t, tmp: t.TempDir(), running: map[string]bool{}, bind: map[string]string{}, execs: map[string]*fakeExec{}}
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	f := newFixture(t)
	f.c = NewDocker("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
	f.config["image"] = "goap/builder:1"
	return f, d
}

func TestDockerInfo(t *testing.T) {
	info := NewDocker("").Info()
	req := info.ConfigSchema.Fields["required"].GetListValue().AsSlice()
	if info.Id != DockerID || !slices.Contains(req, any("image")) {
		t.Fatalf("info %s %v", info.Id, req)
	}
	if _, ok := info.ConfigSchema.Fields["properties"].GetStructValue().Fields["passEnv"]; ok {
		t.Fatal("the host environment never enters a container")
	}
}

// The commands of a workspace run in its container, created once, hardened, with the workspace mounted; the files
// stay on the host.
func TestDockerRunsTheCommandsInAContainer(t *testing.T) {
	f, d := newDockerFixture(t)
	f.must(OpWrite, map[string]any{"path": "src/a.txt", "content": "hello\n"})
	out := f.must(OpBash, map[string]any{"command": "cat a.txt; echo oops >&2; exit 3", "cwd": "src"})
	if out["stdout"] != "hello\n" || out["stderr"] != "oops\n" || out["exitCode"] != float64(3) {
		t.Fatalf("bash %+v", out)
	}
	// the commands write in the workspace
	f.must(OpBash, map[string]any{"command": "echo built > out.txt", "cwd": "src"})
	if got := f.must(OpRead, map[string]any{"path": "src/out.txt"})["text"].(string); !strings.Contains(got, "built") {
		t.Fatalf("read %q", got)
	}
	if len(d.created) != 1 {
		t.Fatalf("%d containers", len(d.created))
	}
	c := d.created[0]
	host := c["HostConfig"].(map[string]any)
	if c["Image"] != "goap/builder:1" || host["NetworkMode"] != "none" || host["ReadonlyRootfs"] != true ||
		host["Binds"].([]any)[0] != f.dir+":"+ContainerDir || !slices.Contains(host["CapDrop"].([]any), any("ALL")) {
		t.Fatalf("container %+v", c)
	}
	for _, e := range c["Env"].([]any) {
		if strings.HasPrefix(e.(string), "PATH=") || strings.Contains(e.(string), "s3cr3t") {
			t.Fatalf("host environment or secret in the container: %v", c["Env"])
		}
	}
	st := f.must(OpStatus, nil)
	if ct, _ := st["container"].(map[string]any); ct["status"] != "running" {
		t.Fatalf("status %+v", st)
	}
	f.must(OpClose, nil)
	if !slices.Contains(d.removed, containerName(space{id: "CHG-1"})) {
		t.Fatalf("container not removed: %v", d.removed)
	}
	if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
		t.Fatalf("workspace kept: %v", err)
	}
}

func TestDockerTimeoutAndBackground(t *testing.T) {
	f, _ := newDockerFixture(t)
	start := time.Now()
	out := f.must(OpBash, map[string]any{"command": "sleep 30", "timeout": float64(1)})
	if out["timedOut"] != true || time.Since(start) > 15*time.Second {
		t.Fatalf("timeout %+v after %s", out, time.Since(start))
	}
	job := f.must(OpBash, map[string]any{"command": "echo one; sleep 0.3; echo two", "background": true})
	id := job["id"].(string)
	var text string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		o := f.must(OpBashOutput, map[string]any{"id": id})
		text += o["output"].(string)
		if o["running"] == false {
			if o["exitCode"] != float64(0) {
				t.Fatalf("job %+v", o)
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if text != "one\ntwo\n" {
		t.Fatalf("output %q", text)
	}
	long := f.must(OpBash, map[string]any{"command": "sleep 30", "background": true})
	f.must(OpBashKill, map[string]any{"id": long["id"]})
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if o := f.must(OpBashOutput, map[string]any{"id": long["id"]}); o["running"] == false {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the killed command still runs")
}

func TestDockerNeedsAnImage(t *testing.T) {
	f, _ := newDockerFixture(t)
	delete(f.config, "image")
	if _, err := f.call(OpBash, map[string]any{"command": "true"}); err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("err %v", err)
	}
	// the call context names the workspace
	if _, err := f.c.Invoke(mcp.WithCall(context.Background(), mcp.CallContext{}), OpBash, map[string]any{"command": "true"}, f.config, nil); err == nil {
		t.Fatal("no change, no workspace")
	}
}
