package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
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

const docItems = `{"items":[{"kind":"artifact","type":"doc","data":{"seen":"ok"}}]}`

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
			return llm.Response{Text: `{"items":[{"kind":"artifact","type":"doc","da`, StopReason: llm.StopMaxTokens}, nil
		}
		return llm.Response{Text: docItems}, nil
	})
	e, base := mcpEngine(t, hub, client)
	e.Methodologies.(StaticMethodologies)["docs"] = parseDocs(t, docsAsLLM)
	p := runDocs(t, e, base, "acme")
	if p.Status != StatusCompleted {
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
	ac := ActionContext{Action: methodology.Action{Name: "a", Kind: methodology.KindLLM}}
	_, err := LLMExecutor{Client: client}.runLoop(context.Background(), ac, "do it", "system", nil)
	if err == nil || !strings.Contains(err.Error(), "unreadable answer after 2 repairs") || calls != MaxRepairs+1 {
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
		if len(lasts) <= MaxToolFailures+1 {
			return llm.Response{Text: `{"tool_calls":[{"name":"document-repository/read","arguments":{"path":"spec.txt"}}]}`}, nil
		}
		return llm.Response{Text: docItems}, nil
	})
	e, base := mcpEngine(t, hub, client)
	e.Methodologies.(StaticMethodologies)["docs"] = parseDocs(t, docsAsLLM)
	p := runDocs(t, e, base, "acme")
	if p.Status != StatusCompleted {
		t.Fatalf("status %s %s", p.Status, p.Error)
	}
	if !strings.Contains(lasts[1], "failure 1 of 3 allowed; do not repeat the same call") {
		t.Fatalf("first failure: %q", lasts[1])
	}
	if !strings.Contains(lasts[3], "this was the last try") {
		t.Fatalf("last failure: %q", lasts[3])
	}
	// the fourth call never reaches the tool
	if !strings.Contains(lasts[4], "failed 3 times: do not call it again") || len(hub.calls) != MaxToolFailures {
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
	e.Methodologies.(StaticMethodologies)["docs"] = parseDocs(t, docsAsLLM)
	if p := runDocs(t, e, base, "acme"); p.Status != StatusCompleted {
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

var _ ToolPort = (*blockingHub)(nil)
