package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/methodology"
)

// stagedYAML describes a delivery as a process: a phase with a sub-step done by an action, a step done by an agent
// that plans, and a step that nests another process whose only step is manual (ADR 0034). The steps are declared out
// of order: their conditions sequence them.
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
roles:
  - {name: writer, description: Writes the notes}
  - {name: reviewer}
  - {name: signer}
methods:
  - name: peer_check
    for: verification
    when: 'artifacts.exists(a, a.type == "note")'
    priority: 10
    guidance: Check the note with a peer
    references: [{title: Peer review guide, ref: "doc:PEER"}]
    actions: [check]
    done: {checked: true}
  - {name: self_check, for: verification, actions: [check], done: {checked: true}}
processes:
  - name: sign_off
    steps:
      - {name: sign, instructions: Sign the delivery off, roles: {responsible: signer}}
  - name: delivery
    description: Deliver the change
    steps:
      - {name: approve, process: sign_off, pre: {checked: true}}
      - {name: verify, method: verification, pre: {noted: true}}
      - name: prepare
        roles: {responsible: writer, accountable: reviewer}
        steps:
          - {name: note, action: write_note}
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
	p, err := e.Start(ctx, StartRequest{Methodology: "staged", Goal: "delivery", Intent: "deliver the note", ProjectID: testProject})
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
	if c := p.Pending.Context; c == nil || c.Path != "delivery/prepare/note" || c.Process != "delivery" {
		t.Fatalf("a task of a step carries the step: %+v", c)
	}
	pr, err := e.Progress(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	states := progressStates(pr.Steps)
	if states["delivery/prepare"] != StepWaiting || states["delivery/prepare/note"] != StepWaiting || states["delivery/verify"] != StepTodo ||
		states["delivery/approve"] != StepTodo || pr.Done != 0 || pr.Total != 3 {
		t.Fatalf("progress at the start: %v %d/%d", states, pr.Done, pr.Total)
	}
	if v := findStep(pr.Steps, "delivery/verify"); len(v.Missing) != 1 || v.Missing[0] != "noted" {
		t.Fatalf("the verify step misses the note: %+v", v)
	}
	// the task of a step is performed by its responsible role (inherited from the phase), held in the unit of the
	// change or above it
	if c := p.Pending.Context; c.Roles == nil || c.Roles.Responsible != "writer" || c.Roles.Accountable != "reviewer" {
		t.Fatalf("the note step inherits the roles of its phase: %+v", c.Roles)
	}
	outsider := authz.With(ctx, authz.Principal{Subject: "o", Roles: []string{"contributor", "writer@TEAM-OTHER"}})
	if _, err := e.Submit(outsider, p.ID, []ItemInput{{Kind: "artifact", Type: "note"}}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("a person without the responsible role here may not perform the step: %v", err)
	}
	writer := authz.With(ctx, authz.Principal{Subject: "w", Roles: []string{"writer@ORG-DEFAULT"}})
	if _, err := e.Submit(writer, p.ID, []ItemInput{{Kind: "artifact", Type: "note"}}); err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	// the step done by an agent starts it on the same change, towards its goal
	if p.Status != StatusWaiting || p.Pending.Kind != TaskAgent || p.Pending.Action != "delivery/verify" {
		t.Fatalf("expected the agent step, got %s %+v %s", p.Status, p.Pending, p.Error)
	}
	if st := progressStates(mustProgress(t, e, p.ID).Steps); st["delivery/prepare/note"] != StepDone || st["delivery/verify"] != StepActive {
		t.Fatalf("progress with the checker at work: %v", st)
	}
	checker, _ := e.Store.Get(ctx, p.Pending.ChildProcessID)
	if checker.Step == nil || checker.Step.Path != "delivery/verify" || checker.Pending.Context == nil || checker.Pending.Context.Path != "delivery/verify" {
		t.Fatalf("the sub-agent of a step carries the step to its tasks: %+v %+v", checker.Step, checker.Pending)
	}
	// the step names a capability: the applicable method with the highest priority was chosen, and its guidance
	// and references reach the agent that acts
	if c := checker.Pending.Context; c.Method != "peer_check" || !strings.Contains(c.Guidance, "with a peer") || len(c.References) != 1 {
		t.Fatalf("the method's guidance reaches its agent: %+v", c)
	}
	if v := findStep(mustProgress(t, e, p.ID).Steps, "delivery/verify"); v.Chosen != "peer_check" || v.Target != "verification" {
		t.Fatalf("the progress shows the chosen method: %+v", v)
	}
	if checker.Agent != "peer_check" || checker.Goal != "peer_check" || checker.ChangeID != p.ChangeID || checker.Pending == nil || checker.Pending.Action != "check" {
		t.Fatalf("unexpected checker %+v", checker)
	}
	if _, err := e.Submit(ctx, checker.ID, []ItemInput{{Kind: "artifact", Type: "check"}}); err != nil { // the method assigns no role
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
	if _, err := e.Submit(authz.With(ctx, authz.Principal{Subject: "s", Roles: []string{"signer"}}), signer.ID, nil); err != nil {
		t.Fatal(err)
	}
	e.schedule(signer.ID)
	e.Drain()
	p, _ = e.Store.Get(ctx, p.ID)
	if p.Status != StatusCompleted {
		t.Fatalf("delivery not completed: %s %+v %s", p.Status, p.Pending, p.Error)
	}
	if pr := mustProgress(t, e, p.ID); pr.Done != pr.Total || progressStates(pr.Steps)["delivery/prepare"] != StepDone {
		t.Fatalf("progress at the end: %v %d/%d", progressStates(pr.Steps), pr.Done, pr.Total)
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

func mustProgress(t *testing.T, e *Engine, id string) *ProcessProgress {
	t.Helper()
	pr, err := e.Progress(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

func progressStates(steps []StepProgress) map[string]string {
	out := map[string]string{}
	for _, s := range steps {
		out[s.Path] = s.State
		for k, v := range progressStates(s.Steps) {
			out[k] = v
		}
	}
	return out
}

// A capability provided by a method that composes its own steps (architecture plan "Activity concept"): the method
// is run as a sub-agent of its own name, exactly like a nested process, and completing its own steps completes the
// outer step that named the capability.
const methodStepsYAML = `
name: inspected
version: 1.0.0
namespace: alm
conditions:
  - {name: noted, expr: 'artifacts.exists(a, a.type == "note")'}
  - {name: checked, expr: 'artifacts.exists(a, a.type == "check")'}
actions:
  - {name: write_note, kind: human, effects: {noted: true}}
  - {name: check, kind: human, pre: {noted: true}, effects: {checked: true}}
methods:
  - name: thorough_check
    for: verification
    steps:
      - {name: note, action: write_note}
      - {name: verify, action: check}
processes:
  - name: delivery
    steps:
      - {name: verify, method: verification}
`

func TestMethodComposedOfStepsRunsAsASubAgent(t *testing.T) {
	ctx := context.Background()
	e, _, _ := setup(t)
	m, err := methodology.Parse([]byte(methodStepsYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(StaticMethodologies)["inspected"] = c
	p, err := e.Start(ctx, StartRequest{Methodology: "inspected", Goal: "delivery", Intent: "inspect it", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	if p.Status != StatusWaiting || p.Pending.Kind != TaskAgent || p.Pending.Action != "delivery/verify" {
		t.Fatalf("expected the method-capability step, got %s %+v %s", p.Status, p.Pending, p.Error)
	}
	sub, err := e.Store.Get(ctx, p.Pending.ChildProcessID)
	if err != nil {
		t.Fatal(err)
	}
	// the method composing its own steps runs as a sub-agent of its own name, towards a goal of its own name
	if sub.Agent != "thorough_check" || sub.Goal != "thorough_check" {
		t.Fatalf("sub-agent of a steps-composed method: %+v", sub)
	}
	if sub.Pending == nil || sub.Pending.Action != "thorough_check/note" {
		t.Fatalf("the method's own first step: %+v", sub.Pending)
	}
	if _, err := e.Submit(ctx, sub.ID, []ItemInput{{Kind: "artifact", Type: "note"}}); err != nil {
		t.Fatal(err)
	}
	e.schedule(sub.ID)
	e.Drain()
	sub, _ = e.Store.Get(ctx, sub.ID)
	if sub.Pending == nil || sub.Pending.Action != "thorough_check/verify" {
		t.Fatalf("the method's own second step: %+v %s", sub.Pending, sub.Status)
	}
	if _, err := e.Submit(ctx, sub.ID, []ItemInput{{Kind: "artifact", Type: "check"}}); err != nil {
		t.Fatal(err)
	}
	e.schedule(sub.ID)
	e.Drain()
	sub, _ = e.Store.Get(ctx, sub.ID)
	if sub.Status != StatusCompleted {
		t.Fatalf("method sub-agent: %s %+v %s", sub.Status, sub.Pending, sub.Error)
	}
	p, _ = e.Store.Get(ctx, p.ID)
	if p.Status != StatusCompleted {
		t.Fatalf("outer process: %s %+v %s", p.Status, p.Pending, p.Error)
	}
}

func findStep(steps []StepProgress, path string) StepProgress {
	for _, s := range steps {
		if s.Path == path {
			return s
		}
		if f := findStep(s.Steps, path); f.Path != "" {
			return f
		}
	}
	return StepProgress{}
}

// The brief of a step tells the agent instance its role and that it starts fresh (ADR 0050).
func TestStepSectionTellsRoleAndFreshStart(t *testing.T) {
	sc := &StepContext{Process: "p", Path: "p/s", Roles: &methodology.Responsibilities{Responsible: "builder"}}
	got := sc.section()
	if !strings.Contains(got, `role "builder"`) || !strings.Contains(got, "start this step fresh") {
		t.Fatalf("section: %s", got)
	}
}
