package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Bounds of the commands.
const (
	DefaultTimeout = 120 * time.Second
	MaxTimeout     = 600 * time.Second
	// MaxOutput bounds the stdout and the stderr given back by a command: the head and, mostly, the tail are kept.
	MaxOutput = 30000
	// MaxJobOutput bounds what a background command keeps of its output (the latest).
	MaxJobOutput = 1 << 20
	// MaxJobs bounds the background commands running in one workspace.
	MaxJobs = 8
	// JobTimeout bounds a background command.
	JobTimeout = 2 * time.Hour
)

// hostEnv are the variables of the host every command gets: what a shell and a toolchain need to start.
var hostEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR", "TZ", "SHELL",
	// Windows
	"SystemRoot", "SystemDrive", "ComSpec", "PATHEXT", "TEMP", "TMP", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "ProgramFiles", "ProgramData", "WINDIR"}

// environment is the cleaned environment of the commands of a workspace: the host variables above and the ones the
// unit passes, its own values, the identity of the commits, the workspace. Never a secret of the adapter.
func environment(s space) []string {
	vars := map[string]string{}
	pass := append([]string{}, hostEnv...)
	if list, ok := s.config["passEnv"].([]any); ok {
		for _, v := range list {
			if name, ok := v.(string); ok {
				pass = append(pass, name)
			}
		}
	}
	for _, name := range pass {
		if v, ok := os.LookupEnv(name); ok {
			vars[name] = v
		}
	}
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
	vars["GOAP_WORKSPACE"], vars["GOAP_CHANGE"], vars["TERM"] = s.dir, s.change, "dumb"
	vars["GIT_TERMINAL_PROMPT"] = "0"
	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// shell is the command line running a command: bash when there is one (Git Bash on Windows), else the system shell.
func shell(command string) []string {
	if p, err := exec.LookPath("bash"); err == nil {
		return []string{p, "-c", command}
	}
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/C", command}
	}
	return []string{"/bin/sh", "-c", command}
}

// workdir is the directory of the workspace a command runs in, on the host.
func workdir(s space, cwd string) (string, error) {
	dir := s.local(cwd)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("cwd %s is not a directory of the workspace", rel(cwd))
	}
	return dir, nil
}

func (c Connector) bash(ctx context.Context, s space, args map[string]any) (map[string]any, error) {
	line := stringArg(args, "command")
	if line == "" {
		return nil, errors.New("bash needs a command")
	}
	if boolArg(args, "background") {
		return c.jobs.start(c.runner(), s, line, stringArg(args, "cwd"))
	}
	timeout := time.Duration(intArg(args, "timeout", 0)) * time.Second
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	timeout = min(timeout, MaxTimeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	start := time.Now()
	p, err := c.runner().start(ctx, s, line, stringArg(args, "cwd"), &stdout, &stderr)
	if err != nil {
		return nil, err
	}
	code, err := p.wait()
	out := map[string]any{"stdout": capOutput(stdout.Bytes()), "stderr": capOutput(stderr.Bytes()),
		"durationMs": float64(time.Since(start).Milliseconds())}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		out["timedOut"], out["exitCode"] = true, float64(-1)
	case err != nil:
		return nil, err
	default:
		out["exitCode"] = float64(code)
	}
	return out, nil
}

// capOutput keeps the head and the tail of an output longer than MaxOutput.
func capOutput(b []byte) string {
	if len(b) <= MaxOutput {
		return string(b)
	}
	head, tail := MaxOutput/5, MaxOutput-MaxOutput/5
	h, t := b[:head], b[len(b)-tail:]
	for len(h) > 0 && !utf8.Valid(h) {
		h = h[:len(h)-1]
	}
	for len(t) > 0 && !utf8.RuneStart(t[0]) {
		t = t[1:]
	}
	return fmt.Sprintf("%s\n... (%d bytes cut) ...\n%s", h, len(b)-len(h)-len(t), t)
}

// ---- background commands ------------------------------------------------------------------------------------

type jobs struct {
	mu          sync.Mutex
	seq         int
	byWorkspace map[string]map[string]*job
}

