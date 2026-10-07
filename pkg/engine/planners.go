package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

// ErrPlannerAnswer flags an llm/llm-scoring planner answer that names an action
// outside the candidate set, or picks one whose preconditions are not met, or is
// otherwise unusable: the process fails (StatusFailed) rather than silently
// proceeding or being mistaken for goap.ErrNoPlan.
var ErrPlannerAnswer = errors.New("planner: invalid LLM answer")

// plan returns the next actions according to the agent's planner:
//   - goap: cheapest action sequence reaching the goal (A*);
//   - utility: the applicable action with the highest utility (no lookahead);
//   - hybrid: A* where each cost is divided by the action utility (from CEL), so
//     that useful actions are preferred while still reaching the goal;
//   - llm: the LLM picks the next action directly;
//   - llm-scoring: the LLM scores utility; A* costs are weighted by it, like hybrid.
//
// It also returns the LLM calls made while planning (llm/llm-scoring only), for the
// caller to record on the tick's journal entry and, when a step follows, on it too.
func (e *Engine) plan(ctx context.Context, m *methodology.Compiled, ag methodology.Agent, world goap.WorldState, actions []goap.Action, goal goap.Goal, utilities map[string]float64) (*goap.Plan, []LLMCall, error) {
	switch ag.Planner {
	case "", methodology.PlannerGOAP:
		p, err := e.Planner.Plan(world, actions, goal)
		return p, nil, err
	case methodology.PlannerUtility:
		p, err := utilityPlan(world, actions, goal, utilities)
		return p, nil, err
	case methodology.PlannerHybrid:
		p, err := reweightPlan(e.Planner, world, actions, goal, utilities)
		return p, nil, err
	case methodology.PlannerLLM:
		return e.llmPlan(ctx, m, ag, world, actions, goal)
	case methodology.PlannerLLMScoring:
		u, calls, err := e.llmUtilities(ctx, m, ag, world, actions, goal)
		if err != nil {
			return nil, calls, err
		}
		p, err := reweightPlan(e.Planner, world, actions, goal, u)
		return p, calls, err
	}
	return nil, nil, fmt.Errorf("unknown planner %q", ag.Planner)
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

// reweightPlan divides each action's cost by its utility (useless actions, utility
// <= 0, are excluded) and runs A* over the reweighted set. It is shared by hybrid
// (utilities from CEL) and llm-scoring (utilities from an LLM call).
func reweightPlan(planner goap.Planner, world goap.WorldState, actions []goap.Action, goal goap.Goal, utilities map[string]float64) (*goap.Plan, error) {
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

// planRecorder wraps an llm.Client to record its calls the way Host.completeLLM
// does, for planning calls made before any Host exists (planning runs ahead of
// action execution, which is when a Host is created).
type planRecorder struct {
	client llm.Client
	calls  []LLMCall
}

func (r *planRecorder) complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if r.client == nil {
		return llm.Response{}, errors.New("no model gateway configured")
	}
	meta := llm.MetaFrom(ctx) // stamped by planStep: the call is the next of the step it chooses (ADR 0089)
	meta.Call = len(r.calls)
	start := time.Now()
	resp, err := r.client.Complete(llm.WithMeta(ctx, meta), req)
	call := LLMCall{Provider: resp.Provider, Model: resp.Model, InputTokens: int64(resp.Usage.InputTokens),
		OutputTokens: int64(resp.Usage.OutputTokens), DurationMs: time.Since(start).Milliseconds(), Exchange: exchangeOf(req, resp)}
	if call.Model == "" {
		call.Model = req.Model
	}
	if err != nil {
		call.Error = err.Error()
	}
	r.calls = append(r.calls, call)
	return resp, err
}

// llmPlanAction describes one candidate action for the llm/llm-scoring prompts.
type llmPlanAction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Pre         map[string]bool `json:"pre,omitempty"`
	Effects     map[string]bool `json:"effects,omitempty"`
	Cost        float64         `json:"cost,omitempty"`
}

type llmPlanGoal struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Pre         map[string]bool `json:"pre"`
}

type llmPlanRequest struct {
	World   map[string]bool `json:"world"`
	Goal    llmPlanGoal     `json:"goal"`
	Actions []llmPlanAction `json:"actions"`
}

func buildLLMPlanRequest(m *methodology.Compiled, world goap.WorldState, actions []goap.Action, goal goap.Goal) llmPlanRequest {
	req := llmPlanRequest{World: map[string]bool(world), Goal: llmPlanGoal{Name: goal.Name, Pre: goal.Pre}}
	if g, ok := m.Goal(goal.Name); ok {
		req.Goal.Description = g.Description
	}
	for _, a := range actions {
		info := llmPlanAction{Name: a.Name, Pre: a.Pre, Effects: a.Effects, Cost: a.Cost}
		if full, ok := m.Action(a.Name); ok {
			info.Description = full.Description
		}
		req.Actions = append(req.Actions, info)
	}
	return req
}

