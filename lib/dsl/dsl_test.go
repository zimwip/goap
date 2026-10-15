package dsl

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeHost struct{ prompts []string }

func (h *fakeHost) Complete(_ context.Context, r CompleteRequest) (CompleteResult, error) {
	h.prompts = append(h.prompts, r.Prompt)
	return CompleteResult{Text: `ok {"n": 2}`, Model: r.Model, InputTokens: 10, OutputTokens: 3}, nil
}
func (h *fakeHost) RunAgent(_ context.Context, name, _ string) (AgentResult, error) {
	if name == "slow" {
		return AgentResult{Status: "waiting"}, ErrSuspended
	}
	return AgentResult{Status: "completed", ProcessID: "child"}, nil
}
func (h *fakeHost) CallTool(context.Context, string, map[string]any) (any, error) { return "tool", nil }
func (h *fakeHost) Node(_ context.Context, key string) (Node, error) {
	return Node{Key: key, Type: "Requirement", Props: map[string]any{"title": "t"}}, nil
}
func (h *fakeHost) Nodes(context.Context, string) ([]Node, error) {
	return []Node{{Key: "REQ-1", Type: "Requirement"}}, nil
}
func (h *fakeHost) Links(context.Context, string, string, string) ([]Link, error) { return nil, nil }

var job = Job{Action: "a", Nodes: []ChangeImpact{
	{ID: "n1", Key: "REQ-1", Type: "Requirement", Intent: "modified", Planned: true, Pre: &Node{Key: "REQ-1", Type: "Requirement", Props: map[string]any{"title": "Pay"}}},
	{ID: "n2", Key: "TST-1", Type: "TestCase", Intent: "modified", Planned: true, Pre: &Node{Key: "TST-1", Type: "TestCase"}},
}}

func TestJavaScript(t *testing.T) {
	j := job
	j.Language = "javascript"
	j.Code = `
function run(ctx) {
  let n = 0;
  for (const c of ctx.changeImpacts()) {
    if (c.type !== "Requirement") continue;
    const t = ctx.impactNodeCreate("TestCase", "TST-" + c.key, "verifies " + c.key);
    ctx.writeNode(t, { props: { title: "Verify " + c.pre.props.title }, links: [{ type: "verifies", to: c.key }] });
    n++;
  }
  const r = ctx.complete({ prompt: "hello", json: true });
  console.log("tokens", r.inputTokens, r.json.n);
  return n + " test(s)";
}`
	h := &fakeHost{}
	res, err := Run(context.Background(), j, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 2 || res.Output != "1 test(s)" || len(h.prompts) != 1 {
		t.Fatalf("unexpected result %+v", res)
	}
	if res.Nodes[0].Op != "declare" || res.Nodes[1].Op != "write" || res.Nodes[1].Node != res.Nodes[0].Ref || res.Nodes[1].Links[0].To != "REQ-1" {
		t.Fatalf("bad operations %+v", res.Nodes)
	}
	if len(res.Logs) != 1 || res.Logs[0].Message != "tokens102" && !strings.Contains(res.Logs[0].Message, "10") {
		t.Fatalf("logs %+v", res.Logs)
	}
}

func TestGo(t *testing.T) {
	j := job
	j.Language = "go"
	j.Code = `package action

import (
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/dsl"
)

func Run(ctx *dsl.Ctx) error {
	for _, n := range ctx.ChangeImpacts() {
		if n.Type != "Requirement" {
			continue
		}
		t := ctx.ImpactNodeCreate("TestCase", "TST-"+strings.ToLower(n.Key), "verifies")
		ctx.WriteNode(t, map[string]any{"props": map[string]any{"title": "x"}})
	}
	n, err := ctx.Node("REQ-1")
	if err != nil {
		return err
	}
	fmt.Println("node", n.Type)
	ctx.AddArtifact("report", map[string]any{"markdown": "# ok"})
	return nil
}
`
	res, err := Run(context.Background(), j, &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || len(res.Nodes) != 2 || len(res.Logs) != 1 || res.Logs[0].Message != "node Requirement" {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestSandboxingAndErrors(t *testing.T) {
	cases := map[string]Job{
		"go os import": {Language: "go", Code: "package action\nimport \"os\"\nimport \"github.com/zimwip/goap/pkg/dsl\"\nfunc Run(ctx *dsl.Ctx) error { os.Exit(1); return nil }"},
		"js throw":     {Language: "javascript", Code: `ctx.impactNode("REQ-1", "x"); throw new Error("boom")`},
		"js timeout":   {Language: "javascript", Code: `while (true) {}`, Timeout: 200 * time.Millisecond},
		"js no fs":     {Language: "javascript", Code: `require("fs")`},
	}
	for name, j := range cases {
		res, err := Run(context.Background(), j, &fakeHost{})
		if err == nil {
			t.Errorf("%s: expected an error", name)
		}
		if len(res.Items) != 0 {
			t.Errorf("%s: writes must be discarded on error", name)
		}
	}
}

func TestSuspension(t *testing.T) {
	res, err := Run(context.Background(), Job{Language: "javascript",
		Code: `ctx.impactNode("REQ-1", "x"); ctx.runAgent("slow", "do it")`}, &fakeHost{})
	if err != nil || !res.Suspended || len(res.Items) != 0 {
		t.Fatalf("expected suspension without writes, got %+v %v", res, err)
	}
}

func TestGoChangeImpacts(t *testing.T) {
	res, err := Run(context.Background(), Job{Language: "go", Nodes: []ChangeImpact{{Key: "REQ-1", Planned: true}}, Code: `package action

import "github.com/zimwip/goap/pkg/dsl"

func Run(ctx *dsl.Ctx) error {
	for _, n := range ctx.ChangeImpacts() {
		if n.Planned {
			ctx.WriteNode(n.Key, map[string]any{"props": map[string]any{"title": "x"}})
		}
	}
	r := ctx.ImpactNodeCreate("TestCase", "TST-1", "cover")
	ctx.WriteNode(r, map[string]any{"links": []any{map[string]any{"type": "verifies", "to": "REQ-1"}}})
	ctx.ImpactNodeReview(r, true, "ok")
	ctx.ImpactNodeTransition(r, "approved")
	ctx.ImpactNodeCancel("REQ-1")
	return nil
}
`}, &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 6 || res.Nodes[0].Op != "write" || res.Nodes[0].Props["title"] != "x" ||
		res.Nodes[2].Links[0].To != "REQ-1" || res.Nodes[3].Comment != "ok" || !res.Nodes[3].Accept ||
		res.Nodes[4].Op != "transition" || res.Nodes[4].State != "approved" || res.Nodes[5].Op != "cancel" {
		t.Fatalf("unexpected operations %+v", res.Nodes)
	}
}
