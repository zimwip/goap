package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

// service is one process of the stack.
type service struct {
	name string
	// bin is the file name of its binary in the directory of the binaries.
	bin string
	// path is the program run instead of bin, looked up in PATH (npm), and dir its working directory (default the
	// root of the repository).
	path, dir string
	args      []string
	// port is the port it listens on (127.0.0.1), 0: none.
	port int
	// ready is the path answering once it serves (/readyz), empty: started is enough.
	ready string
	env   map[string]string
	// once runs it to its end (a bootstrap): never restarted, its failure stops the stack.
	once bool
}

func (s service) url() string { return fmt.Sprintf("http://127.0.0.1:%d", s.port) }

// supervisor starts the processes of the stack, restarts those that stop and stops them all with its context. Each
// process runs in a process group of its own, so that stopping it stops what it started too (vite's esbuild, a go
// test's binary); a second interrupt (hurry) kills them at once.
type supervisor struct {
	cfg config
	log *slog.Logger
	out sync.Mutex // the standard output, shared by the processes' lines
	wg  sync.WaitGroup

	mu       sync.Mutex
	restarts map[string]chan restartReq // the restart requests of each running service, by name
}

// restartReq stops a service: it closes stopped once its process ended, and starts it again once resume is closed.
type restartReq struct{ stopped, resume chan struct{} }

// restart stops the running services named, runs prepare once they all stopped (new binaries put in place: Windows
// refuses to overwrite a running program) and starts them again; the names of no running service are left alone.
func (s *supervisor) restart(names []string, prepare func()) {
	var reqs []restartReq
	for _, name := range names {
		s.mu.Lock()
		ch := s.restarts[name]
		s.mu.Unlock()
		if ch == nil {
			continue
		}
		r := restartReq{stopped: make(chan struct{}), resume: make(chan struct{})}
		ch <- r
		reqs = append(reqs, r)
	}
	for _, r := range reqs {
		<-r.stopped
	}
	prepare()
	for _, r := range reqs {
		close(r.resume)
	}
}

// wait waits for every process to have stopped.
func (s *supervisor) wait() { s.wg.Wait() }

// start runs a service: once to its end, else in the background restarted when it stops, then waits for it to be
// ready.
func (s *supervisor) start(ctx context.Context, svc service) error {
	if svc.once {
		s.log.Info("running", "service", svc.name)
		if err := s.exec(ctx, svc); err != nil {
			return fmt.Errorf("%s: %w", svc.name, err)
		}
		return nil
	}
	s.log.Info("starting", "service", svc.name, "port", svc.port)
	restart := make(chan restartReq)
	s.mu.Lock()
	if s.restarts == nil {
		s.restarts = map[string]chan restartReq{}
	}
	s.restarts[svc.name] = restart
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		backoff := time.Second
		for {
			began := time.Now()
			run, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- s.exec(run, svc) }()
			var err error
			select {
			case err = <-done:
				stop()
			case r := <-restart:
				stop()
				<-done
				close(r.stopped)
				select {
				case <-r.resume:
				case <-ctx.Done():
					return
				}
				s.log.Info("restarting", "service", svc.name)
				backoff = time.Second
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if time.Since(began) > time.Minute {
				backoff = time.Second
			}
			s.log.Warn("stopped, restarting", "service", svc.name, "err", err, "in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				backoff = min(2*backoff, 30*time.Second)
			case r := <-restart: // stopped already: a new binary may make it start
				close(r.stopped)
				select {
				case <-r.resume:
				case <-ctx.Done():
					return
				}
				backoff = time.Second
			}
		}
	}()
	return s.waitReady(ctx, svc)
}

// exec runs the process of a service until it ends or the context is done (then interrupted, killed 10 s later).
func (s *supervisor) exec(ctx context.Context, svc service) error {
	path := filepath.Join(s.cfg.bin, svc.bin)
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if svc.path != "" {
		p, err := exec.LookPath(svc.path)
		if err != nil {
			return err
		}
		path = p
	} else if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("binary %s: %w (make local-build)", path, err)
	}
	logs := filepath.Join(s.cfg.dir, "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(logs, svc.name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	w := &lines{name: svc.name, file: file, out: &s.out}
	dir := s.cfg.root
	if svc.dir != "" {
		dir = svc.dir
	}
	return s.run(ctx, w, dir, path, svc.args, environ(svc.env))
}

// run runs a command in a process group of its own until it ends or the context is done: then the group is
// interrupted, killed 10 s later or at once when the supervisor hurries; what the group still holds once its leader
// ended is killed.
func (s *supervisor) run(ctx context.Context, w *lines, dir, path string, args, env []string) error {
	cmd := exec.Command(path, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = w, w
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		interruptGroup(cmd.Process)
		select {
		case err = <-done:
		case <-time.After(10 * time.Second):
			killGroup(cmd.Process)
			err = <-done
		case <-hurry:
			killGroup(cmd.Process)
			err = <-done
		}
	}
	killGroup(cmd.Process)
	w.flush()
	return err
}

// waitReady waits for a service to answer its readiness path, up to three minutes.
func (s *supervisor) waitReady(ctx context.Context, svc service) error {
	if svc.ready == "" || svc.port == 0 {
		return nil
	}
	return waitURL(ctx, svc.url()+svc.ready, 3*time.Minute, svc.name)
}

// waitURL waits for a URL to answer 200.
func waitURL(ctx context.Context, url string, limit time.Duration, name string) error {
	deadline := time.Now().Add(limit)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if resp, err := client.Do(req); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s not ready after %s (%s)", name, limit, url)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// environ is the environment of this process with the values of env laid over it.
func environ(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := os.Environ()
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// lines writes the output of a process to its log file, and line by line to the standard output with its name.
type lines struct {
	name string
	file io.Writer
	out  *sync.Mutex
	buf  []byte
}

func (l *lines) Write(p []byte) (int, error) {
	l.file.Write(p)
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.print(l.buf[:i])
		l.buf = l.buf[i+1:]
	}
	return len(p), nil
}

func (l *lines) flush() {
	if len(l.buf) > 0 {
		l.print(l.buf)
		l.buf = nil
	}
}

func (l *lines) print(line []byte) {
	l.out.Lock()
	defer l.out.Unlock()
	fmt.Fprintf(os.Stdout, "%-17s| %s\n", l.name, bytes.TrimRight(line, "\r"))
}
