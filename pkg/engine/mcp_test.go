package engine

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/graph/graphtest"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/methodology"
)

// two ways to reach `documented`: through the document repository (cheap, needs the MCP), or by a human.
const mcpMethodology = `
name: docs
version: 1.0.0
namespace: alm
conditions:
  - name: documented
    expr: artifacts.exists(a, a.type == "doc")
actions:
  - name: fetch_doc
    kind: tool
    tool: document-repository/read
    params: {path: "spec.txt", artifact: doc}
    effects: {documented: true}
    cost: 1
  - name: write_doc
    kind: human
    instructions: write the document
    effects: {documented: true}
    cost: 20
goals:
  - name: done
    pre: {documented: true}
    value: 1
agents:
  - name: writer
    planner: goap
`

type fakeHub struct {
	mu    sync.Mutex
	bound map[string][]string // org -> MCPs
	// restricted: org -> tools the org's restrictions remove (ADR 0028)
	restricted map[string][]string
	// scopes: MCP -> scope (ADR 0028)
	scopes map[string]string
	calls  []string
	// contexts are the call contexts the calls carried
	contexts []mcp.CallContext
	tools    []mcp.ToolInfo
	fail     error
}

func (h *fakeHub) CallTool(ctx context.Context, org, name string, args map[string]any) (any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, org+"|"+name+"|"+args["path"].(string))
	h.contexts = append(h.contexts, mcp.CallFrom(ctx))
	if authz.From(ctx).Subject == "" {
		return nil, errors.New("the principal is not forwarded")
	}
	if h.fail != nil {
		return nil, h.fail
	}
	return map[string]any{"text": "contents of " + args["path"].(string)}, nil
}

func (h *fakeHub) Tools(_ context.Context, org string) ([]mcp.ToolInfo, []string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	tools := h.tools
	if tools == nil {
		// every bound MCP has a read tool, unless the org restricts it
		for _, m := range h.bound[org] {
			if name := mcp.ToolName(m, "read"); !slices.Contains(h.restricted[org], name) {
				tools = append(tools, mcp.ToolInfo{Name: name, Scope: h.scopes[m]})
			}
		}
	}
	return tools, h.bound[org], nil
}

func parseDocs(t *testing.T, edit func(*methodology.Methodology)) *methodology.Compiled {
	t.Helper()
	m, err := methodology.Parse([]byte(mcpMethodology))
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(m)
	}
	ds, err := def.LoadDomains("../../domains")
	if err != nil {
		t.Fatal(err)
	}
	m = m.Resolve(def.DomainTypes(ds...))
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return cm
}

func mcpEngine(t *testing.T, hub ToolPort, client llm.Client) (*Engine, domain.BaselineID) {
	t.Helper()
	e, g, base := setup(t)
	// the organisations holding the changes
	for _, u := range []string{"acme", "globex"} {
		if _, err := graphtest.Unit(context.Background(), g, u, u); err != nil {
			t.Fatal(err)
		}
	}
	e.Methodologies.(StaticMethodologies)["docs"] = parseDocs(t, nil)
	e.Executors[methodology.KindTool] = ToolExecutor{}
	if client != nil {
		e.Executors[methodology.KindLLM] = LLMExecutor{Client: client}
	}
	e.Scope = AuthzScope{Authz: e.Scope.(AuthzScope).Authz, Hub: hub}
	return e, base
}

