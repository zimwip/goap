package engine

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/llm"
)

func TestTidyText(t *testing.T) {
	in := "\x1b[32mok\x1b[0m   \r\n" + "progress 10%\rprogress 100%\n" + strings.Repeat("same\n", 6) + "end"
	want := "ok\nprogress 100%\nsame\nsame\n(previous line repeated 4 more times)\nend"
	if got := tidyText(in); got != want {
		t.Fatalf("tidy:\n%q\nwant\n%q", got, want)
	}
	if got := tidyText("plain text"); got != "plain text" {
		t.Fatal(got)
	}
	// blank lines are never counted, two repeats stay
	if got := tidyText("a\na\n\n\n\nb"); got != "a\na\n\n\n\nb" {
		t.Fatalf("%q", got)
	}
	v := map[string]any{"out": "x\x1b[1my", "list": []any{"a\r\n", 3}}
	got := tidy(v).(map[string]any)
	if got["out"] != "xy" || got["list"].([]any)[0] != "a\n" || got["list"].([]any)[1] != 3 || v["out"] != "x\x1b[1my" {
		t.Fatalf("%v (input %v)", got, v)
	}
}

func TestDedupResults(t *testing.T) {
	file1, file2, log := strings.Repeat("v1 ", 300), strings.Repeat("v2 ", 300), strings.Repeat("log ", 300)
	call := func(id, name, path string) llm.Message {
		return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Name: name, Arguments: map[string]any{"path": path}}}}
	}
	result := func(id, content string) llm.Message {
		return llm.Message{Role: "user", ToolResults: []llm.ToolResult{{CallID: id, Content: content}}}
	}
	msgs := []llm.Message{{Role: "user", Content: "the task"},
		call("1", "workspace/read", "a.go"), result("1", file1),
		call("2", "workspace/bash", "x"), result("2", log),
		call("3", "workspace/read", "a.go"), result("3", file2), // read again after an edit
		call("4", "workspace/bash", "y"), result("4", log), // another call, same output
		call("5", "workspace/read", "b.go"), result("5", "short"),
		call("6", "workspace/read", "b.go"), result("6", "short"),
	}
	got := dedupResults(msgs)
	if got[2].ToolResults[0].Content != superseded || got[4].ToolResults[0].Content != repeated {
		t.Fatalf("older results: %.40q / %.40q", got[2].ToolResults[0].Content, got[4].ToolResults[0].Content)
	}
	if got[6].ToolResults[0].Content != file2 || got[8].ToolResults[0].Content != log || got[10].ToolResults[0].Content != "short" {
		t.Fatal("a latest or a short result was replaced")
	}
	if msgs[2].ToolResults[0].Content != file1 || msgs[4].ToolResults[0].Content != log {
		t.Fatal("dedupResults changed its input")
	}
	if again := dedupResults(got); again[2].ToolResults[0].Content != superseded || again[6].ToolResults[0].Content != file2 {
		t.Fatal("not stable")
	}
	// nothing to replace: the same slice
	if plain := msgs[:3]; &dedupResults(plain)[0] != &plain[0] {
		t.Fatal("a conversation without repeats was copied")
	}
}
