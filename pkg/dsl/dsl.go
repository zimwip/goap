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

// Item is a change item of the blackboard snapshot.
type Item struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Op         string `json:"op"`
	ProducedBy string `json:"producedBy"`
	Target     *Node  `json:"target"`
	Node       *Node  `json:"node"` // proposal node (draft), base node when updating
	// Link is the link of add_link / remove_link proposals.
	Link *ItemLink      `json:"link"`
	Data map[string]any `json:"data"`
}

// ItemLink is a proposed link: endpoints are node keys, or "@<itemId>" for
// nodes proposed by other items (usable as is in ProposeLink).
type ItemLink struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	From string `json:"from"`
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
	Agent     string         `json:"agent"`
	Action    string         `json:"action"`
	Intent    string         `json:"intent"`
	Goal      string         `json:"goal"`
	Params    map[string]any `json:"params"`
	Vars      map[string]any `json:"vars"`
	Items     []Item         `json:"items"`
	Timeout   time.Duration  `json:"timeout"`
}

// Result is the outcome of a script: the items to add to the change (engine
// ItemInput format), logs and whether the script was suspended.
type Result struct {
	Items     []map[string]any `json:"items"`
	Output    string           `json:"output"`
	Logs      []LogLine        `json:"logs"`
	Suspended bool             `json:"suspended"`
}

// Ctx is the object injected as `ctx` in scripts.
type Ctx struct {
	job       Job
	host      Host
	gctx      context.Context
	out       []map[string]any
	logs      []LogLine
	seq       int
	suspended bool
	result    any
}

func newCtx(gctx context.Context, job Job, host Host) *Ctx {
	return &Ctx{job: job, host: host, gctx: gctx}
}

// ---- context ----------------------------------------------------------------

func (c *Ctx) Intent() string { return c.job.Intent }
func (c *Ctx) Goal() string   { return c.job.Goal }
func (c *Ctx) Agent() string  { return c.job.Agent }
func (c *Ctx) Action() string { return c.job.Action }

// Param returns an action parameter.
func (c *Ctx) Param(name string) any { return c.job.Params[name] }

// Var returns a process variable.
func (c *Ctx) Var(name string) any { return c.job.Vars[name] }

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

func (c *Ctx) Impacts() []Item   { return c.Items(string(domain.KindImpact)) }
func (c *Ctx) Proposals() []Item { return c.Items(string(domain.KindProposal)) }

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

// AddImpact records a direct impact on a node of the reference baseline.
func (c *Ctx) AddImpact(key, reason string) string {
	return c.emit(map[string]any{"kind": "impact", "type": "direct", "target": key, "data": map[string]any{"reason": reason}})
}

// ProposeNode proposes a new node and returns its reference ("#pN").
func (c *Ctx) ProposeNode(nodeType, key string, props map[string]any) string {
	return c.emit(map[string]any{"kind": "proposal", "proposal": map[string]any{"op": "create_node",
		"node": map[string]any{"type": nodeType, "key": key, "props": props}}})
}

// ProposeUpdate proposes a new version of a node.
func (c *Ctx) ProposeUpdate(key string, props map[string]any) string {
	return c.emit(map[string]any{"kind": "proposal", "proposal": map[string]any{"op": "update_node",
		"node": map[string]any{"base": key, "props": props}}})
}

// ProposeDelete proposes the deletion of a node.
func (c *Ctx) ProposeDelete(key string) string {
	return c.emit(map[string]any{"kind": "proposal", "proposal": map[string]any{"op": "delete_node", "node": map[string]any{"base": key}}})
}

// ProposeTransition proposes to move a node to a lifecycle state. A node of a
// type with a lifecycle is only modified in an editable state: reopen it with a
// transition first, and move it out of the editable states at the end.
func (c *Ctx) ProposeTransition(key, state string) string {
	return c.emit(map[string]any{"kind": "proposal", "proposal": map[string]any{"op": "transition_node",
		"node": map[string]any{"base": key, "state": state}}})
}

// ProposeLink proposes a link; from and to are node keys or "#pN" references.
func (c *Ctx) ProposeLink(from, linkType, to string) string {
	return c.emit(map[string]any{"kind": "proposal", "proposal": map[string]any{"op": "add_link",
		"link": map[string]any{"type": linkType, "from": from, "to": to}}})
}

// AddArtifact records free data (report…).
func (c *Ctx) AddArtifact(artifactType string, data map[string]any) string {
	return c.emit(map[string]any{"kind": "artifact", "type": artifactType, "data": data})
}

// Decide accepts or rejects a proposal (item id, or "#pN").
func (c *Ctx) Decide(item string, accept bool, comment string) string {
	if len(item) > 0 && item[0] != '#' && item[0] != '@' {
		item = "@" + item
	}
	return c.emit(map[string]any{"kind": "decision", "decision": map[string]any{"item": item, "accept": accept, "comment": comment}})
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

// ItemsFromBlackboard builds the item snapshot given to scripts.
func ItemsFromBlackboard(bb domain.Blackboard) []Item {
	view := func(r *domain.NodeRef) *Node {
		if r == nil {
			return nil
		}
		n := &Node{ID: string(r.ID), Version: int(r.Version)}
		if v, ok := bb.Nodes[*r]; ok {
			n.Key, n.Type, n.Props = v.Key, v.Type, v.Properties
		}
		return n
	}
	out := make([]Item, 0, len(bb.Change.Items))
	for _, it := range bb.Change.Items {
		if !bb.Change.Active(it.ID) {
			continue
		}
		x := Item{ID: string(it.ID), Kind: string(it.Kind), Type: it.Type, Status: string(bb.Change.EffectiveStatus(it.ID)),
			ProducedBy: it.ProducedBy, Target: view(it.Target), Data: it.Data}
		if p := it.Proposal; p != nil {
			x.Op = string(p.Op)
			if l := p.Link; l != nil {
				end := func(e domain.Endpoint) string {
					if e.Item != "" {
						return "@" + string(e.Item)
					}
					if n := view(e.Node); n != nil && n.Key != "" {
						return n.Key
					}
					return e.String()
				}
				x.Link = &ItemLink{ID: string(l.LinkID), Type: l.Type, From: end(l.From), To: end(l.To)}
			}
			if p.Node != nil {
				x.Node = &Node{Key: p.Node.Key, Type: p.Node.Type, Props: p.Node.Properties}
				if b := view(p.Node.Base); b != nil {
					x.Node.ID, x.Node.Version = b.ID, b.Version
					if x.Node.Key == "" {
						x.Node.Key = b.Key
					}
					if x.Node.Type == "" {
						x.Node.Type = b.Type
					}
				}
			}
		}
		out = append(out, x)
	}
	return out
}
