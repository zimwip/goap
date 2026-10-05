// Package dsl is the API injected in script actions (JavaScript or Go): it
// reads the blackboard snapshot and the reference domain, buffers writes to
// the change, and calls the platform (LLM, sub-agents, tools) through a Host.
// The same Go type backs both languages: methods are exposed in camelCase to
// JavaScript (goja) and as is to Go (yaegi). See docs/dsl.md.
package dsl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

// Node is a domain node version.
type Node struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Key     string `json:"key"`
	Type    string `json:"type"`
	// State in the lifecycle of the node type (empty: none).
	State string         `json:"state"`
	Props map[string]any `json:"props"`
}

// LinkEnd is a link endpoint summary.
type LinkEnd struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Key     string `json:"key"`
	Type    string `json:"type"`
}

// Link is a domain link.
type Link struct {
	ID   string  `json:"id"`
	Type string  `json:"type"`
	From LinkEnd `json:"from"`
	To   LinkEnd `json:"to"`
}

// Item is a fact of the blackboard snapshot (artifact, decision): the nodes the change acts
// on are the change impacts.
type Item struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Type       string         `json:"type"`
	Status     string         `json:"status"`
	ProducedBy string         `json:"producedBy"`
	Data       map[string]any `json:"data"`
}

// ChangeImpact is a change impact of the blackboard snapshot (ADR 0024): a node the
// change reads, modifies or creates, with the versions it starts from and produces.
type ChangeImpact struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Type      string `json:"type"`
	Intent    string `json:"intent"`
	Rationale string `json:"rationale"`
	Review    string `json:"review"`
	// Planned is set while the change impact has no post version yet.
	Planned bool `json:"planned"`
	// CheckedOut is set while the post version is the working version of the change: editable, not frozen (ADR 0076).
	CheckedOut bool  `json:"checkedOut"`
	Pre        *Node `json:"pre"`
	Post       *Node `json:"post"`
	Landed     *Node `json:"landed"`
	// Links are the outgoing links of the version written (post): from this node to the linked ones.
	Links []Link `json:"links"`
	// Items are the items the change impact is derived from (none when written directly).
	Items []string `json:"items"`
}

// NodeOp is an operation on a change impact buffered by a script; the engine
// applies them in order after the script ends.
type NodeOp struct {
	// Op is declare (ImpactNode, CreateNode), write (WriteNode: the working version, checked out on the first write,
	// edited in place), review (ReviewNode), checkin (CheckinNode), transition (TransitionNode) or cancel
	// (CancelCheckout) (ADR 0076).
	Op string `json:"op"`
	// Ref names a declared change impact ("#nN") for the next operations of the script.
	Ref    string `json:"ref,omitempty"`
	Intent string `json:"intent,omitempty"`
	Key    string `json:"key,omitempty"`
	Type   string `json:"type,omitempty"`
	// Rationale says why (declare); the comment of a review is in Comment.
	Rationale string `json:"rationale,omitempty"`
	// Node designates the change impact of a write or review: a node key or "#nN".
	Node  string         `json:"node,omitempty"`
	Props map[string]any `json:"props,omitempty"`
	// State is the lifecycle state a transition moves the node to.
	State       string       `json:"state,omitempty"`
	Links       []NodeOpLink `json:"links,omitempty"`
	RemoveLinks []string     `json:"removeLinks,omitempty"`
	Accept      bool         `json:"accept,omitempty"`
	// Reserve is the key of the derogation an acceptance stands on (accepted with reserve, ADR 0075 §2).
	Reserve    string `json:"reserve,omitempty"`
	Comment    string `json:"comment,omitempty"`
	ProducedBy string `json:"producedBy,omitempty"`
}

// NodeOpLink is an outgoing link added by a write: To is a node key or "#nN".
type NodeOpLink struct {
	Type string `json:"type"`
	To   string `json:"to"`
}

// CompleteRequest is an LLM request.
type CompleteRequest struct {
	Model     string `json:"model"`
	System    string `json:"system"`
	Prompt    string `json:"prompt"`
	JSON      bool   `json:"json"`
	MaxTokens int    `json:"maxTokens"`
}

// CompleteResult is an LLM answer.
type CompleteResult struct {
	Text         string `json:"text"`
	JSON         any    `json:"json"`
	Model        string `json:"model"`
	InputTokens  int    `json:"inputTokens"`
	OutputTokens int    `json:"outputTokens"`
}

