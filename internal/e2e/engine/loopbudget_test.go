package enginee2e

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/pkg/engine"

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
	e.Methodologies.(engine.StaticMethodologies)["docs"] = parseDocs(t, docsWithLoop(methodology.Loop{MaxRounds: 10}))
	p := runDocs(t, e, base, "acme")
	if p.Status != engine.StatusCompleted {
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
	e.Methodologies.(engine.StaticMethodologies)["docs"] = parseDocs(t, docsWithLoop(methodology.Loop{MaxRounds: 100, MaxTokens: 50}))
	if p := runDocs(t, e, base, "acme"); p.Status != engine.StatusCompleted {
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
	e.Methodologies.(engine.StaticMethodologies)["docs"] = parseDocs(t, docsAsLLM)
	p := runDocs(t, e, base, "acme")
	if p.Status == engine.StatusCompleted || len(hub.calls) < engine.MaxToolRounds {
		t.Fatalf("status %s, %d tool calls", p.Status, len(hub.calls))
	}
}
