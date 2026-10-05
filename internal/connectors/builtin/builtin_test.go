package builtin_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	enginev1 "github.com/zimwip/goap/gen/goap/engine/v1"
	"github.com/zimwip/goap/internal/connectors/builtin"
	"github.com/zimwip/goap/internal/devseed"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/adapter"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/mcpbuiltin"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

// fakeEngine records the requests of goap-scheduler.
type fakeEngine struct {
	start  *enginev1.StartProcessRequest
	header http.Header
}

func (f *fakeEngine) StartProcess(_ context.Context, r *connect.Request[enginev1.StartProcessRequest]) (*connect.Response[enginev1.StartProcessResponse], error) {
	f.start, f.header = r.Msg, r.Header()
	return connect.NewResponse(&enginev1.StartProcessResponse{Process: &enginev1.Process{Id: "P9", Status: "running", Methodology: r.Msg.Methodology,
		Steps: []*enginev1.Step{{Action: "a"}}}}), nil
}

func (f *fakeEngine) AttachChange(_ context.Context, r *connect.Request[enginev1.AttachChangeRequest]) (*connect.Response[enginev1.AttachChangeResponse], error) {
	return connect.NewResponse(&enginev1.AttachChangeResponse{Process: &enginev1.Process{Id: r.Msg.ProcessId, Status: "running", ChangeId: r.Msg.ChangeId}}), nil
}

func (f *fakeEngine) GetProcess(_ context.Context, r *connect.Request[enginev1.GetProcessRequest]) (*connect.Response[enginev1.GetProcessResponse], error) {
	return connect.NewResponse(&enginev1.GetProcessResponse{Process: &enginev1.Process{Id: r.Msg.Id, Status: "completed", Steps: []*enginev1.Step{{Action: "a"}}}}), nil
}

func (f *fakeEngine) ListProcesses(context.Context, *connect.Request[enginev1.ListProcessesRequest]) (*connect.Response[enginev1.ListProcessesResponse], error) {
	return connect.NewResponse(&enginev1.ListProcessesResponse{Processes: []*enginev1.Process{{Id: "P1"}, {Id: "P2"}}}), nil
}

func (f *fakeEngine) ListTriggers(context.Context, *connect.Request[enginev1.ListTriggersRequest]) (*connect.Response[enginev1.ListTriggersResponse], error) {
	return connect.NewResponse(&enginev1.ListTriggersResponse{}), nil
}

func (f *fakeEngine) FireTrigger(_ context.Context, r *connect.Request[enginev1.FireTriggerRequest]) (*connect.Response[enginev1.FireTriggerResponse], error) {
	return connect.NewResponse(&enginev1.FireTriggerResponse{Process: &enginev1.Process{Id: "P3", Trigger: r.Msg.Methodology + "/" + r.Msg.Agent + "/" + r.Msg.Trigger}}), nil
}

type fakeRegistry struct{ domains []*def.Domain }

func (fakeRegistry) List(context.Context) ([]*methodology.Compiled, error) {
	return []*methodology.Compiled{{Methodology: &methodology.Methodology{Name: "impact-analysis", Namespace: "alm"}}}, nil
}
func (f fakeRegistry) Domains(context.Context) ([]*def.Domain, error) {
	return f.domains, nil
}

type platform struct {
	g      *graph.Graph
	hub    *mcpsvc.Service
	engine *fakeEngine
}

