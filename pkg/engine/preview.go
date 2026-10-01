package engine

import (
	"errors"
	"fmt"
	"maps"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/methodology"
)

// ErrLivePlanner is returned by PreviewPlan for a process whose agent plans with a model (llm, llm-scoring): a
// preview driven by condition toggles would need a live model call per toggle, too slow and costly for an
// interactive editor.
var ErrLivePlanner = errors.New("planner calls a model live: no preview")

// PlanStep is one action of a previewed plan.
type PlanStep struct {
	// Name is the planner action name (a step path, ":<alternative>" suffixed for an alternative action); Step is
	// the step path it was generated from, Kind the action kind.
	Name, Step, Kind string
	Cost             float64
}

// PlanPreview is the result of planning a goal from a (possibly overridden) world state: the methodology editor's
// insight into the A*/hybrid/utility planner actually configured for an agent (the generated agent of a process -
// always goap, ADR 0034 - or a declared one a step runs by name, which may be hybrid or utility), without running
// it on a real change.
type PlanPreview struct {
	Agent, Goal string
	Planner     string
	// Reached: the goal already holds in the given world; Actions is then empty.
	Reached bool
	Actions []PlanStep
	Cost    float64
	// Awaiting lists the conditions missing that no action of this agent establishes ("name" / "!name"), set only
	// when no plan reaches the goal (ADR 0036 §3's distinction between waiting and stuck).
	Awaiting []string
}

// PreviewPlan plans toward goal from bb's conditions, overridden by the given names, using the planner agent is
// actually configured with (goap, utility or hybrid). For a process, pass its name as both agent and goal (the
// agent and the goal a process compiles to share its name); for a step naming an agent or a capability, pass the
// step's Agent/Goal (or the chosen method's ActorAgent/goal) instead - that nested agent, not the process itself,
// is where a hybrid or utility planner actually runs. bb may be zero-valued: previewing needs no live Change, the
// same way a process with no change is observed (Engine.observe).
func PreviewPlan(m *methodology.Compiled, bb domain.Blackboard, agent, goalName string, overrides map[string]bool) (*PlanPreview, error) {
	ag, ok := m.Agent(agent)
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", agent)
	}
	goal, ok := m.Goal(goalName)
	if !ok {
		return nil, fmt.Errorf("unknown goal %q", goalName)
	}
	res := m.Conditions.Evaluate(bb)
	world := goap.WorldState(maps.Clone(res.State))
	for k, v := range overrides {
		world[k] = v
	}
	actions := m.AgentActions(ag)
	planningGoal := goal.PlanningGoal()
	plannerKind := ag.Planner
	if plannerKind == "" {
		plannerKind = methodology.PlannerGOAP
	}
	out := &PlanPreview{Agent: ag.Name, Goal: goal.Name, Planner: plannerKind}
	if world.Satisfies(planningGoal.Pre) {
		out.Reached = true
		return out, nil
	}
	var plan *goap.Plan
	var err error
	switch ag.Planner {
	case "", methodology.PlannerGOAP:
		plan, err = goap.Planner{}.Plan(world, actions, planningGoal)
	case methodology.PlannerUtility:
		plan, err = utilityPlan(world, actions, planningGoal, m.Utilities(bb))
	case methodology.PlannerHybrid:
		plan, err = reweightPlan(goap.Planner{}, world, actions, planningGoal, m.Utilities(bb))
	case methodology.PlannerLLM, methodology.PlannerLLMScoring:
		return nil, ErrLivePlanner
	default:
		return nil, fmt.Errorf("unknown planner %q", ag.Planner)
	}
	if errors.Is(err, goap.ErrNoPlan) {
		out.Awaiting = (&Engine{}).awaited(world, actions, actions, planningGoal)
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, a := range plan.Actions {
		full, _ := m.Action(a.Name)
		out.Actions = append(out.Actions, PlanStep{Name: a.Name, Step: full.Step, Kind: full.Kind, Cost: a.Cost})
	}
	out.Cost = plan.Cost
	return out, nil
}
