package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/methodology"
)

// stagedYAML describes a delivery as a process: a phase with a sub-step done by an action, a step done by an agent
// that plans, and a step that nests another process whose only step is manual (ADR 0034).
const stagedYAML = `
name: staged
version: 1.0.0
namespace: alm
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.type == "note")'}
  - {name: checked, expr: 'artifacts.exists(a, a.type == "check")'}
actions:
  - {name: write_note, kind: human, description: Write the note, effects: {noted: true}}
  - {name: check, kind: human, description: Check the note, pre: {noted: true}, effects: {checked: true}}
goals:
  - {name: check_done, pre: {checked: true}}
agents:
  - {name: checker, actions: [check], goals: [check_done]}
processes:
  - name: sign_off
    steps:
      - {name: sign, instructions: Sign the delivery off}
  - name: delivery
    description: Deliver the change
    steps:
      - name: prepare
        steps:
          - {name: note, action: write_note}
      - {name: verify, agent: checker}
      - {name: approve, process: sign_off}
`

func stagedEngine(t *testing.T) *Engine {
	t.Helper()
	e, _, _ := setup(t)
	m, err := methodology.Parse([]byte(stagedYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(StaticMethodologies)["staged"] = c
	e.Schedule = func(id string) { e.background(func() { _, _ = e.Run(context.Background(), id) }) }
	return e
}

func TestProcessStepsAndNestedProcesses(t *testing.T) {
	ctx := context.Background()
	e := stagedEngine(t)
	p, err := e.Start(ctx, StartRequest{Methodology: "staged", Goal: "delivery", Intent: "deliver the note"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Agent != "delivery" {
		t.Fatalf("the process is run by the agent of its name, got %q", p.Agent)
	}
	p, _ = e.Run(ctx, p.ID)
	// the sub-step done by an action comes first
	if p.Status != StatusWaiting || p.Pending.Kind != TaskInput || p.Pending.Action != "delivery/prepare/note" {
		t.Fatalf("expected the note step, got %s %+v %s", p.Status, p.Pending, p.Error)
	}
	if _, err := e.Submit(ctx, p.ID, []ItemInput{{Kind: "artifact", Type: "note"}}); err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	// the step done by an agent starts it on the same change, towards its goal
	if p.Status != StatusWaiting || p.Pending.Kind != TaskAgent || p.Pending.Action != "delivery/verify" {
		t.Fatalf("expected the agent step, got %s %+v %s", p.Status, p.Pending, p.Error)
	}
	checker, _ := e.Store.Get(ctx, p.Pending.ChildProcessID)
	if checker.Agent != "checker" || checker.Goal != "check_done" || checker.ChangeID != p.ChangeID || checker.Pending == nil || checker.Pending.Action != "check" {
		t.Fatalf("unexpected checker %+v", checker)
	}
	if _, err := e.Submit(ctx, checker.ID, []ItemInput{{Kind: "artifact", Type: "check"}}); err != nil {
		t.Fatal(err)
	}
	e.schedule(checker.ID)
	e.Drain()
	// then the nested process, whose manual step is a human task
	p, _ = e.Store.Get(ctx, p.ID)
	if p.Status != StatusWaiting || p.Pending.Kind != TaskAgent || p.Pending.Action != "delivery/approve" {
		t.Fatalf("expected the nested process, got %s %+v %s", p.Status, p.Pending, p.Error)
	}
	signer, _ := e.Store.Get(ctx, p.Pending.ChildProcessID)
	if signer.Agent != "sign_off" || signer.Pending == nil || signer.Pending.Action != "sign_off/sign" || signer.Pending.Instructions != "Sign the delivery off" {
		t.Fatalf("unexpected nested process %+v", signer)
	}
	if _, err := e.Submit(ctx, signer.ID, nil); err != nil {
		t.Fatal(err)
	}
	e.schedule(signer.ID)
	e.Drain()
	p, _ = e.Store.Get(ctx, p.ID)
	if p.Status != StatusCompleted {
		t.Fatalf("delivery not completed: %s %+v %s", p.Status, p.Pending, p.Error)
	}
	var steps []string
	for _, s := range p.Steps {
		if len(steps) == 0 || steps[len(steps)-1] != s.Action { // a step waiting for its sub-agent runs again once it has ended
			steps = append(steps, s.Action)
		}
	}
	if got := strings.Join(steps, ","); got != "delivery/prepare/note,delivery/verify,delivery/approve" {
		t.Fatalf("steps %s", got)
	}
	bb, _ := e.Graph.Blackboard(ctx, p.ChangeID)
	done := map[string]bool{}
	for _, it := range bb.Change.Items {
		if it.Type == methodology.ArtifactStepDone {
			done[it.Data["step"].(string)] = true
		}
	}
	// the agent step is done by its goal (checked), the nested process by its criteria (its manual step submitted)
	if done["delivery/prepare/note"] || !done["sign_off/sign"] {
		t.Fatalf("only a manual step records step_done when submitted: %v", done)
	}
}
