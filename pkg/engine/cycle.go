package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/journal"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

// cycle runs one observe / plan / act iteration.
func (e *Engine) cycle(ctx context.Context, p *Process, m *methodology.Compiled) error {
	bb, goal, done, err := e.observeGoal(ctx, p, m)
	if err != nil || done {
		return err
	}
	// plan with the agent's admissible actions and planner
	ag, actions, err := e.admissible(ctx, p, m)
	if err != nil {
		return err
	}
	pl, err := e.planStep(ctx, p, m, ag, goal, bb, actions)
	if err != nil || pl == nil {
		return err
	}
	// act
	action, _ := m.Action(pl.next)
	step := newStep(p, bb, action, pl.calls, e.clock())
	// the permission is the one of the implementation that will run (a
	// specialization may require more, e.g. a production deployment)
	permission := action.Permission
	if impl, spec, err := e.specialize(ctx, p, m, action, bb); err == nil && spec != "" {
		permission = impl.Permission
		step.Specialization = spec
	}
	if waiting, err := e.gateAction(ctx, p, m, action, step, permission); err != nil || waiting {
		return err
	}
	p.Steps = append(p.Steps, step)
	return e.execute(ctx, p, m, bb, action, len(p.Steps)-1)
}

// observeGoal observes the change and checks the goal: done is true when the cycle ends there, the blackboard is
// inconsistent (nothing is asked of the agent until it is fixed) or the goal is reached.
func (e *Engine) observeGoal(ctx context.Context, p *Process, m *methodology.Compiled) (bb domain.Blackboard, goal methodology.Goal, done bool, err error) {
	if bb, err = e.observe(ctx, p, m); err != nil {
		return
	}
	goal, ok := m.Goal(p.Goal)
	if !ok {
		err = fmt.Errorf("unknown goal %q", p.Goal)
		return
	}
	// the blackboard must be consistent before any action is asked, and before the goal is declared reached
	if blocked, berr := e.checkBoard(ctx, p, bb); berr != nil || blocked {
		return bb, goal, true, berr
	}
	if !p.World.Satisfies(goal.Pre) {
		return bb, goal, false, nil
	}
	p.Plan = nil
	if p.Flow != "" && p.Status == StatusRunning {
		// the relaunched flow reached the goal: a human confirms before it replaces the previous run
		p.Status = StatusWaiting
		desc := "The relaunched flow reached the goal. Adopt it to replace the outputs of the previous run, or discard it."
		// what the flow wrote is on its graph branch, to be reviewed against the change branch before deciding
		p.Pending = &HumanTask{Kind: TaskFlow, Action: "adopt_flow", Step: len(p.Steps), Description: desc}
		e.journal(ctx, p, journal.Record{Kind: journal.KindTick, Step: len(p.Steps), Before: maps.Clone(p.World),
			Data: map[string]any{"goalSatisfied": true, "flow": p.Flow, "awaitingDecision": true}})
		return bb, goal, true, nil
	}
	p.Status = StatusCompleted
	e.journal(ctx, p, journal.Record{Kind: journal.KindTick, Step: len(p.Steps), Before: maps.Clone(p.World),
		Data: map[string]any{"goalSatisfied": true}})
	return bb, goal, true, nil
}

// admissible returns the agent of the process and the actions it may plan with: an action is available only where
// the organization binds the MCPs it uses (the hub is asked only when the methodology has such actions) and while
// it is not disabled.
func (e *Engine) admissible(ctx context.Context, p *Process, m *methodology.Compiled) (methodology.Agent, []goap.Action, error) {
	ag, ok := m.Agent(p.Agent)
	if !ok {
		return ag, nil, fmt.Errorf("unknown agent %q", p.Agent)
	}
	var bound map[string]bool
	if m.UsesMCPs() {
		var err error
		if bound, err = e.boundMCPs(ctx, p); err != nil {
			return ag, nil, err
		}
	}
	var actions []goap.Action
	for _, a := range m.AgentActions(ag) {
		if !p.Disabled[a.Name] && e.schedulable(m, a.Name, bound) {
			actions = append(actions, a)
		}
	}
	return ag, actions, nil
}

// planned is the outcome of a planning phase that found a plan: the action to run first and the model calls the
// planner made.
type planned struct {
	next  string
	calls []LLMCall
}

