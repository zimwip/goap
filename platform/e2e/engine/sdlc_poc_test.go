package enginee2e

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/llm"
	"github.com/zimwip/goap/contracts/mcp"
	"github.com/zimwip/goap/contracts/risk"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/mcpbuiltin"
	"github.com/zimwip/goap/mcp/connectors/builtin"
	"github.com/zimwip/goap/mcp/connectors/workspace"
)

// pocHub binds goap-change (on the graph, acting for the caller) and the workspace (on the local machine, with a
// temporary root) for every unit.
type pocHub struct {
	change builtin.Change
	ws     workspace.Connector
	root   string
}

func (h *pocHub) CallTool(ctx context.Context, _, name string, args map[string]any) (any, error) {
	m, tool, _ := strings.Cut(name, "/")
	switch m {
	case mcpbuiltin.Change:
		return h.change.Invoke(ctx, tool, args, nil, nil)
	case mcpbuiltin.Workspace:
		return h.ws.Invoke(ctx, tool, args, map[string]any{"root": h.root, "gitName": "goap", "gitEmail": "goap@example.org"}, nil)
	}
	return nil, fmt.Errorf("unexpected tool %s", name)
}

func (h *pocHub) Tools(context.Context, string) ([]mcp.ToolInfo, []string, error) {
	var out []mcp.ToolInfo
	defs := []mcp.Def{mcpbuiltin.WorkspaceDef()}
	for _, d := range mcpbuiltin.Defs() {
		if d.Name == mcpbuiltin.Change {
			defs = append(defs, d)
		}
	}
	for _, def := range defs {
		for _, t := range def.Tools {
			out = append(out, mcp.ToolInfo{Name: mcp.ToolName(def.Name, t.Name), Description: t.Description, InputSchema: t.InputSchema, ReadOnly: t.ReadOnly})
		}
	}
	return out, []string{mcpbuiltin.Workspace, mcpbuiltin.Change, "goap-graph"}, nil
}

