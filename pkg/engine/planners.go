package engine

import (
	"fmt"
	"math"
	"sort"

	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/methodology"
)

// plan returns the next actions according to the agent's planner:
//   - goap: cheapest action sequence reaching the goal (A*);
//   - utility: the applicable action with the highest utility (no lookahead);
//   - hybrid: A* where each cost is divided by the action utility, so that
//     useful actions are preferred while still reaching the goal.
func (e *Engine) plan(planner string, world goap.WorldState, actions []goap.Action, goal goap.Goal, utilities map[string]float64) (*goap.Plan, error) {
	switch planner {
	case "", methodology.PlannerGOAP:
		return e.Planner.Plan(world, actions, goal)
	case methodology.PlannerUtility:
		return utilityPlan(world, actions, goal, utilities)
	case methodology.PlannerHybrid:
		weighted := make([]goap.Action, len(actions))
		for i, a := range actions {
			u := utilities[a.Name]
			if u <= 0 {
				continue // useless actions are excluded
			}
			cost := a.Cost
			if cost <= 0 {
				cost = 1
			}
			a.Cost = cost / math.Max(u, 0.01)
			weighted[i] = a
		}
		var kept []goap.Action
		for _, a := range weighted {
			if a.Name != "" {
				kept = append(kept, a)
			}
		}
		return e.Planner.Plan(world, kept, goal)
	}
	return nil, fmt.Errorf("unknown planner %q", planner)
}

func utilityPlan(world goap.WorldState, actions []goap.Action, goal goap.Goal, utilities map[string]float64) (*goap.Plan, error) {
	if world.Satisfies(goal.Pre) {
		return &goap.Plan{Goal: goal}, nil
	}
	var candidates []goap.Action
	for _, a := range actions {
		if world.Satisfies(a.Pre) && utilities[a.Name] > 0 && !world.Satisfies(a.Effects) {
			candidates = append(candidates, a)
		}
	}
	if len(candidates) == 0 {
		return nil, goap.ErrNoPlan
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		ui, uj := utilities[candidates[i].Name], utilities[candidates[j].Name]
		if ui != uj {
			return ui > uj
		}
		if candidates[i].Cost != candidates[j].Cost {
			return candidates[i].Cost < candidates[j].Cost
		}
		return candidates[i].Name < candidates[j].Name
	})
	best := candidates[0]
	return &goap.Plan{Goal: goal, Actions: []goap.Action{best}, Cost: best.Cost}, nil
}
