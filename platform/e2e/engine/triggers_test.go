package enginee2e

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zimwip/goap/engine/engine"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/llm"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
)

func TestEventTriggerStartsReviewerOnSameChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, base := agentsSetup(t)
	broker := engine.NewBroker()
	e.Events = broker
	tm := &engine.TriggerManager{Engine: e}
	tm.Start(ctx)
	go tm.WatchProcesses(ctx, broker)
	time.Sleep(20 * time.Millisecond) // subscription ready

	states := tm.States()
	if len(states) != 2 {
		t.Fatalf("expected 2 triggers, got %+v", states)
	}
	d, _ := e.Start(ctx, engine.StartRequest{Methodology: "test-design", Agent: "test-designer", ForkCommit: base, Intent: "concevoir", ProjectID: testProject})
	d, _ = e.Run(ctx, d.ID)
	if d.Status != engine.StatusCompleted {
		t.Fatalf("designer %s", d.Status)
	}
	var reviewer *engine.Process
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && reviewer == nil {
		e.Drain()
		ps, _ := e.Store.List(ctx)
		for _, p := range ps {
			if p.Agent == "reviewer" && p.Status == engine.StatusCompleted {
				reviewer = p
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reviewer == nil {
		t.Fatalf("trigger did not start the reviewer: %+v", tm.States())
	}
	if reviewer.ChangeID != d.ChangeID || reviewer.Trigger != "test-design/reviewer/after_design" ||
		reviewer.Initiator.Subject != "system:trigger:test-design/reviewer/after_design" {
		t.Fatalf("unexpected triggered process %+v", reviewer)
	}
	// the run whose event fired the trigger is its cause
	if reviewer.Cause != d.ID {
		t.Fatalf("cause = %q, want the designer run %q", reviewer.Cause, d.ID)
	}
	// the reviewer completion does not match the filter: no loop
	e.Drain()
	time.Sleep(50 * time.Millisecond)
	n := 0
	ps, _ := e.Store.List(ctx)
	for _, p := range ps {
		if p.Trigger != "" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one triggered process, got %d", n)
	}
}

func TestManualFireOfScheduleTrigger(t *testing.T) {
	ctx := context.Background()
	e, _ := agentsSetup(t)
	tm := &engine.TriggerManager{Engine: e}
	if err := tm.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := tm.Fire(ctx, "test-design", "test-designer", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != engine.StatusRunning || p.Goal != "designed" || p.Trigger == "" {
		t.Fatalf("unexpected process %+v", p)
	}
	if p.Cause != "" {
		t.Fatalf("a manual fire has no cause, got %q", p.Cause)
	}
	e.Drain()
	p, _ = e.Store.Get(ctx, p.ID)
	if p.Status != engine.StatusCompleted {
		t.Fatalf("scheduled run %s %s", p.Status, p.Error)
	}
	bb, _ := e.Graph.Blackboard(ctx, p.ChangeID)
	if bb.Change.Data["trigger"] != "test-design/test-designer/nightly" {
		t.Fatalf("change not marked with its trigger: %v", bb.Change.Data)
	}
	if s := tm.States(); s[1].Fires != 1 || s[1].LastProcessID != p.ID {
		t.Fatalf("state %+v", s)
	}
	if _, err := tm.Fire(ctx, "test-design", "test-designer", "nope"); err == nil {
		t.Fatal("unknown trigger must fail")
	}
}

func TestLeaderGatesScheduleFiring(t *testing.T) {
	ctx := context.Background()
	e, _ := agentsSetup(t)
	tm := &engine.TriggerManager{Engine: e, Leader: func() bool { return false }}
	if err := tm.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	// Reload/States must stay available on every replica regardless of leadership.
	states := tm.States()
	if len(states) != 2 {
		t.Fatalf("expected 2 triggers, got %+v", states)
	}
	// a non-leader replica's cron callback must not actually fire the trigger.
	engine.Internals{}.CronFire(tm, "test-design/test-designer/nightly")
	for _, s := range tm.States() {
		if s.Name == "nightly" && s.Fires != 0 {
			t.Fatalf("non-leader replica fired a schedule trigger: %+v", s)
		}
	}
	ps, err := e.Store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Fatalf("non-leader replica started a process: %+v", ps)
	}
	// manual Fire is unaffected by Leader and must still work.
	if _, err := tm.Fire(ctx, "test-design", "test-designer", "nightly"); err != nil {
		t.Fatal(err)
	}
}

// noteTriggerYAML runs a writer on the change of the event each time the change is edited; a newer edit cancels the
// run still at work (supersede, ADR 0110).
const noteTriggerYAML = `
name: noted
version: 1.0.0
namespace: alm
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.target == "note")'}
actions:
  - {name: note, kind: llm, model: default, prompt: Write the note, effects: {noted: true}}
goals:
  - {name: noted, pre: {noted: true}}
agents:
  - name: writer
    planner: goap
    goals: [noted]
    triggers:
      - {name: on_edit, type: event, event: change.edited, target: event_change, goal: noted, enabled: true, supersede: true}
`

func TestSupersedingTriggerCancelsTheRunAtWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := stagedEngine(t)
	m, err := methodology.Parse([]byte(noteTriggerYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(engine.StaticMethodologies)["noted"] = c
	tm := &engine.TriggerManager{Engine: e}
	if err := tm.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ch, err := e.Graph.CreateChange(ctx, graph.NewChange{Title: "Note", Intent: "write a note", Methodology: "noted", Namespace: "alm", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	var mu sync.Mutex
	calls := 0
	e.Executors[methodology.KindLLM] = engine.LLMExecutor{Client: llm.ClientFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			started <- struct{}{}
			select {
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			case <-time.After(5 * time.Second):
				return llm.Response{}, errors.New("never interrupted")
			}
		}
		return llm.Response{Text: `{"items":[{"type":"artifact","target":"note","data":{"text":"done"}}]}`}, nil
	})}
	edited := func() {
		bb, _ := e.Graph.Blackboard(ctx, ch.ID)
		tm.Handle(ctx, engine.TriggerEvent{Type: "change.edited", Change: &bb.Change})
	}
	edited()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first run did not start")
	}
	edited()
	e.Drain()
	var cancelled, completed int
	all, _ := e.Store.List(ctx)
	for _, p := range all {
		if p.ChangeID != ch.ID || p.Trigger == "" {
			continue
		}
		switch p.Status {
		case engine.StatusCancelled:
			cancelled++
		case engine.StatusCompleted:
			completed++
		default:
			t.Fatalf("run %s is %s", p.ID, p.Status)
		}
	}
	if cancelled != 1 || completed != 1 || calls != 2 {
		t.Fatalf("cancelled %d, completed %d, %d model calls: the first run is cancelled, the second one writes", cancelled, completed, calls)
	}
}

const commitTriggerYAML = `
name: noted
version: 1.0.0
namespace: alm
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.target == "note")'}
actions:
  - {name: note, kind: llm, model: default, prompt: Write the note, effects: {noted: true}}
goals:
  - {name: noted, pre: {noted: true}}
agents:
  - name: writer
    planner: goap
    goals: [noted]
    triggers:
      - {name: on_need, type: event, event: change.committed, filter: 'event.nodes.exists(n, n.type == "alm@Need" && n.state == "accepted")', goal: noted, enabled: true}
`

// A commit names the node versions it wrote, which a trigger filters on, and the change a trigger creates for an event
// acts in the project of the event's change (ADR 0159).
func TestTriggerFiltersOnCommittedNodesAndKeepsTheProject(t *testing.T) {
	ctx := context.Background()
	e := stagedEngine(t)
	m, err := methodology.Parse([]byte(commitTriggerYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(engine.StaticMethodologies)["noted"] = c
	e.Executors[methodology.KindLLM] = engine.LLMExecutor{Client: llm.ClientFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"items":[{"type":"artifact","target":"note","data":{"text":"done"}}]}`}, nil
	})}
	tm := &engine.TriggerManager{Engine: e}
	if err := tm.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	src, err := e.Graph.CreateChange(ctx, graph.NewChange{Title: "Source", Intent: "commit a need", Namespace: "alm", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	fired := func() []*engine.Process {
		e.Drain()
		var out []*engine.Process
		all, _ := e.Store.List(ctx)
		for _, p := range all {
			if p.Trigger != "" {
				out = append(out, p)
			}
		}
		return out
	}
	// a commit of another type or state fires nothing
	tm.Handle(ctx, engine.TriggerEventOf(domain.ChangeEvent{Type: "change.committed", Change: src, Branch: "b",
		Nodes: []domain.EventNode{{ID: "n1", Version: 1, Key: "N-1", Type: "alm@Need", State: "draft"}}}))
	if runs := fired(); len(runs) != 0 {
		t.Fatalf("no run for a need not accepted: %+v", runs)
	}
	tm.Handle(ctx, engine.TriggerEventOf(domain.ChangeEvent{Type: "change.committed", Change: src, Branch: "b",
		Nodes: []domain.EventNode{{ID: "n1", Version: 2, Key: "N-1", Type: "alm@Need", State: "accepted"}}}))
	runs := fired()
	if len(runs) != 1 || runs[0].ChangeID == "" || runs[0].ChangeID == src.ID {
		t.Fatalf("one run on a new change: %+v", runs)
	}
	got, err := e.Graph.Change(ctx, runs[0].ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID.Key != testProject {
		t.Fatalf("the new change acts in the project of the event, %q: %q", testProject, got.ProjectID.Key)
	}
}
