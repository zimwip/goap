package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/methodology"
)

// Engine runs processes.
type Engine struct {
	Graph         GraphPort
	Methodologies MethodologyPort
	Executors     map[string]Executor
	Intent        intent.Resolver
	Store         Store
	Events        Publisher
	Planner       goap.Planner
	Log           *slog.Logger
	// MaxSteps bounds the number of actions of a process (default 50).
	MaxSteps int
	// MaxFailures disables an action after that many executions without the
	// promised effects (default 2).
	MaxFailures int

	locks sync.Map // process id -> *sync.Mutex
	now   func() time.Time
}

// StartRequest starts a process.
type StartRequest struct {
	Methodology string
	// ChangeID continues an existing change; otherwise a change is opened on BaselineID.
	ChangeID   domain.ChangeID
	BaselineID domain.BaselineID
	Title      string
	Intent     string
	// Goal skips the intent loop.
	Goal string
	Vars map[string]any
}

func (e *Engine) lock(id string) func() {
	m, _ := e.locks.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (e *Engine) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now().UTC()
}

func (e *Engine) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

// Start creates a process and resolves its intent. The returned process is
// either clarifying (a question is pending) or running (call Run).
func (e *Engine) Start(ctx context.Context, req StartRequest) (*Process, error) {
	m, err := e.Methodologies.Methodology(ctx, req.Methodology)
	if err != nil {
		return nil, err
	}
	changeID := req.ChangeID
	if changeID == "" {
		title := req.Title
		if title == "" {
			title = truncate(req.Intent, 80)
		}
		c, err := e.Graph.CreateChange(ctx, graph.NewChange{Title: title, Intent: req.Intent, Methodology: m.Name, BaselineID: req.BaselineID})
		if err != nil {
			return nil, err
		}
		changeID = c.ID
	}
	p := &Process{ID: uuid.NewString(), Methodology: m.Name, ChangeID: changeID, Vars: req.Vars, Disabled: map[string]bool{},
		CreatedAt: e.clock(), UpdatedAt: e.clock()}
	if req.Intent != "" {
		p.Intent.Turns = append(p.Intent.Turns, intent.Turn{Role: "user", Text: req.Intent})
	}
	if req.Goal != "" {
		if _, ok := m.Goal(req.Goal); !ok {
			return nil, fmt.Errorf("unknown goal %q", req.Goal)
		}
		if err := e.selectGoal(ctx, p, req.Goal); err != nil {
			return nil, err
		}
	} else if err := e.resolveIntent(ctx, p, m); err != nil {
		return nil, err
	}
	if err := e.save(ctx, p, "started"); err != nil {
		return nil, err
	}
	return p, nil
}

// Answer adds a user answer to a clarifying process and resolves again.
func (e *Engine) Answer(ctx context.Context, id, answer string) (*Process, error) {
	defer e.lock(id)()
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Status != StatusClarifying {
		return nil, fmt.Errorf("process %s is %s, not clarifying", id, p.Status)
	}
	m, err := e.Methodologies.Methodology(ctx, p.Methodology)
	if err != nil {
		return nil, err
	}
	p.Intent.Turns = append(p.Intent.Turns, intent.Turn{Role: "user", Text: answer})
	if err := e.resolveIntent(ctx, p, m); err != nil {
		return nil, err
	}
	return p, e.save(ctx, p, "intent")
}

func (e *Engine) resolveIntent(ctx context.Context, p *Process, m *methodology.Compiled) error {
	goals := make([]intent.GoalInfo, 0, len(m.Goals))
	for _, g := range m.Goals {
		goals = append(goals, intent.GoalInfo{Name: g.Name, Description: g.Description, Examples: g.Examples})
	}
	res, err := e.Intent.Resolve(ctx, &p.Intent, goals)
	if err != nil {
		return err
	}
	p.Candidates = res.Candidates
	if !res.Resolved() {
		p.Status = StatusClarifying
		p.Question = res.Question
		return nil
	}
	return e.selectGoal(ctx, p, res.Goal)
}