// AgentResult is the outcome of a sub-agent run.
type AgentResult struct {
	Status    string `json:"status"`
	Goal      string `json:"goal"`
	ProcessID string `json:"processId"`
}

// LogLine is a script log line.
type LogLine struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// ErrSuspended is returned by Host.RunAgent when the sub-agent is blocked
// (waiting for a human): the action is suspended and retried later.
var ErrSuspended = errors.New("suspended: waiting for a sub-agent")

// Host performs the operations that leave the script: in a sandbox it is a
// client of the engine RuntimeService.
type Host interface {
	Complete(ctx context.Context, req CompleteRequest) (CompleteResult, error)
	RunAgent(ctx context.Context, name, intent string) (AgentResult, error)
	CallTool(ctx context.Context, name string, args map[string]any) (any, error)
	Node(ctx context.Context, key string) (Node, error)
	Nodes(ctx context.Context, nodeType string) ([]Node, error)
	Links(ctx context.Context, key, direction, linkType string) ([]Link, error)
}

// Job is a script execution.
type Job struct {
	Language  string         `json:"language"`
	Code      string         `json:"code"`
	ProcessID string         `json:"processId"`
	ParentID  string         `json:"parentId,omitempty"`
	Agent     string         `json:"agent"`
	Action    string         `json:"action"`
	Intent    string         `json:"intent"`
	Goal      string         `json:"goal"`
	Params    map[string]any `json:"params"`
	Vars      map[string]any `json:"vars"`
	Items     []Item         `json:"items"`
	Nodes     []ChangeImpact `json:"nodes"`
	// Options and DecisionPoints of the change (ADR 0009 §3-4).
	Options        []Option        `json:"options"`
	DecisionPoints []DecisionPoint `json:"decisionPoints"`
	Timeout        time.Duration   `json:"timeout"`
}

// Result is the outcome of a script: the items to add to the change (engine
// ItemInput format), logs and whether the script was suspended.
type Result struct {
	Items []map[string]any `json:"items"`
	// Nodes are the change impact operations, in order (the engine applies them).
	Nodes     []NodeOp  `json:"nodes"`
	Output    string    `json:"output"`
	Logs      []LogLine `json:"logs"`
	Suspended bool      `json:"suspended"`
	// WakeOn lists signal names that, if emitted (addressed to this process)
	// before a suspended sub-agent terminates, wake this process early.
	WakeOn []string `json:"wakeOn,omitempty"`
	// VarsSet holds the process-private variables set by SetVar during this
	// run (engine-owned execution state, not a graph write).
	VarsSet map[string]any `json:"varsSet,omitempty"`
}

// Ctx is the object injected as `ctx` in scripts.
type Ctx struct {
	job       Job
	host      Host
	gctx      context.Context
	out       []map[string]any
	nodeOps   []NodeOp
	nseq      int
	dseq      int
	logs      []LogLine
	seq       int
	suspended bool
	wakeOn    []string
	vars      map[string]any
	result    any
}

func newCtx(gctx context.Context, job Job, host Host) *Ctx {
	return &Ctx{job: job, host: host, gctx: gctx}
}

// ---- context ----------------------------------------------------------------

func (c *Ctx) Intent() string    { return c.job.Intent }
func (c *Ctx) Goal() string      { return c.job.Goal }
func (c *Ctx) Agent() string     { return c.job.Agent }
func (c *Ctx) Action() string    { return c.job.Action }
func (c *Ctx) ProcessID() string { return c.job.ProcessID }

// ParentID is the process that called this one as a sub-agent (empty if
// none): the target to address a Signal to so the parent may wake on it.
func (c *Ctx) ParentID() string { return c.job.ParentID }

// Param returns an action parameter.
func (c *Ctx) Param(name string) any { return c.job.Params[name] }

// Var returns a process variable.
func (c *Ctx) Var(name string) any { return c.job.Vars[name] }

// SetVar sets a process-private variable: engine-owned execution state, not a
// graph write, visible only to this process's own conditions/scripts.
func (c *Ctx) SetVar(name string, v any) {
	if c.vars == nil {
		c.vars = map[string]any{}
	}
	c.vars[name] = v
}

