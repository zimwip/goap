// Package sandbox runs script actions outside of the engine: a Provisioner
// starts one sandbox per process run (a process on bare metal, a container
// with Docker, a pod on Kubernetes) running goap-runner; the engine sends it
// jobs and serves the DSL operations back through RuntimeService.
package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"connectrpc.com/connect"

	runtimev1 "github.com/zimwip/goap/gen/goap/runtime/v1"
	"github.com/zimwip/goap/gen/goap/runtime/v1/runtimev1connect"
	"github.com/zimwip/goap/pkg/dsl"
)

// Runtime is the engine side RuntimeService: it maps job tokens to the host
// of the running action. A sandbox can only reach the host of its own job.
type Runtime struct {
	mu   sync.RWMutex
	jobs map[string]registered
}

type registered struct {
	token string
	host  dsl.Host
}

var _ runtimev1connect.RuntimeServiceHandler = (*Runtime)(nil)

// NewRuntime returns an empty runtime registry.
func NewRuntime() *Runtime { return &Runtime{jobs: map[string]registered{}} }

// Register exposes host for a job and returns its token.
func (r *Runtime) Register(jobID string, host dsl.Host) (string, func()) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	r.mu.Lock()
	r.jobs[jobID] = registered{token: token, host: host}
	r.mu.Unlock()
	return token, func() {
		r.mu.Lock()
		delete(r.jobs, jobID)
		r.mu.Unlock()
	}
}

func (r *Runtime) host(jobID, token string) (dsl.Host, error) {
	r.mu.RLock()
	j, ok := r.jobs[jobID]
	r.mu.RUnlock()
	if !ok || subtle.ConstantTimeCompare([]byte(j.token), []byte(token)) != 1 {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("unknown job or bad token"))
	}
	return j.host, nil
}

// Call implements runtimev1connect.RuntimeServiceHandler.
func (r *Runtime) Call(ctx context.Context, req *connect.Request[runtimev1.CallRequest]) (*connect.Response[runtimev1.CallResponse], error) {
	host, err := r.host(req.Msg.JobId, req.Msg.Token)
	if err != nil {
		return nil, err
	}
	result, err := dispatch(ctx, host, req.Msg.Op, []byte(req.Msg.ArgsJson))
	out := &runtimev1.CallResponse{}
	if errors.Is(err, dsl.ErrSuspended) {
		out.Suspended = true
	} else if err != nil {
		out.Error = err.Error()
	}
	if result != nil {
		b, jerr := json.Marshal(result)
		if jerr != nil {
			return nil, jerr
		}
		out.ResultJson = string(b)
	}
	return connect.NewResponse(out), nil
}

type callArgs struct {
	Request   dsl.CompleteRequest `json:"request"`
	Name      string              `json:"name"`
	Intent    string              `json:"intent"`
	Args      map[string]any      `json:"args"`
	Key       string              `json:"key"`
	Type      string              `json:"type"`
	Direction string              `json:"direction"`
}

func dispatch(ctx context.Context, h dsl.Host, op string, raw []byte) (any, error) {
	var a callArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s: bad arguments: %w", op, err)
		}
	}
	switch op {
	case "llm.complete":
		return h.Complete(ctx, a.Request)
	case "agents.run":
		return h.RunAgent(ctx, a.Name, a.Intent)
	case "tools.call":
		return h.CallTool(ctx, a.Name, a.Args)
	case "domain.node":
		return h.Node(ctx, a.Key)
	case "domain.nodes":
		return h.Nodes(ctx, a.Type)
	case "domain.links":
		return h.Links(ctx, a.Key, a.Direction, a.Type)
	}
	return nil, fmt.Errorf("unknown runtime operation %q", op)
}

// RemoteHost is the sandbox side dsl.Host: every call goes to the engine
// RuntimeService with the job token.
type RemoteHost struct {
	Client runtimev1connect.RuntimeServiceClient
	JobID  string
	Token  string
}

var _ dsl.Host = RemoteHost{}

func (h RemoteHost) call(ctx context.Context, op string, args callArgs, out any) error {
	raw, _ := json.Marshal(args)
	r, err := h.Client.Call(ctx, connect.NewRequest(&runtimev1.CallRequest{JobId: h.JobID, Token: h.Token, Op: op, ArgsJson: string(raw)}))
	if err != nil {
		return err
	}
	if r.Msg.ResultJson != "" && out != nil {
		if err := json.Unmarshal([]byte(r.Msg.ResultJson), out); err != nil {
			return err
		}
	}
	if r.Msg.Suspended {
		return dsl.ErrSuspended
	}
	if r.Msg.Error != "" {
		return errors.New(r.Msg.Error)
	}
	return nil
}

func (h RemoteHost) Complete(ctx context.Context, req dsl.CompleteRequest) (out dsl.CompleteResult, err error) {
	err = h.call(ctx, "llm.complete", callArgs{Request: req}, &out)
	return
}

func (h RemoteHost) RunAgent(ctx context.Context, name, intent string) (out dsl.AgentResult, err error) {
	err = h.call(ctx, "agents.run", callArgs{Name: name, Intent: intent}, &out)
	return
}

func (h RemoteHost) CallTool(ctx context.Context, name string, args map[string]any) (out any, err error) {
	err = h.call(ctx, "tools.call", callArgs{Name: name, Args: args}, &out)
	return
}

func (h RemoteHost) Node(ctx context.Context, key string) (out dsl.Node, err error) {
	err = h.call(ctx, "domain.node", callArgs{Key: key}, &out)
	return
}

func (h RemoteHost) Nodes(ctx context.Context, nodeType string) (out []dsl.Node, err error) {
	err = h.call(ctx, "domain.nodes", callArgs{Type: nodeType}, &out)
	return
}

func (h RemoteHost) Links(ctx context.Context, key, direction, linkType string) (out []dsl.Link, err error) {
	err = h.call(ctx, "domain.links", callArgs{Key: key, Direction: direction, Type: linkType}, &out)
	return
}