// newPlatform is the demo graph (ALM repository and its organisation) with the defaults and the
// built-in MCPs, and a hub serving the built-in connectors with the default policies.
func newPlatform(t *testing.T) platform {
	t.Helper()
	ctx := context.Background()
	ds, err := def.LoadDomains("../../../domains")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
	for _, seed := range []func(context.Context, *graph.Graph) (bool, error){devseed.Demo, graphsvc.SeedAccess, devseed.DocumentRepository, graphsvc.SeedBuiltins} {
		if _, err := seed(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	az, err := access.NewAuthorizer(&access.Directory{Graph: g})
	if err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{}
	hub := &mcpsvc.Service{Store: mcpsvc.NewMemoryStore(), Directory: &mcpsvc.Directory{Graph: g}}
	cs := builtin.Connectors(builtin.Ports{Graph: g, Engine: eng, Hub: hub, Registry: fakeRegistry{domains: ds}, Authz: az, Floor: az.Floor()})
	if len(cs) != 4 {
		t.Fatalf("%d built-in connectors", len(cs))
	}
	hub.Invoker = mcpsvc.InprocInvoker{Connectors: cs}
	hub.KeepRegistered(t.Context(), cs)
	return platform{g: g, hub: hub, engine: eng}
}

func as(subject, org string, roles ...string) context.Context {
	return authz.With(context.Background(), authz.Principal{Subject: subject, Org: org, Roles: roles})
}

func (p platform) call(t *testing.T, ctx context.Context, unit, tool string, args map[string]any) map[string]any {
	t.Helper()
	out, err := p.hub.Call(ctx, unit, tool, args)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return out
}

func keys(list any) []string {
	var out []string
	for _, v := range list.([]any) {
		out = append(out, v.(map[string]any)["key"].(string))
	}
	return out
}

// The operations of each built-in connector are the tools of the built-in MCP of the same name.
func TestConnectorsImplementTheBuiltinMCPs(t *testing.T) {
	cs := builtin.Connectors(builtin.Ports{Graph: &graph.Graph{}, Engine: &fakeEngine{}, Hub: &mcpsvc.Service{}, Registry: fakeRegistry{}})
	for _, d := range mcpbuiltin.Defs() {
		c, ok := cs[d.Name]
		if !ok || c.Info().Id != d.Name {
			t.Fatalf("no connector %s", d.Name)
		}
		var ops, tools []string
		for _, o := range c.Info().Operations {
			ops = append(ops, o.Name)
		}
		for _, tl := range d.Tools {
			tools = append(tools, tl.Name)
		}
		if !slices.Equal(ops, tools) {
			t.Fatalf("%s: operations %v, tools %v", d.Name, ops, tools)
		}
	}
}

// Every unit can use the built-in MCPs through the default organisation.
func TestEveryUnitGetsTheBuiltins(t *testing.T) {
	p := newPlatform(t)
	for _, unit := range []string{"ORG-CHECKOUT", "ORG-ACME", "", "ORG-UNKNOWN"} {
		_, mcps, err := p.hub.Tools(context.Background(), unit)
		if err != nil || !slices.Equal(mcps, []string{mcpbuiltin.Admin, mcpbuiltin.Change, mcpbuiltin.Graph, mcpbuiltin.Scheduler}) {
			t.Fatalf("%q: mcps %v, %v", unit, mcps, err)
		}
	}
	tools, _, _ := p.hub.Tools(context.Background(), "ORG-CHECKOUT")
	for _, tl := range tools {
		if m, _, _ := mcp.SplitTool(tl.Name); (m == mcpbuiltin.Scheduler) != (tl.Scope == mcp.ScopeAgent) {
			t.Fatalf("%s has scope %s", tl.Name, tl.Scope)
		}
	}
	if again, err := graphsvc.SeedBuiltins(context.Background(), p.g); err != nil || again {
		t.Fatalf("seeding the built-ins twice: %v %v", again, err)
	}
}

func TestGraphTools(t *testing.T) {
	p := newPlatform(t)
	ctx := as("alice", "ORG-CHECKOUT", "contributor")
	out := p.call(t, ctx, "ORG-CHECKOUT", "goap-graph/read", map[string]any{"namespace": "alm", "key": "REQ-1"})
	node := out["node"].(map[string]any)
	if node["type"] != "alm@Requirement" || !strings.Contains(node["properties"].(map[string]any)["title"].(string), "PSP") {
		t.Fatalf("read = %v", out)
	}
	if in := out["in"].([]any); len(in) == 0 {
		t.Fatalf("REQ-1 has incoming links (verifies, realizes): %v", out)
	}
	if got := keys(p.call(t, ctx, "", "goap-graph/glob", map[string]any{"namespace": "alm", "pattern": "REQ-*"})["nodes"]); !slices.Equal(got, []string{"REQ-1", "REQ-2", "REQ-3", "REQ-4"}) {
		t.Fatalf("glob = %v", got)
	}
	if got := keys(p.call(t, ctx, "", "goap-graph/glob", map[string]any{"namespace": "alm", "type": "alm@Environment", "limit": float64(2)})["nodes"]); len(got) != 2 {
		t.Fatalf("glob limited = %v", got)
	}
	if got := keys(p.call(t, ctx, "", "goap-graph/grep", map[string]any{"namespace": "alm", "pattern": "psp", "ignoreCase": true})["nodes"]); !slices.Equal(got, []string{"REQ-1", "REQ-2"}) {
		t.Fatalf("grep = %v", got)
	}
	links := p.call(t, ctx, "", "goap-graph/links", map[string]any{"namespace": "alm", "key": "APP-1", "direction": "out", "type": "alm@composed_of"})
	if out := links["out"].([]any); len(out) != 2 || len(links["in"].([]any)) != 0 {
		t.Fatalf("links = %v", links)
	}
	if bs := p.call(t, ctx, "", "goap-graph/baselines", map[string]any{"namespace": "alm"}); bs["head"] == nil || len(bs["baselines"].([]any)) == 0 {
		t.Fatalf("baselines = %v", bs)
	}
	// the platform APIs act for a caller
	var te *mcpsvc.ToolError
	if _, err := p.hub.Call(context.Background(), "", "goap-graph/read", map[string]any{"namespace": "alm", "key": "REQ-1"}); !errors.As(err, &te) {
		t.Fatalf("anonymous read: %v", err)
	}
}

func TestChangeTools(t *testing.T) {
	p := newPlatform(t)
	ctx := as("alice", "ORG-CHECKOUT", "contributor")
	created := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/create", map[string]any{"title": "Refunds by voucher", "intent": "Customers may be refunded by voucher",
		"namespace": "alm", "methodology": "sdlc"})["change"].(map[string]any)
	id := created["id"].(string)
	if created["unit"] != "ORG-CHECKOUT" || created["namespace"] != "alm" || created["methodology"] != "sdlc" {
		t.Fatalf("change = %v", created)
	}
	// the tools work on the change of the calling process by default
	ctx = mcp.WithCall(ctx, mcp.CallContext{Change: id, Process: "P1"})
	p.call(t, ctx, "ORG-CHECKOUT", "goap-change/write", map[string]any{"key": "REQ-9", "type": "alm@Requirement",
		"properties": map[string]any{"title": "Refund by voucher", "priority": "medium"}, "rationale": "new refund mode"})
	if _, err := p.hub.Call(ctx, "ORG-CHECKOUT", "goap-change/write", map[string]any{"key": "REQ-1", "type": "alm@Requirement", "rationale": "again"}); err == nil {
		t.Fatal("write of an existing node accepted")
	}
	// REQ-2 starts proposed (not editable): reopen it to draft before editing its properties
	p.call(t, ctx, "ORG-CHECKOUT", "goap-change/edit", map[string]any{"key": "REQ-2", "state": "draft", "rationale": "reopen for refunds"})
	// edit guards against a concurrent edit
	if _, err := p.hub.Call(ctx, "ORG-CHECKOUT", "goap-change/edit", map[string]any{"key": "REQ-2", "properties": map[string]any{"priority": "high"},
		"expect": map[string]any{"priority": "low"}, "rationale": "refunds matter"}); err == nil || !strings.Contains(err.Error(), "expects") {
		t.Fatalf("stale edit: %v", err)
	}
	p.call(t, ctx, "ORG-CHECKOUT", "goap-change/edit", map[string]any{"key": "REQ-2", "properties": map[string]any{"priority": "high"},
		"expect": map[string]any{"priority": "medium"}, "rationale": "refunds matter"})
	p.call(t, ctx, "ORG-CHECKOUT", "goap-change/link", map[string]any{"from": "REQ-9", "type": "alm@satisfies", "to": "NEED-2"})
	p.call(t, ctx, "ORG-CHECKOUT", "goap-change/note", map[string]any{"text": "voucher refunds need finance approval", "type": "question"})

	read := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/read", nil)
	nodes := read["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("nodes = %v", nodes)
	}
	for _, n := range nodes {
		n := n.(map[string]any)
		switch n["key"] {
		case "REQ-9":
			if n["intent"] != "created" || n["properties"].(map[string]any)["title"] != "Refund by voucher" {
				t.Fatalf("REQ-9 = %v", n)
			}
		case "REQ-2":
			if n["intent"] != "modified" || n["properties"].(map[string]any)["priority"] != "high" {
				t.Fatalf("REQ-2 = %v", n)
			}
		}
	}
	if items := read["items"].([]any); len(items) != 1 || items[0].(map[string]any)["data"].(map[string]any)["text"] == nil {
		t.Fatalf("items = %v", items)
	}
	bb, err := p.g.Blackboard(context.Background(), domain.ChangeID(id))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range bb.Change.Nodes {
		if n.Key == "REQ-9" && len(bb.Nodes[*n.Post].Out) != 1 {
			t.Fatalf("REQ-9 links = %+v", bb.Nodes[*n.Post].Out)
		}
	}
	if _, err := p.hub.Call(ctx, "ORG-CHECKOUT", "goap-change/validate", nil); err != nil {
		t.Fatal(err)
	}
	// main is untouched until the change is applied
	if n, err := p.g.NodeByKey(context.Background(), "alm", "REQ-9"); err == nil && n.Branch == domain.MainBranch {
		t.Fatal("the change wrote on main")
	}
	// risks and actions (ADR 0036 §1): raised, updated as new versions, traced; the brief tells it all in a few lines
	r1 := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/risk", map[string]any{"title": "Finance rejects voucher refunds", "probability": 3.0, "impact": 4.0, "owner": "product_owner"})
	if r1["risk"].(map[string]any)["data"].(map[string]any)["key"] != "RSK-1" {
		t.Fatalf("risk = %v", r1)
	}
	p.call(t, ctx, "ORG-CHECKOUT", "goap-change/action", map[string]any{"title": "Get the finance approval", "owner": "business_analyst", "for": "RSK-1"})
	p.call(t, ctx, "ORG-CHECKOUT", "goap-change/risk", map[string]any{"key": "RSK-1", "status": "mitigating", "actions": []any{"ACT-1"}})
	if _, err := p.hub.Call(ctx, "ORG-CHECKOUT", "goap-change/risk", map[string]any{"probability": 2.0}); err == nil {
		t.Fatal("a new risk without a title")
	}
	reg := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/risks", nil)
	risks := reg["risks"].([]any)
	if len(risks) != 1 {
		t.Fatalf("register = %v", reg)
	}
	r := risks[0].(map[string]any)
	if r["status"] != "mitigating" || r["title"] != "Finance rejects voucher refunds" || r["probability"] != 3.0 || r["versions"] != 2.0 {
		t.Fatalf("a new version keeps what it does not restate: %v", r)
	}
	// derogations (ADR 0075 §2): signed by the caller, versioned by key, listed
	dctx := authz.With(ctx, authz.Principal{Subject: "alice", Roles: []string{"admin"}})
	exp := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := p.hub.Call(dctx, "ORG-CHECKOUT", "goap-change/derogation", map[string]any{"rule": "tests pass", "target": "REQ-9", "reason": "deadline"}); err == nil {
		t.Fatal("a derogation without an expiry")
	}
	d1 := p.call(t, dctx, "ORG-CHECKOUT", "goap-change/derogation", map[string]any{"rule": "tests pass", "target": "REQ-9", "reason": "deadline", "expires": exp})
	if d1["derogation"].(map[string]any)["data"].(map[string]any)["key"] != "DRG-1" {
		t.Fatalf("derogation = %v", d1)
	}
	p.call(t, dctx, "ORG-CHECKOUT", "goap-change/derogation", map[string]any{"key": "DRG-1", "status": "closed"})
	drg := p.call(t, dctx, "ORG-CHECKOUT", "goap-change/derogations", nil)["derogations"].([]any)
	if len(drg) != 1 || drg[0].(map[string]any)["status"] != "closed" || drg[0].(map[string]any)["signatory"] != "alice" || drg[0].(map[string]any)["versions"] != 2.0 {
		t.Fatalf("derogations = %v", drg)
	}
	brief := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/brief", nil)["brief"].(string)
	for _, want := range []string{"INTENT Customers may be refunded by voucher", "- REQ-9 Requirement created written", "- RSK-1 12=3x4 mitigating product_owner", "ACTIONS 1 open", "- ACT-1 business_analyst - RSK-1"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief lacks %q:\n%s", want, brief)
		}
	}
	trace := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/trace", map[string]any{"ref": "RSK-1"})["trace"].(string)
	if strings.Count(trace, "ITEM ") != 2 {
		t.Fatalf("the trace of a risk shows its versions:\n%s", trace)
	}
	if trace := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/trace", map[string]any{"ref": "REQ-2"})["trace"].(string); !strings.Contains(trace, "NODE REQ-2") {
		t.Fatalf("trace of a node:\n%s", trace)
	}
}