func runDocs(t *testing.T, e *Engine, base domain.BaselineID, org string) *Process {
	t.Helper()
	ctx := authz.With(context.Background(), authz.Principal{Subject: "alice", Org: org, Roles: []string{"admin"}})
	p, err := e.Start(ctx, StartRequest{Methodology: "docs", Agent: "writer", Goal: "done", BaselineID: base, Intent: "document it", OwnerOrg: org, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	p, err = e.Run(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestToolActionOnlyWhereTheOrganizationBindsTheMCP(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}}
	e, base := mcpEngine(t, hub, nil)

	// acme binds the MCP: the cheap tool action is planned and its result lands on the blackboard
	p := runDocs(t, e, base, "acme")
	if p.Status != StatusCompleted || len(p.Steps) != 1 || p.Steps[0].Action != "fetch_doc" {
		t.Fatalf("acme: %s %s steps=%+v", p.Status, p.Error, p.Steps)
	}
	if len(hub.calls) != 1 || hub.calls[0] != "acme|document-repository/read|spec.txt" {
		t.Fatalf("calls = %v", hub.calls)
	}
	if len(p.Steps[0].ToolCalls) != 1 || p.Steps[0].ToolCalls[0].Name != "document-repository/read" {
		t.Fatalf("tool call not recorded: %+v", p.Steps[0])
	}
	bb, err := e.Graph.Blackboard(context.Background(), p.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	c := bb.Change
	if c.OwnerOrg != "acme" || p.Org != "acme" {
		t.Fatalf("org: change %q process %q", c.OwnerOrg, p.Org)
	}
	if arts := c.ItemsOfKind(domain.KindArtifact); len(arts) != 1 || arts[0].Type != "doc" || arts[0].Data["tool"] != "document-repository/read" {
		t.Fatalf("artifacts = %+v", arts)
	}

	// globex does not: the tool action is not available for scheduling, the human one is planned instead
	hub.calls = nil
	p = runDocs(t, e, base, "globex")
	if p.Status != StatusWaiting || len(p.Steps) != 1 || p.Steps[0].Action != "write_doc" {
		t.Fatalf("globex: %s %s steps=%+v", p.Status, p.Error, p.Steps)
	}
	if len(hub.calls) != 0 {
		t.Fatalf("an unbound tool was called: %v", hub.calls)
	}

	// without any hub nothing is bound
	e.Scope = AuthzScope{Authz: e.Scope.(AuthzScope).Authz}
	p = runDocs(t, e, base, "acme")
	if p.Status != StatusWaiting || p.Steps[0].Action != "write_doc" {
		t.Fatalf("no hub: %s steps=%+v", p.Status, p.Steps)
	}
}

func TestToolActionNotScheduledWhereTheUnitRestrictsTheTool(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}, restricted: map[string][]string{"acme": {"document-repository/read"}}}
	e, base := mcpEngine(t, hub, nil)
	p := runDocs(t, e, base, "acme")
	if p.Status != StatusWaiting || len(p.Steps) != 1 || p.Steps[0].Action != "write_doc" {
		t.Fatalf("restricted tool planned: %s %s steps=%+v", p.Status, p.Error, p.Steps)
	}
	if len(hub.calls) != 0 {
		t.Fatalf("a restricted tool was called: %v", hub.calls)
	}
}

func TestToolCallCarriesTheChangeAndTheProcess(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}}
	e, base := mcpEngine(t, hub, nil)
	p := runDocs(t, e, base, "acme")
	if len(hub.contexts) != 1 || hub.contexts[0].Change != string(p.ChangeID) || hub.contexts[0].Process != p.ID {
		t.Fatalf("call contexts = %+v, want change %s process %s", hub.contexts, p.ChangeID, p.ID)
	}
}

func TestToolFailureFailsTheStep(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}, fail: errors.New("no such file")}
	e, base := mcpEngine(t, hub, nil)
	p := runDocs(t, e, base, "acme")
	if len(p.Steps) == 0 || !strings.Contains(p.Steps[0].Error, "no such file") {
		t.Fatalf("status %s steps=%+v", p.Status, p.Steps)
	}
	if p.Steps[0].ToolCalls[0].Error != "no such file" {
		t.Fatalf("tool error not recorded: %+v", p.Steps[0].ToolCalls)
	}
}

func TestActionCanOnlyCallTheMCPsItDeclares(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository", "ticketing"}}}
	e, _ := mcpEngine(t, hub, nil)
	p := &Process{ID: "p", Initiator: authz.Principal{Subject: "alice", Org: "acme"}, Org: "acme"}
	h := e.newHost(p, methodology.Action{Name: "a", Kind: methodology.KindLLM, MCPs: []string{"document-repository"}}, nil)
	if _, err := h.CallTool(context.Background(), "document-repository/read", map[string]any{"path": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.CallTool(context.Background(), "ticketing/create", map[string]any{"path": "x"}); err == nil || !strings.Contains(err.Error(), "does not declare") {
		t.Fatalf("undeclared MCP reachable: %v", err)
	}
	if _, err := h.CallTool(context.Background(), "read", nil); err == nil {
		t.Fatal("unqualified tool accepted")
	}
	if len(hub.calls) != 1 {
		t.Fatalf("calls = %v", hub.calls)
	}
}

func TestLLMActionCallsToolsThenAnswers(t *testing.T) {
	hub := &fakeHub{
		bound: map[string][]string{"acme": {"document-repository"}},
		tools: []mcp.ToolInfo{{Name: "document-repository/read", Description: "Read a document."}, {Name: "ticketing/create"}},
	}
	var systems []string
	var last string
	client := llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		systems = append(systems, r.System)
		last = r.Messages[len(r.Messages)-1].Content
		if len(r.Messages) == 1 {
			return llm.Response{Text: `{"tool_calls":[{"name":"document-repository/read","arguments":{"path":"spec.txt"}},{"name":"ticketing/create","arguments":{"path":"x"}}]}`}, nil
		}
		return llm.Response{Text: `{"items":[{"kind":"artifact","type":"doc","data":{"seen":"ok"}}]}`}, nil
	})
	e, base := mcpEngine(t, hub, client)
	// the fetch action becomes an LLM action declaring the MCP it uses
	e.Methodologies.(StaticMethodologies)["docs"] = parseDocs(t, func(m *methodology.Methodology) {
		for i := range m.Actions {
			if m.Actions[i].Name == "fetch_doc" {
				m.Actions[i].Kind, m.Actions[i].Tool, m.Actions[i].Params = methodology.KindLLM, "", nil
				m.Actions[i].Prompt, m.Actions[i].MCPs = "Read spec.txt and register it.", []string{"document-repository"}
			}
		}
	})

	p := runDocs(t, e, base, "acme")
	if p.Status != StatusCompleted || len(p.Steps) != 1 {
		t.Fatalf("status %s %s steps=%+v", p.Status, p.Error, p.Steps)
	}
	if !strings.Contains(systems[0], "document-repository/read") || strings.Contains(systems[0], "ticketing/create") {
		t.Fatalf("the model must see the declared MCP's tools only:\n%s", systems[0])
	}
	// the undeclared MCP call is refused, and the model is told so; the declared one went through
	if len(hub.calls) != 1 || !strings.HasPrefix(hub.calls[0], "acme|document-repository/read") {
		t.Fatalf("calls = %v", hub.calls)
	}
	var results []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(last, "Tool results:\n"), "\nContinue: call more tools or give your final answer.")), &results); err != nil {
		t.Fatalf("tool results %q: %v", last, err)
	}
	if results[0]["result"] == nil || results[1]["error"] == nil {
		t.Fatalf("results = %v", results)
	}
	if len(p.Steps[0].ToolCalls) != 2 || p.Steps[0].ToolCalls[1].Error == "" {
		t.Fatalf("tool calls = %+v", p.Steps[0].ToolCalls)
	}
}

