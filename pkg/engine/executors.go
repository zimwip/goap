package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"text/template"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/methodology"
)

// ActionContext is given to executors.
type ActionContext struct {
	Process    *Process
	Action     methodology.Action
	Blackboard domain.Blackboard
	Graph      GraphPort
	// Host serves the DSL operations of the action (LLM, sub-agents, tools,
	// domain) and records their usage in the step.
	Host *Host
}

// ActionResult is what an executor produced.
type ActionResult struct {
	Items []ItemInput
	// Wait suspends the process until a human submits the items.
	Wait   bool
	Output string
	// Suspended: the action waits for the sub-agent Child and is retried
	// when it completes.
	Suspended bool
	Child     string
	Logs      []LogLine
	Sandbox   string
}

// Executor runs one kind of action.
type Executor interface {
	Execute(ctx context.Context, ac ActionContext) (ActionResult, error)
}

// ---- llm ------------------------------------------------------------------

// LLMExecutor renders the action prompt, calls the model gateway and parses
// the items of the answer.
type LLMExecutor struct {
	Client llm.Client
}

const llmSystem = `You are an agent of an enterprise methodology platform. You work on a change of a versioned domain graph.
Answer ONLY with a JSON object {"items":[...]} where each item is one of:
{"ref":"<optional local name>","kind":"impact","type":"<direct|propagated|...>","target":"<node key>","data":{...}}
{"ref":"...","kind":"proposal","proposal":{"op":"create_node","node":{"key":"...","type":"...","props":{...}}}}
{"kind":"proposal","proposal":{"op":"update_node","node":{"base":"<node key>","props":{...}}}}
{"kind":"proposal","proposal":{"op":"delete_node","node":{"base":"<node key>"}}}
{"kind":"proposal","proposal":{"op":"transition_node","node":{"base":"<node key>","state":"<target lifecycle state>"}}}
{"kind":"proposal","proposal":{"op":"add_link","link":{"type":"...","from":"<node key or #ref>","to":"<node key or #ref>"}}}
{"kind":"artifact","type":"...","data":{...}}
A node whose type has a lifecycle can only be modified in an editable state: reopen it with a transition_node first, and finish with a transition_node to a non-editable state.
Reference nodes by their key. Reference items created in the same answer by "#<ref>".`

// PromptData is exposed to prompt templates.
type PromptData struct {
	Change    domain.ChangeSet
	Goal      string
	Action    methodology.Action
	Vars      map[string]any
	Baseline  struct{ Nodes []domain.Node }
	Impacts   []ItemView
	Proposals []ItemView
	Artifacts []ItemView
}

// ItemView is a change item with its hydrated target.
type ItemView struct {
	domain.ChangeItem
	Target domain.NodeView
}

var funcs = template.FuncMap{
	"json": func(v any) string { b, _ := json.Marshal(v); return string(b) },
}

// RenderPrompt renders an action prompt against the blackboard.
func RenderPrompt(ctx context.Context, ac ActionContext) (string, error) {
	tpl, err := template.New(ac.Action.Name).Funcs(funcs).Option("missingkey=zero").Parse(ac.Action.Prompt)
	if err != nil {
		return "", fmt.Errorf("prompt template: %w", err)
	}
	d := PromptData{Change: ac.Blackboard.Change, Goal: ac.Process.Goal, Action: ac.Action, Vars: ac.Process.Vars}
	nodes, _, err := ac.Graph.BaselineGraph(ctx, ac.Blackboard.Change.BaselineID)
	if err != nil {
		return "", err
	}
	d.Baseline.Nodes = nodes
	for _, it := range ac.Blackboard.Change.Items {
		v := ItemView{ChangeItem: it}
		if it.Target != nil {
			v.Target = ac.Blackboard.Nodes[*it.Target]
		}
		switch it.Kind {
		case domain.KindImpact:
			d.Impacts = append(d.Impacts, v)
		case domain.KindProposal:
			d.Proposals = append(d.Proposals, v)
		case domain.KindArtifact:
			d.Artifacts = append(d.Artifacts, v)
		}
	}
	var b bytes.Buffer
	if err := tpl.Execute(&b, d); err != nil {
		return "", fmt.Errorf("prompt template: %w", err)
	}
	return b.String(), nil
}

// GuidanceType is the type of the artifact items that carry a comment for the agents (a human
// steering a relaunched flow, or any note left on the blackboard).
const GuidanceType = "guidance"

