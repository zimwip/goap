package jobkit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"

	"github.com/zimwip/goap/contracts/authz"
)

// ResultChunk is the size of the chunks a result is written in.
const ResultChunk = 512 << 10

// Job is a job as its runner sees it: what it was asked, and where it says how far it is.
type Job struct {
	ID, Kind, Label, Target string
	Params                  map[string]any
	ProjectID, OwnerOrg     string
	// By is the person the job runs for; Principal what the run acts as (the person, as the creating service saw
	// them; the worker puts it in the context of the run).
	By        string
	Principal authz.Principal

	queue  Queue
	worker string

	mu       sync.Mutex
	progress string
	done     int64
	total    int64
	stats    map[string]int64
	output   map[string]any
	dirty    bool
}

// String is a parameter as a string ("" when unset).
func (j *Job) String(name string) string {
	switch v := j.Params[name].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// Int is a numeric parameter (0 when unset).
func (j *Job) Int(name string) int {
	switch v := j.Params[name].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		var n int
		_, _ = fmt.Sscan(v, &n)
		return n
	}
	return 0
}

// Bool is a boolean parameter (false when unset; "true" counts).
func (j *Job) Bool(name string) bool {
	switch v := j.Params[name].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// Progress records the last progress line of the run.
func (j *Job) Progress(line string) {
	j.mu.Lock()
	j.progress, j.dirty = line, true
	j.mu.Unlock()
}

// Step records how far the run is: done of total (total 0: unknown).
func (j *Job) Step(done, total int64) {
	j.mu.Lock()
	j.done, j.total, j.dirty = done, total, true
	j.mu.Unlock()
}

// Set records a counter of the run.
func (j *Job) Set(name string, n int64) {
	j.mu.Lock()
	if j.stats == nil {
		j.stats = map[string]int64{}
	}
	j.stats[name], j.dirty = n, true
	j.mu.Unlock()
}

// Output records the answer of the run (a small JSON object: the change an import created); keys add up.
func (j *Job) Output(kv map[string]any) {
	j.mu.Lock()
	if j.output == nil {
		j.output = map[string]any{}
	}
	maps.Copy(j.output, kv)
	j.dirty = true
	j.mu.Unlock()
}

// Write takes the progress lines a runner writes (a Job is an io.Writer): the last non empty line is kept.
func (j *Job) Write(b []byte) (int, error) {
	s := strings.TrimRight(string(b), "\r\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if s != "" {
		j.Progress(s)
	}
	return len(b), nil
}

// Input reads the whole input of the job.
func (j *Job) Input(ctx context.Context) ([]byte, error) {
	if j.queue == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	for {
		data, eof, err := j.queue.ReadInput(ctx, j.ID, j.worker, int64(buf.Len()))
		if err != nil {
			return nil, err
		}
		buf.Write(data)
		if eof || len(data) == 0 {
			return buf.Bytes(), nil
		}
	}
}

// Result opens the file of the job: what is written goes to the job service in chunks; Close sends the rest. A job
// writes one result.
func (j *Job) Result(ctx context.Context, name, mediaType string) io.WriteCloser {
	return &resultWriter{ctx: ctx, j: j, name: name, mediaType: mediaType}
}

type resultWriter struct {
	ctx             context.Context
	j               *Job
	name, mediaType string
	buf             []byte
	sent            bool
}

func (w *resultWriter) flush() error {
	if len(w.buf) == 0 && w.sent {
		return nil
	}
	if w.j.queue != nil {
		if _, err := w.j.queue.WriteResult(w.ctx, w.j.ID, w.j.worker, w.name, w.mediaType, w.buf); err != nil {
			return err
		}
	}
	w.buf, w.sent = w.buf[:0], true
	return nil
}

func (w *resultWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for len(w.buf) >= ResultChunk {
		rest := append([]byte(nil), w.buf[ResultChunk:]...)
		w.buf = w.buf[:ResultChunk]
		if err := w.flush(); err != nil {
			return 0, err
		}
		w.buf = rest
	}
	return len(p), nil
}

func (w *resultWriter) Close() error { return w.flush() }

// report is what the job says now, and whether it changed since the last call.
func (j *Job) report() (Report, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	dirty := j.dirty
	j.dirty = false
	return Report{Progress: j.progress, Done: j.done, Total: j.total, Stats: maps.Clone(j.stats), Output: maps.Clone(j.output)}, dirty
}
