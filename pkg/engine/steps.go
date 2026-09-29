package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/methodology"
)

// RunStep is the builtin process.step (ADR 0034): the action generated for a step of a process done by an agent or a
// nested process. It starts that agent (towards the step's goal) or the agent of the nested process as a sub-agent on
// the same change and waits for it; once it has completed, a step_done artifact records the step. A sub-agent that
// ends stuck or failed fails the step. Params: step (path), agent, goal, methodology (empty: this one).
func RunStep(ctx context.Context, ac ActionContext) (ActionResult, error) {
	h := ac.Host
	if h == nil || ac.Process.ChangeID == "" {
		return ActionResult{}, errors.New("process.step needs a process on a change")
	}
	step, _ := ac.Action.Params["step"].(string)
	agent, _ := ac.Action.Params["agent"].(string)
	goal, _ := ac.Action.Params["goal"].(string)
	methodologyName, _ := ac.Action.Params["methodology"].(string)
	if methodologyName == "" {
		methodologyName = ac.Process.Methodology
	}
	if step == "" || agent == "" || goal == "" {
		return ActionResult{}, fmt.Errorf("process.step %s: step, agent and goal are required", ac.Action.Name)
	}
	intent := ac.Action.Description
	if intent == "" {
		intent = "step " + step
	}
	if ac.Blackboard.Change.Intent != "" {
		intent += "\n\n(change: " + ac.Blackboard.Change.Intent + ")"
	}
	res, err := h.e.runChildStep(authz.With(ctx, ac.Process.Initiator), h, ac.Action.Name+"#step", methodologyName, agent, goal, intent, false, ac.Step)
	if errors.Is(err, dsl.ErrSuspended) {
		return ActionResult{Suspended: true, Child: h.waitingOn, Output: fmt.Sprintf("step %s: %s/%s at work", step, methodologyName, agent)}, nil
	}
	if err != nil {
		return ActionResult{}, fmt.Errorf("step %s: %w", step, err)
	}
	if res.Status != string(StatusCompleted) {
		return ActionResult{}, fmt.Errorf("step %s: %s/%s ended %s (process %s)", step, methodologyName, agent, res.Status, res.ProcessID)
	}
	return ActionResult{
		Items: []ItemInput{{Kind: string(domain.KindArtifact), Type: methodology.ArtifactStepDone,
			Data: map[string]any{"step": step, "process": res.ProcessID, "agent": agent, "goal": goal}}},
		Output: fmt.Sprintf("step %s done by %s/%s (process %s)", step, methodologyName, agent, res.ProcessID),
	}, nil
}

// StepContext is the step of a process an action or a human task carries out (ADR 0034, ADR 0035 §2): what it is for
// and how to go about it, for the person doing it or the agent.
type StepContext struct {
	Process      string                  `json:"process"`
	Path         string                  `json:"path"`
	Name         string                  `json:"name"`
	Description  string                  `json:"description,omitempty"`
	Guidance     string                  `json:"guidance,omitempty"`
	Checklist    []string                `json:"checklist,omitempty"`
	Deliverables []string                `json:"deliverables,omitempty"`
	References   []methodology.Reference `json:"references,omitempty"`
}

// stepContext is the step an action of p carries out: the step it was generated for, else the step p itself carries
// out for its parent (nil: none).
func stepContext(m *methodology.Compiled, p *Process, a methodology.Action) *StepContext {
	if a.Step == "" {
		return p.Step
	}
	s, ok := m.StepByPath(a.Step)
	if !ok {
		return p.Step
	}
	process, _, _ := strings.Cut(s.Path, "/")
	return &StepContext{Process: process, Path: s.Path, Name: s.Name, Description: s.Description, Guidance: s.Guidance,
		Checklist: s.Checklist, Deliverables: s.Deliverables, References: s.References}
}

// section is the step as the system prompt of an LLM action tells it ("" when there is none).
func (s *StepContext) section() string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nYou are carrying out the step %q of the process %q", s.Path, s.Process)
	if s.Description != "" {
		fmt.Fprintf(&b, ": %s", s.Description)
	}
	b.WriteString(".\n")
	if s.Guidance != "" {
		fmt.Fprintf(&b, "Guidance for this step:\n%s\n", strings.TrimSpace(s.Guidance))
	}
	if len(s.Deliverables) > 0 {
		fmt.Fprintf(&b, "Deliverables of this step: %s.\n", strings.Join(s.Deliverables, ", "))
	}
	if len(s.References) > 0 {
		b.WriteString("Reference documents of this step:\n")
		for _, r := range s.References {
			fmt.Fprintf(&b, "- %s\n", r.String())
		}
	}
	return b.String()
}
