package engine

import (
	"context"
	"fmt"
	"maps"
	"time"
	"unicode/utf8"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/llm"
	"github.com/zimwip/goap/engine/intent"
	"github.com/zimwip/goap/engine/methodology"
)

// Persistent agents (ADR 0112): an agent declaring `persistent` has one instance per scope (a change, or what the
// event starting it names), a process the engine keeps. Every new execution of that agent on that scope goes to the
// instance: it runs again in place when it has ended, or the event waits in its inbox while it is at work. Its llm
// actions get back the exchanges they had with the model, so the same instance, with the same conversation, handles
// the work from start to end. An instance is closed when a run completes with its `until` conditions holding (or
// when a person cancels it): the next execution on its scope starts a new one.

// instanceLock prefixes the lock serializing the lookup and the creation of the instance of a key of a persistent
// agent, across the engine replicas (ADR 0124).
const instanceLock = "instance:"

// Turn is one exchange of an llm action of a persistent instance with the model: what was asked and the answer.
type Turn struct {
	At     time.Time `json:"at"`
	Run    int       `json:"run"`
	Action string    `json:"action"`
	Event  string    `json:"event,omitempty"`
	Prompt string    `json:"prompt"`
	Answer string    `json:"answer"`
}

// Caps of what an instance remembers of an exchange.
const (
	maxTurnPrompt = 2000
	maxTurnAnswer = 4000
)

// IsInstance reports whether the process is the instance of a persistent agent.
func (p *Process) IsInstance() bool { return p.Instance != "" }

// instanceKey is the key of the instance a start request goes to ("" when the agent is not persistent, or the
// scope names nothing for this request).
func (e *Engine) instanceKey(ctx context.Context, req StartRequest) (string, *methodology.Persistence, error) {
	if req.Agent == "" || req.Methodology == "" {
		return "", nil, nil
	}
	m, err := e.Methodologies.Methodology(ctx, req.Methodology)
	if err != nil {
		return "", nil, err
	}
	ag, ok := m.Agent(req.Agent)
	if !ok || ag.Persistent == nil {
		return "", nil, nil
	}
	event, _ := req.Vars["event"].(map[string]any)
	scope := ag.Persistent.Resolve(string(req.ChangeID), event)
	if scope == "" {
		return "", nil, nil
	}
	return m.Name() + "/" + ag.Name + "@" + scope, ag.Persistent, nil
}

// openInstance is the open instance of key, the latest one (nil when there is none).
func (e *Engine) openInstance(ctx context.Context, key string) (*Process, error) {
	all, err := e.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	var out *Process
	for _, p := range all {
		if p.Instance != key || p.Closed || p.Status == StatusCancelled {
			continue
		}
		if out == nil || p.CreatedAt.After(out.CreatedAt) {
			out = p
		}
	}
	return out, nil
}

// resumeInstance hands a start request to the open instance of key: it runs it again in place when it has ended,
// or keeps the event in its inbox when it is at work. handled is false when there is no open instance, or when the
// request is a sub-agent call of another process while the instance is at work (fresh is then true: that call
// gets a run of its own, which is not the instance).
func (e *Engine) resumeInstance(ctx context.Context, key string, req StartRequest) (p *Process, handled, fresh bool, err error) {
	open, err := e.openInstance(ctx, key)
	if err != nil || open == nil {
		return nil, false, false, err
	}
	defer e.lock(open.ID)()
	if p, err = e.Store.Get(ctx, open.ID); err != nil {
		return nil, false, false, err
	}
	if p.ChangeID != "" {
		if ch, err := e.Graph.Change(ctx, p.ChangeID); err == nil && (ch.Status == domain.ChangeApplied || ch.Status == domain.ChangeAbandoned) {
			p.Closed = true // its change is over: so is the instance
			return nil, false, false, e.save(ctx, p, "step")
		}
	}
	ended := p.Status == StatusCompleted || p.Status == StatusStuck || p.Status == StatusFailed || p.WaitsForConditions()
	if !ended {
		if req.ParentID != "" && req.ParentID != p.ParentID {
			return nil, false, true, nil
		}
		if event, ok := req.Vars["event"].(map[string]any); ok {
			p.Inbox = append(p.Inbox, event)
			return p, true, false, e.save(ctx, p, "step")
		}
		return p, true, false, nil // at work on it already
	}
	vars := maps.Clone(p.Vars)
	if vars == nil {
		vars = map[string]any{}
	}
	maps.Copy(vars, req.Vars)
	p.Vars = vars
	if req.Goal != "" {
		p.Goal = req.Goal
	}
	if req.ParentID != "" {
		p.ParentID, p.Step = req.ParentID, req.Step
	}
	if req.Cause != "" {
		p.Cause = req.Cause
	}
	// the execution acts for whoever asks for it now
	if who := authz.From(ctx); who.Subject != "" {
		p.Initiator = who
	}
	if req.Intent != "" {
		if err := e.appendIntentTurns(ctx, p, intent.Turn{Role: "user", Text: req.Intent}); err != nil {
			return nil, false, false, err
		}
	}
	p.Status, p.Error, p.Plan, p.Pending = StatusRunning, "", nil, nil
	p.Disabled = map[string]bool{}
	p.Runs++
	if err := e.checkAgentRoles(ctx, p); err != nil {
		return nil, false, false, err
	}
	e.queue(ctx, p, "new execution", map[string]any{"instance": key, "run": p.Runs, "parent": req.ParentID, "trigger": req.Trigger})
	return p, true, false, e.save(ctx, p, "step")
}