// planStep plans from the world of the process and journals the tick. When no plan reaches the goal it settles the
// process instead (waiting for what is established outside it, else stuck with a task to unblock it) and returns
// nil.
func (e *Engine) planStep(ctx context.Context, p *Process, m *methodology.Compiled, ag methodology.Agent, goal methodology.Goal,
	bb domain.Blackboard, actions []goap.Action) (*planned, error) {
	tickStart := e.clock()
	// the planning calls belong to the step this plan opens (ledger of the gateway, ADR 0089)
	ctx = llm.WithMeta(ctx, llm.CallMeta{Source: llm.SourceEngine, ProcessID: p.ID, ChangeID: string(p.ChangeID), Step: len(p.Steps), Agent: p.Agent})
	plan, calls, err := e.plan(ctx, m, ag, p.World, actions, goal.PlanningGoal(), m.Utilities(bb))
	if errors.Is(err, goap.ErrNoPlan) {
		e.settleNoPlan(ctx, p, m, ag, goal, actions, tickStart, calls)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	prev := p.Plan
	p.Plan = make([]string, len(plan.Actions))
	for i, a := range plan.Actions {
		p.Plan[i] = a.Name
	}
	tick := tickRecordWithCalls(journal.Record{Kind: journal.KindTick, Step: len(p.Steps), Before: maps.Clone(p.World), Plan: slices.Clone(p.Plan),
		BoardBefore: len(bb.Change.Items), BoardAfter: len(bb.Change.Items), Action: p.Plan[0], StartedAt: tickStart, EndedAt: e.clock(),
		Data: map[string]any{"replanned": replanned(prev, p.Plan), "candidates": len(actions)}}, calls)
	if len(p.Unknown) > 0 {
		tick.Data["unknown"] = maps.Clone(p.Unknown)
	}
	e.journal(ctx, p, tick)
	return &planned{next: p.Plan[0], calls: calls}, nil
}

// settleNoPlan handles a goal no plan reaches: the process waits when what is missing is established outside it, and
// is tried again when the change moves; else it is stuck, and a person can always unblock it.
func (e *Engine) settleNoPlan(ctx context.Context, p *Process, m *methodology.Compiled, ag methodology.Agent, goal methodology.Goal,
	actions []goap.Action, tickStart time.Time, calls []LLMCall) {
	p.Plan = nil
	if awaited := e.awaited(p.World, m.AgentActions(ag), actions, goal.PlanningGoal()); len(awaited) > 0 {
		p.Status = StatusWaiting
		p.Pending = &HumanTask{Kind: TaskCondition, Step: len(p.Steps), Conditions: awaited,
			Description: "Waiting for " + strings.Join(awaited, ", ") + ": conditions this agent's actions do not establish"}
		e.journal(ctx, p, tickRecordWithCalls(journal.Record{Kind: journal.KindTick, Step: len(p.Steps), Before: maps.Clone(p.World),
			StartedAt: tickStart, EndedAt: e.clock(), Data: map[string]any{"awaiting": slices.Clone(awaited)}}, calls))
		return
	}
	p.Status = StatusStuck
	p.Error = fmt.Sprintf("no plan reaches goal %s from the current state", goal.Name)
	// a person can always unblock it: what would, if they declare it established
	p.Pending = &HumanTask{Kind: TaskUnblock, Step: len(p.Steps), Conditions: e.unblocking(p.World, actions, goal.PlanningGoal()),
		Description: "Stuck: " + p.Error + ". Declare conditions established, retry the actions it gave up on, or abandon it."}
	e.journal(ctx, p, tickRecordWithCalls(journal.Record{Kind: journal.KindTick, Step: len(p.Steps), Before: maps.Clone(p.World), Error: p.Error,
		StartedAt: tickStart, EndedAt: e.clock()}, calls))
}

// newStep is the step an action about to run opens, with the model calls of the planning that chose it.
func newStep(p *Process, bb domain.Blackboard, action methodology.Action, calls []LLMCall, now time.Time) Step {
	step := Step{Index: len(p.Steps), Action: action.Name, Plan: p.Plan, Before: maps.Clone(p.World), StartedAt: now,
		Reads: bb.Change.ReferencedNodes(), BoardBefore: len(bb.Change.Items), LastItem: lastItem(bb.Change)}
	for _, c := range calls {
		step.LLMCalls = append(step.LLMCalls, c)
		step.Usage.LLMCalls++
		step.Usage.InputTokens += c.InputTokens
		step.Usage.OutputTokens += c.OutputTokens
	}
	return step
}

// gateAction decides whether the initiator may run the action now: waiting is true when a person has to approve it
// first (the step is recorded and the process waits). Two gates apply, in order: the roles allowed to run the action
// (ADR 0043: a human action waits for a person holding one, see Submit; any other for one to approve it when the
// initiator holds none) and the permission of the implementation that will run.
func (e *Engine) gateAction(ctx context.Context, p *Process, m *methodology.Compiled, action methodology.Action, step Step, permission string) (waiting bool, err error) {
	if roles := e.runRoles(m, p, action); len(roles) > 0 && action.Kind != methodology.KindHuman {
		ok, err := e.mayRun(ctx, p, p.Initiator, "action", action.Name, roles)
		if err != nil {
			return false, err
		}
		if !ok {
			e.awaitApproval(p, m, action, step, PermissionRunAction, roles)
			return true, nil
		}
	}
	if permission != "" {
		ok, err := e.allowed(ctx, p, p.Initiator, permission)
		if err != nil {
			return false, err
		}
		if !ok {
			// the initiator may not run this action: wait for an authorized approver
			e.awaitApproval(p, m, action, step, permission, nil)
			return true, nil
		}
	}
	return false, nil
}

// awaitApproval records the step and makes the process wait for an approval of the permission.
func (e *Engine) awaitApproval(p *Process, m *methodology.Compiled, action methodology.Action, step Step, permission string, roles []string) {
	p.Steps = append(p.Steps, step)
	p.Status = StatusWaiting
	p.Pending = &HumanTask{Kind: TaskApproval, Permission: permission, Roles: roles, Action: action.Name,
		Description: action.Description, Instructions: action.Instructions, Step: step.Index, Context: stepContext(m, p, action)}
}
