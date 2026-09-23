package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/llm"
)

// ToolCaller calls external tools (the MCP connector).
type ToolCaller interface {
	CallTool(ctx context.Context, name string, args map[string]any) (any, error)
}

// Host implements dsl.Host for one action execution: every call leaving the
// script goes through it, is attributed to the process / agent / action and
// is recorded in the step (tokens, durations, errors). It is safe for
// concurrent use (sandboxes call it from RuntimeService goroutines).
type Host struct {
	e       *Engine
	process *Process // read-only while the action runs
	action  string

	mu        sync.Mutex
	llmCalls  []LLMCall
	toolCalls []ToolCall
	children  map[string]string // new sub-agent calls
	spawned   []string          // their processes, in call order
	waitingOn string            // child process blocking the action
	calls     int

	graphOnce sync.Once
	nodes     []domain.Node
	links     []domain.Link
	graphErr  error
}

var _ dsl.Host = (*Host)(nil)

func (e *Engine) newHost(p *Process, action string) *Host {
	return &Host{e: e, process: p, action: action, children: map[string]string{}}
}

// Attributes identify the caller of platform calls (used for tracing).
type Attributes struct {
	ProcessID, Methodology, Agent, Action string
}

type attrKey struct{}

// WithAttributes returns a context carrying the caller attributes.
func WithAttributes(ctx context.Context, a Attributes) context.Context {
	return context.WithValue(ctx, attrKey{}, a)
}

// AttributesFrom returns the caller attributes of ctx.
func AttributesFrom(ctx context.Context) (Attributes, bool) {
	a, ok := ctx.Value(attrKey{}).(Attributes)
	return a, ok
}

func (h *Host) ctx(ctx context.Context) context.Context {
	return WithAttributes(ctx, Attributes{ProcessID: h.process.ID, Methodology: h.process.Methodology, Agent: h.process.Agent, Action: h.action})
}

func (h *Host) completeLLM(ctx context.Context, client llm.Client, req llm.Request) (llm.Response, error) {
	if client == nil {
		return llm.Response{}, errors.New("no model gateway configured")
	}
	start := time.Now()
	resp, err := client.Complete(h.ctx(ctx), req)
	call := LLMCall{Provider: resp.Provider, Model: resp.Model, InputTokens: int64(resp.Usage.InputTokens),
		OutputTokens: int64(resp.Usage.OutputTokens), DurationMs: time.Since(start).Milliseconds()}
	if call.Model == "" {
		call.Model = req.Model
	}
	if err != nil {
		call.Error = err.Error()
	}
	h.mu.Lock()
	h.llmCalls = append(h.llmCalls, call)
	h.mu.Unlock()
	return resp, err
}

// Complete implements dsl.Host.
func (h *Host) Complete(ctx context.Context, r dsl.CompleteRequest) (dsl.CompleteResult, error) {
	resp, err := h.completeLLM(ctx, h.e.LLM, llm.Request{Model: r.Model, System: r.System, JSON: r.JSON, MaxTokens: r.MaxTokens,
		Messages: []llm.Message{{Role: "user", Content: r.Prompt}}})
	if err != nil {
		return dsl.CompleteResult{}, err
	}
	out := dsl.CompleteResult{Text: resp.Text, Model: resp.Model, InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens}
	if r.JSON {
		var v any
		if llm.DecodeJSON(resp.Text, &v) == nil {
			out.JSON = v
		}
	}
	return out, nil
}

// CallTool implements dsl.Host.
func (h *Host) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	start := time.Now()
	var out any
	var err error
	ctx, end := h.e.tracer().StartTool(h.ctx(ctx), h.process, h.action, name)
	if h.e.Tools == nil {
		err = fmt.Errorf("tool %s: no MCP connector configured", name)
	} else {
		out, err = h.e.Tools.CallTool(ctx, name, args)
	}
	end(err)
	call := ToolCall{Name: name, DurationMs: time.Since(start).Milliseconds()}
	if err != nil {
		call.Error = err.Error()
	}
	h.mu.Lock()
	h.toolCalls = append(h.toolCalls, call)
	h.mu.Unlock()
	return out, err
}

// RunAgent implements dsl.Host: the sub-agent runs on the same change.
func (h *Host) RunAgent(ctx context.Context, name, intentText string) (dsl.AgentResult, error) {
	h.mu.Lock()
	key := fmt.Sprintf("%s#%d:%s", h.action, h.calls, name)
	h.calls++
	h.mu.Unlock()
	return h.e.runChild(authz.With(ctx, h.process.Initiator), h, key, name, intentText)
}

func (h *Host) graph(ctx context.Context) ([]domain.Node, []domain.Link, error) {
	h.graphOnce.Do(func() {
		h.nodes, h.links, h.graphErr = h.e.Graph.BaselineGraph(ctx, h.process.BaselineID)
	})
	return h.nodes, h.links, h.graphErr
}

func toDSL(n domain.Node) dsl.Node {
	return dsl.Node{ID: string(n.ID), Version: int(n.Version), Key: n.Key, Type: n.Type, Props: n.Properties}
}

// Node implements dsl.Host (reference baseline).
func (h *Host) Node(ctx context.Context, key string) (dsl.Node, error) {
	nodes, _, err := h.graph(ctx)
	if err != nil {
		return dsl.Node{}, err
	}
	for _, n := range nodes {
		if n.Key == key {
			return toDSL(n), nil
		}
	}
	return dsl.Node{}, fmt.Errorf("node %q not in the reference baseline", key)
}

