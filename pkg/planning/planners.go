package planning

import (
	"math"
	"slices"
	"sort"

	"github.com/zimwip/goap/pkg/goap"
)

// UtilityPlan picks the one admissible action of highest utility (cost, then name, break the ties): the utility
// planner plans one step at a time.
func UtilityPlan(world goap.WorldState, actions []goap.Action, goal goap.Goal, utilities map[string]float64) (*goap.Plan, error) {
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

// ReweightPlan divides each action's cost by its utility (useless actions, utility
// <= 0, are excluded) and runs A* over the reweighted set. It is shared by hybrid
// (utilities from CEL) and llm-scoring (utilities from an LLM call).
func ReweightPlan(planner goap.Planner, world goap.WorldState, actions []goap.Action, goal goap.Goal, utilities map[string]float64) (*goap.Plan, error) {
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
	return planner.Plan(world, kept, goal)
}

// Awaited tells a process that waits from one that is stuck, when no plan reaches its goal (ADR 0036 §3). A condition
// is established outside the process when no action of its agent (all, admissible or not) has it as an effect with the
// value required: another process, a person or the state of the change establishes it (risks_under_control, a flow
// approved). If assuming those conditions lets the admissible actions reach the goal, the process waits for them:
// Awaited returns those the plan then relies on ("name" when expected true, "!name" when expected false). Otherwise
// (an action of the agent is disabled or unbound, the methodology cannot reach the goal) it returns nil: the process
// is stuck.
func Awaited(planner goap.Planner, world goap.WorldState, all, admissible []goap.Action, goal goap.Goal) []string {
	produced := map[string]map[bool]bool{}
	for _, a := range all {
		for k, v := range a.Effects {
			if produced[k] == nil {
				produced[k] = map[bool]bool{}
			}
			produced[k][v] = true
		}
	}
	wanted := map[string]map[bool]bool{}
	want := func(req map[string]bool) {
		for k, v := range req {
			if have, ok := world[k]; (ok && have == v) || produced[k][v] {
				continue
			}
			if wanted[k] == nil {
				wanted[k] = map[bool]bool{}
			}
			wanted[k][v] = true
		}
	}
	want(goal.Pre)
	for _, a := range admissible {
		want(a.Pre)
	}
	relaxed := world.Apply(nil)
	external := map[string]bool{}
	for k, vs := range wanted {
		if len(vs) != 1 {
			continue // required both ways: nothing to assume
		}
		for v := range vs {
			relaxed[k], external[k] = v, true
		}
	}
	if len(external) == 0 {
		return nil
	}
	plan, err := planner.Plan(relaxed, admissible, goal)
	if err != nil {
		return nil
	}
	// the external conditions the plan relies on: required by the goal or by one of its actions
	var out []string
	relies := func(req map[string]bool) {
		for k, v := range req {
			if !external[k] || relaxed[k] != v {
				continue
			}
			name := k
			if !v {
				name = "!" + k
			}
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	relies(goal.Pre)
	for _, a := range plan.Actions {
		relies(a.Pre)
	}
	slices.Sort(out)
	return out
}
