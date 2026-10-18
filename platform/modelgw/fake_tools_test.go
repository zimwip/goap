package modelgw

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/llm"
)

func TestFakeCallsTools(t *testing.T) {
	str := map[string]any{"type": "string"}
	tools := []llm.ToolSpec{
		{Name: "list_agents", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}},
		{Name: "start_agent", InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"methodology": str, "agent": str,
			"newChange": map[string]any{"type": "object", "properties": map[string]any{"title": str, "intent": str}, "required": []string{"title", "intent"}},
		}}},
		{Name: "select_project", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"project": str}, "required": []string{"project"}}},
	}
	complete := func(msgs ...llm.Message) llm.Response {
		t.Helper()
		resp, err := Fake{}.Complete(context.Background(), "echo", llm.Request{Messages: msgs, Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	user := llm.Message{Role: "user", Content: "which agents can I run?"}
	r := complete(user)
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Name != "list_agents" {
		t.Fatalf("listing: %+v", r)
	}
	listed := []llm.Message{user, {Role: "assistant", ToolCalls: r.ToolCalls},
		{Role: "user", ToolResults: []llm.ToolResult{{CallID: "call_1", Name: "list_agents", Content: `{"agents":[{"methodology":"sdlc","agent":"intake","description":"frames a change"}],"project":"PROJ-DEMO"}`}}}}
	r = complete(listed...)
	if len(r.ToolCalls) != 0 || !strings.Contains(r.Text, "- sdlc · intake · frames a change") {
		t.Fatalf("answer: %+v", r)
	}

	// a tool changing something is not called blind: the listing first, then the call with its arguments
	start := llm.Message{Role: "user", Content: "start the intake agent"}
	r = complete(start)
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Name != "list_agents" {
		t.Fatalf("start, first: %+v", r)
	}
	r = complete(append(listed[1:], start, listed[1], listed[2])...)
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Name != "start_agent" {
		t.Fatalf("start, then: %+v", r)
	}
	args := r.ToolCalls[0].Arguments
	nc, _ := args["newChange"].(map[string]any)
	if args["methodology"] != "sdlc" || args["agent"] != "intake" || nc["title"] != start.Content {
		t.Fatalf("start args: %v", args)
	}

	// required arguments from a JSON object of the text; none to find: no call
	r = complete(llm.Message{Role: "user", Content: `select project {"project":"PROJ-DEMO"}`})
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Arguments["project"] != "PROJ-DEMO" {
		t.Fatalf("select: %+v", r)
	}
	if r = complete(llm.Message{Role: "user", Content: "select a project"}); len(r.ToolCalls) != 0 || r.Text != "echo: select a project" {
		t.Fatalf("select blind: %+v", r)
	}

	// JSON requests keep their empty item list
	resp, _ := Fake{}.Complete(context.Background(), "echo", llm.Request{Messages: []llm.Message{user}, Tools: tools, JSON: true})
	if len(resp.ToolCalls) != 0 || !strings.Contains(resp.Text, `"items":[]`) {
		t.Fatalf("json: %+v", resp)
	}
}
