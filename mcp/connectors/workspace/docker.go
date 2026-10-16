package workspace

import (
	"bufio"
	"bytes"
	"context"
	bin "encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults of the containers of the docker connector.
const (
	DefaultContainerMemoryMB = 2048
	DefaultContainerCPUs     = 2
	DefaultContainerPIDs     = 1024
	// ContainerDir is where the workspace is mounted in its container.
	ContainerDir = "/workspace"
)

// dockerRunner runs the commands of a workspace in a container of its own (ADR 0118 §7): created at the first
// command from the image the unit configures, the directory of the workspace mounted at /workspace, no network by
// default, every capability dropped, no privilege escalation, a read-only root file system, limits; the commands
// are `docker exec`s in it. The files and git stay on the host: the connector reads, writes and searches the
// directory itself and clones and pushes with the token, which never enters the container.
type dockerRunner struct {
	api *dockerAPI
	mu  sync.Mutex // serializes the creation of the containers
	seq int
}

// containerName is the name of the container of a workspace.
func containerName(s space) string { return "goap-ws-" + strings.TrimLeft(s.id, "._-") }

// containerEnv is the environment of the commands in the container: the image's, plus what the unit sets, the
// identity of the commits and the workspace. Nothing of the host.
func containerEnv(s space) []string {
	vars := map[string]string{}
	if set, ok := s.config["env"].([]any); ok {
		for _, v := range set {
			str, _ := v.(string)
			if k, val, ok := strings.Cut(str, "="); ok && k != "" {
				vars[k] = val
			}
		}
	}
	if name, _ := s.config["gitName"].(string); name != "" {
		vars["GIT_AUTHOR_NAME"], vars["GIT_COMMITTER_NAME"] = name, name
	}
	if mail, _ := s.config["gitEmail"].(string); mail != "" {
		vars["GIT_AUTHOR_EMAIL"], vars["GIT_COMMITTER_EMAIL"] = mail, mail
	}
	// the root file system is read-only: the home of the toolchains (caches, settings) is in the workspace
	vars["HOME"] = ContainerDir + "/.home"
	vars["GOAP_WORKSPACE"], vars["GOAP_CHANGE"], vars["TERM"], vars["GIT_TERMINAL_PROMPT"] = ContainerDir, s.change, "dumb", "0"
	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func number(v any, def float64) float64 {
	switch n := v.(type) {
	case float64:
		if n > 0 {
			return n
		}
	case int:
		if n > 0 {
			return float64(n)
		}
	case int64:
		if n > 0 {
			return float64(n)
		}
	}
	return def
}

// user is the user of the commands: the one the unit configures, else the connector's own, so that the files the
// commands write are the connector's to read, change and remove.
func user(s space) string {
	if u, _ := s.config["user"].(string); u != "" {
		return u
	}
	if uid := os.Getuid(); uid >= 0 {
		return fmt.Sprintf("%d:%d", uid, os.Getgid())
	}
	return ""
}

// container returns the id of the running container of a workspace, creating or starting it when needed.
func (d *dockerRunner) container(ctx context.Context, s space) (string, error) {
	image, _ := s.config["image"].(string)
	if image == "" {
		return "", errors.New("the adapter has no \"image\" configured for the containers of the workspaces")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	name := containerName(s)
	var info struct {
		ID    string `json:"Id"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	err := d.api.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, &info)
	switch {
	case err == nil && info.State.Running:
		return info.ID, nil
	case err == nil:
		return info.ID, d.api.do(ctx, http.MethodPost, "/containers/"+info.ID+"/start", nil, nil)
	case !errors.Is(err, errNotFound):
		return "", err
	}
	if err := os.MkdirAll(s.local(".home"), 0o755); err != nil {
		return "", err
	}
	network, _ := s.config["network"].(string)
	if network == "" {
		network = "none"
	}
	host := map[string]any{
		"Binds":          []string{s.dir + ":" + ContainerDir},
		"NetworkMode":    network,
		"ReadonlyRootfs": true,
		"CapDrop":        []string{"ALL"},
		"SecurityOpt":    []string{"no-new-privileges"},
		"Tmpfs":          map[string]string{"/tmp": "rw,nosuid,size=512m"},
		"Init":           true,
		"Memory":         int64(number(s.config["memoryMB"], DefaultContainerMemoryMB)) * 1024 * 1024,
		"NanoCpus":       int64(number(s.config["cpus"], DefaultContainerCPUs) * 1e9),
		"PidsLimit":      int64(number(s.config["pids"], DefaultContainerPIDs)),
	}
	body := map[string]any{
		"Image": image, "User": user(s), "WorkingDir": ContainerDir, "Env": containerEnv(s),
		"Cmd":        []string{"sleep", "infinity"},
		"Labels":     map[string]string{"goap.workspace": s.change},
		"HostConfig": host,
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := d.api.do(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), body, &created); err != nil {
		return "", err
	}
	if err := d.api.do(ctx, http.MethodPost, "/containers/"+created.ID+"/start", nil, nil); err != nil {
		_ = d.api.do(context.Background(), http.MethodDelete, "/containers/"+created.ID+"?force=true", nil, nil)
		return "", err
	}
	return created.ID, nil
}

// wrap is the command line run by an exec: it records its pid (to be killed) and runs the line with bash when the
// image has it, else sh.
const wrap = `echo $$ > "$GOAP_PIDFILE"; if command -v bash >/dev/null 2>&1; then exec bash -c "$1"; else exec sh -c "$1"; fi`

type dockerProcess struct {
	d         *dockerRunner
	container string
	exec      string
	pidfile   string
	body      io.ReadCloser
	stdout    io.Writer
	stderr    io.Writer
	stop      chan struct{}
	killed    atomic.Bool
}

func (d *dockerRunner) start(ctx context.Context, s space, line, cwd string, stdout, stderr io.Writer) (process, error) {
	if _, err := workdir(s, cwd); err != nil {
		return nil, err
	}
	id, err := d.container(ctx, s)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.seq++
	pidfile := "/tmp/.goap-cmd-" + strconv.Itoa(d.seq) + ".pid"
	d.mu.Unlock()
	var created struct {
		ID string `json:"Id"`
	}
	exec := map[string]any{"AttachStdout": true, "AttachStderr": true, "Tty": false, "User": user(s),
		"WorkingDir": path.Join(ContainerDir, rel(cwd)), "Env": append(containerEnv(s), "GOAP_PIDFILE="+pidfile),
		"Cmd": []string{"/bin/sh", "-c", wrap, "goap", line}}
	if err := d.api.do(ctx, http.MethodPost, "/containers/"+id+"/exec", exec, &created); err != nil {
		return nil, err
	}
	body, err := d.api.stream(ctx, "/exec/"+created.ID+"/start", map[string]any{"Detach": false, "Tty": false})
	if err != nil {
		return nil, err
	}
	p := &dockerProcess{d: d, container: id, exec: created.ID, pidfile: pidfile, body: body, stdout: stdout, stderr: stderr, stop: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			p.kill()
		case <-p.stop:
		}
	}()
	return p, nil
}

// kill kills the command and what it started (its children), then stops reading its outputs.
func (p *dockerProcess) kill() {
	p.killed.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	script := `p=$(cat "` + p.pidfile + `" 2>/dev/null) && { pkill -KILL -P "$p" 2>/dev/null; kill -KILL "$p"; }`
	var created struct {
		ID string `json:"Id"`
	}
	if p.d.api.do(ctx, http.MethodPost, "/containers/"+p.container+"/exec", map[string]any{"Cmd": []string{"/bin/sh", "-c", script}}, &created) == nil {
		_ = p.d.api.do(ctx, http.MethodPost, "/exec/"+created.ID+"/start", map[string]any{"Detach": true}, nil)
	}
	_ = p.body.Close()
}

func (p *dockerProcess) wait() (int, error) {
	defer close(p.stop)
	err := demux(p.body, p.stdout, p.stderr)
	_ = p.body.Close()
	var info struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i := 0; ; i++ {
		if ierr := p.d.api.do(ctx, http.MethodGet, "/exec/"+p.exec+"/json", nil, &info); ierr != nil {
			return -1, ierr
		}
		if !info.Running || i >= 50 {
			break
		}
		time.Sleep(100 * time.Millisecond) // the stream ended a moment before the exit is recorded
	}
	switch {
	case p.killed.Load():
		return -1, nil
	case err != nil:
		return -1, err
	}
	return info.ExitCode, nil
}

// demux copies the multiplexed stream of an exec without a terminal (8-byte headers: stream, 0, 0, 0, size) to
// stdout and stderr, until its end.
func demux(r io.Reader, stdout, stderr io.Writer) error {
	br := bufio.NewReader(r)
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		w := stdout
		if hdr[0] == 2 {
			w = stderr
		}
		if _, err := io.CopyN(w, br, int64(bin.BigEndian.Uint32(hdr[4:]))); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
	}
}

func (d *dockerRunner) remove(ctx context.Context, s space) error {
	err := d.api.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(containerName(s))+"?force=true", nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func (d *dockerRunner) status(ctx context.Context, s space) map[string]any {
	var info struct {
		Config struct {
			Image string `json:"Image"`
		} `json:"Config"`
		State struct {
			Status string `json:"Status"`
		} `json:"State"`
	}
	name := containerName(s)
	if err := d.api.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, &info); err != nil {
		return map[string]any{"name": name, "status": "none"}
	}
	return map[string]any{"name": name, "image": info.Config.Image, "status": info.State.Status}
}

// ---- the Docker Engine API ----------------------------------------------------------------------------------

const dockerVersion = "/v1.43"

var errNotFound = errors.New("not found")

type dockerAPI struct {
	host   string
	once   sync.Once
	client *http.Client
	base   string
	err    error
}

func (a *dockerAPI) http() (*http.Client, string, error) {
	a.once.Do(func() {
		host := a.host
		if host == "" {
			host = "unix:///var/run/docker.sock"
		}
		u, err := url.Parse(host)
		if err != nil {
			a.err = err
			return
		}
		switch u.Scheme {
		case "unix":
			sock := u.Path
			a.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dl net.Dialer
				return dl.DialContext(ctx, "unix", sock)
			}}}
			a.base = "http://docker" + dockerVersion
		case "tcp", "http":
			a.client, a.base = &http.Client{}, "http://"+u.Host+dockerVersion
		default:
			a.err = fmt.Errorf("unsupported docker host %q", host)
		}
	})
	return a.client, a.base, a.err
}

func (a *dockerAPI) request(ctx context.Context, method, p string, body any) (*http.Response, error) {
	hc, base, err := a.http()
	if err != nil {
		return nil, err
	}
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+p, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return hc.Do(req)
}

func (a *dockerAPI) do(ctx context.Context, method, p string, body, out any) error {
	resp, err := a.request(ctx, method, p, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("docker %s %s: %w: %s", method, p, errNotFound, bytes.TrimSpace(raw))
	case resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified:
		return fmt.Errorf("docker %s %s: %s: %s", method, p, resp.Status, bytes.TrimSpace(raw))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// stream posts a request whose answer is a stream read until its end (the outputs of an exec). The context bounds
// the request only: the caller closes the stream.
func (a *dockerAPI) stream(ctx context.Context, p string, body any) (io.ReadCloser, error) {
	resp, err := a.request(context.WithoutCancel(ctx), http.MethodPost, p, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("docker POST %s: %s: %s", p, resp.Status, bytes.TrimSpace(raw))
	}
	return resp.Body, nil
}