func (e *Engine) selectGoal(ctx context.Context, p *Process, goal string) error {
	p.Goal = goal
	p.Question = ""
	p.Status = StatusRunning
	_, err := e.Graph.UpdateChange(ctx, p.ChangeID, graph.ChangePatch{Goal: &goal})
	return err
}

// Submit provides the items of a pending human task and resumes the process
// (call Run afterwards).
func (e *Engine) Submit(ctx context.Context, id string, items []ItemInput) (*Process, error) {
	defer e.lock(id)()
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Status != StatusWaiting || p.Pending == nil {
		return nil, fmt.Errorf("process %s has no pending human task", id)
	}
	m, err := e.Methodologies.Methodology(ctx, p.Methodology)
	if err != nil {
		return nil, err
	}
	step := &p.Steps[p.Pending.Step]
	ids, err := e.addItems(ctx, p, items, p.Pending.Action)
	if err != nil {
		return nil, err
	}
	step.Items = ids
	if err := e.finishStep(ctx, p, m, step); err != nil {
		return nil, err
	}
	p.Pending = nil
	p.Status = StatusRunning
	return p, e.save(ctx, p, "step")
}

// Run executes cycles until the process completes, blocks or fails.
func (e *Engine) Run(ctx context.Context, id string) (*Process, error) {
	defer e.lock(id)()
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	m, err := e.Methodologies.Methodology(ctx, p.Methodology)
	if err != nil {
		return nil, err
	}
	maxSteps := e.MaxSteps
	if maxSteps == 0 {
		maxSteps = 50
	}
	for p.Status == StatusRunning {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		if len(p.Steps) >= maxSteps {
			e.fail(p, fmt.Errorf("step limit %d reached", maxSteps))
			break
		}
		if err := e.cycle(ctx, p, m); err != nil {
			e.fail(p, err)
		}
		if err := e.save(ctx, p, eventOf(p)); err != nil {
			return p, err
		}
	}
	return p, nil
}

func eventOf(p *Process) string {
	switch p.Status {
	case StatusRunning:
		return "step"
	default:
		return string(p.Status)
	}
}

func (e *Engine) fail(p *Process, err error) {
	p.Status = StatusFailed
	p.Error = err.Error()
	e.log().Error("process failed", "process", p.ID, "err", err)
}

// cycle runs one observe / plan / act iteration.
func (e *Engine) cycle(ctx context.Context, p *Process, m *methodology.Compiled) error {
	// observe
	bb, err := e.observe(ctx, p, m)
	if err != nil {
		return err
	}
	goal, ok := m.Goal(p.Goal)
	if !ok {
		return fmt.Errorf("unknown goal %q", p.Goal)
	}
	if p.World.Satisfies(goal.Pre) {
		p.Status = StatusCompleted
		p.Plan = nil
		return nil
	}
	// plan
	var actions []goap.Action
	for _, a := range m.PlanningActions() {
		if !p.Disabled[a.Name] {
			actions = append(actions, a)
		}
	}
	plan, err := e.Planner.Plan(p.World, actions, goal.PlanningGoal())
	if errors.Is(err, goap.ErrNoPlan) {
		p.Status = StatusStuck
		p.Plan = nil
		p.Error = fmt.Sprintf("no plan reaches goal %s from the current state", goal.Name)
		return nil
	}
	if err != nil {
		return err
	}
	p.Plan = make([]string, len(plan.Actions))
	for i, a := range plan.Actions {
		p.Plan[i] = a.Name
	}
	// act
	action, _ := m.Action(plan.Actions[0].Name)
	step := Step{Index: len(p.Steps), Action: action.Name, Plan: p.Plan, Before: maps.Clone(p.World), StartedAt: e.clock()}
	exec, ok := e.Executors[action.Kind]
	if !ok {
		return fmt.Errorf("no executor for action kind %q", action.Kind)
	}
	e.log().Info("executing action", "process", p.ID, "action", action.Name, "plan", p.Plan)
	res, err := exec.Execute(ctx, ActionContext{Process: p, Action: action, Blackboard: bb, Graph: e.Graph})
	step.Output = res.Output
	if err != nil {
		step.Error = err.Error()
		step.EndedAt = e.clock()
		p.Steps = append(p.Steps, step)
		return e.recordFailure(p, action.Name)
	}
	if res.Wait {
		p.Steps = append(p.Steps, step)
		p.Status = StatusWaiting
		p.Pending = &HumanTask{Action: action.Name, Description: action.Description, Instructions: action.Instructions, Step: step.Index}
		return nil
	}
	ids, err := e.addItems(ctx, p, res.Items, action.Name)
	if err != nil {
		step.Error = err.Error()
		step.EndedAt = e.clock()
		p.Steps = append(p.Steps, step)
		return e.recordFailure(p, action.Name)
	}
	step.Items = ids
	p.Steps = append(p.Steps, step)
	return e.finishStep(ctx, p, m, &p.Steps[len(p.Steps)-1])
}

