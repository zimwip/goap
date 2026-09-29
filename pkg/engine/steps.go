package engine

import (
	"context"
	"errors"
	"fmt"

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
	res, err := h.e.runChildGoal(authz.With(ctx, ac.Process.Initiator), h, ac.Action.Name+"#step", methodologyName, agent, goal, intent, false)
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
