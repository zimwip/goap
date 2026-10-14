package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/assess"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

// describedYAML reviews the description of its changes with a reactive step (ADR 0110): the step runs on the change
// when it is created, when its description is edited and when a person comments on the rating.
const describedYAML = `
name: described
version: 1.0.0
namespace: alm
imports: [assessments]
actions:
  - {name: rate, kind: human, description: Rate the description, effects: {description_reviewed: true}}
  - {name: write, kind: human, description: Write the note, pre: {description_accepted: true}, effects: {noted: true}}
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.type == "note")'}
processes:
  - name: delivery
    steps:
      - {name: note, action: write}
  - name: review
    steps:
      - name: rate
        action: rate
        on:
          - {event: change.created}
          - {event: change.edited, filter: '"intent" in event.fields || "title" in event.fields'}
          - {event: change.item_added, filter: 'event.items.exists(i, i.kind == "comment" && i.data.about == "description")'}
`

func TestReactiveStepReviewsTheDescription(t *testing.T) {
	ctx := context.Background()
	e := stagedEngine(t)
	m, err := methodology.Parse([]byte(describedYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if rs := c.Reactions(); len(rs) != 1 || rs[0].Activity != "review/rate" || rs[0].Agent != "review" || rs[0].Goal != "review/rate" || !rs[0].Owns("review/rate") {
		t.Fatalf("reactions %+v", rs)
	}
	e.Methodologies.(StaticMethodologies)["described"] = c

	ch, err := e.Graph.CreateChange(ctx, graph.NewChange{Title: "Note", Intent: "write a note", Methodology: "described", Namespace: "alm", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	event := func(typ string, items []domain.ChangeItem, fields map[string]domain.HeaderValue) {
		t.Helper()
		bb, err := e.Graph.Blackboard(ctx, ch.ID)
		if err != nil {
			t.Fatal(err)
		}
		e.Accompany(ctx, TriggerEvent{Type: typ, Change: &bb.Change, Items: items, Fields: fields})
		e.Drain()
	}
	rater := func() *Process {
		t.Helper()
		cs := companionOf(t, e, ch.ID)
		if len(cs) != 1 {
			t.Fatalf("one run of the reaction per change: %d", len(cs))
		}
		return cs[0]
	}
	rate := func(score int, reply string) {
		t.Helper()
		bb, _ := e.Graph.Blackboard(ctx, ch.ID)
		text, _ := assess.Text(bb, assess.SubjectDescription)
		p := rater()
		if _, err := e.Submit(ctx, p.ID, []ItemInput{{Kind: string(assess.KindAssessment), Data: map[string]any{"key": "description",
			"score": score, "verdict": "unclear", "rated": text, "reply": reply}}}); err != nil {
			t.Fatal(err)
		}
		if p, _ = e.Run(ctx, p.ID); p.Status != StatusCompleted {
			t.Fatalf("rated: %s %s", p.Status, p.Error)
		}
	}
	waiting := func(why string) {
		t.Helper()
		if p := rater(); p.Status != StatusWaiting || p.Pending == nil || p.Pending.Action != "review/rate" {
			t.Fatalf("%s: the rater runs again, got %s %+v", why, p.Status, p.Pending)
		}
	}

	// the change is created: the step rates its description
	event("change.created", nil, nil)
	if p := rater(); p.Trigger != "companion:described/review/rate" || p.Goal != "review/rate" {
		t.Fatalf("reaction run %+v", p)
	}
	waiting("created")
	rate(2, "say what the note is about")
	run := rater().ID

	// the person answers in the thread: the same run is woken and rates again
	items, err := e.Graph.AddItems(ctx, ch.ID, []domain.ChangeItem{{Kind: assess.KindComment, Status: domain.ItemProposed, ProducedBy: "alice",
		Data: map[string]any{"about": "description", "text": "it is the release note"}}})
	if err != nil {
		t.Fatal(err)
	}
	event("change.item_added", items, nil)
	waiting("commented")
	rate(2, "then say so in the description")
	if rater().ID != run {
		t.Fatal("the reaction keeps its run")
	}

	// an edit of the goal is not one of its events; an edit of the description is
	event("change.edited", nil, map[string]domain.HeaderValue{"goal": {From: "a", To: "b"}})
	if p := rater(); p.Status != StatusCompleted {
		t.Fatalf("filtered out: %s", p.Status)
	}
	intent := "write the release note of 1.2"
	if _, err := e.Graph.UpdateChange(ctx, ch.ID, graph.ChangePatch{Intent: &intent}); err != nil {
		t.Fatal(err)
	}
	event("change.edited", nil, map[string]domain.HeaderValue{"intent": {From: "write a note", To: intent}})
	waiting("edited")
	rate(4, "clear")
	bb, _ := e.Graph.Blackboard(ctx, ch.ID)
	if a, ok := assess.Of(bb, "description"); !ok || !a.Current || !a.Answered || a.Score != 4 || a.Versions != 3 {
		t.Fatalf("assessment %+v", a)
	}
}

func TestReactiveActionsAndTheirChecks(t *testing.T) {
	m, err := methodology.Parse([]byte(`
name: nudging
version: 1.0.0
namespace: alm
conditions:
  - {name: nudged, expr: 'artifacts.exists(a, a.type == "nudge")'}
actions:
  - {name: nudge, kind: human, effects: {nudged: true}, on: [{event: change.edited}]}
processes:
  - name: main
    steps:
      - {name: one, action: nudge}
`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	rs := c.Reactions()
	if len(rs) != 1 || rs[0].Kind != methodology.ReactAction || rs[0].Agent != "on.nudge" {
		t.Fatalf("reactions %+v", rs)
	}
	if g, ok := c.Goal("on.nudge"); !ok || !g.Pre["nudged"] {
		t.Fatalf("goal of a reactive action: %+v", g)
	}
	if ag, ok := c.Agent("on.nudge"); !ok || len(c.AgentActions(ag)) != 1 {
		t.Fatalf("agent of a reactive action: %+v", ag)
	}
	for name, src := range map[string]string{
		"no effects": `
name: bad
version: 1.0.0
namespace: alm
actions:
  - {name: nudge, kind: human, on: [{event: change.edited}]}
goals: [{name: g, pre: {x: true}}]
conditions: [{name: x, expr: "true"}]
`,
		"unknown event": `
name: bad
version: 1.0.0
namespace: alm
conditions: [{name: x, expr: "true"}]
actions: [{name: a, kind: human, effects: {x: true}}]
processes: [{name: p, on: [{event: change.nothing}], steps: [{name: s, action: a}]}]
`,
		"transverse": `
name: bad
version: 1.0.0
appliesTo: [staged]
conditions: [{name: x, expr: "true"}]
actions: [{name: a, kind: human, effects: {x: true}}]
processes: [{name: p, steps: [{name: s, action: a, on: [{event: change.edited}]}]}]
`,
	} {
		m, err := methodology.Parse([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		if issues := m.Validate(); !issues.HasErrors() {
			t.Errorf("%s: refused expected", name)
		}
	}
}

// eventfulYAML declares an event of its own (ADR 0110): the action writing the spec raises it, a reactive action
// follows it.
const eventfulYAML = `
name: eventful
version: 1.0.0
namespace: alm
events:
  - {name: spec.ready, description: The spec is written}
conditions:
  - {name: written, expr: 'artifacts.exists(a, a.type == "spec")'}
  - {name: notified, expr: 'artifacts.exists(a, a.type == "notice")'}
actions:
  - {name: write, kind: human, effects: {written: true}, emits: [spec.ready]}
  - {name: notify, kind: human, effects: {notified: true}, on: [{event: spec.ready}]}
processes:
  - name: delivery
    steps:
      - {name: spec, action: write}
`

func TestDeclaredEventsAreEmittedAndFollowed(t *testing.T) {
	ctx := context.Background()
	e := stagedEngine(t)
	m, err := methodology.Parse([]byte(eventfulYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(StaticMethodologies)["eventful"] = c
	p, err := e.Start(ctx, StartRequest{Methodology: "eventful", Goal: "delivery", Intent: "write the spec", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Run(ctx, p.ID); p.Status != StatusWaiting {
		t.Fatalf("the spec step waits: %s %s", p.Status, p.Error)
	}
	if _, err := e.Submit(ctx, p.ID, []ItemInput{{Kind: "artifact", Type: "spec", Data: map[string]any{"text": "the spec"}}}); err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Run(ctx, p.ID); p.Status != StatusCompleted {
		t.Fatalf("delivered: %s %s", p.Status, p.Error)
	}
	bb, err := e.Graph.Blackboard(ctx, p.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	var signals []domain.ChangeItem
	for _, it := range bb.Change.Items {
		if it.Kind == domain.KindSignal {
			signals = append(signals, it)
		}
	}
	if len(signals) != 1 || signals[0].Type != "spec.ready" || signals[0].Data["step"] != "delivery/spec" {
		t.Fatalf("the action raises its event: %+v", signals)
	}

	// the item added raises the declared event, which wakes the reactive action
	ev := TriggerEvent{Type: "change.item_added", Change: &bb.Change, Items: signals}
	if own := DeclaredEvents(ev); len(own) != 1 || own[0].Type != "spec.ready" || len(own[0].Items) != 1 {
		t.Fatalf("declared events %+v", own)
	}
	(&TriggerManager{Engine: e}).Handle(ctx, ev)
	e.Drain()
	cs := companionOf(t, e, p.ChangeID)
	if len(cs) != 1 || cs[0].Trigger != "companion:eventful/notify" || cs[0].Status != StatusWaiting {
		t.Fatalf("the reaction to the declared event: %+v", cs)
	}

	// a signal of a standard name raises nothing more, an undeclared event is refused at compile
	if own := DeclaredEvents(TriggerEvent{Type: "change.item_added", Items: []domain.ChangeItem{{Kind: domain.KindSignal, Type: "change.applied"}}}); len(own) != 0 {
		t.Fatalf("a standard name is not a declared event: %+v", own)
	}
	for name, src := range map[string]string{
		"undeclared": "name: bad\nversion: 1.0.0\nnamespace: alm\nconditions: [{name: x, expr: 'true'}]\nactions: [{name: a, kind: human, effects: {x: true}, on: [{event: spec.ready}]}]\n",
		"reserved":   "name: bad\nversion: 1.0.0\nnamespace: alm\nevents: [{name: change.ready}]\nconditions: [{name: x, expr: 'true'}]\nactions: [{name: a, kind: human, effects: {x: true}}]\ngoals: [{name: g, pre: {x: true}}]\n",
		"emits":      "name: bad\nversion: 1.0.0\nnamespace: alm\nconditions: [{name: x, expr: 'true'}]\nactions: [{name: a, kind: human, effects: {x: true}, emits: [nope]}]\ngoals: [{name: g, pre: {x: true}}]\n",
	} {
		m, err := methodology.Parse([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		if issues := m.Validate(); !issues.HasErrors() {
			t.Errorf("%s: refused expected", name)
		}
	}
}

// ratedYAML rates the description the way sdlc does (ADR 0110): a goap method whose llm action comes first and whose
// human action is the fallback, every activity carried out by an agent.
const ratedYAML = `
name: rated
version: 1.0.0
namespace: alm
imports: [assessments]
roles: [{name: analyst}]
actions:
  - {name: rate, kind: llm, model: default, prompt: Rate the description, effects: {description_reviewed: true}, cost: 1}
  - {name: rate_by_hand, kind: human, effects: {description_reviewed: true}, cost: 6}
methods:
  - {name: rating, for: rating, planner: goap, actions: [rate, rate_by_hand], done: {description_reviewed: true}, roles: {responsible: analyst}}
processes:
  - name: review
    steps:
      - {name: rate, method: rating, on: [{event: change.created}, {event: change.edited}]}
`

func TestDescriptionRatingFallsBackToAPerson(t *testing.T) {
	ctx := context.Background()
	for name, answer := range map[string]string{
		"model":  `{"items":[{"kind":"assessment","data":{"key":"description","score":4,"verdict":"clear","rated":"FP"}}]}`,
		"person": `{"items":[]}`, // the model rates nothing: its action fails, is disabled, the person rates
	} {
		t.Run(name, func(t *testing.T) {
			e := stagedEngine(t)
			m, err := methodology.Parse([]byte(ratedYAML))
			if err != nil {
				t.Fatal(err)
			}
			c, err := m.Compile()
			if err != nil {
				t.Fatal(err)
			}
			if rs := c.Reactions(); len(rs) != 1 || !rs[0].Owns("rate") || !rs[0].Owns("rate_by_hand") {
				t.Fatalf("the reaction owns the actions of the method it runs: %+v", rs)
			}
			e.Methodologies.(StaticMethodologies)["rated"] = c
			ch, err := e.Graph.CreateChange(ctx, graph.NewChange{Title: "Note", Intent: "write the release note of 1.2", Methodology: "rated", Namespace: "alm", ProjectID: testProject})
			if err != nil {
				t.Fatal(err)
			}
			e.Executors[methodology.KindLLM] = LLMExecutor{Client: llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
				bb, _ := e.Graph.Blackboard(ctx, ch.ID)
				return llm.Response{Text: strings.Replace(answer, "FP", assess.FingerprintOf(bb, assess.SubjectDescription), 1)}, nil
			})}
			bb, _ := e.Graph.Blackboard(ctx, ch.ID)
			e.Accompany(ctx, TriggerEvent{Type: "change.created", Change: &bb.Change})
			e.Drain()
			var waiting *Process
			all, _ := e.Store.List(ctx)
			for _, p := range all {
				if p.ChangeID == ch.ID && p.Status == StatusWaiting && p.ParentID != "" {
					waiting = p // the agent of the method, a sub-agent of the reaction's run
				}
			}
			bb, _ = e.Graph.Blackboard(ctx, ch.ID)
			a, rated := assess.Of(bb, assess.SubjectDescription)
			switch name {
			case "model":
				if !rated || !a.Current || a.By != "rate" || waiting != nil {
					t.Fatalf("the model rates first: %+v, waiting %+v", a, waiting)
				}
			case "person":
				if rated || waiting == nil || waiting.Pending == nil || waiting.Pending.Action != "rate_by_hand" || !waiting.Disabled["rate"] {
					t.Fatalf("the person rates when the model cannot: %+v", waiting)
				}
			}
		})
	}
}