// TestSDLCDerisksByAProofOfConcept runs a proof of concept end to end (ADR 0131): a high risk of an sdlc change is
// mitigated by a poc action; a person opens it as a sub-change of the poc methodology, which states the risk as its
// hypothesis, probes it, builds the probe in its workspace, runs its check, judges it and is concluded by a person;
// the event of its conclusion reaches the parent, which takes the verdict back (the risk closed), marks the action
// done and closes the sub-change, landing nothing.
func TestSDLCDerisksByAProofOfConcept(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	e, g, head := sdlcSetup(t)
	pm, err := methodology.LoadFile("../../../methodologies/poc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	poc, err := pm.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(engine.StaticMethodologies)["poc"] = poc
	e.Scope = engine.AuthzScope{Authz: e.Scope.(engine.AuthzScope).Authz, Hub: &pocHub{
		change: builtin.Connectors(builtin.Ports{Graph: g})[mcpbuiltin.Change].(builtin.Change),
		ws:     workspace.New(), root: filepath.Join(t.TempDir(), "workspaces")}}
	base := sdlcModel(t)
	e.Executors[methodology.KindLLM] = engine.LLMExecutor{Client: llm.ClientFunc(func(ctx context.Context, req llm.Request) (llm.Response, error) {
		p := req.Messages[0].Content
		switch {
		case strings.Contains(p, "You prepare a proof of concept"):
			return llm.Response{Text: `{"items":[{"type":"artifact","target":"probe","data":{"hypothesis":"HYP-1","build":"a split call to the PSP sandbox",
			  "observe":"the answer of the PSP","refutes":"three installments accepted","dir":"probe-HYP-1","check":"test -f split.ok"}}]}`}, nil
		case strings.Contains(p, "You build a PROOF OF CONCEPT"):
			if last := req.Messages[len(req.Messages)-1]; len(last.ToolResults) > 0 {
				return llm.Response{Text: `{"items":[{"type":"artifact","target":"probe_change","data":{"hypothesis":"HYP-1","summary":"split call","files":["split.ok"]}}]}`}, nil
			}
			return llm.Response{StopReason: llm.StopToolUse, ToolCalls: []llm.ToolCall{
				{ID: "w", Name: "workspace/write", Arguments: map[string]any{"path": "probe-HYP-1/split.ok", "content": "3 installments accepted\n"}}}}, nil
		case strings.Contains(p, "A proof of concept checked a hypothesis"):
			if !strings.Contains(p, "succeeded") {
				t.Errorf("the verdict is drafted without the successful check:\n%s", p)
			}
			return llm.Response{Text: `{"items":[{"type":"assessment","data":{"key":"item:HYP-1","verdict":"refuted","score":4,
			  "findings":[{"text":"the PSP accepts three installments","severity":"info"}],"suggestion":"close the risk"}}]}`}, nil
		}
		return base.Complete(ctx, req)
	})}

	ctx := devCtx
	ch, err := g.CreateChange(ctx, graph.NewChange{Title: "Payment in 3 installments", Intent: "Allow payment in 3 installments with no fees",
		Methodology: "sdlc", Namespace: "alm", BaselineID: head, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.AddItems(ctx, ch.ID, []domain.ChangeItem{
		{Type: risk.TypeRisk, Data: map[string]any{"key": "RSK-1", "title": "the PSP does not split payments", "probability": 4.0, "impact": 4.0, "status": "mitigating"}},
		{Type: risk.TypeAction, Data: map[string]any{"key": "ACT-1", "title": "split a payment on the PSP sandbox", "for": "RSK-1",
			"kind": "poc", "purpose": "derisk", "risks": []any{"RSK-1"}, "status": "open"}},
	}); err != nil {
		t.Fatal(err)
	}

	// the step that opens the proofs of concept, as the starting points launch it: a person opens ACT-1
	p, err := e.Start(ctx, engine.StartRequest{Methodology: "sdlc", Agent: "software_delivery", Goal: "software_delivery/proofs/open", ChangeID: ch.ID})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(context.Background(), p.ID); err != nil || p.Pending == nil || !strings.HasSuffix(p.Pending.Action, "proofs/open") {
		t.Fatalf("must wait for a person to open the POC: %v %s %s %+v", err, p.Status, p.Error, p.Pending)
	}
	lead := authz.With(context.Background(), authz.Principal{Subject: "dev", Org: "acme", Roles: []string{"contributor", "tech_lead"}})
	if _, err = e.SubmitForm(lead, p.ID, map[string]any{"timebox": "1 day", "notes": "PSP sandbox"}); err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(context.Background(), p.ID); err != nil || p.Status != engine.StatusCompleted {
		t.Fatalf("the step is done once the POC is open: %v %s %s %+v", err, p.Status, p.Error, p.Pending)
	}
	parent, _ := g.Change(ctx, ch.ID)
	act := actionOf(t, parent, "ACT-1")
	if act.Change == "" || act.Kind != risk.KindPOC || act.Status != risk.ActionOpen {
		t.Fatalf("the action names its sub-change and stays a POC: %+v", act)
	}
	sub, err := g.Change(ctx, domain.ChangeID(act.Change))
	if err != nil {
		t.Fatal(err)
	}
	if sub.ParentID != ch.ID || sub.Methodology != "poc" || sub.Data["criticality"] != "C1" {
		t.Fatalf("sub-change: parent %s, methodology %s, data %v", sub.ParentID, sub.Methodology, sub.Data)
	}

	// the POC runs by itself on its sub-change, up to the confirmation of its probe by a person
	e.Accompany(ctx, engine.TriggerEvent{Type: "change.created", Change: &sub})
	e.Drain()
	run := onlyCompanion(t, e, sub.ID)
	if run.Pending == nil || !strings.Contains(run.Pending.Action, "confirmation") {
		t.Fatalf("the POC waits for its probes to be confirmed: %s %s %+v", run.Status, run.Error, run.Pending)
	}
	if _, err = e.Submit(ctx, run.ID, nil); err != nil {
		t.Fatal(err)
	}
	if run, err = e.Run(context.Background(), run.ID); err != nil || run.Pending == nil || !strings.HasSuffix(run.Pending.Action, "closing") {
		t.Fatalf("the POC builds, checks, judges, then waits for its conclusion: %v %s %s %+v", err, run.Status, run.Error, run.Pending)
	}
	if _, err = e.SubmitForm(ctx, run.ID, map[string]any{"summary": "the PSP splits a payment in three"}); err != nil {
		t.Fatal(err)
	}
	if run, err = e.Run(context.Background(), run.ID); err != nil || run.Status != engine.StatusCompleted {
		t.Fatalf("the POC is concluded: %v %s %s %+v", err, run.Status, run.Error, run.Pending)
	}
	sub, _ = g.Change(ctx, sub.ID)
	var signal []domain.ChangeItem
	for _, it := range sub.Items {
		if it.Type == domain.TypeSignal && it.Target == "poc.concluded" {
			signal = append(signal, it)
		}
	}
	if len(signal) != 1 {
		t.Fatalf("the conclusion raises poc.concluded once: %+v", signal)
	}

	// the event reaches the parent (on.from integrated), which takes the verdict back and closes the sub-change
	e.Accompany(ctx, engine.TriggerEvent{Type: "poc.concluded", Change: &sub, Items: signal})
	e.Drain()
	parent, _ = g.Change(ctx, ch.ID)
	if act = actionOf(t, parent, "ACT-1"); act.Status != risk.ActionDone || !strings.Contains(act.Result, "HYP-1 refuted") {
		t.Fatalf("the action is done with its result: %+v", act)
	}
	var rsk risk.Risk
	for _, r := range risk.Risks(parent) {
		if r.Key == "RSK-1" {
			rsk = r
		}
	}
	if rsk.Status != risk.RiskClosed {
		t.Fatalf("the risk the POC refuted is closed: %+v", rsk)
	}
	if sub, _ = g.Change(ctx, sub.ID); sub.Status != domain.ChangeAbandoned {
		t.Fatalf("the sub-change is closed, landing nothing: %s", sub.Status)
	}
}

func actionOf(t *testing.T, c domain.Change, key string) risk.ActionItem {
	t.Helper()
	for _, a := range risk.Actions(c) {
		if a.Key == key {
			return a
		}
	}
	t.Fatalf("no action %s", key)
	return risk.ActionItem{}
}

func onlyCompanion(t *testing.T, e *engine.Engine, change domain.ChangeID) *engine.Process {
	t.Helper()
	runs := companionOf(t, e, change)
	if len(runs) != 1 {
		t.Fatalf("%d companion runs on %s", len(runs), change)
	}
	return runs[0]
}