// Options through goap-change (ADR 0032 §6): an agent opens two hypotheses, works on each in turn (the edits go to
// the active option), and compares them.
func TestChangeOptionTools(t *testing.T) {
	p := newPlatform(t)
	ctx := as("alice", "ORG-CHECKOUT", "contributor")
	id := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/create", map[string]any{"title": "Refunds", "intent": "Refund faster",
		"namespace": "alm", "methodology": "sdlc"})["change"].(map[string]any)["id"].(string)
	ctx = mcp.WithCall(ctx, mcp.CallContext{Change: id, Process: "P1"})
	a := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/option", map[string]any{"name": "high", "hypothesis": "refunds are urgent", "activate": true})["option"].(map[string]any)
	// REQ-2 starts proposed (not editable): reopen it to draft before editing its properties, in each option
	p.call(t, ctx, "ORG-CHECKOUT", "goap-change/edit", map[string]any{"key": "REQ-2", "state": "draft", "rationale": "reopen for refunds"})
	p.call(t, ctx, "ORG-CHECKOUT", "goap-change/edit", map[string]any{"key": "REQ-2", "properties": map[string]any{"priority": "high"}, "rationale": "urgent"})
	b := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/option", map[string]any{"name": "low", "hypothesis": "refunds can wait"})["option"].(map[string]any)
	if act := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/activate", map[string]any{"option": b["id"]}); act["active"] != b["id"] {
		t.Fatalf("activate = %v", act)
	}
	p.call(t, ctx, "ORG-CHECKOUT", "goap-change/edit", map[string]any{"key": "REQ-2", "state": "draft", "rationale": "reopen for refunds"})
	p.call(t, ctx, "ORG-CHECKOUT", "goap-change/edit", map[string]any{"key": "REQ-2", "properties": map[string]any{"priority": "low"}, "rationale": "can wait"})
	list := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/options", nil)
	if list["active"] != b["id"] || len(list["options"].([]any)) != 2 {
		t.Fatalf("options = %v", list)
	}
	cmp := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/compare", nil)
	nodes := cmp["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("compare = %v", cmp)
	}
	props := nodes[0].(map[string]any)["props"].(map[string]any)
	if props[a["id"].(string)].(map[string]any)["priority"] != "high" || props[b["id"].(string)].(map[string]any)["priority"] != "low" ||
		props["main"].(map[string]any)["priority"] != "medium" {
		t.Fatalf("compared properties = %v", props)
	}
	p.call(t, ctx, "ORG-CHECKOUT", "goap-change/evaluate", map[string]any{"option": a["id"], "comment": "finance agrees"})
	if act := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/activate", map[string]any{"option": "main"}); act["active"] != "main" {
		t.Fatalf("back to main = %v", act)
	}
	// the main flow did not see the options' edits
	if nodes := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/read", nil)["nodes"].([]any); len(nodes) != 0 {
		t.Fatalf("main flow nodes = %v", nodes)
	}
	// a decision point on the two options (ADR 0009 §4): undecidable, answered, then decided above the threshold
	point := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/decision", map[string]any{"question": "How urgent are refunds?", "criteria": []any{"cost"}})["point"].(map[string]any)
	if point["status"] != "open" || len(point["options"].([]any)) != 2 {
		t.Fatalf("decision = %v", point)
	}
	ruled := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/rule", map[string]any{"outcome": "undecidable", "justification": "cost unknown",
		"questions": []any{"What does an urgent refund cost?"}})["point"].(map[string]any)
	q := ruled["questions"].([]any)[0].(map[string]any)
	if ruled["status"] != "blocked" {
		t.Fatalf("undecidable = %v", ruled)
	}
	p.call(t, ctx, "ORG-CHECKOUT", "goap-change/answer", map[string]any{"question": q["id"], "answer": "2 EUR"})
	// the edit of the option is reviewed and checked in: a flow is adopted with its versions checked in (ADR 0076)
	c, err := p.g.Change(ctx, domain.ChangeID(id))
	if err != nil {
		t.Fatal(err)
	}
	for _, cn := range c.Nodes {
		if cn.Key == "REQ-2" && cn.Flow == a["id"] {
			if _, err := p.g.ReviewNodeOn(ctx, c.ID, cn.Flow, "", cn.ID, domain.ReviewAccepted, "reviewer", "agreed"); err != nil {
				t.Fatal(err)
			}
			if _, err := p.g.CheckinNode(ctx, c.ID, cn.ID, cn.Flow, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	decided := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/rule", map[string]any{"outcome": "decided", "option": "high", "confidence": 0.95,
		"justification": "cheap enough"})["point"].(map[string]any)
	if decided["status"] != "decided" || decided["option"] != a["id"] {
		t.Fatalf("decided = %v", decided)
	}
	if pts := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/decisions", nil)["points"].([]any); len(pts) != 1 {
		t.Fatalf("decisions = %v", pts)
	}
	if list := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/options", nil); list["options"].([]any)[0].(map[string]any)["status"] != "selected" {
		t.Fatalf("the decision selects its option: %v", list)
	}
}

