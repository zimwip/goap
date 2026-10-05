package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"text/template"

	"github.com/zimwip/goap/pkg/builtins"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
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
	// Step is the step of a process the action carries out (nil outside processes).
	Step *StepContext
}

// ActionResult is what an executor produced.
type ActionResult struct {
	Items []ItemInput
	// Nodes are change impact operations (ADR 0024), applied after the items.
	Nodes []dsl.NodeOp
	// Wait suspends the process until a human submits the items.
	Wait   bool
	Output string
	// Suspended: the action waits for the sub-agent Child and is retried
	// when it completes.
	Suspended bool
	Child     string
	// WakeOn lists signal names that, if emitted (addressed to this process)
	// before Child terminates, wake this process early instead of waiting for
	// Child's termination.
	WakeOn  []string
	Logs    []LogLine
	Sandbox string
	// VarsSet holds process-private variables set by the action (engine-owned
	// execution state, not a graph write) — merged into Process.Vars.
	VarsSet map[string]any
	// Method is the method a step chose to carry it out (ADR 0035 §1), recorded as the step's specialization.
	Method string
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
{"kind":"artifact","type":"...","data":{...}}
{"kind":"decision","decision":{"item":"@<item id>","accept":true,"comment":"why"}}
The nodes the change acts on are change impacts: items of kind "changeImpact", applied in order:
{"kind":"changeImpact","changeImpact":{"op":"declare","ref":"#n1","intent":"modified","key":"<node key>","rationale":"why the node is impacted"}}
{"kind":"changeImpact","changeImpact":{"op":"declare","ref":"#n2","intent":"created","type":"<node type>","key":"<new key>","rationale":"why"}}
{"kind":"changeImpact","changeImpact":{"op":"write","node":"<node key or #n1>","props":{...},"links":[{"type":"...","to":"<node key or a #nN already written>"}]}}
{"kind":"changeImpact","changeImpact":{"op":"review","node":"<node key>","accept":true,"comment":"why"}}
{"kind":"changeImpact","changeImpact":{"op":"checkin","node":"<node key>"}}
{"kind":"changeImpact","changeImpact":{"op":"transition","node":"<node key>","state":"<lifecycle state>"}}
{"kind":"changeImpact","changeImpact":{"op":"cancel","node":"<node key>"}}
Decision points of the change (a question to settle, usually which option) are items of kind "decisionPoint":
{"kind":"decisionPoint","decisionPoint":{"op":"open","ref":"#d1","question":"...","options":["<option name>"],"criteria":["..."]}}
{"kind":"decisionPoint","decisionPoint":{"op":"rule","point":"<point id or #d1>","outcome":"decided","option":"<option name>","confidence":0.8,"justification":"why"}}
{"kind":"decisionPoint","decisionPoint":{"op":"rule","point":"<point id>","outcome":"undecidable","justification":"why it cannot be decided","questions":["what must be known first"]}}
{"kind":"decisionPoint","decisionPoint":{"op":"answer","questionId":"<question id>","answer":"..."}}
A write edits the working version of the node: the first write checks it out, the next ones edit it in place. An accepted review authorizes its check-in, which freezes it; a lifecycle state is a transition of its own, from a checked-in version. A node whose type has a lifecycle is only written in an editable state: move it there first with a transition, and leave it with a transition once its review is accepted and it is checked in. A node is never deleted: removing a child is a write of its parent without the link.
Reference nodes by their key. Reference items and change impacts created in the same answer by "#<ref>".`

// PromptData is exposed to prompt templates.
type PromptData struct {
	Change    domain.Change
	Goal      string
	Action    methodology.Action
	Vars      map[string]any
	Baseline  struct{ Nodes []domain.Node }
	Artifacts []ItemView
	// ChangeImpacts are the nodes the change acts on (ADR 0024), as the process sees them.
	ChangeImpacts []ChangeImpactView
	// Options and DecisionPoints of the change (ADR 0009 §3-4), ActiveOption the option it works on.
	Options        []domain.Flow
	ActiveOption   string
	DecisionPoints []domain.DecisionPoint
	// Step is the step of a process the action carries out (nil outside processes): {{ .Step.Guidance }}.
	Step *StepContext
}

// ChangeImpactView is a change impact with its hydrated pre and post versions.
type ChangeImpactView struct {
	domain.ChangeImpact
	Pre  domain.NodeView
	Post domain.NodeView
}

// ItemView is a fact of the change (artifact, decision).
type ItemView struct {
	domain.ChangeItem
}

var funcs = template.FuncMap{
	"json": func(v any) string { b, _ := json.Marshal(v); return string(b) },
}

// filterNamespace keeps only the nodes of the given namespace: a change acts
// on one namespace (its declared WHAT), so what it queries must be bound to
// it too, whatever domains share the baseline.
func filterNamespace(nodes []domain.Node, namespace string) []domain.Node {
	if namespace == "" {
		return nodes
	}
	kept := make([]domain.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Namespace == namespace {
			kept = append(kept, n)
		}
	}
	return kept
}

// RenderPrompt renders an action prompt against the blackboard.
func RenderPrompt(ctx context.Context, ac ActionContext) (string, error) {
	tpl, err := template.New(ac.Action.Name).Funcs(funcs).Option("missingkey=zero").Parse(ac.Action.Prompt)
	if err != nil {
		return "", fmt.Errorf("prompt template: %w", err)
	}
	d := PromptData{Change: ac.Blackboard.Change, Goal: ac.Process.Goal, Action: ac.Action, Vars: ac.Process.Vars,
		Options: domain.OptionsOf(ac.Blackboard), ActiveOption: domain.ActiveOptionOf(ac.Blackboard), DecisionPoints: domain.DecisionPointsOf(ac.Blackboard), Step: ac.Step}
	nodes, _, err := readGraph(ctx, ac.Graph, ac.Blackboard.Change.ID, ac.Process.Flow, ac.Blackboard.Change.BaselineID)
	if err != nil {
		return "", err
	}
	d.Baseline.Nodes = filterNamespace(nodes, ac.Blackboard.Change.Namespace)
	for _, it := range ac.Blackboard.Change.Items {
		if it.Kind == domain.KindArtifact {
			d.Artifacts = append(d.Artifacts, ItemView{ChangeItem: it})
		}
	}
	for _, cn := range ac.Blackboard.Change.Nodes {
		v := ChangeImpactView{ChangeImpact: cn}
		if cn.Pre != nil {
			v.Pre = ac.Blackboard.Nodes[*cn.Pre]
		}
		if cn.Post != nil {
			v.Post = ac.Blackboard.Nodes[*cn.Post]
		}
		d.ChangeImpacts = append(d.ChangeImpacts, v)
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
	system := llmSystem + ac.Step.section() + briefSection(ac)
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

// BuiltinExecutor dispatches to registered Go functions. It is the engine's table of the builtin names (ADR 0062).
type BuiltinExecutor map[string]BuiltinFunc

// Register adds the implementation of a builtin; it panics on a name already registered.
func (b BuiltinExecutor) Register(name string, f BuiltinFunc) {
	if _, dup := b[name]; dup {
		panic(fmt.Sprintf("engine: builtin %q registered twice", name))
	}
	b[name] = f
}

// HasBuiltin implements methodology.BuiltinSet.
func (b BuiltinExecutor) HasBuiltin(name string) bool {
	_, ok := b[name]
	return ok
}

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
	b := BuiltinExecutor{}
	b.Register(builtins.GraphPropagate, Propagate)
	b.Register(builtins.GraphApply, ApplyChange)
	b.Register(builtins.DecisionInvestigate, Investigate)
	b.Register(builtins.DerogationExpire, expireAllDerogations)
	b.Register(builtins.ProcessStep, RunStep)
	return b
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
	nodes, links, err := readGraph(ctx, ac.Graph, ac.Blackboard.Change.ID, ac.Process.Flow, ac.Blackboard.Change.BaselineID)
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
		depth int
	}
	seen := map[domain.NodeRef]bool{}
	var queue []entry
	// the nodes the change modifies are the seeds; each propagated node is declared as an impact
	for _, cn := range ac.Blackboard.Change.Nodes {
		if cn.Pre != nil && cn.Intent == domain.IntentModified {
			seen[*cn.Pre] = true
			queue = append(queue, entry{ref: *cn.Pre})
		}
	}
	var ops []dsl.NodeOp
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
			ops = append(ops, dsl.NodeOp{Op: "declare", Ref: fmt.Sprintf("#p%d", len(ops)), Intent: string(domain.IntentModified), Key: src.Key,
				Rationale: fmt.Sprintf("propagated (depth %d): %s %s %s", e.depth+1, src.Key, l.Type, byRef[e.ref].Key)})
			queue = append(queue, entry{ref: l.From, depth: e.depth + 1})
		}
	}
	items := []ItemInput{{Kind: string(domain.KindArtifact), Type: "propagation",
		Data: map[string]any{"propagated": len(ops), "maxDepth": maxDepth}}}
	return ActionResult{Items: items, Nodes: ops, Output: fmt.Sprintf("%d propagated impacts", len(ops))}, nil
}

// ApplyChange materializes the change into a new baseline (graph.apply). The
// change is then "applied": conditions observe it through change.status and
// change.resultBaseline. Param baselineName defaults to the change title.
// Every working version whose review is accepted is checked in first: the
// acceptance authorizes the check-in (ADR 0076); one still proposed keeps the
// change from applying.
func ApplyChange(ctx context.Context, ac ActionContext) (ActionResult, error) {
	name, _ := ac.Action.Params["baselineName"].(string)
	c := ac.Blackboard.Change
	for _, cn := range c.Nodes {
		if cn.Post == nil || cn.Flow != "" || cn.Superseded || cn.Review != domain.ReviewAccepted || !ac.Blackboard.Nodes[*cn.Post].CheckedOut {
			continue
		}
		if _, err := ac.Graph.ImpactNodeCheckin(ctx, c.ID, cn.ID, domain.MainFlow, ""); err != nil {
			return ActionResult{}, err
		}
	}
	b, err := ac.Graph.Apply(ctx, c.ID, name)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Output: fmt.Sprintf("baseline %s (%s) created", b.Name, b.ID)}, nil
}