// finishStep re-observes the blackboard and checks the promised effects.
func (e *Engine) finishStep(ctx context.Context, p *Process, m *methodology.Compiled, step *Step) error {
	if _, err := e.observe(ctx, p, m); err != nil {
		return err
	}
	action, _ := m.Action(step.Action)
	step.After = maps.Clone(p.World)
	step.EndedAt = e.clock()
	step.EffectsMet = p.World.Satisfies(action.Effects)
	if !step.EffectsMet {
		return e.recordFailure(p, action.Name)
	}
	return nil
}

func (e *Engine) recordFailure(p *Process, action string) error {
	max := e.MaxFailures
	if max == 0 {
		max = 2
	}
	n := 0
	for _, s := range p.Steps {
		if s.Action == action && !s.EffectsMet && !s.EndedAt.IsZero() {
			n++
		}
	}
	if n >= max {
		if p.Disabled == nil {
			p.Disabled = map[string]bool{}
		}
		p.Disabled[action] = true
		e.log().Warn("action disabled", "process", p.ID, "action", action, "failures", n)
	}
	return nil
}

func (e *Engine) observe(ctx context.Context, p *Process, m *methodology.Compiled) (domain.Blackboard, error) {
	bb, err := e.Graph.Blackboard(ctx, p.ChangeID)
	if err != nil {
		return bb, err
	}
	bb.Vars = p.Vars
	res := m.Conditions.Evaluate(bb)
	p.World = res.State
	p.Unknown = res.Errors
	return bb, nil
}

func (e *Engine) addItems(ctx context.Context, p *Process, in []ItemInput, producedBy string) ([]domain.ItemID, error) {
	if len(in) == 0 {
		return nil, nil
	}
	bb, err := e.Graph.Blackboard(ctx, p.ChangeID)
	if err != nil {
		return nil, err
	}
	nodes, _, err := e.Graph.BaselineGraph(ctx, bb.Change.BaselineID)
	if err != nil {
		return nil, err
	}
	items, err := newResolver(nodes, bb.Change, uuid.NewString).resolve(in, producedBy)
	if err != nil {
		return nil, err
	}
	added, err := e.Graph.AddItems(ctx, p.ChangeID, items)
	if err != nil {
		return nil, err
	}
	ids := make([]domain.ItemID, len(added))
	for i, it := range added {
		ids[i] = it.ID
	}
	return ids, nil
}

// ProcessEvent is published on every process save.
type ProcessEvent struct {
	Event   string   `json:"event"`
	Process *Process `json:"process"`
}

func (e *Engine) save(ctx context.Context, p *Process, event string) error {
	p.UpdatedAt = e.clock()
	if err := e.Store.Put(ctx, p); err != nil {
		return err
	}
	if e.Events != nil {
		if err := e.Events.Publish(ctx, fmt.Sprintf("goap.process.%s.%s", p.ID, event), ProcessEvent{Event: event, Process: p}); err != nil {
			e.log().Warn("publish failed", "err", err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
