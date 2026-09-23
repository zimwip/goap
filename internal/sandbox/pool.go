package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	runtimev1 "github.com/zimwip/goap/gen/goap/runtime/v1"
	"github.com/zimwip/goap/gen/goap/runtime/v1/runtimev1connect"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/engine"
)

func timestamp(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }

// Spec describes a sandbox to start.
type Spec struct {
	ID        string
	ProcessID string
	Image     string
	// Limits
	MemoryMB int64
	CPUs     float64
	PIDs     int64
}

// Instance is a started sandbox.
type Instance struct {
	ID       string
	Endpoint string // base URL of its SandboxService
}

// Provisioner starts and stops sandboxes on an infrastructure.
type Provisioner interface {
	Name() string
	Start(ctx context.Context, spec Spec) (Instance, error)
	Stop(ctx context.Context, id string) error
}

// Pool implements engine.Sandboxes on a Provisioner: one sandbox per process
// run, started on first use, stopped when the process ends or after an idle
// period (a process waiting for a human does not keep its sandbox).
type Pool struct {
	Provisioner Provisioner
	Runtime     *Runtime
	// RuntimeURL is the engine URL sandboxes call back.
	RuntimeURL string
	Template   Spec
	HTTP       *http.Client
	Options    []connect.ClientOption
	IdleTTL    time.Duration
	Timeout    time.Duration
	Log        *slog.Logger

	mu    sync.Mutex
	boxes map[string]*box // by process id
}

type box struct {
	inst     Instance
	client   runtimev1connect.SandboxServiceClient
	lastUsed time.Time
	ready    chan struct{}
	err      error
}

var _ engine.Sandboxes = (*Pool)(nil)

// Acquire implements engine.Sandboxes.
func (p *Pool) Acquire(ctx context.Context, processID string) (engine.Sandbox, error) {
	p.mu.Lock()
	if p.boxes == nil {
		p.boxes = map[string]*box{}
	}
	b, ok := p.boxes[processID]
	if !ok {
		b = &box{ready: make(chan struct{})}
		p.boxes[processID] = b
		go p.start(processID, b)
	}
	b.lastUsed = time.Now()
	p.mu.Unlock()
	select {
	case <-b.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if b.err != nil {
		p.mu.Lock()
		delete(p.boxes, processID)
		p.mu.Unlock()
		return nil, b.err
	}
	return &remote{pool: p, box: b}, nil
}

func (p *Pool) start(processID string, b *box) {
	defer close(b.ready)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	spec := p.Template
	spec.ID = "goap-sbx-" + uuid.NewString()[:8]
	spec.ProcessID = processID
	inst, err := p.Provisioner.Start(ctx, spec)
	if err != nil {
		b.err = fmt.Errorf("%s: start sandbox: %w", p.Provisioner.Name(), err)
		return
	}
	if err := waitReady(ctx, p.http(), inst.Endpoint); err != nil {
		_ = p.Provisioner.Stop(context.Background(), inst.ID)
		b.err = fmt.Errorf("%s: sandbox %s not ready: %w", p.Provisioner.Name(), inst.ID, err)
		return
	}
	b.inst = inst
	b.client = runtimev1connect.NewSandboxServiceClient(p.http(), inst.Endpoint, p.Options...)
	p.log().Info("sandbox started", "provisioner", p.Provisioner.Name(), "sandbox", inst.ID, "process", processID)
}

func (p *Pool) http() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return http.DefaultClient
}

func (p *Pool) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

func waitReady(ctx context.Context, hc *http.Client, endpoint string) error {
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/healthz", nil)
		resp, err := hc.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			if err == nil {
				err = ctx.Err()
			}
			return err
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Release implements engine.Sandboxes.
func (p *Pool) Release(ctx context.Context, processID string) {
	p.mu.Lock()
	b, ok := p.boxes[processID]
	delete(p.boxes, processID)
	p.mu.Unlock()
	if !ok {
		return
	}
	go func() {
		<-b.ready
		if b.err == nil {
			if err := p.Provisioner.Stop(context.Background(), b.inst.ID); err != nil {
				p.log().Warn("sandbox stop", "sandbox", b.inst.ID, "err", err)
			} else {
				p.log().Info("sandbox stopped", "sandbox", b.inst.ID, "process", processID)
			}
		}
	}()
}

// ReapIdle stops the sandboxes unused for IdleTTL (call periodically).
func (p *Pool) ReapIdle() {
	ttl := p.IdleTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	p.mu.Lock()
	var idle []string
	for id, b := range p.boxes {
		if time.Since(b.lastUsed) > ttl {
			idle = append(idle, id)
		}
	}
	p.mu.Unlock()
	for _, id := range idle {
		p.Release(context.Background(), id)
	}
}

// Close stops every sandbox.
func (p *Pool) Close() {
	p.mu.Lock()
	ids := make([]string, 0, len(p.boxes))
	for id := range p.boxes {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		p.Release(context.Background(), id)
	}
}

type remote struct {
	pool *Pool
	box  *box
}

func (r *remote) ID() string { return r.box.inst.ID }

// Execute sends the job to the sandbox; the host is reachable from it through
// RuntimeService for the duration of the job only.
func (r *remote) Execute(ctx context.Context, job dsl.Job, host dsl.Host) (dsl.Result, error) {
	jobID := uuid.NewString()
	token, unregister := r.pool.Runtime.Register(jobID, host)
	defer unregister()
	snapshot, _ := json.Marshal(map[string]any{"items": job.Items, "intent": job.Intent, "goal": job.Goal})
	params, _ := json.Marshal(job.Params)
	vars, _ := json.Marshal(job.Vars)
	timeout := r.pool.Timeout
	if timeout <= 0 {
		timeout = dsl.DefaultTimeout
	}
	resp, err := r.box.client.Execute(ctx, connect.NewRequest(&runtimev1.ExecuteRequest{
		JobId: jobID, Token: token, RuntimeUrl: r.pool.RuntimeURL, Language: job.Language, Code: job.Code,
		ProcessId: job.ProcessID, Agent: job.Agent, Action: job.Action, ParamsJson: string(params), VarsJson: string(vars),
		BlackboardJson: string(snapshot), TimeoutMs: timeout.Milliseconds(),
	}))
	if err != nil {
		return dsl.Result{}, fmt.Errorf("sandbox %s: %w", r.box.inst.ID, err)
	}
	m := resp.Msg
	res := dsl.Result{Output: m.Output, Suspended: m.Suspended}
	for _, l := range m.Logs {
		res.Logs = append(res.Logs, dsl.LogLine{Time: l.Time.AsTime(), Level: l.Level, Message: l.Message})
	}
	if m.Error != "" {
		return res, errors.New(m.Error)
	}
	if m.ItemsJson != "" {
		if err := json.Unmarshal([]byte(m.ItemsJson), &res.Items); err != nil {
			return res, fmt.Errorf("sandbox items: %w", err)
		}
	}
	return res, nil
}