func TestChangeSignal(t *testing.T) {
	p := newPlatform(t)
	ctx := as("alice", "ORG-CHECKOUT", "contributor")
	created := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/create", map[string]any{"title": "Refunds by voucher", "intent": "Customers may be refunded by voucher",
		"namespace": "alm", "methodology": "sdlc"})["change"].(map[string]any)
	id := created["id"].(string)
	ctx = mcp.WithCall(ctx, mcp.CallContext{Change: id, Process: "P1"})

	out := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/signal", map[string]any{"type": "review_needed",
		"data": map[string]any{"reason": "voucher policy"}, "target": "P0"})
	item := out["item"].(map[string]any)
	if item["kind"] != "signal" || item["type"] != "review_needed" || item["target"] != "P0" || item["producedBy"] != "P1" {
		t.Fatalf("item = %v", item)
	}
	if data := item["data"].(map[string]any); data["reason"] != "voucher policy" {
		t.Fatalf("data = %v", data)
	}

	if _, err := p.hub.Call(ctx, "ORG-CHECKOUT", "goap-change/signal", map[string]any{"data": map[string]any{}}); err == nil {
		t.Fatal("signal without a type accepted")
	}
}

// A request either continues an open change or needs a new one: goap-change.list finds the
// candidates, filtered by namespace, status and unit, the latest first, capped by limit.
func TestChangeList(t *testing.T) {
	p := newPlatform(t)
	ctx := as("alice", "ORG-CHECKOUT", "contributor")
	almID := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/create", map[string]any{"title": "Refunds by voucher", "intent": "refund by voucher",
		"namespace": "alm", "methodology": "sdlc"})["change"].(map[string]any)["id"].(string)
	orgID := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/create", map[string]any{"title": "Reorg checkout", "intent": "reorg checkout",
		"namespace": "organisation"})["change"].(map[string]any)["id"].(string)

	all := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/list", nil)["changes"].([]any)
	var ids []string
	for _, c := range all {
		ids = append(ids, c.(map[string]any)["id"].(string))
	}
	if !slices.Contains(ids, almID) || !slices.Contains(ids, orgID) {
		t.Fatalf("list = %v, want %s and %s", ids, almID, orgID)
	}

	almOnly := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/list", map[string]any{"namespace": "alm"})["changes"].([]any)
	for _, c := range almOnly {
		if c.(map[string]any)["namespace"] != "alm" {
			t.Fatalf("namespace filter leaked: %v", c)
		}
	}
	var sawAlm bool
	for _, c := range almOnly {
		if c.(map[string]any)["id"] == almID {
			sawAlm = true
		}
	}
	if !sawAlm {
		t.Fatalf("namespace filter dropped %s: %v", almID, almOnly)
	}

	none := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/list", map[string]any{"status": "applied"})["changes"].([]any)
	for _, c := range none {
		if c.(map[string]any)["id"] == almID || c.(map[string]any)["id"] == orgID {
			t.Fatalf("status filter leaked a draft change: %v", c)
		}
	}

	capped := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/list", map[string]any{"limit": float64(1)})
	if changes := capped["changes"].([]any); len(changes) != 1 || capped["truncated"] != true {
		t.Fatalf("limit = %v, truncated = %v", changes, capped["truncated"])
	}
}

