package enginee2e

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/engine/engine"

	"github.com/zimwip/goap/contracts/llm"
	"github.com/zimwip/goap/engine/methodology"
)

// docsAsLLM turns the fetch action of the docs methodology into an LLM action declaring the document repository.
func docsAsLLM(m *methodology.Methodology) {
	for i := range m.Actions {
		if m.Actions[i].Name == "fetch_doc" {
			m.Actions[i].Kind, m.Actions[i].Tool, m.Actions[i].Params = methodology.KindLLM, "", nil
			m.Actions[i].Prompt, m.Actions[i].MCPs = "Read spec.txt and register it.", []string{"document-repository"}
		}
	}
}

const docItems = `{"items":[{"type":"artifact","target":"doc","data":{"seen":"ok"}}]}`

func TestLLMActionRepairsAnUnreadableAnswer(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}}
	var mu sync.Mutex
	var reqs []llm.Request
	client := llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, r)
		switch len(reqs) {
		case 1:
			return llm.Response{Text: "Sure, here is what I found."}, nil
		case 2:
			return llm.Response{Text: `{"items":[{"type":"artifact","target":"doc","da`, StopReason: llm.StopMaxTokens}, nil
		}
		return llm.Response{Text: docItems}, nil
	})
	e, base := mcpEngine(t, hub, client)
	e.Methodologies.(engine.StaticMethodologies)["docs"] = parseDocs(t, docsAsLLM)
	p := runDocs(t, e, base, "acme")
	if p.Status != engine.StatusCompleted {
		t.Fatalf("status %s %s", p.Status, p.Error)
	}
	if len(reqs) != 3 {
		t.Fatalf("%d model calls, want 3", len(reqs))
	}
	last := func(r llm.Request) string { return r.Messages[len(r.Messages)-1].Content }
	if s := last(reqs[1]); !strings.Contains(s, "could not be read") || !strings.Contains(s, `{"tool_calls":[...]}`) {
		t.Fatalf("first repair: %q", s)
	}
	if s := last(reqs[2]); !strings.Contains(s, "cut by the output limit") {
		t.Fatalf("second repair: %q", s)
	}
	// the unreadable answers stay in the conversation, and a tool loop asks for the prompt cache
	if len(reqs[2].Messages) != 5 || reqs[2].Messages[1].Content != "Sure, here is what I found." || !reqs[2].Cache {
		t.Fatalf("messages = %+v cache=%v", reqs[2].Messages, reqs[2].Cache)
	}
}

func TestLLMActionFailsAfterMaxRepairs(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	client := llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return llm.Response{Text: "no JSON here"}, nil
	})
	ac := engine.ActionContext{Action: methodology.Action{Name: "a", Kind: methodology.KindLLM}}
	_, err := engine.Internals{}.RunLoop(engine.LLMExecutor{Client: client}, context.Background(), ac, "do it", "system", nil)
	if err == nil || !strings.Contains(err.Error(), "unreadable answer after 2 repairs") || calls != engine.MaxRepairs+1 {
		t.Fatalf("err = %v after %d calls", err, calls)
	}
}

func TestLLMActionGuidesThenStopsAFailingTool(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}, fail: errors.New("path not found")}
	var mu sync.Mutex
	var lasts []string
	client := llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		lasts = append(lasts, r.Messages[len(r.Messages)-1].Content)
		if len(lasts) <= engine.MaxToolFailures+1 {
			return llm.Response{Text: `{"tool_calls":[{"name":"document-repository/read","arguments":{"path":"spec.txt"}}]}`}, nil
		}
		return llm.Response{Text: docItems}, nil
	})
	e, base := mcpEngine(t, hub, client)
	e.Methodologies.(engine.StaticMethodologies)["docs"] = parseDocs(t, docsAsLLM)
	p := runDocs(t, e, base, "acme")
	if p.Status != engine.StatusCompleted {
		t.Fatalf("status %s %s", p.Status, p.Error)
	}
	if !strings.Contains(lasts[1], "failure 1 of 3 allowed; do not repeat the same call") {
		t.Fatalf("first failure: %q", lasts[1])
	}
	if !strings.Contains(lasts[3], "this was the last try") {
		t.Fatalf("last failure: %q", lasts[3])
	}
	// the fourth call never reaches the tool
	if !strings.Contains(lasts[4], "failed 3 times: do not call it again") || len(hub.calls) != engine.MaxToolFailures {
		t.Fatalf("refusal %q, hub calls %v", lasts[4], hub.calls)
	}
}