type job struct {
	id, command string
	started     time.Time
	cancel      context.CancelFunc
	out         ring
	read        int64 // bytes of the output already given back
	done        bool
	exitCode    int
	timedOut    bool
}

// ring keeps the latest MaxJobOutput bytes of an output and how many were written.
type ring struct {
	mu      sync.Mutex
	buf     []byte
	written int64
}

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if over := len(r.buf) - MaxJobOutput; over > 0 {
		r.buf = r.buf[over:]
	}
	r.written += int64(len(p))
	return len(p), nil
}

// since returns what was written after offset (what is still kept of it) and how many bytes were lost.
func (r *ring) since(offset int64) (string, int64, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	first := r.written - int64(len(r.buf))
	lost := int64(0)
	if offset < first {
		lost, offset = first-offset, first
	}
	return string(r.buf[offset-first:]), lost, r.written
}

func (j *jobs) start(r runner, s space, line, cwd string) (map[string]any, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	running := 0
	for _, jb := range j.byWorkspace[s.dir] {
		if !jb.done {
			running++
		}
	}
	if running >= MaxJobs {
		return nil, fmt.Errorf("%d commands already run in the background of this workspace: stop one first", running)
	}
	ctx, cancel := context.WithTimeout(context.Background(), JobTimeout)
	j.seq++
	jb := &job{id: "job-" + strconv.Itoa(j.seq), command: line, started: time.Now(), cancel: cancel}
	p, err := r.start(ctx, s, line, cwd, &jb.out, &jb.out)
	if err != nil {
		cancel()
		return nil, err
	}
	if j.byWorkspace[s.dir] == nil {
		j.byWorkspace[s.dir] = map[string]*job{}
	}
	j.byWorkspace[s.dir][jb.id] = jb
	go func() {
		code, err := p.wait()
		j.mu.Lock()
		defer j.mu.Unlock()
		jb.done, jb.exitCode = true, code
		if err != nil {
			jb.exitCode = -1
		}
		jb.timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		cancel()
	}()
	return map[string]any{"id": jb.id, "running": true}, nil
}

func (j *jobs) find(dir, id string) (*job, error) {
	jb := j.byWorkspace[dir][id]
	if jb == nil {
		return nil, fmt.Errorf("no background command %q in this workspace", id)
	}
	return jb, nil
}

func (j *jobs) output(dir, id string) (map[string]any, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	jb, err := j.find(dir, id)
	if err != nil {
		return nil, err
	}
	text, lost, written := jb.out.since(jb.read)
	jb.read = written
	out := map[string]any{"id": id, "output": capOutput([]byte(text)), "running": !jb.done}
	if lost > 0 {
		out["lost"] = float64(lost)
	}
	if jb.done {
		out["exitCode"] = float64(jb.exitCode)
		out["timedOut"] = jb.timedOut
	}
	return out, nil
}

func (j *jobs) kill(dir, id string) (map[string]any, error) {
	j.mu.Lock()
	jb, err := j.find(dir, id)
	j.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !jb.done {
		jb.cancel()
	}
	return map[string]any{"id": id, "stopped": true}, nil
}

// stopAll stops the commands of a workspace and forgets them, returning how many were running.
func (j *jobs) stopAll(dir string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := 0
	for _, jb := range j.byWorkspace[dir] {
		if !jb.done {
			n++
			jb.cancel()
		}
	}
	delete(j.byWorkspace, dir)
	return n
}

func (j *jobs) list(dir string) []any {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := []any{}
	for _, jb := range j.byWorkspace[dir] {
		item := map[string]any{"id": jb.id, "command": jb.command, "running": !jb.done, "started": jb.started.UTC().Format(time.RFC3339)}
		if jb.done {
			item["exitCode"] = float64(jb.exitCode)
		}
		out = append(out, item)
	}
	sort.Slice(out, func(a, b int) bool {
		return out[a].(map[string]any)["id"].(string) < out[b].(map[string]any)["id"].(string)
	})
	return out
}