// Reformulating a change never erases its definition: the previous title/intent is superseded as
// an "intent" item (the one set at creation included), the header always holding the current one.
func TestChangeReformulate(t *testing.T) {
	p := newPlatform(t)
	ctx := as("alice", "ORG-CHECKOUT", "contributor")
	created := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/create", map[string]any{"title": "Refunds", "intent": "refund by voucher",
		"namespace": "alm", "methodology": "sdlc"})["change"].(map[string]any)
	id := created["id"].(string)
	ctx = mcp.WithCall(ctx, mcp.CallContext{Change: id, Process: "P1"})

	out := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/reformulate", map[string]any{"title": "Refunds by voucher",
		"intent": "customers may be refunded by voucher up to 30 days after purchase", "rationale": "clarified with support"})
	ch := out["change"].(map[string]any)
	if ch["title"] != "Refunds by voucher" || ch["intent"] != "customers may be refunded by voucher up to 30 days after purchase" {
		t.Fatalf("change after reformulate = %v", ch)
	}
	item1 := out["item"].(map[string]any)

	out2 := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/reformulate", map[string]any{
		"intent": "customers may be refunded by voucher or bank transfer up to 30 days after purchase", "rationale": "finance asked for a bank transfer option"})
	ch2 := out2["change"].(map[string]any)
	if ch2["title"] != "Refunds by voucher" || ch2["intent"] != "customers may be refunded by voucher or bank transfer up to 30 days after purchase" {
		t.Fatalf("change after 2nd reformulate = %v", ch2)
	}
	item2 := out2["item"].(map[string]any)
	if s := item2["supersedes"].([]any); len(s) != 1 || s[0] != item1["id"] {
		t.Fatalf("item2 supersedes = %v, want [%v]", s, item1["id"])
	}

	read := p.call(t, ctx, "ORG-CHECKOUT", "goap-change/read", nil)
	items := read["items"].([]any)
	var intents []map[string]any
	for _, it := range items {
		it := it.(map[string]any)
		if it["type"] == "intent" {
			intents = append(intents, it)
		}
	}
	if len(intents) != 3 {
		t.Fatalf("intent items = %v, want 3 (original snapshot + 2 reformulations)", intents)
	}
	if orig := intents[0]["data"].(map[string]any); orig["title"] != "Refunds" || orig["intent"] != "refund by voucher" {
		t.Fatalf("original snapshot lost: %v", orig)
	}
}