const llmPlannerSystem = `You are the planner of an autonomous GOAP agent. Given the current world state ` +
	`(booleans), a goal (preconditions to reach) and the available actions (name, description, preconditions, ` +
	`effects, cost), answer ONLY with a JSON object {"action": "<name>", "reason": "<why, briefly>"} naming ` +
	`exactly one action from the given list whose preconditions the world state already satisfies and that makes ` +
	`progress toward the goal. If no such action exists, answer {"action": "", "reason": "<why>"}. Never invent ` +
	`an action name that is not in the list.`

type llmPlanResponse struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

// llmPlan asks the LLM (via the agent's model alias) to pick the next action directly.
func (e *Engine) llmPlan(ctx context.Context, m *methodology.Compiled, ag methodology.Agent, world goap.WorldState, actions []goap.Action, goal goap.Goal) (*goap.Plan, []LLMCall, error) {
	rec := &planRecorder{client: e.LLM}
	req := buildLLMPlanRequest(m, world, actions, goal)
	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("llm planner: %w", err)
	}
	resp, err := rec.complete(ctx, llm.Request{Model: ag.Model, System: llmPlannerSystem, JSON: true, MaxTokens: 1000,
		Messages: []llm.Message{{Role: "user", Content: string(body)}}})
	if err != nil {
		return nil, rec.calls, fmt.Errorf("llm planner: %w", err)
	}
	var out llmPlanResponse
	if err := llm.DecodeJSON(resp.Text, &out); err != nil {
		return nil, rec.calls, fmt.Errorf("llm planner: %w: %w", err, ErrPlannerAnswer)
	}
	if out.Action == "" {
		return nil, rec.calls, goap.ErrNoPlan
	}
	var chosen *goap.Action
	for i := range actions {
		if actions[i].Name == out.Action {
			chosen = &actions[i]
			break
		}
	}
	if chosen == nil {
		return nil, rec.calls, fmt.Errorf("llm planner picked unavailable action %q: %w", out.Action, ErrPlannerAnswer)
	}
	if !world.Satisfies(chosen.Pre) {
		return nil, rec.calls, fmt.Errorf("llm planner picked action %q whose preconditions are not met: %w", out.Action, ErrPlannerAnswer)
	}
	return &goap.Plan{Goal: goal, Actions: []goap.Action{*chosen}, Cost: chosen.Cost}, rec.calls, nil
}

const llmScoringSystem = `You are scoring the actions of an autonomous GOAP agent for usefulness. Given the ` +
	`current world state, the goal and the available actions (name, description, preconditions, effects, cost), ` +
	`answer ONLY with a JSON object {"utilities": {"<action>": <score>, ...}} with one non-negative number per ` +
	`action: higher means more useful toward the goal from the current state; 0 means useless or inapplicable now. ` +
	`Score every action listed.`

type llmScoringResponse struct {
	Utilities map[string]float64 `json:"utilities"`
}

// llmUtilities asks the LLM (via the agent's model alias) to score every candidate action's
// utility; the result feeds reweightPlan the same way hybrid's CEL-computed utilities do.
func (e *Engine) llmUtilities(ctx context.Context, m *methodology.Compiled, ag methodology.Agent, world goap.WorldState, actions []goap.Action, goal goap.Goal) (map[string]float64, []LLMCall, error) {
	rec := &planRecorder{client: e.LLM}
	req := buildLLMPlanRequest(m, world, actions, goal)
	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("llm-scoring planner: %w", err)
	}
	resp, err := rec.complete(ctx, llm.Request{Model: ag.Model, System: llmScoringSystem, JSON: true, MaxTokens: 1000,
		Messages: []llm.Message{{Role: "user", Content: string(body)}}})
	if err != nil {
		return nil, rec.calls, fmt.Errorf("llm-scoring planner: %w", err)
	}
	var out llmScoringResponse
	if err := llm.DecodeJSON(resp.Text, &out); err != nil {
		return nil, rec.calls, fmt.Errorf("llm-scoring planner: %w: %w", err, ErrPlannerAnswer)
	}
	u := make(map[string]float64, len(actions))
	for _, a := range actions {
		u[a.Name] = out.Utilities[a.Name] // zero value when absent: excluded, same as reweightPlan's u<=0 rule
	}
	return u, rec.calls, nil
}
