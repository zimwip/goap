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

var job = Job{Action: "a", Items: []Item{
	{ID: "i1", Kind: "impact", Target: &Node{Key: "REQ-1", Type: "Requirement", Props: map[string]any{"title": "Pay"}}},
	{ID: "i2", Kind: "impact", Target: &Node{Key: "TST-1", Type: "TestCase"}},
}}

func TestJavaScript(t *testing.T) {
	j := job
	j.Language = "javascript"
	j.Code = `
function run(ctx) {
  let n = 0;
  for (const i of ctx.impacts()) {
    if (i.target.type !== "Requirement") continue;
    const t = ctx.proposeNode("TestCase", "TST-" + i.target.key, { title: "Verify " + i.target.props.title });
    ctx.proposeLink(t, "verifies", i.target.key);
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
	if len(res.Items) != 2 || res.Output != "1 test(s)" || len(h.prompts) != 1 {
		t.Fatalf("unexpected result %+v", res)
	}
	link := res.Items[1]["proposal"].(map[string]any)["link"].(map[string]any)
	if link["from"] != "#p1" || link["to"] != "REQ-1" {
		t.Fatalf("bad link %v", link)
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
	for _, i := range ctx.Impacts() {
		if i.Target == nil || i.Target.Type != "Requirement" {
			continue
		}
		t := ctx.ProposeNode("TestCase", "TST-"+strings.ToLower(i.Target.Key), map[string]any{"title": "x"})
		ctx.ProposeLink(t, "verifies", i.Target.Key)
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
	if len(res.Items) != 3 || len(res.Logs) != 1 || res.Logs[0].Message != "node Requirement" {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestSandboxingAndErrors(t *testing.T) {
	cases := map[string]Job{
		"go os import": {Language: "go", Code: "package action\nimport \"os\"\nimport \"github.com/zimwip/goap/pkg/dsl\"\nfunc Run(ctx *dsl.Ctx) error { os.Exit(1); return nil }"},
		"js throw":     {Language: "javascript", Code: `ctx.addImpact("REQ-1", "x"); throw new Error("boom")`},
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
		Code: `ctx.addImpact("REQ-1", "x"); ctx.runAgent("slow", "do it")`}, &fakeHost{})
	if err != nil || !res.Suspended || len(res.Items) != 0 {
		t.Fatalf("expected suspension without writes, got %+v %v", res, err)
	}
}
