package enginee2e

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/zimwip/goap/engine/engine"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	rsk "github.com/zimwip/goap/contracts/risk"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
)

// riskyYAML is a transverse methodology (ADR 0036 §3): it applies to staged, so its process runs alongside the
// changes of staged, on the same change.
const riskyYAML = `
name: risky
version: 1.0.0
appliesTo: [staged]
imports: [risks]
on:
  - {event: process.attached}
  - {event: step.completed}
  - {event: change.item_added, filter: 'event.items.exists(i, i.type == "risk" || i.type == "action")'}
conditions:
  - {name: risks_identified, expr: 'artifacts.exists(a, a.target == "risk_review")'}
actions:
  - {name: identify, kind: human, description: Identify the risks, effects: {risks_identified: true}}
  - {name: mitigate, kind: human, description: Define the mitigation actions, pre: {risks_identified: true}, effects: {risks_under_control: true}}
roles: [{name: risk_manager}]
processes:
  - name: risk_watch
    roles: {responsible: risk_manager}
    steps:
      - {name: identify, action: identify}
      - {name: mitigate, action: mitigate}
`

// riskManager performs the steps of risk_watch: the role responsible for the process (ADR 0139).
var riskManager = authz.With(context.Background(), authz.Principal{Subject: "rm", Org: "acme", Roles: []string{"risk_manager"}})