// closeInstance closes the instance of a persistent agent whose run completed with its until conditions holding.
func closeInstance(p *Process, m *methodology.Compiled) {
	if !p.IsInstance() {
		return
	}
	ag, ok := m.Agent(p.Agent)
	if ok && ag.Persistent != nil && len(ag.Persistent.Until) > 0 && p.World.Satisfies(ag.Persistent.Until) {
		p.Closed = true
	}
}

// handOver starts a new instance for the events left in the inbox of an instance that closed: the next cycle. The
// instance of a change keeps its change; one of another scope starts from the latest baseline, like the trigger
// that started it.
func (e *Engine) handOver(ctx context.Context, p *Process) error {
	if len(p.Inbox) == 0 {
		return nil
	}
	m, err := e.methodologyOf(ctx, p)
	if err != nil {
		return err
	}
	ag, _ := m.Agent(p.Agent)
	event, rest := p.Inbox[0], p.Inbox[1:]
	vars := maps.Clone(p.Vars)
	if vars == nil {
		vars = map[string]any{}
	}
	vars["event"] = event
	req := StartRequest{Methodology: p.Methodology, Agent: p.Agent, Goal: p.Goal, Trigger: p.Trigger, Title: p.Title, OwnerOrg: p.Org,
		ProjectID: p.Project, Vars: vars, Intent: fmt.Sprintf("next cycle of %s after %s", p.Agent, p.ID)}
	if ag.Persistent.ScopePath() == methodology.ScopeChange {
		req.ChangeID = p.ChangeID
	} else if req.BaselineID, err = e.latestBaseline(ctx, domain.NamespaceOf(m.Namespace())); err != nil {
		return err
	}
	unlock := e.lock(p.ID)
	p.Inbox = nil
	err = e.save(ctx, p, "step")
	unlock()
	if err != nil {
		return err
	}
	next, err := e.Start(authz.With(ctx, p.Initiator), req)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		unlock := e.lock(next.ID)
		if cur, err := e.Store.Get(ctx, next.ID); err == nil {
			cur.Inbox = append(cur.Inbox, rest...)
			_ = e.Store.Put(ctx, cur)
			next = cur
		}
		unlock()
	}
	if next.Status == StatusRunning {
		e.schedule(next.ID)
	}
	return nil
}

// memoryMessages are the exchanges an instance had with the model, given back before the prompt of its next call.
func memoryMessages(p *Process) []llm.Message {
	if p == nil || !p.IsInstance() || len(p.Memory) == 0 {
		return nil
	}
	out := make([]llm.Message, 0, 2*len(p.Memory))
	for _, t := range p.Memory {
		head := fmt.Sprintf("[earlier execution %d of this instance, %s, action %s", t.Run, t.At.Format(time.RFC3339), t.Action)
		if t.Event != "" {
			head += ", on " + t.Event
		}
		out = append(out, llm.Message{Role: "user", Content: head + "]\n" + t.Prompt}, llm.Message{Role: "assistant", Content: t.Answer})
	}
	return out
}

// memoryNote tells the model the exchanges before the prompt are its own earlier work on the same scope.
const memoryNote = "\n\nYou are a persistent agent: the exchanges before the last message are your earlier executions on this same scope. " +
	"Go on from them: keep what still holds, say what you change and why, do not start over."

// remember adds the exchange of an llm action to the memory of an instance, keeping the last turns.
func (e *Engine) remember(p *Process, m *methodology.Compiled, action string, t *Turn) {
	if t == nil || !p.IsInstance() {
		return
	}
	ag, _ := m.Agent(p.Agent)
	keep := ag.Persistent.Turns()
	if keep == 0 {
		return
	}
	t.At, t.Run, t.Action = e.clock(), p.Runs, action
	if ev, ok := p.Vars["event"].(map[string]any); ok {
		t.Event, _ = ev["type"].(string)
	}
	t.Prompt, t.Answer = cut(t.Prompt, maxTurnPrompt), cut(t.Answer, maxTurnAnswer)
	p.Memory = append(p.Memory, *t)
	if len(p.Memory) > keep {
		p.Memory = p.Memory[len(p.Memory)-keep:]
	}
}

// cut keeps the first max bytes of s, on a rune boundary.
func cut(s string, max int) string {
	if len(s) <= max {
		return s
	}
	n := max
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + " (...)"
}
