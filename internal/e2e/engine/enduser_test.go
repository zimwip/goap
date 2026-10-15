package enginee2e

import (
	"context"
	"sync"
	"testing"

	"github.com/zimwip/goap/pkg/engine"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

// What a person's action sets off acts on their behalf, from one consequence to the next (ADR 0114): the run a trigger
// starts on the event of their edit, its model calls, and the sub-agents it starts; the attribution never grants
// anything (the run acts as the trigger).
func TestTriggeredRunActsOnBehalfOfTheEndUser(t *testing.T) {
	ctx := context.Background()
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
	var mu sync.Mutex
	var callers []authz.Principal
	e.Executors[methodology.KindLLM] = engine.LLMExecutor{Client: llm.ClientFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
		mu.Lock()
		callers = append(callers, authz.From(ctx))
		mu.Unlock()
		return llm.Response{Text: `{"items":[{"type":"artifact","target":"note","data":{"text":"done"}}]}`}, nil
	})}
	// alice edits the change: the event names her (domain.ChangeEvent.EndUser), the trigger's run acts for her
	bb, _ := e.Graph.Blackboard(ctx, ch.ID)
	tm.Handle(ctx, engine.TriggerEventOf(domain.ChangeEvent{Type: "change.edited", Change: bb.Change, EndUser: "alice"}))
	e.Drain()
	var run *engine.Process
	all, _ := e.Store.List(ctx)
	for _, p := range all {
		if p.ChangeID == ch.ID && p.Trigger != "" {
			run = p
		}
	}
	if run == nil || run.Status != engine.StatusCompleted {
		t.Fatalf("the trigger ran: %+v", run)
	}
	if run.Initiator.Subject != "system:trigger:noted/writer/on_edit" || run.Initiator.OnBehalfOf != "alice" || run.Initiator.EndUser() != "alice" {
		t.Fatalf("the run acts as the trigger, on behalf of alice: %+v", run.Initiator)
	}
	if len(callers) != 1 || callers[0].Subject != run.Initiator.Subject || callers[0].EndUser() != "alice" {
		t.Fatalf("its model call is attributed to alice: %+v", callers)
	}

	// a sub-agent a platform service starts for the run inherits its end user
	sub, err := e.Start(authz.With(ctx, authz.System("engine")), engine.StartRequest{Methodology: "noted", Goal: "noted", ChangeID: ch.ID, ParentID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if sub.Initiator.Subject != "system:engine" || sub.Initiator.OnBehalfOf != "alice" {
		t.Fatalf("the sub-agent acts on behalf of alice: %+v", sub.Initiator)
	}
	// a person starting one acts for themselves
	own, err := e.Start(authz.With(ctx, authz.Principal{Subject: "bob", OnBehalfOf: "alice"}), engine.StartRequest{Methodology: "noted", Goal: "noted", ChangeID: ch.ID, ParentID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if own.Initiator.EndUser() != "bob" {
		t.Fatalf("a person is their own end user: %+v", own.Initiator)
	}
}

// A companion run (ADR 0036 §3) set off by a person's run acts on their behalf.
func TestCompanionActsOnBehalfOfTheEndUser(t *testing.T) {
	ctx := context.Background()
	e := stagedEngine(t)
	m, err := methodology.Parse([]byte(riskyYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(engine.StaticMethodologies)["risky"] = c
	p, err := e.Start(authz.With(ctx, authz.Principal{Subject: "alice"}), engine.StartRequest{Methodology: "staged", Goal: "delivery", Intent: "deliver the note", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	e.Accompany(ctx, engine.TriggerEvent{Type: "process.attached", Process: p})
	e.Drain()
	cs := companionOf(t, e, p.ChangeID)
	if len(cs) != 1 || !cs[0].Initiator.System() || cs[0].Initiator.OnBehalfOf != "alice" {
		t.Fatalf("the companion acts on behalf of alice: %+v", cs)
	}
}
