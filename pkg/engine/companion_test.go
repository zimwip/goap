package engine

import (
	"context"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
)

// riskyYAML is a transverse methodology (ADR 0036 §3): it applies to staged, so its process runs alongside the
// changes of staged, on the same change.
const riskyYAML = `
name: risky
version: 1.0.0
appliesTo: [staged]
conditions:
  - {name: risks_identified, expr: 'artifacts.exists(a, a.type == "risk_review")'}
actions:
  - {name: identify, kind: human, description: Identify the risks, effects: {risks_identified: true}}
  - {name: mitigate, kind: human, description: Define the mitigation actions, pre: {risks_identified: true}, effects: {risks_under_control: true}}
processes:
  - name: risk_watch
    steps:
      - {name: identify, action: identify}
      - {name: mitigate, action: mitigate}
`

func companionOf(t *testing.T, e *Engine, change domain.ChangeID) []*Process {
	t.Helper()
	all, err := e.Store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []*Process
	for _, p := range all {
		if p.ChangeID == change && p.IsCompanion() {
			out = append(out, p)
		}
	}
	return out
}

func TestTransverseProcessRunsAlongsideTheChange(t *testing.T) {
	ctx := context.Background()
	e := stagedEngine(t)
	m, err := methodology.Parse([]byte(riskyYAML))
	if err != nil {
		t.Fatal(err)
	}
	if issues := m.ValidateStored(); len(issues) > 0 {
		t.Fatalf("a transverse methodology needs no namespace: %v", issues)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(StaticMethodologies)["risky"] = c

	p, err := e.Start(ctx, StartRequest{Methodology: "staged", Goal: "delivery", Intent: "deliver the note"})
	if err != nil {
		t.Fatal(err)
	}
	// the change of staged is created: the risk process joins it
	e.Accompany(ctx, TriggerEvent{Type: "process.attached", Process: p})
	e.Drain()
	cs := companionOf(t, e, p.ChangeID)
	if len(cs) != 1 || cs[0].Methodology != "risky" || cs[0].Agent != "risk_watch" || cs[0].Pending == nil || cs[0].Pending.Action != "risk_watch/identify" {
		t.Fatalf("companion %+v", cs)
	}
	risk := cs[0]
	// the risks are identified: a high one needs an action
	if _, err := e.Submit(ctx, risk.ID, []ItemInput{{Kind: "artifact", Type: "risk_review"},
		{Kind: "risk", Data: map[string]any{"key": "RSK-1", "title": "the note is wrong", "probability": 4.0, "impact": 4.0}}}); err != nil {
		t.Fatal(err)
	}
	if risk, _ = e.Run(ctx, risk.ID); risk.Pending == nil || risk.Pending.Action != "risk_watch/mitigate" {
		t.Fatalf("mitigation expected: %+v", risk)
	}
	if _, err := e.Submit(ctx, risk.ID, []ItemInput{{Kind: "action", Data: map[string]any{"key": "ACT-1", "title": "review the note", "for": "RSK-1"}}}); err != nil {
		t.Fatal(err)
	}
	if risk, _ = e.Run(ctx, risk.ID); risk.Status != StatusCompleted {
		t.Fatalf("risks under control: %s %s", risk.Status, risk.Error)
	}
	steps := len(risk.Steps)
	// the change moves: the companion runs again in place, and has nothing to do
	e.Accompany(ctx, TriggerEvent{Type: "process.completed", Process: p})
	e.Drain()
	if risk, _ = e.Store.Get(ctx, risk.ID); risk.Status != StatusCompleted || len(risk.Steps) != steps {
		t.Fatalf("nothing to do: %s %d steps", risk.Status, len(risk.Steps))
	}
	// someone raises a new high risk: the companion wakes up and asks for its mitigation
	items, err := e.Graph.AddItems(ctx, p.ChangeID, []domain.ChangeItem{{Kind: domain.KindRisk, Status: domain.ItemProposed, ProducedBy: "alice",
		Data: map[string]any{"key": "RSK-2", "title": "the reviewer is away", "probability": 3.0, "impact": 5.0}}})
	if err != nil {
		t.Fatal(err)
	}
	bb, _ := e.Graph.Blackboard(ctx, p.ChangeID)
	e.Accompany(ctx, TriggerEvent{Type: "change.item_added", Change: &bb.Change, Items: items})
	e.Drain()
	if risk, _ = e.Store.Get(ctx, risk.ID); risk.Status != StatusWaiting || risk.Pending.Action != "risk_watch/mitigate" {
		t.Fatalf("the new risk needs a mitigation: %s %+v", risk.Status, risk.Pending)
	}
	// its own productions do not wake it, and there is only ever one companion per change and process
	e.Accompany(ctx, TriggerEvent{Type: "process.attached", Process: p})
	e.Drain()
	if cs := companionOf(t, e, p.ChangeID); len(cs) != 1 {
		t.Fatalf("one companion per change: %d", len(cs))
	}
}
