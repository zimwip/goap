package engine

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

// docsWithLoop is docsAsLLM with a loop budget on the fetch action (ADR 0118).
func docsWithLoop(l methodology.Loop) func(*methodology.Methodology) {
	return func(m *methodology.Methodology) {
		docsAsLLM(m)
		for i := range m.Actions {
			if m.Actions[i].Name == "fetch_doc" {
				m.Actions[i].Loop = &l
			}
		}
	}
}

const askRead = `{"tool_calls":[{"name":"document-repository/read","arguments":{"path":"spec.txt"}}]}`

// A budget allows more rounds than the default; once spent, the model is asked once for its final answer, with no
// tool, instead of the action failing.
func TestLoopBudgetRoundsThenFinalAnswer(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}}
	var mu sync.Mutex
	var reqs []llm.Request
	client := llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, r)
		if len(r.Tools) == 0 && len(reqs) > 1 {
			return llm.Response{Text: docItems}, nil
		}
		return llm.Response{Text: askRead}, nil
	})
	e, base := mcpEngine(t, hub, client)
	e.Methodologies.(StaticMethodologies)["docs"] = parseDocs(t, docsWithLoop(methodology.Loop{MaxRounds: 10}))
	p := runDocs(t, e, base, "acme")
	if p.Status != StatusCompleted {
		t.Fatalf("status %s %s", p.Status, p.Error)
	}
	if len(hub.calls) != 10 || len(reqs) != 12 {
		t.Fatalf("%d tool calls, %d model calls", len(hub.calls), len(reqs))
	}
	last := reqs[len(reqs)-1]
	if len(last.Tools) != 0 || !strings.Contains(last.Messages[len(last.Messages)-1].Content, "budget of this task is spent (10 tool rounds)") {
		t.Fatalf("final request: tools %d, %q", len(last.Tools), last.Messages[len(last.Messages)-1].Content)
	}
}

func TestLoopBudgetTokens(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}}
	var mu sync.Mutex
	n := 0
	client := llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if len(r.Tools) == 0 {
			return llm.Response{Text: docItems}, nil
		}
		return llm.Response{Text: askRead, Usage: llm.Usage{InputTokens: 20, OutputTokens: 10}}, nil
	})
	e, base := mcpEngine(t, hub, client)
	e.Methodologies.(StaticMethodologies)["docs"] = parseDocs(t, docsWithLoop(methodology.Loop{MaxRounds: 100, MaxTokens: 50}))
	if p := runDocs(t, e, base, "acme"); p.Status != StatusCompleted {
		t.Fatalf("status %s %s", p.Status, p.Error)
	}
	// 30 tokens a call: the second call spends the budget, its tools are not run
	if len(hub.calls) != 1 || n != 3 {
		t.Fatalf("%d tool calls, %d model calls", len(hub.calls), n)
	}
}

// Without a budget an action keeps the default rounds and fails past them.
func TestNoLoopKeepsTheDefault(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}}
	client := llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		return llm.Response{Text: askRead}, nil
	})
	e, base := mcpEngine(t, hub, client)
	e.Methodologies.(StaticMethodologies)["docs"] = parseDocs(t, docsAsLLM)
	p := runDocs(t, e, base, "acme")
	if p.Status == StatusCompleted || len(hub.calls) < MaxToolRounds {
		t.Fatalf("status %s, %d tool calls", p.Status, len(hub.calls))
	}
}

func TestCompact(t *testing.T) {
	big := strings.Repeat("x", 5000)
	msgs := []llm.Message{{Role: "user", Content: "the task " + big}}
	for range 20 {
		msgs = append(msgs, llm.Message{Role: "assistant", Content: "calling"},
			llm.Message{Role: "user", ToolResults: []llm.ToolResult{{CallID: "c", Content: big}}})
	}
	if got := compact(msgs, 1<<20); len(got[2].ToolResults[0].Content) != len(big) {
		t.Fatal("a conversation under the limit is compacted")
	}
	got := compact(msgs, 50_000)
	if len(got) != len(msgs) || got[0].Content != msgs[0].Content {
		t.Fatal("the task or the structure changed")
	}
	if c := got[2].ToolResults[0].Content; !strings.HasSuffix(c, elided) || len(c) > 300 {
		t.Fatalf("old result: %d bytes", len(c))
	}
	if c := got[len(got)-1].ToolResults[0].Content; c != big {
		t.Fatal("a latest result was elided")
	}
	if msgs[2].ToolResults[0].Content != big {
		t.Fatal("compact changed its input")
	}
	// compacting again changes nothing
	again := compact(got, 50_000)
	if again[2].ToolResults[0].Content != got[2].ToolResults[0].Content {
		t.Fatal("compaction is not stable")
	}
}

func TestCapResultKeepsTheTail(t *testing.T) {
	s := "BEGIN" + strings.Repeat("a", 1000) + "the build failed here"
	got := capResult(s, 300)
	if !strings.HasPrefix(got, "BEGIN") || !strings.HasSuffix(got, "the build failed here") || !strings.Contains(got, "bytes cut") || len(got) > 350 {
		t.Fatalf("cap: %q", got)
	}
}