// WakeOn declares signal names that, if emitted (addressed to this process)
// before a sub-agent this script suspends on terminates, wake it early.
func (c *Ctx) WakeOn(names ...string) {
	c.wakeOn = append(c.wakeOn, names...)
}

// ---- blackboard (read) ----------------------------------------------------

// Items returns the change items of a kind ("" = all).
func (c *Ctx) Items(kind string) []Item {
	out := []Item{}
	for _, it := range c.job.Items {
		if kind == "" || it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}

// ChangeImpacts returns the change impacts of the change: the stored ones and the ones derived from its items.
func (c *Ctx) ChangeImpacts() []ChangeImpact {
	if c.job.Nodes == nil {
		return []ChangeImpact{}
	}
	return c.job.Nodes
}

// ---- domain (read, reference baseline) --------------------------------------

func (c *Ctx) Node(key string) (Node, error) { return c.host.Node(c.gctx, key) }
func (c *Ctx) Nodes(nodeType string) ([]Node, error) {
	return c.host.Nodes(c.gctx, nodeType)
}
func (c *Ctx) Links(key, direction, linkType string) ([]Link, error) {
	return c.host.Links(c.gctx, key, direction, linkType)
}

// ---- change (buffered writes) ---------------------------------------------

func (c *Ctx) emit(item map[string]any) string {
	c.seq++
	ref := fmt.Sprintf("p%d", c.seq)
	item["ref"] = ref
	item["producedBy"] = c.job.Action
	c.out = append(c.out, item)
	return "#" + ref
}

// ImpactNode declares that the change acts on an existing node of the reference
// baseline, and why: a change impact with no version written yet. It returns a
// reference ("#nN") for WriteNode and ReviewNode of the same script.
func (c *Ctx) ImpactNode(key, rationale string) string {
	c.nseq++
	ref := fmt.Sprintf("#n%d", c.nseq)
	c.nodeOps = append(c.nodeOps, NodeOp{Op: "declare", Ref: ref, Intent: string(domain.IntentModified), Key: key, Rationale: rationale, ProducedBy: c.job.Action})
	return ref
}

// CreateNode declares that the change creates a node of a type, and why.
func (c *Ctx) CreateNode(nodeType, key, rationale string) string {
	c.nseq++
	ref := fmt.Sprintf("#n%d", c.nseq)
	c.nodeOps = append(c.nodeOps, NodeOp{Op: "declare", Ref: ref, Intent: string(domain.IntentCreated), Key: key, Type: nodeType, Rationale: rationale, ProducedBy: c.job.Action})
	return ref
}

// WriteNode edits the working version of a declared node (ADR 0076): the first write checks the node out (a new
// version on the change branch), the next ones edit it in place. node is a key of a change impact or the reference
// returned by ImpactNode / CreateNode; w may hold props (merged over the current ones), links ([{type, to}] with to a
// node key or a reference of a node written earlier) and removeLinks (link ids). A lifecycle state is a transition
// (TransitionNode); a node is removed by its parent losing the link to it.
func (c *Ctx) WriteNode(node string, w map[string]any) {
	op := NodeOp{Op: "write", Node: node, ProducedBy: c.job.Action}
	op.Props, _ = w["props"].(map[string]any)
	for _, x := range asList(w["links"]) {
		if m, ok := x.(map[string]any); ok {
			t, _ := m["type"].(string)
			to, _ := m["to"].(string)
			op.Links = append(op.Links, NodeOpLink{Type: t, To: to})
		}
	}
	for _, x := range asList(w["removeLinks"]) {
		if id, ok := x.(string); ok {
			op.RemoveLinks = append(op.RemoveLinks, id)
		}
	}
	c.nodeOps = append(c.nodeOps, op)
}

func asList(v any) []any {
	switch l := v.(type) {
	case []any:
		return l
	case []map[string]any:
		out := make([]any, len(l))
		for i, m := range l {
			out[i] = m
		}
		return out
	case []string:
		out := make([]any, len(l))
		for i, m := range l {
			out[i] = m
		}
		return out
	}
	return nil
}

// ReviewNode accepts or rejects a change impact; the comment is mandatory.
func (c *Ctx) ReviewNode(node string, accept bool, comment string) {
	c.nodeOps = append(c.nodeOps, NodeOp{Op: "review", Node: node, Accept: accept, Comment: comment, ProducedBy: c.job.Action})
}

// ReviewNodeWithReserve accepts a change impact with a reserve: the derogation (its key) must be open, unexpired and
// target the impact or its action, else the engine refuses the review (ADR 0075).
func (c *Ctx) ReviewNodeWithReserve(node, derogation, comment string) {
	c.nodeOps = append(c.nodeOps, NodeOp{Op: "review", Node: node, Accept: true, Reserve: derogation, Comment: comment, ProducedBy: c.job.Action})
}

// CheckinNode freezes the working version of a change impact: its accepted review authorizes it (ADR 0076).
func (c *Ctx) CheckinNode(node string) {
	c.nodeOps = append(c.nodeOps, NodeOp{Op: "checkin", Node: node, ProducedBy: c.job.Action})
}

// TransitionNode moves the node of a change impact to a lifecycle state: a version of its own, from a checked-in
// version, authorized and guarded when it is taken (ADR 0076).
func (c *Ctx) TransitionNode(node, state string) {
	c.nodeOps = append(c.nodeOps, NodeOp{Op: "transition", Node: node, State: state, ProducedBy: c.job.Action})
}

// CancelCheckout drops the working version of a change impact; a creation cancelled before its first check-in removes
// the node (ADR 0076).
func (c *Ctx) CancelCheckout(node string) {
	c.nodeOps = append(c.nodeOps, NodeOp{Op: "cancel", Node: node, ProducedBy: c.job.Action})
}

// AddArtifact records free data (report…).
func (c *Ctx) AddArtifact(artifactType string, data map[string]any) string {
	return c.emit(map[string]any{"kind": "artifact", "type": artifactType, "data": data})
}

// Signal emits a named notification other agents or a live parent may react
// to. target addresses a process id ("" = broadcast, visible to triggers and
// to readers of the change).
func (c *Ctx) Signal(name string, data map[string]any, target string) string {
	return c.emit(map[string]any{"kind": "signal", "type": name, "data": data, "target": target})
}

// ---- platform calls ---------------------------------------------------------

// LLM completes a prompt with the default model and returns the text.
func (c *Ctx) LLM(prompt string) (string, error) {
	r, err := c.Complete(CompleteRequest{Prompt: prompt})
	return r.Text, err
}

// Complete calls the model gateway.
func (c *Ctx) Complete(req CompleteRequest) (CompleteResult, error) {
	if req.Model == "" {
		req.Model = "default"
	}
	r, err := c.host.Complete(c.gctx, req)
	if err != nil {
		return r, err
	}
	if req.JSON && r.JSON == nil {
		var v any
		if json.Unmarshal([]byte(extractJSON(r.Text)), &v) == nil {
			r.JSON = v
		}
	}
	return r, nil
}

// RunAgent runs a sub-agent on the same change.
func (c *Ctx) RunAgent(name, intent string) (AgentResult, error) {
	r, err := c.host.RunAgent(c.gctx, name, intent)
	if errors.Is(err, ErrSuspended) {
		c.suspended = true
	}
	return r, err
}

// CallTool calls an MCP tool ("server/tool").
func (c *Ctx) CallTool(name string, args map[string]any) (any, error) {
	return c.host.CallTool(c.gctx, name, args)
}

// ---- logs -------------------------------------------------------------------

func (c *Ctx) logf(level, msg string) {
	c.logs = append(c.logs, LogLine{Time: time.Now().UTC(), Level: level, Message: msg})
}

func (c *Ctx) Log(msg string)  { c.logf("info", msg) }
func (c *Ctx) Warn(msg string) { c.logf("warn", msg) }

// SetOutput sets the human readable output of the action.
func (c *Ctx) SetOutput(v any) { c.result = v }

func extractJSON(s string) string {
	for i, r := range s {
		if r == '{' || r == '[' {
			return s[i:]
		}
	}
	return s
}

// ItemsFromBlackboard builds the snapshot of the facts (artifacts, decisions) given to scripts.
func ItemsFromBlackboard(bb domain.Blackboard) []Item {
	out := make([]Item, 0, len(bb.Change.Items))
	for _, it := range bb.Change.Items {
		if !bb.Change.Active(it.ID) || it.Kind == domain.KindFlow {
			continue
		}
		out = append(out, Item{ID: string(it.ID), Kind: string(it.Kind), Type: it.Type, Status: string(bb.Change.EffectiveStatus(it.ID)),
			ProducedBy: it.ProducedBy, Data: it.Data})
	}
	return out
}
