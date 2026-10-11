package assistantsvc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/pkg/llm"
)

func TestScreenToolsAreDeclaredWithTheirSchema(t *testing.T) {
	spec := uiToolSpec(filterTool())
	b, _ := json.Marshal(spec.InputSchema)
	for _, want := range []string{`"status":{"enum":["open","closed"],"type":"string"}`, `"tags":{"items":{"type":"string"},"type":"array"}`,
		`"limit":{"type":"number"}`, `"rationale":{`, `"required":["status"]`, `"additionalProperties":false`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("schema lacks %s: %s", want, b)
		}
	}
	if spec.Name != "ui.filter_list" || !strings.Contains(spec.Description, "Required to feed it: give the status") || !strings.Contains(spec.Description, "effect") {
		t.Fatalf("%+v", spec)
	}
	if w := uiToolSpec(setFieldTool()); !strings.Contains(w.Description, "only proposed") {
		t.Fatalf("%s", w.Description)
	}
}

func TestNativeToolCallsAndResults(t *testing.T) {
	e := newEnv(t)
	e.model.responses = []llm.Response{
		{Text: "Let me look.", StopReason: llm.StopToolUse, ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: ToolListMethodologies, Arguments: map[string]any{}},
			{ID: "c2", Name: "ui.set_field", Arguments: map[string]any{"value": "New title", "rationale": "you asked for it"}},
			{ID: "c3", Name: "delete_everything"},
		}},
		{Text: "I propose the new title."},
	}
	if err := e.sendTools("rename it", Context{}, []UITool{setFieldTool()}); err != nil {
		t.Fatal(err)
	}
	first, second := e.model.got[0], e.model.got[1]
	if first.JSON || len(first.Tools) != len(Tools())+1 || first.Tools[len(first.Tools)-1].Name != "ui.set_field" ||
		strings.Contains(first.System, `"args"`) {
		t.Fatalf("first request: json %v, %d tools", first.JSON, len(first.Tools))
	}
	n := len(second.Messages)
	call, res := second.Messages[n-2], second.Messages[n-1]
	if call.Role != "assistant" || len(call.ToolCalls) != 3 || call.Content != "Let me look." {
		t.Fatalf("assistant turn %+v", call)
	}
	r := res.ToolResults
	if len(r) != 3 || r[0].CallID != "c1" || r[0].IsError || !strings.Contains(r[0].Content, "sdlc") ||
		r[1].CallID != "c2" || r[1].IsError || r[2].CallID != "c3" || !r[2].IsError || !strings.Contains(r[2].Content, "unknown tool") {
		t.Fatalf("results %+v", r)
	}
	a := e.answer(t)
	if a.Status != convsvc.StatusDone || a.Text != "I propose the new title." || len(a.Actions) != 1 {
		t.Fatalf("answer %+v", a)
	}
	// the rationale travelled as an argument: it is the proposal's, not an argument of the screen tool
	act := a.Actions[0]
	if act["rationale"] != "you asked for it" || act["status"] != "proposed" {
		t.Fatalf("action %+v", act)
	}
	if args, _ := act["args"].(map[string]any); args["value"] != "New title" || args["rationale"] != nil {
		t.Fatalf("args %+v", act["args"])
	}
}

func TestNativeSettlingCallsEndTheTurn(t *testing.T) {
	e := newEnv(t)
	e.model.responses = []llm.Response{{Text: "Opening it.", ToolCalls: []llm.ToolCall{{ID: "c1", Name: ToolOpenChange, Arguments: map[string]any{"changeId": "CHG-1"}}}}}
	if _, _, err := e.send("open CHG-1", Context{}); err != nil {
		t.Fatal(err)
	}
	if a := e.answer(t); len(e.model.got) != 1 || a.Text != "Opening it." || len(a.Actions) != 1 {
		t.Fatalf("%d calls, answer %+v", len(e.model.got), a)
	}
}