func TestLLMActionRunsTheCallsOfARoundTogether(t *testing.T) {
	release := make(chan struct{})
	hub := &blockingHub{fakeHub: fakeHub{bound: map[string][]string{"acme": {"document-repository"}}}, started: make(chan struct{}, 2), release: release}
	go func() {
		// both calls must be in flight before either may end
		<-hub.started
		<-hub.started
		close(release)
	}()
	var mu sync.Mutex
	n := 0
	client := llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return llm.Response{Text: `{"tool_calls":[{"name":"document-repository/read","arguments":{"path":"a"}},{"name":"document-repository/read","arguments":{"path":"b"}}]}`}, nil
		}
		return llm.Response{Text: docItems}, nil
	})
	e, base := mcpEngine(t, hub, client)
	e.Methodologies.(engine.StaticMethodologies)["docs"] = parseDocs(t, docsAsLLM)
	if p := runDocs(t, e, base, "acme"); p.Status != engine.StatusCompleted {
		t.Fatalf("status %s %s", p.Status, p.Error)
	}
}

// blockingHub holds every call until release is closed, signalling started when a call begins.
type blockingHub struct {
	fakeHub
	started chan struct{}
	release chan struct{}
}

func (h *blockingHub) CallTool(ctx context.Context, org, name string, args map[string]any) (any, error) {
	h.started <- struct{}{}
	<-h.release
	return h.fakeHub.CallTool(ctx, org, name, args)
}

var _ engine.ToolPort = (*blockingHub)(nil)

func TestLLMActionCallsToolsNatively(t *testing.T) {
	hub := &fakeHub{bound: map[string][]string{"acme": {"document-repository"}}}
	var mu sync.Mutex
	var reqs []llm.Request
	client := llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, r)
		if len(reqs) == 1 {
			return llm.Response{Text: "Reading it.", StopReason: llm.StopToolUse, ToolCalls: []llm.ToolCall{
				{ID: "c1", Name: "document-repository/read", Arguments: map[string]any{"path": "spec.txt"}},
				{ID: "c2", Name: "ticketing/create", Arguments: map[string]any{"path": "x"}},
			}}, nil
		}
		return llm.Response{Text: docItems}, nil
	})
	e, base := mcpEngine(t, hub, client)
	e.Methodologies.(engine.StaticMethodologies)["docs"] = parseDocs(t, docsAsLLM)
	p := runDocs(t, e, base, "acme")
	if p.Status != engine.StatusCompleted || len(reqs) != 2 {
		t.Fatalf("status %s %s after %d calls", p.Status, p.Error, len(reqs))
	}
	if len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "document-repository/read" || strings.Contains(reqs[0].System, "Available tools") {
		t.Fatalf("tools %+v", reqs[0].Tools)
	}
	msgs := reqs[1].Messages
	call, results := msgs[len(msgs)-2], msgs[len(msgs)-1]
	if call.Role != "assistant" || len(call.ToolCalls) != 2 || call.Content != "Reading it." {
		t.Fatalf("assistant turn %+v", call)
	}
	r := results.ToolResults
	if len(r) != 2 || r[0].CallID != "c1" || r[0].IsError || !strings.Contains(r[0].Content, "contents of spec.txt") ||
		r[1].CallID != "c2" || !r[1].IsError || !strings.Contains(r[1].Content, "the tools you can call are document-repository/read") {
		t.Fatalf("results %+v", r)
	}
	if len(hub.calls) != 1 {
		t.Fatalf("hub calls %v", hub.calls)
	}
}