// User and Policy nodes stay behind the access gate of the platform (ADR 0020).
func TestChangeToolsKeepTheAccessGate(t *testing.T) {
	p := newPlatform(t)
	for _, c := range []struct {
		who    context.Context
		denied bool
	}{
		{as("alice", "ORG-CHECKOUT", "contributor"), true},
		{as("root", "ORG-CHECKOUT", "admin"), false},
	} {
		ch := p.call(t, c.who, "ORG-CHECKOUT", "goap-change/create", map[string]any{"title": "Access", "intent": "a new user", "namespace": "organisation"})["change"].(map[string]any)
		_, err := p.hub.Call(c.who, "ORG-CHECKOUT", "goap-change/write", map[string]any{"change": ch["id"], "key": access.UserKey("bob"), "type": access.NodeTypeUser,
			"properties": map[string]any{"subject": "bob", "roles": []any{"admin"}}, "rationale": "promote bob"})
		if denied := err != nil && strings.Contains(err.Error(), "may not write policy"); denied != c.denied {
			t.Fatalf("%v: %v", authz.From(c.who), err)
		}
	}
}

// Every adminOnly node type (ADR 0068) stays behind the gate through write, edit, link and unlink: a project
// member could otherwise write an organisation@Assignment granting admin through the in-process connector.
func TestChangeToolsGateEveryAccessType(t *testing.T) {
	p := newPlatform(t)
	ctx := context.Background()
	if err := graphsvc.SeedAdapter(ctx, p.g, adapter.Instance{Unit: "ORG-CRM", MCP: mcpbuiltin.Change}); err != nil {
		t.Fatal(err)
	}
	asgKey := access.PlatformAssignmentKey("ORG-CHECKOUT")
	_, err := p.g.Commit(ctx, graph.Commit{Namespace: access.NamespaceOrganisation, Title: "Assignment", Intent: "Assignment", By: "test", Edits: []graph.NodeEdit{{
		Key: asgKey, Type: access.NodeTypeAssignment, Props: access.Assignment{Roles: []string{access.RoleReader}}.Props(), Rationale: "seed",
		Links: []graph.LinkEdit{{Type: access.LinkAssignsOrg, ToKey: "ORG-CHECKOUT"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	existing := map[string]string{asgKey: access.NodeTypeAssignment, "ORG-CHECKOUT": access.NodeTypeOrgUnit, access.DefaultProject: access.NodeTypeProjectUnit, adapter.Key("ORG-CRM", mcpbuiltin.Change): domain.TypeAdapter}
	denied := func(err error) bool { return err != nil && strings.Contains(err.Error(), "may not write policy") }
	alice := as("alice", "ORG-CHECKOUT", "contributor")
	open := func() any {
		return p.call(t, alice, "ORG-CHECKOUT", "goap-change/create", map[string]any{"title": "Access", "intent": "grant", "namespace": "organisation"})["change"].(map[string]any)["id"]
	}
	for key, typ := range existing {
		for op, args := range map[string]map[string]any{
			"write":  {"key": key + "-new", "type": typ, "properties": map[string]any{}, "rationale": "x"},
			"edit":   {"key": key, "properties": map[string]any{"description": "x"}, "rationale": "x"},
			"link":   {"from": key, "type": access.LinkAssignsOrg, "to": "ORG-DEFAULT"},
			"unlink": {"from": key, "type": access.LinkAssignsOrg, "to": "ORG-DEFAULT"},
		} {
			args["change"] = open()
			if _, err := p.hub.Call(alice, "ORG-CHECKOUT", "goap-change/"+op, args); !denied(err) {
				t.Errorf("%s of %s by a contributor: %v", op, typ, err)
			}
		}
	}
	// an administrator passes the gate (the write itself may still fail on other grounds)
	root := as("root", "ORG-CHECKOUT", "admin")
	ch := p.call(t, root, "ORG-CHECKOUT", "goap-change/create", map[string]any{"title": "Access", "intent": "grant", "namespace": "organisation"})["change"].(map[string]any)["id"]
	if _, err := p.hub.Call(root, "ORG-CHECKOUT", "goap-change/edit", map[string]any{"change": ch, "key": asgKey, "properties": map[string]any{"description": "x"}, "rationale": "x"}); denied(err) {
		t.Fatalf("administrator denied: %v", err)
	}
}

// A unit restricts a built-in MCP for itself and its sub-units: goap-change read-only for the CRM team.
func TestUnitRestrictsABuiltin(t *testing.T) {
	p := newPlatform(t)
	if err := graphsvc.SeedAdapter(context.Background(), p.g, adapter.Instance{Unit: "ORG-CRM", MCP: mcpbuiltin.Change, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedAdapter(context.Background(), p.g, adapter.Instance{Unit: "ORG-DIGITAL", MCP: mcpbuiltin.Admin, Disabled: true}); err != nil {
		t.Fatal(err)
	}
	tools, mcps, err := p.hub.Tools(context.Background(), "ORG-CRM")
	if err != nil {
		t.Fatal(err)
	}
	var change []string
	for _, tl := range tools {
		if m, name, _ := mcp.SplitTool(tl.Name); m == mcpbuiltin.Change {
			change = append(change, name)
		}
	}
	if !slices.Equal(change, []string{"read", "list", "validate", "options", "compare", "decisions", "brief", "trace", "risks", "derogations"}) || slices.Contains(mcps, mcpbuiltin.Admin) {
		t.Fatalf("ORG-CRM: goap-change tools %v, mcps %v", change, mcps)
	}
	ctx := as("carol", "ORG-CRM", "contributor")
	if _, err := p.hub.Call(ctx, "ORG-CRM", "goap-change/create", map[string]any{"title": "x", "intent": "y", "namespace": "alm"}); !errors.Is(err, mcpsvc.ErrNotBound) {
		t.Fatalf("restricted tool called: %v", err)
	}
	// siblings keep everything but the admin MCP their direction disabled; the company keeps all
	if _, mcps, _ := p.hub.Tools(context.Background(), "ORG-CHECKOUT"); slices.Contains(mcps, mcpbuiltin.Admin) || !slices.Contains(mcps, mcpbuiltin.Change) {
		t.Fatalf("ORG-CHECKOUT: %v", mcps)
	}
	if _, mcps, _ := p.hub.Tools(context.Background(), "ORG-ACME"); len(mcps) != 4 {
		t.Fatalf("ORG-ACME: %v", mcps)
	}
	// goap-admin describes the restriction
	out := p.call(t, as("root", "ORG-ACME", "admin"), "ORG-ACME", "goap-admin/mcps", map[string]any{"unit": "ORG-CRM"})
	for _, m := range out["mcps"].([]any) {
		m := m.(map[string]any)
		if m["mcp"] == mcpbuiltin.Change && (len(m["tools"].([]any)) != 10 || m["restrictedBy"].([]any)[0] != "ORG-CRM" || m["definedIn"] != access.DefaultOrg) {
			t.Fatalf("goap-change for ORG-CRM = %v", m)
		}
	}
}

func TestSchedulerTools(t *testing.T) {
	p := newPlatform(t)
	ctx := mcp.WithCall(as("alice", "ORG-CHECKOUT", "contributor"), mcp.CallContext{Change: "C1", Process: "P1"})
	out := p.call(t, ctx, "ORG-CHECKOUT", "goap-scheduler/start", map[string]any{"intent": "assess the impact of REQ-1", "methodology": "impact-analysis"})
	if proc := out["process"].(map[string]any); proc["id"] != "P9" || proc["steps"] != nil {
		t.Fatalf("start = %v", out)
	}
	head, err := p.g.BranchHead(context.Background(), "alm", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	// a new change on the head of main of the namespace of the methodology
	if r := p.engine.start; r.OwnerOrg != "ORG-CHECKOUT" || r.Title != "assess the impact of REQ-1" || r.ChangeId != "" || r.BaselineId != string(head.ID) {
		t.Fatalf("start request = %+v", r)
	}
	if _, err := p.hub.Call(as("alice", "ORG-CHECKOUT", "contributor"), "", "goap-scheduler/start", map[string]any{"intent": "anything"}); err == nil {
		t.Fatal("a process started without knowing its namespace")
	}
	if p.engine.header.Get(identity.HeaderSubject) != "alice" || p.engine.header.Get(identity.HeaderRoles) != "contributor" {
		t.Fatalf("the caller is not forwarded: %v", p.engine.header)
	}
	if got := p.call(t, ctx, "", "goap-scheduler/get", map[string]any{"id": "P9"})["process"].(map[string]any); got["steps"] == nil {
		t.Fatalf("get = %v", got)
	}
	if got := p.call(t, ctx, "", "goap-scheduler/list", map[string]any{"limit": float64(1)}); len(got["processes"].([]any)) != 1 || got["truncated"] != true {
		t.Fatalf("list = %v", got)
	}
	if got := p.call(t, ctx, "", "goap-scheduler/fire", map[string]any{"methodology": "m", "agent": "a", "trigger": "t"}); got["process"].(map[string]any)["trigger"] != "m/a/t" {
		t.Fatalf("fire = %v", got)
	}
}

func TestAdminTools(t *testing.T) {
	p := newPlatform(t)
	ctx := as("alice", "ORG-CHECKOUT", "contributor")
	units := p.call(t, ctx, "ORG-CHECKOUT", "goap-admin/units", nil)["units"].([]any)
	parents := map[string]any{}
	for _, u := range units {
		u := u.(map[string]any)
		parents[u["key"].(string)] = u["parent"]
	}
	if parents["ORG-CHECKOUT"] != "ORG-DIGITAL" || parents["ORG-ACME"] != access.DefaultOrg || parents[access.DefaultOrg] != nil {
		t.Fatalf("units = %v", units)
	}
	mcps := p.call(t, ctx, "ORG-CHECKOUT", "goap-admin/mcps", nil)
	if mcps["unit"] != "ORG-CHECKOUT" || len(mcps["mcps"].([]any)) != 4 {
		t.Fatalf("mcps = %v", mcps)
	}
	conns := p.call(t, ctx, "", "goap-admin/connectors", nil)["connectors"].([]any)
	if len(conns) != 4 || conns[0].(map[string]any)["builtin"] != true || conns[0].(map[string]any)["live"] != true {
		t.Fatalf("connectors = %v", conns)
	}
	doms := p.call(t, ctx, "", "goap-admin/domains", nil)["domains"].([]any)
	if len(doms) == 0 {
		t.Fatal("no domain")
	}
	p.call(t, ctx, "", "goap-admin/methodologies", nil)
	p.call(t, ctx, "", "goap-admin/users", map[string]any{"unit": "ORG-CHECKOUT"})
}