func companionOf(t *testing.T, e *engine.Engine, change domain.ChangeID) []*engine.Process {
	t.Helper()
	all, err := e.Store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []*engine.Process
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
	e.Methodologies.(engine.StaticMethodologies)["risky"] = c

	p, err := e.Start(ctx, engine.StartRequest{Methodology: "staged", Goal: "delivery", Intent: "deliver the note", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	// the change of staged is created: the risk process joins it
	e.Accompany(ctx, engine.TriggerEvent{Type: "process.attached", Process: p})
	e.Drain()
	cs := companionOf(t, e, p.ChangeID)
	if len(cs) != 1 || cs[0].Methodology != "risky" || cs[0].Agent != "risk_watch" || cs[0].Pending == nil || cs[0].Pending.Action != "risk_watch/identify" {
		t.Fatalf("companion %+v", cs)
	}
	risk := cs[0]
	// the risks are identified: a high one needs an action
	if _, err := e.Submit(riskManager, risk.ID, []engine.ItemInput{{Type: "artifact", Target: "risk_review"},
		{Type: "risk", Data: map[string]any{"key": "RSK-1", "title": "the note is wrong", "probability": 4.0, "impact": 4.0}}}); err != nil {
		t.Fatal(err)
	}
	if risk, _ = e.Run(ctx, risk.ID); risk.Pending == nil || risk.Pending.Action != "risk_watch/mitigate" {
		t.Fatalf("mitigation expected: %+v", risk)
	}
	if _, err := e.Submit(riskManager, risk.ID, []engine.ItemInput{{Type: "action", Data: map[string]any{"key": "ACT-1", "title": "review the note", "for": "RSK-1"}}}); err != nil {
		t.Fatal(err)
	}
	if risk, _ = e.Run(ctx, risk.ID); risk.Status != engine.StatusCompleted {
		t.Fatalf("risks under control: %s %s", risk.Status, risk.Error)
	}
	steps := len(risk.Steps)
	// the change moves: the companion runs again in place, and has nothing to do
	e.Accompany(ctx, engine.TriggerEvent{Type: "process.completed", Process: p})
	e.Drain()
	if risk, _ = e.Store.Get(ctx, risk.ID); risk.Status != engine.StatusCompleted || len(risk.Steps) != steps {
		t.Fatalf("nothing to do: %s %d steps", risk.Status, len(risk.Steps))
	}
	// someone raises a new high risk: the companion wakes up and asks for its mitigation
	items, err := e.Graph.AddItems(ctx, p.ChangeID, []domain.ChangeItem{{Type: rsk.TypeRisk, Status: domain.ItemProposed, ProducedBy: "alice",
		Data: map[string]any{"key": "RSK-2", "title": "the reviewer is away", "probability": 3.0, "impact": 5.0}}})
	if err != nil {
		t.Fatal(err)
	}
	bb, _ := e.Graph.Blackboard(ctx, p.ChangeID)
	e.Accompany(ctx, engine.TriggerEvent{Type: "change.item_added", Change: &bb.Change, Items: items})
	e.Drain()
	if risk, _ = e.Store.Get(ctx, risk.ID); risk.Status != engine.StatusWaiting || risk.Pending.Action != "risk_watch/mitigate" {
		t.Fatalf("the new risk needs a mitigation: %s %+v", risk.Status, risk.Pending)
	}
	// its own productions do not wake it, and there is only ever one companion per change and process
	e.Accompany(ctx, engine.TriggerEvent{Type: "process.attached", Process: p})
	e.Drain()
	if cs := companionOf(t, e, p.ChangeID); len(cs) != 1 {
		t.Fatalf("one companion per change: %d", len(cs))
	}
}

// choreography of two methodologies through the events of the change they share (ADR 0036 §3): the risk watch reviews
// the risks at each step of the flow (the events that arrive while it works wait in its inbox), and the flow's approval
// waits for the risks to be under control — it is tried again when the watch has mitigated them.
const choreoYAML = `
name: choreo
version: 1.0.0
namespace: alm
imports: [risks]
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.target == "note")'}
actions:
  - {name: write_note, kind: human, effects: {noted: true}}
processes:
  - name: flow
    steps:
      - {name: note, action: write_note}
      - {name: approve, instructions: Approve the delivery, pre: {noted: true, risks_under_control: true}}
`

const watchYAML = `
name: watch
version: 1.0.0
appliesTo: [choreo]
imports: [risks]
on:
  - {event: process.attached}
  - {event: step.completed, filter: 'event.step.process == "flow"'}
conditions:
  # reviewed for the event that woke the watch: a review written after it
  - {name: reviewed, expr: 'artifacts.exists(a, a.target == "risk_review" && has(vars.event) && double(a.at) >= double(vars.event.at))'}
actions:
  - {name: review, kind: human, effects: {reviewed: true}}
  - {name: mitigate, kind: human, pre: {reviewed: true}, effects: {risks_under_control: true}}
processes:
  - name: risk_watch
    steps:
      - {name: review, action: review}
      - {name: mitigate, action: mitigate}
`

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

func TestChoreographyThroughTheEventsOfTheChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := stagedEngine(t)
	e.Schedule = nil // runs in the background, as the service does
	for _, y := range []string{choreoYAML, watchYAML} {
		m, err := methodology.Parse([]byte(y))
		if err != nil {
			t.Fatal(err)
		}
		c, err := m.Compile()
		if err != nil {
			t.Fatal(err)
		}
		e.Methodologies.(engine.StaticMethodologies)[c.Definition().Name] = c
	}
	broker := engine.NewBroker()
	e.Events = broker
	tm := &engine.TriggerManager{Engine: e}
	go tm.WatchProcesses(ctx, broker)
	time.Sleep(20 * time.Millisecond) // subscription ready

	p, err := e.Start(ctx, engine.StartRequest{Methodology: "choreo", Goal: "flow", Intent: "deliver", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	engine.InternalsOf(e).Schedule(p.ID)
	get := func(id string) *engine.Process { q, _ := e.Store.Get(ctx, id); return q }
	var watch *engine.Process
	eventually(t, "the watch joins the change and asks for a review", func() bool {
		cs := companionOf(t, e, p.ChangeID)
		if len(cs) == 1 && cs[0].Pending != nil && cs[0].Pending.Action == "risk_watch/review" {
			watch = cs[0]
			return true
		}
		return false
	})
	// the review raises a high risk: its mitigation is asked
	if _, err := e.Submit(riskManager, watch.ID, []engine.ItemInput{{Type: "artifact", Target: "risk_review"},
		{Type: "risk", Data: map[string]any{"key": "RSK-1", "title": "wrong note", "probability": 4.0, "impact": 4.0}}}); err != nil {
		t.Fatal(err)
	}
	engine.InternalsOf(e).Schedule(watch.ID)
	eventually(t, "mitigation asked", func() bool { w := get(watch.ID); return w.Pending != nil && w.Pending.Action == "risk_watch/mitigate" })
	eventually(t, "the flow asks for the note", func() bool { q := get(p.ID); return q.Pending != nil && q.Pending.Action == "flow/note" })

	// the flow completes its note step: the event waits in the watch's inbox; the approval waits for the risks
	if _, err := e.Submit(ctx, p.ID, []engine.ItemInput{{Type: "artifact", Target: "note"}}); err != nil {
		t.Fatal(err)
	}
	engine.InternalsOf(e).Schedule(p.ID)
	eventually(t, "the flow waits for the risks to be under control and the watch has the step in its inbox", func() bool {
		return get(p.ID).WaitsForConditions() && len(get(watch.ID).Inbox) == 1
	})
	if q := get(p.ID); !slices.Equal(q.Pending.Conditions, []string{"risks_under_control"}) {
		t.Fatalf("waiting for %v", q.Pending.Conditions)
	}
	if st := findStep(mustProgress(t, e, p.ID).Steps, "flow/approve"); st.State != engine.StepWaiting || st.Waiting != engine.TaskCondition ||
		!slices.Contains(st.Missing, "risks_under_control") {
		t.Fatalf("approval: %+v", st)
	}

	// the watch mitigates: the flow is tried again and reaches its approval; the watch reviews the risks of the step
	if _, err := e.Submit(riskManager, watch.ID, []engine.ItemInput{{Type: "action", Data: map[string]any{"key": "ACT-1", "title": "check the note", "for": "RSK-1"}}}); err != nil {
		t.Fatal(err)
	}
	engine.InternalsOf(e).Schedule(watch.ID)
	eventually(t, "the flow resumes to its approval", func() bool { q := get(p.ID); return q.Pending != nil && q.Pending.Action == "flow/approve" })
	eventually(t, "the watch reviews the risks of the step that completed", func() bool {
		w := get(watch.ID)
		ev, _ := w.Vars["event"].(map[string]any)
		step, _ := ev["step"].(map[string]any)
		return w.Pending != nil && w.Pending.Action == "risk_watch/review" && step["path"] == "flow/note" && len(w.Inbox) == 0
	})
	if cs := companionOf(t, e, p.ChangeID); len(cs) != 1 {
		t.Fatalf("one watch per change: %d", len(cs))
	}
}

// a step waiting for the changes its change depends on (ADR 0108): no action produces `dependencies_met`, so the run
// waits; the dependency merging wakes it (Engine.relay to the dependents).
const dependentYAML = `
name: dependent
version: 1.0.0
namespace: alm
imports: [dependencies]
processes:
  - name: flow
    steps:
      - {name: deliver, instructions: Deliver once the changes it depends on have landed, pre: {dependencies_met: true}}
`

func TestRunWaitsForTheChangesItDependsOn(t *testing.T) {
	ctx := context.Background()
	e := stagedEngine(t)
	g := e.Graph.(*graph.Graph)
	m, err := methodology.Parse([]byte(dependentYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(engine.StaticMethodologies)[c.Definition().Name] = c
	head, err := g.BranchHead(ctx, "alm", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	dep, err := g.CreateChange(ctx, graph.NewChange{Title: "first", Methodology: "dependent", Namespace: "alm", ProjectID: testProject,
		BaselineID: head.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Start(ctx, engine.StartRequest{Methodology: "dependent", Goal: "flow", Intent: "deliver after the first", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.SetChangeDependencies(ctx, p.ChangeID, []domain.ChangeID{dep.ID}); err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Run(ctx, p.ID); !p.WaitsForConditions() || !slices.Equal(p.Pending.Conditions, []string{"dependencies_met"}) {
		t.Fatalf("the run waits for its dependency: %s %+v", p.Status, p.Pending)
	}
	// the first change lands: it modifies a component
	cn, err := g.ImpactNodeCheckout(ctx, dep.ID, graph.NodeCheckout{Key: "CMP-1", Rationale: "rename"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.ImpactNodeUpdate(ctx, dep.ID, cn.ID, graph.NodeUpdate{Properties: map[string]any{"title": "renamed"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ImpactNodeReview(ctx, dep.ID, cn.ID, domain.ReviewAccepted, "u", "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, dep.ID, ""); err != nil {
		t.Fatal(err)
	}
	applied, err := g.Change(ctx, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.Accompany(ctx, engine.TriggerEvent{Type: "change.applied", Change: &applied})
	e.Drain()
	if p, _ = e.Store.Get(ctx, p.ID); p.Pending == nil || p.Pending.Action != "flow/deliver" {
		t.Fatalf("the dependency landed: the run resumes: %s %+v", p.Status, p.Pending)
	}
}