// guidanceSection renders the guidance items in effect on the blackboard as a section appended to
// the prompt of an LLM action, so that the agent adapts its answer.
func guidanceSection(bb domain.Blackboard) string {
	var lines []string
	for _, it := range bb.Change.Items {
		if it.Kind != domain.KindArtifact || it.Type != GuidanceType || !bb.Change.InEffect(it.ID) {
			continue
		}
		if text, _ := it.Data["text"].(string); strings.TrimSpace(text) != "" {
			lines = append(lines, "- "+strings.TrimSpace(text))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\nGuidance from a human reviewer (take it into account: it takes precedence over your previous answers):\n" + strings.Join(lines, "\n") + "\n"
}

// MaxToolRounds bounds the tool calls of one LLM action: after that many rounds the model
// must answer with its items.
const MaxToolRounds = 6

// maxToolResult bounds the size of a tool result given back to the model.
const maxToolResult = 20000

// toolsSection describes the tools an LLM action may call and the protocol to call them.
// Tool calls are exchanged as JSON on top of any model (no provider-specific tool API).
func toolsSection(tools []mcp.ToolInfo) string {
	var b strings.Builder
	b.WriteString(`

You can call tools before giving your final answer. To call tools, answer ONLY with
{"tool_calls":[{"name":"<tool>","arguments":{...}}]}; the results are given back to you and you continue.
When you have what you need, answer with {"items":[...]} as described above. Available tools:
`)
	for _, t := range tools {
		schema, _ := json.Marshal(t.InputSchema)
		fmt.Fprintf(&b, "- %s: %s arguments: %s\n", t.Name, t.Description, schema)
	}
	return b.String()
}

type toolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// Execute implements Executor. An action that declares MCPs may call their tools (bound by the
// organization of the change) in a bounded loop before answering with its items.
func (e LLMExecutor) Execute(ctx context.Context, ac ActionContext) (ActionResult, error) {
	prompt, err := RenderPrompt(ctx, ac)
	if err != nil {
		return ActionResult{}, err
	}
	prompt += guidanceSection(ac.Blackboard)
	model := ac.Action.Model
	if model == "" {
		model = "default"
	}
	system := llmSystem
	var tools []mcp.ToolInfo
	if ac.Host != nil && len(ac.Host.mcps) > 0 {
		if tools, err = ac.Host.Tools(ctx); err != nil {
			return ActionResult{}, err
		}
		if len(tools) > 0 {
			system += toolsSection(tools)
		}
	}
	msgs := []llm.Message{{Role: "user", Content: prompt}}
	calls := 0
	for round := 0; ; round++ {
		req := llm.Request{Model: model, System: system, JSON: true, MaxTokens: 16000, Messages: msgs}
		var resp llm.Response
		if ac.Host != nil {
			resp, err = ac.Host.completeLLM(ctx, e.Client, req)
		} else {
			resp, err = e.Client.Complete(ctx, req)
		}
		if err != nil {
			return ActionResult{}, err
		}
		var out struct {
			Items     []ItemInput `json:"items"`
			ToolCalls []toolCall  `json:"tool_calls"`
		}
		if err := llm.DecodeJSON(resp.Text, &out); err != nil {
			return ActionResult{Output: resp.Text}, err
		}
		if len(out.ToolCalls) == 0 || len(tools) == 0 {
			return ActionResult{Items: out.Items, Output: fmt.Sprintf("%s/%s: %d items, %d tool calls", resp.Provider, resp.Model, len(out.Items), calls)}, nil
		}
		if round >= MaxToolRounds {
			return ActionResult{Output: resp.Text}, fmt.Errorf("the model still asks for tools after %d rounds", MaxToolRounds)
		}
		results := make([]map[string]any, 0, len(out.ToolCalls))
		for _, c := range out.ToolCalls {
			calls++
			res, err := ac.Host.CallTool(ctx, c.Name, c.Arguments)
			r := map[string]any{"tool": c.Name}
			if err != nil {
				r["error"] = err.Error() // the model sees the failure and may adapt
			} else {
				r["result"] = res
			}
			results = append(results, r)
		}
		b, _ := json.Marshal(results)
		if len(b) > maxToolResult {
			b = append(b[:maxToolResult], []byte("... (truncated)")...)
		}
		msgs = append(msgs, llm.Message{Role: "assistant", Content: resp.Text},
			llm.Message{Role: "user", Content: "Tool results:\n" + string(b) + "\nContinue: call more tools or give your final answer."})
	}
}

// ---- tool -------------------------------------------------------------------

// ToolExecutor calls the tool of the action ("<mcp>/<tool>") of the MCP hub, with the
// action params as arguments, and records the result as an artifact item of the type
// params["artifact"] (default "tool_result") with data {tool, result}.
type ToolExecutor struct{}

// Execute implements Executor.
func (ToolExecutor) Execute(ctx context.Context, ac ActionContext) (ActionResult, error) {
	args := maps.Clone(ac.Action.Params)
	artifact, _ := args["artifact"].(string)
	delete(args, "artifact")
	if artifact == "" {
		artifact = "tool_result"
	}
	res, err := ac.Host.CallTool(ctx, ac.Action.Tool, args)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Items:  []ItemInput{{Kind: string(domain.KindArtifact), Type: artifact, Data: map[string]any{"tool": ac.Action.Tool, "result": res}}},
		Output: "called " + ac.Action.Tool,
	}, nil
}

// ---- human ----------------------------------------------------------------

// HumanExecutor suspends the process until a human submits items.
type HumanExecutor struct{}

// Execute implements Executor.
func (HumanExecutor) Execute(context.Context, ActionContext) (ActionResult, error) {
	return ActionResult{Wait: true}, nil
}

// ---- builtin --------------------------------------------------------------

// BuiltinFunc is a Go implementation of an action.
type BuiltinFunc func(ctx context.Context, ac ActionContext) (ActionResult, error)

// BuiltinExecutor dispatches to registered Go functions.
type BuiltinExecutor map[string]BuiltinFunc

// Execute implements Executor.
func (b BuiltinExecutor) Execute(ctx context.Context, ac ActionContext) (ActionResult, error) {
	f, ok := b[ac.Action.Builtin]
	if !ok {
		return ActionResult{}, fmt.Errorf("unknown builtin %q", ac.Action.Builtin)
	}
	return f(ctx, ac)
}

// DefaultBuiltins returns the builtin actions shipped with the engine.
func DefaultBuiltins() BuiltinExecutor {
	return BuiltinExecutor{"graph.propagate": Propagate, "graph.apply": ApplyChange}
}

// Propagate follows links backwards from impacted nodes (an impact on the
// target of a link propagates to its source) within the reference baseline.
// Params: maxDepth (default 3), linkTypes (default all).
func Propagate(ctx context.Context, ac ActionContext) (ActionResult, error) {
	maxDepth := 3
	switch v := ac.Action.Params["maxDepth"].(type) {
	case int:
		maxDepth = v
	case float64:
		maxDepth = int(v)
	}
	allowed := map[string]bool{}
	if lts, ok := ac.Action.Params["linkTypes"].([]any); ok {
		for _, lt := range lts {
			allowed[fmt.Sprint(lt)] = true
		}
	}
	nodes, links, err := ac.Graph.BaselineGraph(ctx, ac.Blackboard.Change.BaselineID)
	if err != nil {
		return ActionResult{}, err
	}
	byRef := map[domain.NodeRef]domain.Node{}
	for _, n := range nodes {
		byRef[n.Ref()] = n
	}
	incoming := map[domain.NodeRef][]domain.Link{}
	for _, l := range links {
		if len(allowed) == 0 || allowed[l.Type] {
			incoming[l.To] = append(incoming[l.To], l)
		}
	}
	type entry struct {
		ref   domain.NodeRef
		item  string
		depth int
	}
	seen := map[domain.NodeRef]bool{}
	var queue []entry
	for _, it := range ac.Blackboard.Change.ItemsOfKind(domain.KindImpact) {
		seen[*it.Target] = true
		queue = append(queue, entry{ref: *it.Target, item: "@" + string(it.ID)})
	}
	var items []ItemInput
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		if e.depth >= maxDepth {
			continue
		}
		for _, l := range incoming[e.ref] {
			if seen[l.From] {
				continue
			}
			seen[l.From] = true
			src := byRef[l.From]
			ref := fmt.Sprintf("p%d", len(items))
			items = append(items, ItemInput{Ref: ref, Kind: string(domain.KindImpact), Type: "propagated", Target: src.Key,
				DerivedFrom: []string{e.item},
				Data:        map[string]any{"reason": fmt.Sprintf("%s %s %s", src.Key, l.Type, byRef[e.ref].Key), "depth": e.depth + 1}})
			queue = append(queue, entry{ref: l.From, item: "#" + ref, depth: e.depth + 1})
		}
	}
	items = append(items, ItemInput{Kind: string(domain.KindArtifact), Type: "propagation",
		Data: map[string]any{"propagated": len(items), "maxDepth": maxDepth}})
	return ActionResult{Items: items, Output: fmt.Sprintf("%d propagated impacts", len(items)-1)}, nil
}

// ApplyChange materializes the change into a new baseline (graph.apply). The
// change is then "applied": conditions observe it through change.status and
// change.resultBaseline. Param baselineName defaults to the change title.
func ApplyChange(ctx context.Context, ac ActionContext) (ActionResult, error) {
	name, _ := ac.Action.Params["baselineName"].(string)
	b, err := ac.Graph.Apply(ctx, ac.Blackboard.Change.ID, name)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Output: fmt.Sprintf("baseline %s (%s) created", b.Name, b.ID)}, nil
}