func TestAgentMCPsExtendTheToolsOfItsLLMAndScriptActions(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}}
	e, _ := mcpEngine(t, hub, nil)
	p := &Process{ID: "p", Initiator: authz.Principal{Subject: "alice"}, Org: "acme"}
	agentMCPs := []string{"document-repository"}
	ctx := context.Background()
	call := func(kind string) error {
		h := e.newHost(p, methodology.Action{Name: "a", Kind: kind}, agentMCPs)
		_, err := h.CallTool(ctx, "document-repository/read", map[string]any{"path": "x"})
		return err
	}
	for _, kind := range []string{methodology.KindLLM, methodology.KindScript} {
		if err := call(kind); err != nil {
			t.Errorf("%s action cannot use the agent's MCP: %v", kind, err)
		}
	}
	if err := call(methodology.KindBuiltin); err == nil {
		t.Error("a builtin action can use the agent's MCP")
	}
}

// An MCP of scope agent is never scheduled through an action (ADR 0028): the tool action is left out.
func TestAgentScopedMCPIsNotScheduledForActions(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}, scopes: map[string]string{"document-repository": mcp.ScopeAgent}}
	e, base := mcpEngine(t, hub, nil)
	p := runDocs(t, e, base, "acme")
	if p.Status != StatusWaiting || len(p.Steps) != 1 || p.Steps[0].Action != "write_doc" {
		t.Fatalf("agent-scoped tool planned for an action: %s %s steps=%+v", p.Status, p.Error, p.Steps)
	}
}

// Where a tool can be reached from depends on the scope of its MCP: an agent-scoped MCP through the agent,
// by llm actions only; an action-scoped MCP through the action only.
func TestToolScopes(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"orchestrator", "files", "notes"}},
		scopes: map[string]string{"orchestrator": mcp.ScopeAgent, "files": mcp.ScopeAction}}
	e, _ := mcpEngine(t, hub, nil)
	p := &Process{ID: "p", Initiator: authz.Principal{Subject: "alice", Org: "acme"}, Org: "acme"}
	agent := []string{"orchestrator", "files", "notes"}
	cases := []struct {
		name    string
		action  methodology.Action
		agent   []string
		allowed []string
	}{
		// the agent level: orchestration and shared MCPs, not the action-scoped one
		{"llm through its agent", methodology.Action{Name: "plan", Kind: methodology.KindLLM}, agent, []string{"orchestrator/read", "notes/read"}},
		// scripts do not orchestrate
		{"script through its agent", methodology.Action{Name: "s", Kind: methodology.KindScript}, agent, []string{"notes/read"}},
		// an action declaring the MCPs itself: never the agent-scoped one
		{"llm declaring them", methodology.Action{Name: "own", Kind: methodology.KindLLM, MCPs: []string{"orchestrator", "files"}}, nil, []string{"files/read"}},
	}
	for _, c := range cases {
		h := e.newHost(p, c.action, c.agent)
		tools, err := h.Tools(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, tl := range tools {
			got = append(got, tl.Name)
		}
		if !slices.Equal(got, c.allowed) {
			t.Errorf("%s: tools %v, want %v", c.name, got, c.allowed)
		}
		for _, name := range []string{"orchestrator/read", "files/read", "notes/read"} {
			_, err := h.CallTool(context.Background(), name, map[string]any{"path": "x"})
			if want := slices.Contains(c.allowed, name); (err == nil) != want {
				t.Errorf("%s: call %s = %v, allowed %v", c.name, name, err, want)
			}
		}
	}
}
