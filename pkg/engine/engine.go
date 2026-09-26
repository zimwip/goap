package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/llm"
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
	// Authz decides action permissions (nil: every permission is granted).
	Authz authz.Authorizer
	// LLM serves DSL model calls (the model gateway).
	LLM llm.Client
	// Tools serves tool calls and decides which actions can be scheduled (the MCP hub).
	// Without it no action needing an MCP is available.
	Tools ToolPort
	// Sandboxes isolates script actions (one sandbox per process run).
	Sandboxes Sandboxes
	// Tracer instruments processes and actions (nil: no tracing).
	Tracer Tracer
	// Schedule runs a process in the background (default: a goroutine).
	Schedule func(id string)
	Log      *slog.Logger
	// MaxSteps bounds the number of actions of a process (default 50).
	MaxSteps int
	// MaxFailures disables an action after that many executions without the
	// promised effects (default 2).
	MaxFailures int

	locks      sync.Map // process id -> *sync.Mutex
	supertypes SupertypesCache
	now        func() time.Time
	bg         sync.WaitGroup
}

// InvalidateSupertypes drops the cached NodeType ancestry of a methodology
// (called when the graph service reports a metadata-layer change, ADR 0012).
func (e *Engine) InvalidateSupertypes(methodology string) { e.supertypes.Invalidate(methodology) }

// background runs f in a tracked goroutine (see Drain).
func (e *Engine) background(f func()) {
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		f()
	}()
}

// Drain waits for the background work (tests).
func (e *Engine) Drain() { e.bg.Wait() }

func (e *Engine) schedule(id string) {
	if e.Schedule != nil {
		e.Schedule(id)
		return
	}
	e.background(func() {
		if _, err := e.Run(context.Background(), id); err != nil {
			e.log().Error("run", "process", id, "err", err)
		}
	})
}