// Nodes implements dsl.Host.
func (h *Host) Nodes(ctx context.Context, nodeType string) ([]dsl.Node, error) {
	nodes, _, err := h.graph(ctx)
	if err != nil {
		return nil, err
	}
	out := []dsl.Node{}
	for _, n := range nodes {
		if nodeType == "" || n.Type == nodeType {
			out = append(out, toDSL(n))
		}
	}
	return out, nil
}

// Links implements dsl.Host.
func (h *Host) Links(ctx context.Context, key, direction, linkType string) ([]dsl.Link, error) {
	nodes, links, err := h.graph(ctx)
	if err != nil {
		return nil, err
	}
	byRef := map[domain.NodeRef]domain.Node{}
	for _, n := range nodes {
		byRef[n.Ref()] = n
	}
	end := func(r domain.NodeRef) dsl.LinkEnd {
		n := byRef[r]
		return dsl.LinkEnd{ID: string(r.ID), Version: int(r.Version), Key: n.Key, Type: n.Type}
	}
	out := []dsl.Link{}
	for _, l := range links {
		if linkType != "" && l.Type != linkType {
			continue
		}
		from, to := byRef[l.From], byRef[l.To]
		if (direction != "in" && from.Key == key) || (direction != "out" && to.Key == key) {
			out = append(out, dsl.Link{ID: string(l.ID), Type: l.Type, From: end(l.From), To: end(l.To)})
		}
	}
	return out, nil
}

// record merges the calls of the host into the step.
func (h *Host) record(step *Step) {
	h.mu.Lock()
	defer h.mu.Unlock()
	step.LLMCalls = append(step.LLMCalls, h.llmCalls...)
	step.ToolCalls = append(step.ToolCalls, h.toolCalls...)
	for _, c := range h.llmCalls {
		step.Usage.LLMCalls++
		step.Usage.InputTokens += c.InputTokens
		step.Usage.OutputTokens += c.OutputTokens
	}
	step.Usage.ToolCalls += len(h.toolCalls)
	step.Children = append(step.Children, h.spawned...)
}

// ---- script actions ---------------------------------------------------------

// Sandbox executes script jobs in isolation.
type Sandbox interface {
	// ID identifies the sandbox (container, pod, process…).
	ID() string
	Execute(ctx context.Context, job dsl.Job, host dsl.Host) (dsl.Result, error)
}

// Sandboxes provides one sandbox per process run.
type Sandboxes interface {
	Acquire(ctx context.Context, processID string) (Sandbox, error)
	// Release destroys the sandbox of a finished process.
	Release(ctx context.Context, processID string)
}

// InprocSandboxes runs scripts inside the engine process. The interpreters
// have no file system, network or process access, but this is NOT an
// isolation boundary: use it for tests and single-user development only.
type InprocSandboxes struct{}

type inproc struct{}

func (inproc) ID() string { return "inproc" }
func (inproc) Execute(ctx context.Context, job dsl.Job, host dsl.Host) (dsl.Result, error) {
	return dsl.Run(ctx, job, host)
}

// Acquire implements Sandboxes.
func (InprocSandboxes) Acquire(context.Context, string) (Sandbox, error) { return inproc{}, nil }

// Release implements Sandboxes.
func (InprocSandboxes) Release(context.Context, string) {}

// ScriptExecutor runs script actions (JavaScript / Go) in the sandbox of the
// process.
type ScriptExecutor struct {
	Sandboxes Sandboxes
}

// Execute implements Executor.
func (s ScriptExecutor) Execute(ctx context.Context, ac ActionContext) (ActionResult, error) {
	if s.Sandboxes == nil {
		return ActionResult{}, errors.New("no sandbox provider configured")
	}
	sb, err := s.Sandboxes.Acquire(ctx, ac.Process.ID)
	if err != nil {
		return ActionResult{}, fmt.Errorf("sandbox: %w", err)
	}
	intentText := ""
	for _, t := range ac.Process.Intent.Turns {
		if t.Role == "user" {
			intentText = t.Text
			break
		}
	}
	job := dsl.Job{Language: ac.Action.Language, Code: ac.Action.Code, ProcessID: ac.Process.ID, Agent: ac.Process.Agent,
		Action: ac.Action.Name, Intent: intentText, Goal: ac.Process.Goal, Params: ac.Action.Params, Vars: ac.Process.Vars,
		Items: dsl.ItemsFromBlackboard(ac.Blackboard)}
	res, err := sb.Execute(ctx, job, ac.Host)
	out := ActionResult{Output: res.Output, Sandbox: sb.ID()}
	for _, l := range res.Logs {
		out.Logs = append(out.Logs, LogLine{Time: l.Time, Level: l.Level, Message: l.Message, ProcessID: ac.Process.ID, Action: ac.Action.Name})
	}
	if err != nil {
		return out, err
	}
	if res.Suspended {
		out.Suspended = true
		out.Child = ac.Host.waitingOn
		return out, nil
	}
	raw, _ := json.Marshal(res.Items)
	if err := json.Unmarshal(raw, &out.Items); err != nil {
		return out, fmt.Errorf("script items: %w", err)
	}
	return out, nil
}
