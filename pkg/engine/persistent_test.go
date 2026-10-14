package engine

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/pkg/assess"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

// persistentRatedYAML is ratedYAML with a persistent rater (ADR 0112): one instance per change applies the method.
var persistentRatedYAML = strings.Replace(ratedYAML,
	"roles: {responsible: analyst}}",
	"roles: {responsible: analyst}, persistent: {scope: change, memory: 2}}", 1)

func TestPersistentRaterKeepsItsInstanceAndConversation(t *testing.T) {
	ctx := context.Background()
	e := stagedEngine(t)
	m, err := methodology.Parse([]byte(persistentRatedYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if ag, _ := c.Agent("rating"); ag.Persistent == nil || ag.Persistent.Turns() != 2 {
		t.Fatalf("the agent of the method is persistent: %+v", ag.Persistent)
	}
	e.Methodologies.(StaticMethodologies)["rated"] = c
	ch, err := e.Graph.CreateChange(ctx, graph.NewChange{Title: "Note", Intent: "write a note", Methodology: "rated", Namespace: "alm", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var reqs []llm.Request
	e.Executors[methodology.KindLLM] = LLMExecutor{Client: llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, r)
		bb, _ := e.Graph.Blackboard(ctx, ch.ID)
		fp := assess.FingerprintOf(bb, assess.SubjectDescription)
		return llm.Response{Text: `{"items":[{"kind":"assessment","data":{"key":"description","score":3,"verdict":"clear","rated":"` + fp + `","reply":"answer ` + string(rune('0'+len(reqs))) + `"}}]}`}, nil
	})}
	raters := func() []*Process {
		t.Helper()
		all, _ := e.Store.List(ctx)
		var out []*Process
		for _, p := range all {
			if p.ChangeID == ch.ID && p.Agent == "rating" {
				out = append(out, p)
			}
		}
		return out
	}
	edit := func(text string) {
		t.Helper()
		bb, _ := e.Graph.Blackboard(ctx, ch.ID)
		from := bb.Change.Intent
		if _, err := e.Graph.UpdateChange(ctx, ch.ID, graph.ChangePatch{Intent: &text}); err != nil {
			t.Fatal(err)
		}
		bb, _ = e.Graph.Blackboard(ctx, ch.ID)
		e.Accompany(ctx, TriggerEvent{Type: "change.edited", Change: &bb.Change, Fields: map[string]domain.HeaderValue{"intent": {From: from, To: text}}})
		e.Drain()
	}
	bb, _ := e.Graph.Blackboard(ctx, ch.ID)
	e.Accompany(ctx, TriggerEvent{Type: "change.created", Change: &bb.Change})
	e.Drain()
	edit("write the release note of 1.2")
	edit("write the release note of 1.2, for the users")

	rs := raters()
	if len(rs) != 1 {
		t.Fatalf("one instance of the rater for the change, got %d", len(rs))
	}
	p := rs[0]
	if p.Instance != "rated/rating@"+string(ch.ID) || p.Runs != 2 || p.Status != StatusCompleted || p.Closed {
		t.Fatalf("instance %q runs %d status %s closed %v", p.Instance, p.Runs, p.Status, p.Closed)
	}
	if len(reqs) != 3 {
		t.Fatalf("%d model calls, want 3", len(reqs))
	}
	// the first call starts the conversation; the next ones get the earlier exchanges back, the last two only
	if n := len(reqs[0].Messages); n != 1 || strings.Contains(reqs[0].System, "persistent agent") {
		t.Fatalf("first call: %d messages", n)
	}
	if n := len(reqs[1].Messages); n != 3 || !strings.Contains(reqs[1].Messages[1].Content, "answer 1") || !strings.Contains(reqs[1].System, "persistent agent") {
		t.Fatalf("second call: %+v", reqs[1].Messages)
	}
	if n := len(reqs[2].Messages); n != 5 || !strings.Contains(reqs[2].Messages[3].Content, "answer 2") || !strings.Contains(reqs[2].Messages[2].Content, "earlier execution 1") {
		t.Fatalf("third call: %+v", reqs[2].Messages)
	}
	if len(p.Memory) != 2 || p.Memory[1].Run != 2 || p.Memory[1].Event != "change.edited" {
		t.Fatalf("memory %+v", p.Memory)
	}
}

// noteYAML has a persistent agent on its change, closed once a final note is written.
const noteYAML = `
name: notes
version: 1.0.0
namespace: alm
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.type == "note" && a.data.n == vars.event.n)'}
  - {name: final, expr: 'artifacts.exists(a, a.type == "note" && a.data.final == true)'}
actions:
  - {name: note, kind: llm, model: default, prompt: Write the note, effects: {noted: true}}
goals:
  - {name: noted, pre: {noted: true}}
agents:
  - {name: writer, planner: goap, goals: [noted], persistent: {until: {final: true}}}
`

func TestPersistentInstanceClosesUntil(t *testing.T) {
	ctx := context.Background()
	e := stagedEngine(t)
	m, err := methodology.Parse([]byte(noteYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(StaticMethodologies)["notes"] = c
	ch, err := e.Graph.CreateChange(ctx, graph.NewChange{Title: "Notes", Intent: "notes", Methodology: "notes", Namespace: "alm", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	final := map[int]bool{2: true}
	run := func(n int) *Process {
		t.Helper()
		e.Executors[methodology.KindLLM] = LLMExecutor{Client: llm.ClientFunc(func(context.Context, llm.Request) (llm.Response, error) {
			f := "false"
			if final[n] {
				f = "true"
			}
			return llm.Response{Text: `{"items":[{"kind":"artifact","type":"note","data":{"n":` + string(rune('0'+n)) + `,"final":` + f + `}}]}`}, nil
		})}
		p, err := e.Start(ctx, StartRequest{Methodology: "notes", Agent: "writer", Goal: "noted", ChangeID: ch.ID, Vars: map[string]any{"event": map[string]any{"type": "manual", "n": n}}})
		if err != nil {
			t.Fatal(err)
		}
		if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusCompleted {
			t.Fatalf("run %d: %v %s %s", n, err, p.Status, p.Error)
		}
		return p
	}
	first, second, third := run(1), run(2), run(3)
	if first.ID != second.ID || second.Runs != 1 || !second.Closed {
		t.Fatalf("the second execution goes to the instance, and its final note closes it: %s %s runs %d closed %v", first.ID, second.ID, second.Runs, second.Closed)
	}
	if third.ID == second.ID || third.Instance != second.Instance {
		t.Fatalf("after closing, a new instance: %s %s", third.ID, third.Instance)
	}
}

func TestPersistenceScope(t *testing.T) {
	p := &methodology.Persistence{Scope: "event.process.methodology"}
	if got := p.Resolve("C1", map[string]any{"process": map[string]any{"methodology": "sdlc"}}); got != "sdlc" {
		t.Fatalf("scope %q", got)
	}
	if got := p.Resolve("C1", nil); got != "" {
		t.Fatalf("no event, no scope: %q", got)
	}
	if got := (&methodology.Persistence{}).Resolve("C1", nil); got != "C1" {
		t.Fatalf("default scope is the change: %q", got)
	}
	m, err := methodology.Parse([]byte(strings.Replace(noteYAML, "until: {final: true}", "scope: process, until: {nope: true}, memory: 50", 1)))
	if err != nil {
		t.Fatal(err)
	}
	_, issues := m.CompileLenient()
	var paths []string
	for _, is := range issues {
		paths = append(paths, is.Path)
	}
	got := strings.Join(paths, " ")
	for _, want := range []string{"agents[0].persistent.scope", "agents[0].persistent.until.nope", "agents[0].persistent.memory"} {
		if !strings.Contains(got, want) {
			t.Fatalf("issues %v, want %s", paths, want)
		}
	}
}

// A persistent rater given its earlier exchanges back copies the fingerprint of its first rating in the next ones: the
// engine stamps what was rated with the text the action read, so every new rating counts.
func TestPersistentRaterRatesTheCurrentText(t *testing.T) {
	ctx := context.Background()
	e := stagedEngine(t)
	m, err := methodology.Parse([]byte(persistentRatedYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(StaticMethodologies)["rated"] = c
	ch, err := e.Graph.CreateChange(ctx, graph.NewChange{Title: "Note", Intent: "write a note", Methodology: "rated", Namespace: "alm", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	stale, n := "", 0
	e.Executors[methodology.KindLLM] = LLMExecutor{Client: llm.ClientFunc(func(context.Context, llm.Request) (llm.Response, error) {
		if stale == "" {
			bb, _ := e.Graph.Blackboard(ctx, ch.ID)
			stale = assess.FingerprintOf(bb, assess.SubjectDescription)
		}
		n++
		return llm.Response{Text: `{"items":[{"kind":"assessment","data":{"key":"description","score":` + string(rune('0'+n+1)) + `,"verdict":"clear","rated":"` + stale + `"}}]}`}, nil
	})}
	bb, _ := e.Graph.Blackboard(ctx, ch.ID)
	e.Accompany(ctx, TriggerEvent{Type: "change.created", Change: &bb.Change})
	e.Drain()
	for _, text := range []string{"write the release note of 1.2", "write the release note of 1.2, for the users"} {
		from := bb.Change.Intent
		if _, err := e.Graph.UpdateChange(ctx, ch.ID, graph.ChangePatch{Intent: &text}); err != nil {
			t.Fatal(err)
		}
		bb, _ = e.Graph.Blackboard(ctx, ch.ID)
		e.Accompany(ctx, TriggerEvent{Type: "change.edited", Change: &bb.Change, Fields: map[string]domain.HeaderValue{"intent": {From: from, To: text}}})
		e.Drain()
	}
	bb, _ = e.Graph.Blackboard(ctx, ch.ID)
	a, ok := assess.Of(bb, assess.SubjectDescription)
	if !ok || !a.Current || a.Score != 4 || a.Versions != 3 || n != 3 {
		t.Fatalf("the third rating is about the current description: %+v (%d calls)", a, n)
	}
	all, _ := e.Store.List(ctx)
	for _, p := range all {
		if p.ChangeID == ch.ID && p.Agent == "rating" && (p.Status != StatusCompleted || p.Disabled["rate"]) {
			t.Fatalf("the rater completed with its model: %s disabled %v", p.Status, p.Disabled)
		}
	}
}