// StartRequest starts a process.
type StartRequest struct {
	Methodology string
	// ChangeID continues an existing change; otherwise a change is opened on BaselineID.
	ChangeID   domain.ChangeID
	BaselineID domain.BaselineID
	// Namespace of the new change (default: domain.DefaultNamespace).
	Namespace string
	// OwnBranch gives the new change a branch of its own (merged into main when applied).
	OwnBranch bool
	Title     string
	Intent    string
	// Goal skips the intent loop (with Agent, or the first agent having it).
	Goal string
	// Agent restricts identification to one agent of the methodology.
	Agent string
	// ParentID is set for sub-agent processes.
	ParentID string
	// Trigger is set for processes started by a trigger.
	Trigger string
	Vars    map[string]any
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
// either clarifying (a question is pending) or running (call Run). Without
// methodology, identification ranks the agents of every published methodology.
func (e *Engine) Start(ctx context.Context, req StartRequest) (*Process, error) {
	p := &Process{ID: uuid.NewString(), Methodology: req.Methodology, Agent: req.Agent, ChangeID: req.ChangeID, BaselineID: req.BaselineID, Namespace: req.Namespace, OwnBranch: req.OwnBranch,
		Title: req.Title, ParentID: req.ParentID, Trigger: req.Trigger, Initiator: authz.From(ctx), Vars: req.Vars, Disabled: map[string]bool{},
		CreatedAt: e.clock(), UpdatedAt: e.clock()}
	if req.Intent != "" {
		p.Intent.Turns = append(p.Intent.Turns, intent.Turn{Role: "user", Text: req.Intent})
	}
	if req.ChangeID != "" && req.BaselineID == "" {
		bb, err := e.Graph.Blackboard(ctx, req.ChangeID)
		if err != nil {
			return nil, err
		}
		p.BaselineID = bb.Change.BaselineID
	}
	if p.ChangeID == "" && p.BaselineID == "" {
		return nil, fmt.Errorf("a baseline or a change is required: %w", ErrInvalidState)
	}
	if req.Goal != "" {
		m, err := e.Methodologies.Methodology(ctx, req.Methodology)
		if err != nil {
			return nil, err
		}
		if _, ok := m.Goal(req.Goal); !ok {
			return nil, fmt.Errorf("unknown goal %q", req.Goal)
		}
		agent := req.Agent
		if agent == "" {
			for _, ag := range m.AgentList() {
				if slices.ContainsFunc(m.AgentGoals(ag), func(g methodology.Goal) bool { return g.Name == req.Goal }) {
					agent = ag.Name
					break
				}
			}
		}
		if err := e.selectTarget(ctx, p, m, agent, req.Goal); err != nil {
			return nil, err
		}
	} else if err := e.resolveIntent(ctx, p); err != nil {
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
	p.Intent.Turns = append(p.Intent.Turns, intent.Turn{Role: "user", Text: answer})
	if err := e.resolveIntent(ctx, p); err != nil {
		return nil, err
	}
	return p, e.save(ctx, p, "intent")
}

// target is an identifiable (methodology, agent, goal) triple.
type target struct {
	m     *methodology.Compiled
	agent methodology.Agent
	goal  methodology.Goal
}

func (t target) key() string { return t.m.Name + "/" + t.agent.Name + "/" + t.goal.Name }

func (e *Engine) targets(ctx context.Context, p *Process) ([]target, error) {
	var ms []*methodology.Compiled
	if p.Methodology != "" {
		m, err := e.Methodologies.Methodology(ctx, p.Methodology)
		if err != nil {
			return nil, err
		}
		ms = []*methodology.Compiled{m}
	} else {
		all, err := e.Methodologies.List(ctx)
		if err != nil {
			return nil, err
		}
		ms = all
	}
	var out []target
	for _, m := range ms {
		for _, ag := range m.AgentList() {
			if p.Agent != "" && ag.Name != p.Agent {
				continue
			}
			for _, g := range m.AgentGoals(ag) {
				out = append(out, target{m: m, agent: ag, goal: g})
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no agent goal matches methodology %q agent %q: %w", p.Methodology, p.Agent, ErrInvalidState)
	}
	return out, nil
}

// resolveIntent identifies the (methodology, agent, goal) of the intent.
func (e *Engine) resolveIntent(ctx context.Context, p *Process) error {
	ts, err := e.targets(ctx, p)
	if err != nil {
		return err
	}
	byKey := map[string]target{}
	goals := make([]intent.GoalInfo, 0, len(ts))
	for _, t := range ts {
		byKey[t.key()] = t
		desc := t.goal.Description
		if t.agent.Description != "" && t.agent.Description != t.m.Description {
			desc = t.agent.Description + " — " + desc
		}
		goals = append(goals, intent.GoalInfo{Name: t.key(), Description: desc, Examples: append(slices.Clone(t.agent.Examples), t.goal.Examples...)})
	}
	res, err := e.Intent.Resolve(ctx, &p.Intent, goals)
	if err != nil {
		return err
	}
	p.Candidates = nil
	for _, c := range res.Candidates {
		if t, ok := byKey[c.Goal]; ok {
			c.Methodology, c.Agent, c.Goal = t.m.Name, t.agent.Name, t.goal.Name
		}
		p.Candidates = append(p.Candidates, c)
	}
	if !res.Resolved() {
		p.Status = StatusClarifying
		p.Question = res.Question
		return nil
	}
	t := byKey[res.Goal]
	return e.selectTarget(ctx, p, t.m, t.agent.Name, t.goal.Name)
}

// selectTarget fixes the methodology, agent and goal and opens the change.
func (e *Engine) selectTarget(ctx context.Context, p *Process, m *methodology.Compiled, agentName, goal string) error {
	if agentName == "" {
		agentName = m.AgentList()[0].Name
	}
	ag, ok := m.Agent(agentName)
	if !ok {
		return fmt.Errorf("unknown agent %q in %s: %w", agentName, m.Name, ErrInvalidState)
	}
	p.Methodology, p.Agent, p.Planner, p.Goal = m.Name, ag.Name, ag.Planner, goal
	p.Question = ""
	p.Status = StatusRunning
	if p.ChangeID == "" {
		title := p.Title
		if title == "" {
			title = truncate(firstUserTurn(p), 80)
		}
		if title == "" {
			title = m.Name + " / " + goal
		}
		p.Title = title
		var data map[string]any
		if p.Trigger != "" {
			data = map[string]any{"trigger": p.Trigger}
		}
		c, err := e.Graph.CreateChange(ctx, graph.NewChange{Title: title, Intent: firstUserTurn(p), Methodology: m.Name, OrgID: p.Initiator.Org, Namespace: firstNonEmpty(p.Namespace, m.Namespace), OwnBranch: p.OwnBranch, BaselineID: p.BaselineID, Data: data})
		if err != nil {
			return err
		}
		p.ChangeID = c.ID
		p.OrgID = c.OrgID
	}
	if p.ParentID != "" {
		return nil // the change goal belongs to the parent process
	}
	_, err := e.Graph.UpdateChange(ctx, p.ChangeID, graph.ChangePatch{Goal: &goal})
	return err
}

func firstUserTurn(p *Process) string {
	for _, t := range p.Intent.Turns {
		if t.Role == "user" {
			return t.Text
		}
	}
	return ""
}

// Submit provides the items of a pending human task and resumes the process
// (call Run afterwards).
func (e *Engine) Submit(ctx context.Context, id string, items []ItemInput) (*Process, error) {
	defer e.lock(id)()
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Status != StatusWaiting || p.Pending == nil || p.Pending.Kind != TaskInput {
		return nil, fmt.Errorf("process %s has no pending human task: %w", id, ErrInvalidState)
	}
	m, err := e.Methodologies.Methodology(ctx, p.Methodology)
	if err != nil {
		return nil, err
	}
	i := p.Pending.Step
	step := &p.Steps[i]
	rec := uuid.NewString()
	submitted := e.clock()
	ids, err := e.addItems(ctx, p, items, p.Pending.Action, rec)
	if err != nil {
		return nil, err
	}
	step.Items = ids
	if err := e.finishStep(ctx, p, m, step); err != nil {
		return nil, err
	}
	p.Pending = nil
	p.Status = StatusRunning
	r := actionRecord(p, i, methodology.KindHuman, rec)
	r.Actor, r.StartedAt = authz.From(ctx).Subject, submitted
	r.Data = map[string]any{"submitted": len(ids)}
	e.journal(ctx, p, r)
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
	ctx, end := e.tracer().StartProcess(ctx, p)
	defer func() { end(p) }()
	p.MethodologyVersion = m.Version
	if p.JournalSeq == 0 {
		e.journal(ctx, p, domain.ExecutionRecord{Kind: domain.ExecProcessStarted, StartedAt: p.CreatedAt,
			Data: map[string]any{"intent": firstUserTurn(p), "trigger": p.Trigger, "title": p.Title}})
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
	// the blackboard must be consistent before any action is asked, and before the goal is declared reached
	if blocked, err := e.checkBoard(ctx, p, bb); err != nil {
		return err
	} else if blocked {
		return nil
	}
	if p.World.Satisfies(goal.Pre) {
		p.Plan = nil
		if p.Flow != "" && p.Status == StatusRunning {
			// the relaunched flow reached the goal: a human confirms before it replaces the previous run
			p.Status = StatusWaiting
			desc := "The relaunched flow reached the goal. Adopt it to replace the outputs of the previous run, or discard it."
			// the proposals are applied on a domain branch of their own so that the resulting graph can be reviewed
			if f, err := e.Graph.MaterializeFlow(ctx, p.ChangeID, p.Flow); err != nil {
				e.log().Warn("flow preview failed", "process", p.ID, "flow", p.Flow, "err", err)
				desc += " (the resulting graph could not be previewed: " + err.Error() + ")"
			} else if f.Branch != "" {
				desc += " Its proposals are applied on the graph branch " + f.Branch + "."
			}
			p.Pending = &HumanTask{Kind: TaskFlow, Action: "adopt_flow", Step: len(p.Steps), Description: desc}
			e.journal(ctx, p, domain.ExecutionRecord{Kind: domain.ExecTick, Step: len(p.Steps), Before: maps.Clone(p.World),
				Data: map[string]any{"goalSatisfied": true, "flow": p.Flow, "awaitingDecision": true}})
			return nil
		}
		p.Status = StatusCompleted
		e.journal(ctx, p, domain.ExecutionRecord{Kind: domain.ExecTick, Step: len(p.Steps), Before: maps.Clone(p.World),
			Data: map[string]any{"goalSatisfied": true}})
		return nil
	}
	// plan with the agent's admissible actions and planner
	ag, ok := m.Agent(p.Agent)
	if !ok {
		return fmt.Errorf("unknown agent %q", p.Agent)
	}
	// an action is available only where the organization binds the MCPs it uses; the
	// hub is asked only when the methodology has such actions
	var bound map[string]bool
	if m.UsesMCPs() {
		if bound, err = e.boundMCPs(ctx, p); err != nil {
			return err
		}
	}
	var actions []goap.Action
	for _, a := range m.AgentActions(ag) {
		if !p.Disabled[a.Name] && e.schedulable(m, a.Name, bound) {
			actions = append(actions, a)
		}
	}
	tickStart := e.clock()
	plan, err := e.plan(ag.Planner, p.World, actions, goal.PlanningGoal(), m.Utilities(bb))
	if errors.Is(err, goap.ErrNoPlan) {
		p.Status = StatusStuck
		p.Plan = nil
		p.Error = fmt.Sprintf("no plan reaches goal %s from the current state", goal.Name)
		e.journal(ctx, p, domain.ExecutionRecord{Kind: domain.ExecTick, Step: len(p.Steps), Before: maps.Clone(p.World), Error: p.Error,
			StartedAt: tickStart, EndedAt: e.clock()})
		return nil
	}
	if err != nil {
		return err
	}
	prev := p.Plan
	p.Plan = make([]string, len(plan.Actions))
	for i, a := range plan.Actions {
		p.Plan[i] = a.Name
	}
	tick := domain.ExecutionRecord{Kind: domain.ExecTick, Step: len(p.Steps), Before: maps.Clone(p.World), Plan: slices.Clone(p.Plan),
		BoardBefore: len(bb.Change.Items), BoardAfter: len(bb.Change.Items), Action: p.Plan[0], StartedAt: tickStart, EndedAt: e.clock(),
		Data: map[string]any{"replanned": replanned(prev, p.Plan), "candidates": len(actions)}}
	if len(p.Unknown) > 0 {
		tick.Data["unknown"] = maps.Clone(p.Unknown)
	}
	e.journal(ctx, p, tick)
	// act
	action, _ := m.Action(plan.Actions[0].Name)
	step := Step{Index: len(p.Steps), Action: action.Name, Plan: p.Plan, Before: maps.Clone(p.World), StartedAt: e.clock(),
		Reads: bb.Change.ReferencedNodes(), BoardBefore: len(bb.Change.Items), LastItem: lastItem(bb.Change)}
	// the permission is the one of the implementation that will run (a
	// specialization may require more, e.g. a production deployment)
	permission := action.Permission
	if impl, spec, err := e.specialize(ctx, p, m, action, bb); err == nil && spec != "" {
		permission = impl.Permission
		step.Specialization = spec
	}
	if permission != "" {
		ok, err := e.allowed(ctx, p, p.Initiator, permission)
		if err != nil {
			return err
		}
		if !ok {
			// the initiator may not run this action: wait for an authorized approver
			p.Steps = append(p.Steps, step)
			p.Status = StatusWaiting
			p.Pending = &HumanTask{Kind: TaskApproval, Permission: permission, Action: action.Name,
				Description: action.Description, Instructions: action.Instructions, Step: step.Index}
			return nil
		}
	}
	p.Steps = append(p.Steps, step)
	return e.execute(ctx, p, m, bb, action, len(p.Steps)-1)
}

// execute runs an action for the step at index i, records its outcome and
// journals it (the produced items carry the journal record id).
func (e *Engine) execute(ctx context.Context, p *Process, m *methodology.Compiled, bb domain.Blackboard, action methodology.Action, i int) error {
	id := uuid.NewString()
	p.Steps[i].Execution = id
	impl, spec, err := e.specialize(ctx, p, m, action, bb)
	if err != nil {
		step := &p.Steps[i]
		step.Error, step.EndedAt = err.Error(), e.clock()
		e.journal(ctx, p, actionRecord(p, i, action.Kind, id))
		return e.recordFailure(p, action.Name)
	}
	p.Steps[i].Specialization = spec
	err = e.executeStep(ctx, p, m, bb, impl, i)
	e.journal(ctx, p, actionRecord(p, i, impl.Kind, id))
	return err
}

// orgOf is the organization of a process: the one of its change.
func (e *Engine) orgOf(p *Process) string {
	return cmp.Or(p.OrgID, p.Initiator.Org, domain.DefaultOrg)
}

// boundMCPs returns the MCPs the organization of the process binds. Without a hub
// nothing is bound.
func (e *Engine) boundMCPs(ctx context.Context, p *Process) (map[string]bool, error) {
	bound := map[string]bool{}
	if e.Tools == nil {
		return bound, nil
	}
	_, names, err := e.Tools.Tools(authz.With(ctx, p.Initiator), e.orgOf(p))
	if err != nil {
		return nil, fmt.Errorf("MCPs of organization %s: %w", e.orgOf(p), err)
	}
	for _, n := range names {
		bound[n] = true
	}
	return bound, nil
}

// schedulable reports whether every MCP the action needs is bound by the organization.
// bound is nil when the methodology uses no MCP (nothing to check).
func (e *Engine) schedulable(m *methodology.Compiled, name string, bound map[string]bool) bool {
	if bound == nil {
		return true
	}
	a, ok := m.Action(name)
	return !ok || allBound(a, bound)
}

func allBound(a methodology.Action, bound map[string]bool) bool {
	for _, n := range a.RequiredMCPs() {
		if !bound[n] {
			return false
		}
	}
	return true
}

// specialize returns the implementation to run for a planned action: the
// applicable specialization with the highest priority (declared in this
// methodology, then in the others), else the action itself. The result keeps
// the name, pre-conditions, effects and cost of the planned action.
func (e *Engine) specialize(ctx context.Context, p *Process, m *methodology.Compiled, action methodology.Action, bb domain.Blackboard) (methodology.Action, string, error) {
	var bound map[string]bool
	var boundErr error
	fetched := false
	isBound := func(s methodology.Action) bool {
		if len(s.RequiredMCPs()) == 0 {
			return true
		}
		if !fetched {
			bound, boundErr = e.boundMCPs(ctx, p)
			fetched = true
		}
		return boundErr == nil && allBound(s, bound)
	}
	type candidate struct {
		a    methodology.Action
		name string
	}
	var best *candidate
	consider := func(owner *methodology.Compiled) {
		for _, s := range owner.SpecializationsOf(m.Name, action.Name) {
			if !owner.Applicable(s, bb) || (best != nil && s.Priority <= best.a.Priority) || !isBound(s) {
				continue
			}
			name := s.Name
			if owner.Name != m.Name {
				name = owner.Name + "/" + s.Name
			}
			best = &candidate{a: s, name: name}
		}
	}
	consider(m)
	if others, err := e.Methodologies.List(ctx); err != nil {
		e.log().Warn("specializations of other methodologies unavailable", "err", err)
	} else {
		for _, o := range others {
			if o.Name != m.Name {
				consider(o)
			}
		}
	}
	if best == nil {
		if action.Kind == methodology.KindAbstract {
			return action, "", fmt.Errorf("no applicable specialization for abstract action %s", action.Name)
		}
		return action, "", nil
	}
	impl := best.a
	impl.Name, impl.Pre, impl.Effects, impl.Expects, impl.Cost = action.Name, action.Pre, action.Effects, action.Expects, action.Cost
	impl.Incremental = action.Incremental
	impl.Permission = cmp.Or(impl.Permission, action.Permission)
	return impl, best.name, nil
}

func (e *Engine) executeStep(ctx context.Context, p *Process, m *methodology.Compiled, bb domain.Blackboard, action methodology.Action, i int) error {
	exec, ok := e.Executors[action.Kind]
	if !ok {
		return fmt.Errorf("no executor for action kind %q", action.Kind)
	}
	e.log().Info("executing action", "process", p.ID, "agent", p.Agent, "action", action.Name, "plan", p.Plan)
	ctx, end := e.tracer().StartAction(ctx, p, action.Name, action.Kind)
	p.Steps[i].SpanID = e.spanID(ctx)
	host := e.newHost(p, action)
	res, err := exec.Execute(ctx, ActionContext{Process: p, Action: action, Blackboard: bb, Graph: e.Graph, Host: host})
	step := &p.Steps[i]
	host.record(step)
	step.Output, step.Sandbox = res.Output, res.Sandbox
	for _, l := range res.Logs {
		l.Step = i
		step.Logs = append(step.Logs, l)
		e.emitLog(ctx, p, l)
	}
	p.Usage = Usage{}
	for _, st := range p.Steps {
		p.Usage.Add(st.Usage)
	}
	if len(host.children) > 0 {
		if p.Children == nil {
			p.Children = map[string]string{}
		}
		maps.Copy(p.Children, host.children)
	}
	end(step, err)
	if err != nil {
		step.Error = err.Error()
		step.EndedAt = e.clock()
		return e.recordFailure(p, action.Name)
	}
	if res.Suspended {
		// the action waits for a sub-agent: it is retried when the child ends
		step.EndedAt = e.clock()
		step.Output = "suspended: waiting for sub-agent " + res.Child
		p.Status = StatusWaiting
		p.Pending = &HumanTask{Kind: TaskAgent, Action: action.Name, Description: action.Description, Step: i, ChildProcessID: res.Child}
		return nil
	}
	e.forgetChildren(p, action.Name)
	if res.Wait {
		p.Status = StatusWaiting
		p.Pending = &HumanTask{Kind: TaskInput, Action: action.Name, Description: action.Description, Instructions: action.Instructions, Step: i}
		return nil
	}
	ids, err := e.addItems(ctx, p, res.Items, action.Name, step.Execution)
	if err != nil {
		step.Error = err.Error()
		step.EndedAt = e.clock()
		return e.recordFailure(p, action.Name)
	}
	step.Items = ids
	return e.finishStep(ctx, p, m, step)
}

// forgetChildren drops the sub-agent calls of a finished action execution, so
// that a later execution of the same action starts new sub-agents.
func (e *Engine) forgetChildren(p *Process, action string) {
	for k := range p.Children {
		if strings.HasPrefix(k, action+"#") {
			delete(p.Children, k)
		}
	}
}

// runChild runs (or resumes) a sub-agent for a host call.
func (e *Engine) runChild(ctx context.Context, h *Host, key, agentName, intentText string) (dsl.AgentResult, error) {
	parent := h.process
	var child *Process
	if id, ok := parent.Children[key]; ok {
		c, err := e.Store.Get(ctx, id)
		if err != nil {
			return dsl.AgentResult{}, err
		}
		child = c
	} else {
		c, err := e.Start(ctx, StartRequest{Methodology: parent.Methodology, ChangeID: parent.ChangeID, BaselineID: parent.BaselineID,
			Agent: agentName, Intent: intentText, ParentID: parent.ID, Vars: parent.Vars})
		if err != nil {
			return dsl.AgentResult{}, err
		}
		h.mu.Lock()
		h.children[key] = c.ID
		h.spawned = append(h.spawned, c.ID)
		h.mu.Unlock()
		child = c
		if child.Status == StatusRunning {
			if child, err = e.Run(ctx, child.ID); err != nil {
				return dsl.AgentResult{}, err
			}
		}
	}
	res := dsl.AgentResult{Status: string(child.Status), Goal: child.Goal, ProcessID: child.ID}
	switch child.Status {
	case StatusCompleted, StatusStuck, StatusFailed:
		return res, nil
	}
	h.mu.Lock()
	h.waitingOn = child.ID
	h.mu.Unlock()
	return res, dsl.ErrSuspended
}

// resumeParent wakes a parent process whose action waits for child.
func (e *Engine) resumeParent(parentID, childID string) {
	ctx := context.Background()
	unlock := e.lock(parentID)
	p, err := e.Store.Get(ctx, parentID)
	if err != nil || p.Status != StatusWaiting || p.Pending == nil || p.Pending.Kind != TaskAgent || p.Pending.ChildProcessID != childID {
		unlock()
		return
	}
	p.Pending = nil
	p.Status = StatusRunning
	err = e.save(ctx, p, "step")
	unlock()
	if err == nil {
		e.schedule(parentID)
	}
}

func (e *Engine) allowed(ctx context.Context, p *Process, who authz.Principal, permission string) (bool, error) {
	if e.Authz == nil {
		return true, nil
	}
	typ, act, err := authz.ParsePermission(permission)
	if err != nil {
		return false, err
	}
	res := authz.Resource{Type: typ, Org: p.Initiator.Org, Owner: p.Initiator.Subject, Name: p.Methodology}
	if typ == "change" {
		res.ID = string(p.ChangeID)
	} else {
		res.ID = p.ID
	}
	return e.Authz.Authorize(ctx, authz.Request{Subject: who, Action: act, Resource: res})
}

// ErrInvalidState is returned when an operation does not match the process state.
var ErrInvalidState = errors.New("invalid process state")

// Approve decides a pending approval with the permissions of the principal of
// ctx. On approval the action runs immediately (call Run afterwards to
// continue); on rejection the action is disabled for this process and the
// planner looks for another way.
func (e *Engine) Approve(ctx context.Context, id string, approve bool, comment string) (*Process, error) {
	defer e.lock(id)()
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Status != StatusWaiting || p.Pending == nil || p.Pending.Kind != TaskApproval {
		return nil, fmt.Errorf("process %s has no pending approval: %w", id, ErrInvalidState)
	}
	approver := authz.From(ctx)
	ok, err := e.allowed(ctx, p, approver, p.Pending.Permission)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%q lacks %s: %w", approver.Subject, p.Pending.Permission, authz.ErrForbidden)
	}
	m, err := e.Methodologies.Methodology(ctx, p.Methodology)
	if err != nil {
		return nil, err
	}
	i := p.Pending.Step
	action, _ := m.Action(p.Pending.Action)
	permission := p.Pending.Permission
	p.Pending = nil
	p.Status = StatusRunning
	p.Steps[i].ApprovedBy = approver.Subject
	e.journal(ctx, p, domain.ExecutionRecord{Kind: domain.ExecApproval, Step: i, Action: action.Name, Actor: approver.Subject,
		Data: map[string]any{"approved": approve, "comment": comment, "permission": permission}})
	if !approve {
		p.Steps[i].Error = fmt.Sprintf("rejected by %s: %s", approver.Subject, comment)
		p.Steps[i].EndedAt = e.clock()
		if p.Disabled == nil {
			p.Disabled = map[string]bool{}
		}
		p.Disabled[action.Name] = true
		return p, e.save(ctx, p, "step")
	}
	bb, err := e.observe(ctx, p, m)
	if err != nil {
		return nil, err
	}
	if err := e.execute(ctx, p, m, bb, action, i); err != nil {
		e.fail(p, err)
	}
	return p, e.save(ctx, p, eventOf(p))
}

// finishStep re-observes the blackboard and checks the promised effects.
func (e *Engine) finishStep(ctx context.Context, p *Process, m *methodology.Compiled, step *Step) error {
	bb, err := e.observe(ctx, p, m)
	if err != nil {
		return err
	}
	step.BoardAfter = len(bb.Change.Items)
	action, _ := m.Action(step.Action)
	step.After = maps.Clone(p.World)
	step.EndedAt = e.clock()
	step.EffectsMet = p.World.Satisfies(action.Effects)
	if !step.EffectsMet {
		if action.Incremental && len(step.Items) > 0 {
			step.Progress = true // the action runs again on the next cycle
			return nil
		}
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
		if s.Action == action && !s.EffectsMet && !s.Progress && !s.EndedAt.IsZero() {
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
	bb, err := e.Graph.BlackboardIn(ctx, p.ChangeID, p.Flow)
	if err != nil {
		return bb, err
	}
	bb.Vars = p.Vars
	if bb.Change.OrgID != "" {
		p.OrgID = bb.Change.OrgID
	}
	bb.Supertypes = e.supertypes.Get(ctx, e.Graph, m)
	res := m.Conditions.Evaluate(bb)
	p.World = res.State
	p.Unknown = res.Errors
	return bb, nil
}

func (e *Engine) addItems(ctx context.Context, p *Process, in []ItemInput, producedBy, execution string) ([]domain.ItemID, error) {
	if len(in) == 0 {
		return nil, nil
	}
	bb, err := e.Graph.BlackboardIn(ctx, p.ChangeID, p.Flow)
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
	for k := range items {
		items[k].Execution, items[k].Flow = execution, p.Flow
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

// ProcessEvent is published on every process save, and for log lines.
type ProcessEvent struct {
	Event   string    `json:"event"`
	Process *Process  `json:"process,omitempty"`
	Log     *LogLine  `json:"log,omitempty"`
	Time    time.Time `json:"time"`
}

func (e *Engine) emitLog(ctx context.Context, p *Process, l LogLine) {
	if e.Events == nil {
		return
	}
	if l.ProcessID == "" {
		l.ProcessID = p.ID
	}
	_ = e.Events.Publish(ctx, fmt.Sprintf("goap.process.%s.log", p.ID), ProcessEvent{Event: "log", Log: &l, Time: l.Time})
}

func (e *Engine) save(ctx context.Context, p *Process, event string) error {
	p.UpdatedAt = e.clock()
	if p.Status.Terminal() {
		p.Children = nil
	}
	if p.TraceID == "" {
		p.TraceID = e.tracer().TraceID(ctx)
	}
	if p.Status.Terminal() && event == string(p.Status) {
		r := endRecord(p)
		r.EndedAt = p.UpdatedAt
		e.journal(ctx, p, r)
	}
	if err := e.Store.Put(ctx, p); err != nil {
		return err
	}
	if p.Status.Terminal() {
		if e.Sandboxes != nil {
			e.Sandboxes.Release(ctx, p.ID)
		}
		if p.ParentID != "" {
			parent, child := p.ParentID, p.ID
			e.background(func() { e.resumeParent(parent, child) })
		}
	}
	if e.Events != nil {
		if err := e.Events.Publish(ctx, fmt.Sprintf("goap.process.%s.%s", p.ID, event), ProcessEvent{Event: event, Process: p, Time: p.UpdatedAt}); err != nil {
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
