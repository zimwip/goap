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
	bin  string
	args []string
	// port is the port it listens on (127.0.0.1), 0: none.
	port int
	// ready is the path answering once it serves (/readyz), empty: started is enough.
	ready string
	env   map[string]string
	// once runs it to its end (a bootstrap): never restarted, its failure stops the stack.
	once bool
}

func (s service) url() string { return fmt.Sprintf("http://127.0.0.1:%d", s.port) }

// supervisor starts the processes of the stack, restarts those that stop and stops them all with its context.
type supervisor struct {
	cfg config
	log *slog.Logger
	out sync.Mutex // the standard output, shared by the processes' lines
	wg  sync.WaitGroup
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
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		backoff := time.Second
		for {
			began := time.Now()
			err := s.exec(ctx, svc)
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
			}
			backoff = min(2*backoff, 30*time.Second)
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
	if _, err := os.Stat(path); err != nil {
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
	cmd := exec.CommandContext(ctx, path, svc.args...)
	cmd.Dir = s.cfg.root
	cmd.Env = environ(svc.env)
	w := &lines{name: svc.name, file: file, out: &s.out}
	cmd.Stdout, cmd.Stderr = w, w
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 10 * time.Second
	err = cmd.Run()
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
