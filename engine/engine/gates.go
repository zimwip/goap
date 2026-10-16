package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/engine/methodology"
)

// The builtins of a gate step (ADR 0132): gate.open opens the decision of the gate once its vetos hold, gate.pass
// takes its transition once the decision said go. The decision itself is ruled by people (the board, its chair) or
// by the decision method of the gate; the graph checks the criteria of the transition again when it is taken.

// gateOf is the gate a builtin of a gate step acts for (params.gate), with the change it runs on.
func gateOf(ctx context.Context, ac ActionContext) (methodology.GateInfo, error) {
	if ac.Host == nil || ac.Process == nil || ac.Process.ChangeID == "" {
		return methodology.GateInfo{}, errors.New("a gate needs a process on a change")
	}
	path, _ := ac.Action.Params["gate"].(string)
	m, err := ac.Host.e.methodologyOf(ctx, ac.Process)
	if err != nil {
		return methodology.GateInfo{}, err
	}
	g, ok := m.GateAt(path)
	if !ok {
		return methodology.GateInfo{}, fmt.Errorf("methodology %s has no gate %q", m.Name(), path)
	}
	return g, nil
}

// gatePoints are the decision points of a gate the moves of the change have not consumed yet, in order.
func gatePoints(bb domain.Blackboard, path string) []domain.DecisionPoint {
	consumed := bb.Change.ConsumedDecisions()
	var out []domain.DecisionPoint
	for _, d := range domain.DecisionPointsOf(bb) {
		if gate, _ := d.Policy["gate"].(string); gate == path && !slices.Contains(consumed, d.ID) {
			out = append(out, d)
		}
	}
	return out
}

// GateOpen opens the decision of a gate: a decision point asking its question among go, no go and (when the gate
// names one) back, with the policy of the gate (its board, its rule, its chair). A point already open is kept. A
// decided back sends the change back by its transition first and opens nothing: the gates before it are to pass again.
func GateOpen(ctx context.Context, ac ActionContext) (ActionResult, error) {
	g, err := gateOf(ctx, ac)
	if err != nil {
		return ActionResult{}, err
	}
	for _, d := range gatePoints(ac.Blackboard, g.Path) {
		switch {
		case d.Status != domain.PointDecided:
			return ActionResult{Output: fmt.Sprintf("the decision of the gate %s is open (%s)", g.Path, d.ID)}, nil
		case d.Option == methodology.OptionBack && g.Back != "":
			if _, err := ac.Graph.TransitionChange(ctx, ac.Process.ChangeID, graphapi.TransitionRequest{Transition: g.Back, Decision: d.ID, By: ac.Action.Name}); err != nil {
				return ActionResult{}, fmt.Errorf("gate %s: back by %s: %w", g.Path, g.Back, err)
			}
			return ActionResult{Output: fmt.Sprintf("the decision of the gate %s sent the change back (%s)", g.Path, g.Back),
				Items: []ItemInput{{Type: string(domain.TypeArtifact), Target: "gate:" + g.Path,
					Data: map[string]any{"gate": g.Path, "decision": d.ID, "option": d.Option, "transition": g.Back}}}}, nil
		}
	}
	if slices.Contains(ac.Blackboard.Change.Passed(), g.Transition) {
		return ActionResult{Output: fmt.Sprintf("the gate %s is passed (%s)", g.Path, ac.Blackboard.Change.State)}, nil
	}
	question := g.Question
	if question == "" {
		question = fmt.Sprintf("Go for %s?", g.Transition)
	}
	d, err := ac.Graph.OpenDecision(ctx, ac.Process.ChangeID, graphapi.OpenDecisionRequest{Question: question, Options: []string{}, Choices: g.Options(),
		Criteria: gateCriteria(g), Policy: g.Policy(), By: ac.Action.Name})
	if err != nil {
		return ActionResult{}, fmt.Errorf("gate %s: %w", g.Path, err)
	}
	return ActionResult{Output: fmt.Sprintf("the decision of the gate %s is open (%s)", g.Path, d.ID)}, nil
}

// gateCriteria are the items of the checklist, as the criteria the decision weighs.
func gateCriteria(g methodology.GateInfo) []string {
	out := make([]string, 0, len(g.Items))
	for _, it := range g.Items {
		out = append(out, it.Name)
	}
	return out
}

// GatePass takes the transition of a gate with the decided go of its decision: the graph checks the criteria of the
// transition (the checklist of the gate merged into it) and records what the go freezes.
func GatePass(ctx context.Context, ac ActionContext) (ActionResult, error) {
	g, err := gateOf(ctx, ac)
	if err != nil {
		return ActionResult{}, err
	}
	for _, d := range gatePoints(ac.Blackboard, g.Path) {
		if d.Status != domain.PointDecided || d.Option != methodology.OptionGo {
			continue
		}
		c, err := ac.Graph.TransitionChange(ctx, ac.Process.ChangeID, graphapi.TransitionRequest{Transition: g.Transition, Decision: d.ID, By: ac.Action.Name})
		if err != nil {
			return ActionResult{}, fmt.Errorf("gate %s: %w", g.Path, err)
		}
		return ActionResult{Output: fmt.Sprintf("the gate %s is passed: %s, the change is %s", g.Path, g.Transition, c.State)}, nil
	}
	return ActionResult{}, fmt.Errorf("gate %s: no decided go to pass it with", g.Path)
}
