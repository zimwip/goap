package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/zimwip/goap/pkg/events"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/changeapi"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/engine/blackboard"
	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/journal"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/risk"
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
	// Scope is who and where: the organisation and project a process works in, what its principals may do there
	// and the MCPs it binds (ADR 0063). Unset: an AuthzScope granting everything and binding no MCP.
	Scope Scope
	// Lifecycles resolves the lifecycle the changes of a methodology follow (ADR 0058, 0098): the engine moves the state
	// of a change (TransitionChange) and guards its impacts (Guardian). Unset: no change follows a lifecycle.
	Lifecycles LifecyclePort
	// TransitionAuthorizer, when set, is asked before every transition of the lifecycle of a change: the permission the
	// transition declares, by default change:transition, for the caller.
	TransitionAuthorizer func(ctx context.Context, c domain.Change, t domain.Transition) error
	// LLM serves DSL model calls (the model gateway).
	LLM llm.Client
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
	// Types returns the type catalogue in force (ADR 0012 §2): the ancestors behind `x.types`. Unset, or nil, the
	// methodology's own resolved types apply.
	Types func() def.TypeSet

	locks sync.Map // process id -> *sync.Mutex
	now   func() time.Time
	bg    sync.WaitGroup
}

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

// queue notes why p is runnable: the run that picks it up journals it as a schedule record.
func (e *Engine) queue(ctx context.Context, p *Process, reason string, cause map[string]any) {
	by := authz.From(ctx).Subject
	if by == "" {
		by = p.Initiator.Subject
	}
	p.Queued = &Queued{Reason: reason, By: by, At: e.clock(), Cause: cause}
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
	// OwnerOrg is the key of the OrgUnit holding the new change (empty: the default organisation):
	// its adapters decide which MCPs the actions can use.
	OwnerOrg string
	// ProjectID is the key of the ProjectUnit the new change belongs to (empty: the root project,
	// ADR 0039): Assignment nodes on its chain grant the roles step checks see.
	ProjectID string
	Title     string
	Intent    string
	// Goal skips the intent loop (with Agent, or the first agent having it).
	Goal string
	// Agent restricts identification to one agent of the methodology.
	Agent string
	// ParentID is set for sub-agent processes, Call names the call of the parent's action that started it.
	ParentID string
	Call     string
	// Trigger is set for processes started by a trigger.
	Trigger string
	// Cause is the process whose event fired the trigger.
	Cause string
	Vars  map[string]any
	// Step is the step of the parent's process a sub-agent carries out (ADR 0034).
	Step *StepContext
	// Flow puts the process on an already-open flow branch of ChangeID (ADR
	// 0017), e.g. a "solution branch" (ADR 0031, gap 6) several concurrent
	// processes are deliberately pointed at so their impacts are isolated
	// together and reviewed as one merge decision later. "" (default) is the
	// main flow. Unlike Vars/ParentID, this is never defaulted automatically:
	// Change.View returns nothing for a flow that was never opened via
	// Graph.OpenFlow (pkg/domain/flow.go), so passing an ad-hoc id here
	// without having opened it first would make the process see an empty
	// blackboard, not an isolated-but-readable one.
	Flow string
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
	p := &Process{ID: uuid.NewString(), Methodology: req.Methodology, Agent: req.Agent, ChangeID: req.ChangeID, BaselineID: req.BaselineID, Namespace: req.Namespace, OwnBranch: req.OwnBranch, Org: req.OwnerOrg,
		Project: req.ProjectID, Title: req.Title, ParentID: req.ParentID, Trigger: req.Trigger, Cause: req.Cause, Flow: req.Flow, Initiator: authz.From(ctx), Vars: maps.Clone(req.Vars), Step: req.Step, Disabled: map[string]bool{},
		CreatedAt: e.clock(), UpdatedAt: e.clock()}
	if req.Intent != "" {
		if err := e.appendIntentTurns(ctx, p, intent.Turn{Role: "user", Text: req.Intent}); err != nil {
			return nil, err
		}
	}
	if req.ChangeID != "" {
		bb, err := e.Graph.Blackboard(ctx, req.ChangeID)
		if err != nil {
			return nil, err
		}
		if req.BaselineID == "" {
			p.BaselineID = bb.Change.BaselineID
		}
		// a run works where its change does (ADR 0091): the roles it checks are held on the project of the change,
		// which a move may have changed since the caller's token was issued
		p.Project = bb.Change.ProjectID
	}
	if req.Goal != "" {
		m, err := e.Methodologies.Methodology(ctx, req.Methodology)
		if err != nil {
			return nil, err
		}
		if _, ok := m.Goal(req.Goal); !ok {
			return nil, fmt.Errorf("unknown goal %q", req.Goal)
		}
		if si, ok := m.StepByPath(req.Goal); ok && p.Step == nil && strings.Contains(req.Goal, "/") {
			p.Step = contextOfStep(si) // a run towards the goal of a step carries that step out (ADR 0097)
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
	if p.Status == StatusRunning {
		if err := e.checkAgentRoles(ctx, p); err != nil {
			return nil, err
		}
		switch {
		case p.ParentID != "":
			e.queue(ctx, p, "sub-agent", map[string]any{"parent": p.ParentID, "call": req.Call})
		case p.Trigger != "":
			e.queue(ctx, p, "trigger", map[string]any{"trigger": p.Trigger})
		default:
			e.queue(ctx, p, "started", nil)
		}
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
	if err := e.appendIntentTurns(ctx, p, intent.Turn{Role: "user", Text: answer}); err != nil {
		return nil, err
	}
	if err := e.resolveIntent(ctx, p); err != nil {
		return nil, err
	}
	if p.Status == StatusRunning {
		e.queue(ctx, p, "answered", nil)
	}
	return p, e.save(ctx, p, "intent")
}

// appendIntentTurns adds turns to the in-memory session and logs them
// (ADR 0031): the intent dialogue is never persisted in the Process row's
// body, only in process_log, so a growing conversation never forces a full
// rewrite of the process.
func (e *Engine) appendIntentTurns(ctx context.Context, p *Process, turns ...intent.Turn) error {
	p.Intent.Turns = append(p.Intent.Turns, turns...)
	return e.logIntentTurns(ctx, p.ID, turns...)
}

func (e *Engine) logIntentTurns(ctx context.Context, processID string, turns ...intent.Turn) error {
	entries := make([]ProcessLogEntry, len(turns))
	for i, t := range turns {
		entries[i] = ProcessLogEntry{Type: "intent.turn", At: e.clock(), Payload: map[string]any{"role": t.Role, "text": t.Text}}
	}
	return e.Store.AppendProcessLog(ctx, processID, entries...)
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
	before := len(p.Intent.Turns)
	res, err := e.Intent.Resolve(ctx, &p.Intent, goals)
	if err != nil {
		return err
	}
	if added := p.Intent.Turns[before:]; len(added) > 0 {
		if err := e.logIntentTurns(ctx, p.ID, added...); err != nil {
			return err
		}
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

// selectTarget fixes the methodology, agent and goal and, unless the agent
// declares its own action to bind one (an `effects: {change_bound: true}`
// action, ADR 0031), opens the change eagerly, exactly as before.
func (e *Engine) selectTarget(ctx context.Context, p *Process, m *methodology.Compiled, agentName, goal string) error {
	if agentName == "" {
		agentName = m.AgentList()[0].Name
	}
	ag, ok := m.Agent(agentName)
	if !ok {
		return fmt.Errorf("unknown agent %q in %s: %w", agentName, m.Name, ErrInvalidState)
	}
	p.Methodology, p.Agent, p.Planner, p.Goal = m.Name, ag.Name, ag.Planner, goal
	// set early (Run refreshes it to the current published version before it
	// runs) so an eager bindChange's journal.attach record, journaled here
	// before Run ever executes, still carries it.
	p.MethodologyVersion = m.Version
	p.Question = ""
	p.Status = StatusRunning
	if p.ChangeID == "" && !agentBindsOwnChange(m, ag) {
		if err := e.bindChange(ctx, p, m, AttachRequest{}); err != nil {
			return err
		}
	}
	// the run's goal lives in the process; the goal of the change is its own (ADR 0096): the main goal of its
	// methodology at creation, then only an explicit edit
	return nil
}

// agentBindsOwnChange reports whether the agent declares its own action to
// bind a change (an action whose effects establish change_bound), rather than
// relying on the engine's eager default (ADR 0031, gap 5). No shipped
// methodology declares this today, so this is always false for them.
func agentBindsOwnChange(m *methodology.Compiled, ag methodology.Agent) bool {
	for _, a := range m.AgentActions(ag) {
		if a.Effects["change_bound"] {
			return true
		}
	}
	return false
}

// AttachRequest binds a process to a change: an existing one by ChangeID, or
// a new one resolved from the given (or process-derived) defaults.
type AttachRequest struct {
	ChangeID                                      domain.ChangeID // reuse an existing change if set
	Title, Intent, Namespace, OwnerOrg, ProjectID string
	BaselineID                                    domain.BaselineID
}

// AttachChange binds a standalone (not-yet-bound) process to a change: the
// deferred counterpart of selectTarget's eager default (ADR 0031, gap 5). It
// locks and reloads the process, so it is safe to call from outside the
// process's own run loop (e.g. a human/UI action, or a tool call that does
// not already hold the process's lock).
func (e *Engine) AttachChange(ctx context.Context, processID string, req AttachRequest) error {
	defer e.lock(processID)()
	p, err := e.Store.Get(ctx, processID)
	if err != nil {
		return err
	}
	m, err := e.Methodologies.Methodology(ctx, p.Methodology)
	if err != nil {
		return err
	}
	if err := e.bindChange(ctx, p, m, req); err != nil {
		return err
	}
	// the process was idle (outside any active Run loop, e.g. StatusWaiting or
	// between Run calls); selectTarget's own eager path never reaches here, it
	// runs inside Start's/Answer's existing queue/save flow instead.
	e.schedule(p.ID)
	return nil
}

// bindChange resolves and binds the change of an already-locked process (used
// by selectTarget's eager default, and by AttachChange). It journals a
// journal.attach record and persists+publishes an "attached" process event
// (ADR 0031, gap 5) for every bind, eager or deferred: WatchProcesses turns
// that into the fixed process.attached trigger event, the same way it already
// does for process.completed/failed/stuck.
func (e *Engine) bindChange(ctx context.Context, p *Process, m *methodology.Compiled, req AttachRequest) error {
	id := req.ChangeID
	if id == "" {
		var err error
		if id, err = e.resolveChange(ctx, p, m, req); err != nil {
			return err
		}
	} else if _, err := e.Graph.Blackboard(ctx, id); err != nil {
		return fmt.Errorf("attach %s: %w", id, err)
	}
	p.ChangeID = id
	e.journal(ctx, p, journal.Record{Kind: journal.KindAttach, Step: len(p.Steps),
		Data: map[string]any{"changeId": string(id), "reused": req.ChangeID != ""}})
	return e.save(ctx, p, events.BrokerAttached)
}

// resolveChange creates a new change for p, defaulting from req where given
// and from p/its methodology otherwise (identical defaults to what selectTarget
// always did eagerly: title from the first user turn or "<methodology> / <goal>",
// namespace from the methodology, baseline the latest of that namespace).
func (e *Engine) resolveChange(ctx context.Context, p *Process, m *methodology.Compiled, req AttachRequest) (domain.ChangeID, error) {
	title := firstNonEmpty(req.Title, p.Title)
	if title == "" {
		title = truncate(firstUserTurn(p), 80)
	}
	if title == "" {
		title = m.Name + " / " + p.Goal
	}
	p.Title = title
	intent := firstNonEmpty(req.Intent, firstUserTurn(p))
	ownerOrg := firstNonEmpty(req.OwnerOrg, p.Org)
	// a change always acts in a project (ADR 0054, 0091): the request's, else the run's (the caller's active project),
	// else the root project (an empty claim means the root); the graph refuses a change naming none
	projectID := firstNonEmpty(req.ProjectID, p.Project)
	if projectID == "" {
		st, err := e.Graph.Structures(ctx)
		if err != nil {
			return "", err
		}
		projectID = st.Project().Root
	}
	// the change's free-form data carries the use-case marks of its methodology (domain.DataAdministrative)
	data := map[string]any{}
	if p.Trigger != "" {
		data["trigger"] = p.Trigger
	}
	if m.Administrative {
		data[domain.DataAdministrative] = true
	}
	// the default criticality of the methodology (ADR 0075 §3): the requester raises it on the change header, lowering it
	// there asks change:lower-criticality
	if m.Criticality != "" {
		data[domain.DataCriticality] = m.Criticality
	}
	if len(data) == 0 {
		data = nil
	}
	ns := firstNonEmpty(req.Namespace, p.Namespace, m.Namespace)
	baseline := req.BaselineID
	if baseline == "" {
		baseline = p.BaselineID
	}
	// Without a baseline, the change starts from the latest one of the namespace it acts on: a request may
	// only know its methodology (hence its namespace) once its intent is identified.
	if baseline == "" {
		b, err := e.latestBaseline(ctx, domain.NamespaceOf(ns))
		if err != nil {
			return "", fmt.Errorf("methodology %s: %w", m.Name, err)
		}
		baseline = b
	}
	p.BaselineID = baseline
	// a trigger opening a change is a request of its own (ADR 0098): recorded first, linked as the origin of the change
	var origin domain.Request
	if p.Trigger != "" {
		var err error
		// asked by the identity the trigger runs as (system:trigger:<methodology>/<agent>/<trigger>)
		requester := firstNonEmpty(authz.From(ctx).Subject, "system:trigger:"+p.Trigger)
		if origin, err = e.Graph.CreateRequest(ctx, changeapi.NewRequest{Title: title, Text: intent, ProjectID: projectID, Requester: requester,
			Origin: domain.RequestOrigin{Kind: domain.OriginTrigger, Ref: p.Trigger}}); err != nil {
			return "", fmt.Errorf("the request of trigger %s: %w", p.Trigger, err)
		}
	}
	c, err := e.Graph.CreateChange(ctx, changeapi.NewChange{Title: title, Intent: intent, Methodology: m.Name, OwnerOrg: ownerOrg, ProjectID: projectID,
		Namespace: ns, OwnBranch: p.OwnBranch, BaselineID: baseline, Data: data})
	if err != nil {
		return "", err
	}
	if origin.ID != "" {
		if _, err := e.Graph.LinkRequest(ctx, origin.ID, c.ID, domain.LinkOrigin); err != nil {
			return "", fmt.Errorf("the request of trigger %s: %w", p.Trigger, err)
		}
	}
	// the run works where its change does: the roles it checks are held on that project (ADR 0043)
	if p.Project == "" {
		p.Project = c.ProjectID
	}
	return c.ID, nil
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
	// the task of a step is performed by the step's responsible role, in the unit holding the change (ADR 0035 §2)
	if ok, err := e.stepAllowed(ctx, p, authz.From(ctx), p.Pending.Context, "perform"); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%q does not hold the role %s for step %s: %w", authz.From(ctx).Subject, p.Pending.Context.Roles.Responsible,
			p.Pending.Context.Path, authz.ErrForbidden)
	}
	m, err := e.Methodologies.Methodology(ctx, p.Methodology)
	if err != nil {
		return nil, err
	}
	// a human action only some roles may run is performed by a person holding one (ADR 0043)
	if a, ok := m.Action(p.Pending.Action); ok {
		if roles := e.runRoles(m, p, a); len(roles) > 0 {
			if ok, err := e.mayRun(ctx, p, authz.From(ctx), "action", a.Name, roles); err != nil {
				return nil, err
			} else if !ok {
				return nil, fmt.Errorf("%q holds none of the roles %s on the project for %s: %w", authz.From(ctx).Subject,
					strings.Join(roles, ", "), a.Name, authz.ErrForbidden)
			}
		}
	}
	i := p.Pending.Step
	waitedOn := p.Pending.Action
	step := &p.Steps[i]
	rec := uuid.NewString()
	submitted := e.clock()
	if a, ok := m.Action(waitedOn); ok && a.Step != "" && a.Implements == "" {
		// a manual step of a process is done once a person has submitted it (ADR 0034)
		items = append(slices.Clone(items), ItemInput{Kind: string(domain.KindArtifact), Type: methodology.ArtifactStepDone,
			Data: map[string]any{"step": a.Step, "by": authz.From(ctx).Subject}})
	}
	ids, nodes, points, err := e.addItems(ctx, p, items, p.Pending.Action, rec, true, firstNonEmpty(authz.From(ctx).Subject, p.Pending.Action))
	if err != nil {
		return nil, err
	}
	step.Items, step.Nodes, step.Decisions = ids, nodes, points
	if err := e.finishStep(ctx, p, m, step); err != nil {
		return nil, err
	}
	p.Pending = nil
	p.Status = StatusRunning
	e.queue(ctx, p, "input", map[string]any{"step": i, "action": waitedOn})
	r := actionRecord(p, i, methodology.KindHuman, actionStep(m, waitedOn), rec)
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
	// a run scheduled in the background acts for the principal who started the process: what it writes is
	// recorded under that name (ADR 0029)
	if authz.From(ctx).Anonymous() {
		ctx = e.as(ctx, p)
	}
	ctx, end := e.tracer().StartProcess(ctx, p)
	defer func() { end(p) }()
	p.MethodologyVersion = m.Version
	// process.started is written the first time a process has a change to journal to.
	// JournalSeq can't gate this any more (a deferred process, ADR 0031 gap 5, may
	// journal an attach record before it ever reaches here); an eager bind always has
	// one by now (bindChange runs inside Start, before Run's first call), so this is
	// the common case, kept at its original position, before the schedule record.
	e.journalStarted(ctx, p)
	if q := p.Queued; q != nil && p.Status == StatusRunning {
		// why this run happens: what made the process runnable, by whom, and how long it waited
		data := map[string]any{"reason": q.Reason}
		maps.Copy(data, q.Cause)
		e.journal(ctx, p, journal.Record{Kind: journal.KindSchedule, Step: len(p.Steps), Actor: q.By, StartedAt: q.At, EndedAt: e.clock(), Data: data})
		p.Queued = nil
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
		// fallback for a deferred process that only attaches mid-loop, after the
		// check above already ran once with ChangeID still empty.
		e.journalStarted(ctx, p)
		if err := e.cycle(ctx, p, m); err != nil {
			e.fail(p, err)
		}
		if err := e.save(ctx, p, eventOf(p)); err != nil {
			return p, err
		}
	}
	return p, nil
}

// journalStarted writes process.started the first time a process has a change to journal to.
func (e *Engine) journalStarted(ctx context.Context, p *Process) {
	if p.Started || p.ChangeID == "" {
		return
	}
	e.journal(ctx, p, journal.Record{Kind: journal.KindProcessStarted, StartedAt: p.CreatedAt,
		Data: map[string]any{"intent": firstUserTurn(p), "trigger": p.Trigger, "title": p.Title}})
	p.Started = true
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

// execute runs an action for the step at index i, records its outcome and
// journals it (the produced items carry the journal record id).
func (e *Engine) execute(ctx context.Context, p *Process, m *methodology.Compiled, bb domain.Blackboard, action methodology.Action, i int) error {
	id := uuid.NewString()
	p.Steps[i].Execution = id
	impl, spec, err := e.specialize(ctx, p, m, action, bb)
	if err != nil {
		step := &p.Steps[i]
		step.Error, step.EndedAt = err.Error(), e.clock()
		e.journal(ctx, p, actionRecord(p, i, action.Kind, action.Step, id))
		p.Steps[i].dropExchanges()
		return e.recordFailure(p, action.Name)
	}
	p.Steps[i].Specialization = spec
	err = e.executeStep(ctx, p, m, bb, impl, i)
	e.journal(ctx, p, actionRecord(p, i, impl.Kind, action.Step, id))
	p.Steps[i].dropExchanges()
	return err
}

// boundMCPs returns the MCPs the organization of the process binds for actions and the tools they may
// call ("<mcp>/<tool>", once its restrictions and the scope of its MCP are applied, ADR 0028). Without a
// hub nothing is bound.
func (e *Engine) boundMCPs(ctx context.Context, p *Process) (map[string]bool, error) {
	bound := map[string]bool{}
	ref := e.ref(p)
	tools, names, err := e.scope().Tools(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("MCPs of organization %s: %w", ref.Org, err)
	}
	// an MCP of scope agent is not the business of an action (ADR 0028): an action declaring it, or a tool
	// action on it, is never scheduled; the agent level reaches it through agents[].mcps
	agentOnly := map[string]bool{}
	for _, t := range tools {
		if !mcp.ForActions(t.Scope) {
			if m, _, err := mcp.SplitTool(t.Name); err == nil {
				agentOnly[m] = true
			}
			continue
		}
		bound[t.Name] = true
	}
	for _, n := range names {
		bound[n] = !agentOnly[n]
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
	// a tool action also needs its tool, which the unit may have restricted
	return a.Kind != methodology.KindTool || bound[a.Tool]
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
	var candidates []candidate
	consider := func(owner *methodology.Compiled) {
		for _, s := range owner.SpecializationsOf(m.Name, action.Declared()) {
			if !owner.Applicable(s, bb) || !isBound(s) {
				continue
			}
			name := s.Name
			if owner.Name != m.Name {
				name = owner.Name + "/" + s.Name
			}
			candidates = append(candidates, candidate{a: s, name: name})
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
	// the generic Activity-specialization mechanism (methodology.RankByPriority, ADR 0009 §5): the applicable
	// candidate with the highest priority wins, ties keeping declaration order (methodology m first).
	ranked := methodology.RankByPriority(candidates, func(candidate) bool { return true }, func(c candidate) int { return c.a.Priority })
	if len(ranked) == 0 {
		if action.Kind == methodology.KindAbstract {
			return action, "", fmt.Errorf("no applicable specialization for abstract action %s", action.Name)
		}
		return action, "", nil
	}
	best := ranked[0]
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
	var agentMCPs []string
	if ag, ok := m.Agent(p.Agent); ok {
		agentMCPs = ag.MCPs
	}
	host := e.newHost(p, action, agentMCPs)
	host.step, host.base = i, len(p.Steps[i].LLMCalls)
	sc := stepContext(m, p, action)
	res, err := exec.Execute(ctx, ActionContext{Process: p, Action: action, Blackboard: bb, Graph: e.Graph, Host: host, Step: sc})
	step := &p.Steps[i]
	host.record(step)
	step.Output, step.Sandbox = res.Output, res.Sandbox
	if res.Method != "" {
		step.Specialization = res.Method
	}
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
	if len(res.VarsSet) > 0 {
		nv := maps.Clone(p.Vars)
		if nv == nil {
			nv = map[string]any{}
		}
		maps.Copy(nv, res.VarsSet)
		p.Vars = nv
	}
	end(step, err)
	if err != nil {
		step.Error = err.Error()
		step.EndedAt = e.clock()
		return e.recordFailure(p, action.Name)
	}
	if res.Suspended {
		// the action waits for a sub-agent: it is retried when the child ends,
		// or earlier if it emits a signal this action declared via WakeOn.
		step.EndedAt = e.clock()
		step.Output = "suspended: waiting for sub-agent " + res.Child
		p.Status = StatusWaiting
		p.Pending = &HumanTask{Kind: TaskAgent, Action: action.Name, Description: action.Description, Step: i, ChildProcessID: res.Child, WakeOn: res.WakeOn, Context: sc}
		return nil
	}
	e.forgetChildren(p, action.Name)
	if res.Wait {
		p.Status = StatusWaiting
		p.Pending = &HumanTask{Kind: TaskInput, Action: action.Name, Description: action.Description, Instructions: action.Instructions, NodeTypes: action.NodeTypes, Step: i, Context: sc}
		return nil
	}
	ctx = withVerify(ctx, action)
	ids, nodes, points, err := e.addItems(ctx, p, res.Items, action.Name, step.Execution, false, action.Name)
	if err == nil {
		var more []domain.ChangeImpactID
		more, err = e.applyNodeOps(ctx, p, res.Nodes, action.Name, step.Execution)
		nodes = append(nodes, more...)
	}
	if err != nil {
		step.Error = err.Error()
		step.EndedAt = e.clock()
		return e.recordFailure(p, action.Name)
	}
	step.Items, step.Nodes, step.Decisions = ids, nodes, points
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

// runChild runs (or resumes) a sub-agent for a host call, in the methodology of the parent.
func (e *Engine) runChild(ctx context.Context, h *Host, key, agentName, intentText string) (dsl.AgentResult, error) {
	return e.runChildStep(ctx, h, key, h.process.Methodology, agentName, "", intentText, nil)
}

// runChildStep runs (or resumes) a sub-agent on the change of h.process, in a methodology (empty: identification
// picks among every published one) with the goal fixed (empty: identification picks it), as a step of a process
// runs its agent or nested process (ADR 0034): a step carries its context (ADR 0050).
func (e *Engine) runChildStep(ctx context.Context, h *Host, key, methodologyName, agentName, goal, intentText string, step *StepContext) (dsl.AgentResult, error) {
	parent := h.process
	var child *Process
	if id, ok := parent.Children[key]; ok {
		c, err := e.Store.Get(ctx, id)
		if err != nil {
			return dsl.AgentResult{}, err
		}
		child = c
	} else {
		vars := maps.Clone(parent.Vars)
		if step != nil && step.ItemKey != "" {
			// a stream of a step with foreach (ADR 0050): the element it is carried out for
			if vars == nil {
				vars = map[string]any{}
			}
			vars["item"] = step.Item
		}
		c, err := e.Start(ctx, StartRequest{Methodology: methodologyName, ChangeID: parent.ChangeID, BaselineID: parent.BaselineID,
			Agent: agentName, Goal: goal, Intent: intentText, ParentID: parent.ID, Call: key, Vars: vars, Step: step})
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
	case StatusCompleted, StatusFailed:
		return res, nil // a stuck child is not an end: the step waits until a person unblocks it
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
	e.queue(ctx, p, "sub-agent-ended", map[string]any{"child": childID})
	err = e.save(ctx, p, "step")
	unlock()
	if err == nil {
		e.schedule(parentID)
	}
}

// allowed reports whether who holds a permission "<type>:<action>" on the process.
func (e *Engine) allowed(ctx context.Context, p *Process, who authz.Principal, permission string) (bool, error) {
	return e.scope().Allowed(ctx, e.ref(p), who, permission)
}

// PermissionRunAction is the permission of an approval an action waits for when its initiator holds none of the
// roles allowed to run it (ADR 0043): someone holding one approves it.
const PermissionRunAction = "action:run"

// runRoles are the roles allowed to run an action of a process (ADR 0043): its own, else those of the agent
// running it; none means any member of the project (already checked when the process started).
func (e *Engine) runRoles(m *methodology.Compiled, p *Process, a methodology.Action) []string {
	if len(a.Roles) > 0 {
		return a.Roles
	}
	if ag, ok := m.Agent(p.Agent); ok {
		return ag.Roles
	}
	return nil
}

// mayRun reports whether who holds, on the project of the process, one of the roles allowed to run an agent or
// an action (resource agent or action, act run).
func (e *Engine) mayRun(ctx context.Context, p *Process, who authz.Principal, typ, name string, roles []string) (bool, error) {
	return e.scope().MayRun(ctx, e.ref(p), who, typ, name, roles)
}

// MayRunAgent reports whether the caller of ctx holds, on the project, one of the roles the agent of a methodology
// declares (no roles declared: any member), with the rule Start applies to its initiator (checkAgentRoles), and the
// roles the agent declares. It starts nothing (ADR 0090).
func (e *Engine) MayRunAgent(ctx context.Context, methodologyName, agentName, project string) (bool, []string, error) {
	m, err := e.Methodologies.Methodology(ctx, methodologyName)
	if err != nil {
		return false, nil, err
	}
	ag, ok := m.Agent(agentName)
	if !ok {
		return false, nil, fmt.Errorf("unknown agent %q in %s: %w", agentName, methodologyName, ErrInvalidState)
	}
	if len(ag.Roles) == 0 {
		return true, nil, nil
	}
	p := &Process{Methodology: methodologyName, Agent: agentName, Project: project, Initiator: authz.From(ctx)}
	ok, err = e.mayRun(ctx, p, p.Initiator, "agent", ag.Name, ag.Roles)
	return ok, ag.Roles, err
}

// conversationOf is the conversation of the assistant that started the process (ADR 0090), "" for any other.
func conversationOf(p *Process) string {
	s, _ := p.Vars[VarConversation].(string)
	return s
}

// VarConversation is the process variable naming the assistant conversation that started it (ADR 0090): accounting
// only, it reaches the ledger of the gateway as the conversation of the calls of the process.
const VarConversation = "conversationId"

// checkAgentRoles refuses to start an agent its initiator may not run: it declares roles and they hold none of
// them on the project (ADR 0043).
func (e *Engine) checkAgentRoles(ctx context.Context, p *Process) error {
	m, err := e.Methodologies.Methodology(ctx, p.Methodology)
	if err != nil {
		return err
	}
	ag, ok := m.Agent(p.Agent)
	if !ok || len(ag.Roles) == 0 {
		return nil
	}
	if ok, err := e.mayRun(ctx, p, p.Initiator, "agent", ag.Name, ag.Roles); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%q holds none of the roles %s on project %s to run %s: %w", p.Initiator.Subject, strings.Join(ag.Roles, ", "),
			e.ref(p).Project, ag.Name, authz.ErrForbidden)
	}
	return nil
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
	var ok bool
	if p.Pending.Permission == PermissionRunAction {
		// an action only some roles may run (ADR 0043): one of them approves it, holding the permission the
		// action requires as well
		ok, err = e.mayRun(ctx, p, approver, "action", p.Pending.Action, p.Pending.Roles)
		if ok && err == nil {
			var m *methodology.Compiled
			if m, err = e.Methodologies.Methodology(ctx, p.Methodology); err == nil {
				if a, found := m.Action(p.Pending.Action); found && a.Permission != "" {
					ok, err = e.allowed(ctx, p, approver, a.Permission)
				}
			}
		}
	} else {
		ok, err = e.allowed(ctx, p, approver, p.Pending.Permission)
	}
	if err != nil {
		return nil, err
	}
	if !ok && p.Pending.Context != nil && p.Pending.Context.Roles != nil && p.Pending.Context.Roles.Accountable != "" {
		// the accountable role of the step answers for it: it may approve its gates (ADR 0035 §2)
		if ok, err = e.stepAllowed(ctx, p, approver, p.Pending.Context, "approve"); err != nil {
			return nil, err
		}
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
	e.queue(ctx, p, map[bool]string{true: "approved", false: "rejected"}[approve], map[string]any{"step": i, "action": action.Name})
	e.journal(ctx, p, journal.Record{Kind: journal.KindApproval, Step: i, Action: action.Name, Actor: approver.Subject,
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
		if action.Incremental && len(step.Items)+len(step.Nodes)+len(step.Decisions) > 0 {
			step.Progress = true // the action runs again on the next cycle
			return nil
		}
		return e.recordFailure(p, action.Name)
	}
	if action.Step != "" {
		e.stepCompleted(ctx, p, action, step)
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

// observe reads the blackboard of p and evaluates the methodology's conditions
// against it. A process not yet bound to a change (ADR 0031, gap 5) has no
// graph-backed blackboard to read: it observes only its own Vars, and the
// synthetic change_bound fact is false. change_bound is ordinary condition
// data a methodology may reference in its own Pre/Effects — it is never
// implied by the engine onto a goal (CLAUDE.md rule 2 gates graph writes, not
// a process's existence: a process that never writes may finish unbound).
func (e *Engine) observe(ctx context.Context, p *Process, m *methodology.Compiled) (domain.Blackboard, error) {
	var bb domain.Blackboard
	if p.ChangeID == "" {
		bb = domain.Blackboard{Vars: p.Vars}
	} else {
		var err error
		if bb, err = e.Graph.BlackboardIn(ctx, p.ChangeID, p.Flow); err != nil {
			return bb, err
		}
		bb.Vars = p.Vars
		p.Org = bb.Change.OwnerOrg
		p.Project = bb.Change.ProjectID
	}
	bb.Supertypes = e.supertypesOf(m)
	res := m.Conditions.Evaluate(bb)
	res.State["change_bound"] = p.ChangeID != ""
	// what people declared established for this run (Engine.Unblock)
	for k, v := range risk.Waivers(bb.Change, p.ID) {
		res.State[k] = v
		delete(res.Errors, k)
	}
	p.World = res.State
	p.Unknown = res.Errors
	return bb, nil
}

func (e *Engine) addItems(ctx context.Context, p *Process, in []ItemInput, producedBy, execution string, human bool, by string) ([]domain.ItemID, []domain.ChangeImpactID, []string, error) {
	// change impact operations (LLM output, human input) are applied in order, after the items; the decision
	// operations last (ADR 0009 §4)
	var ops []dsl.NodeOp
	var decisions []DecisionOp
	items := make([]ItemInput, 0, len(in))
	for _, it := range in {
		if it.Kind == "decisionPoint" {
			if it.DecisionPoint == nil {
				return nil, nil, nil, fmt.Errorf("item of kind decisionPoint needs a decisionPoint operation: %w", ErrInvalidState)
			}
			decisions = append(decisions, *it.DecisionPoint)
			continue
		}
		if it.Kind == "changeImpact" {
			if it.ChangeImpact == nil {
				return nil, nil, nil, fmt.Errorf("item of kind changeImpact needs a changeImpact operation: %w", ErrInvalidState)
			}
			ops = append(ops, *it.ChangeImpact)
			continue
		}
		items = append(items, it)
	}
	ids, err := e.addPlainItems(ctx, p, items, producedBy, execution)
	if err != nil {
		return ids, nil, nil, err
	}
	nodes, err := e.applyNodeOps(ctx, p, ops, producedBy, execution)
	if err != nil {
		return ids, nodes, nil, err
	}
	points, err := e.applyDecisionOps(ctx, p, decisions, human, by)
	return ids, nodes, points, err
}

func (e *Engine) addPlainItems(ctx context.Context, p *Process, in []ItemInput, producedBy, execution string) ([]domain.ItemID, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if p.ChangeID == "" {
		return nil, fmt.Errorf("process %s has no change attached: call goap-scheduler/attach first (ADR 0031)", p.ID)
	}
	bb, err := e.Graph.BlackboardIn(ctx, p.ChangeID, p.Flow)
	if err != nil {
		return nil, err
	}
	items, err := newResolver(bb.Change, uuid.NewString).resolve(in, producedBy)
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
	e.wakeOnSignals(ctx, p, added)
	return ids, nil
}

// wakeOnSignals wakes a live parent process suspended on this process (a
// TaskAgent Pending whose ChildProcessID is p.ID) as soon as one of the newly
// written items is a signal, addressed to it or broadcast, whose name the
// parent's action declared via WakeOn — instead of only at child termination.
// resumeParent already re-checks Pending under the process lock, so a signal
// that arrives after the parent has already moved on, or several signals in a
// row before the parent is actually rescheduled, are harmless no-ops.
func (e *Engine) wakeOnSignals(ctx context.Context, p *Process, items []domain.ChangeItem) {
	if p.ParentID == "" {
		return
	}
	var names []string
	for _, it := range items {
		if it.Kind == domain.KindSignal && (it.Target == "" || it.Target == p.ParentID) {
			names = append(names, it.Type)
		}
	}
	if len(names) == 0 {
		return
	}
	parent, err := e.Store.Get(ctx, p.ParentID)
	if err != nil || parent.Status != StatusWaiting || parent.Pending == nil || parent.Pending.Kind != TaskAgent || parent.Pending.ChildProcessID != p.ID {
		return
	}
	for _, name := range names {
		if slices.Contains(parent.Pending.WakeOn, name) {
			parentID, childID := p.ParentID, p.ID
			e.background(func() { e.resumeParent(parentID, childID) })
			return
		}
	}
}

// ProcessEvent is published on every process save, and for log lines.
type ProcessEvent struct {
	Event   string   `json:"event"`
	Process *Process `json:"process,omitempty"`
	// Step is the step of a process that completed (Event "step_completed", ADR 0036 §3).
	Step *StepEvent `json:"step,omitempty"`
	Log  *LogLine   `json:"log,omitempty"`
	Time time.Time  `json:"time"`
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
	e.recordRun(ctx, p)
	if p.Status.Terminal() && event == string(p.Status) {
		r := endRecord(p)
		r.EndedAt = p.UpdatedAt
		e.journal(ctx, p, r)
	}
	if err := e.Store.Put(ctx, p); err != nil {
		return err
	}
	if p.Status.Terminal() || p.Status == StatusStuck {
		if e.Sandboxes != nil {
			e.Sandboxes.Release(ctx, p.ID)
		}
	}
	if p.Status.Terminal() {
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

// recordRun records the run on its change (ADR 0098), best effort: the methodology the change carries
// (execution@Methodology, once per change: the primary one, or a companion for a transverse run) and the run itself
// (execution@Run, its status as it changes). A change that takes no more change objects (committed, applied) is not
// asked again; any other failure is retried at the next save.
func (e *Engine) recordRun(ctx context.Context, p *Process) {
	if p.ChangeID == "" || p.Methodology == "" || p.Status == "" {
		return
	}
	rec := p.Recorded
	if rec != nil && rec.Change == p.ChangeID && rec.Status == p.Status {
		return
	}
	var writes []domain.ObjectWrite
	if rec == nil || rec.Change != p.ChangeID {
		ref := blackboard.MethodologyRef{Name: p.Methodology, Version: p.MethodologyVersion, Role: blackboard.RolePrimary}
		if strings.HasPrefix(p.Trigger, CompanionPrefix) {
			ref.Role = blackboard.RoleCompanion
		}
		if ref.Role == blackboard.RolePrimary {
			if m, err := e.Methodologies.Methodology(ctx, p.Methodology); err == nil && m != nil {
				ref.Goal = m.MainGoal()
				if ref.Version == "" {
					ref.Version = m.Version
				}
			}
		}
		// a methodology already declared on the change (by another run) keeps its record: merged, never downgraded
		if bb, err := e.Graph.Objects(ctx, p.ChangeID, domain.ObjectFilter{Types: []string{blackboard.TypeMethodology}, KeyPrefix: p.Methodology}); err == nil &&
			slices.ContainsFunc(bb, func(o domain.ChangeObject) bool { return o.Key == p.Methodology }) {
			ref = blackboard.MethodologyRef{}
		}
		if ref.Name != "" {
			writes = append(writes, blackboard.DeclareMethodology(ref))
		}
		if ref.Role == blackboard.RolePrimary {
			e.ensureState(ctx, p.ChangeID, p.Methodology)
		}
	}
	writes = append(writes, blackboard.RecordRun(blackboard.RunRef{ID: p.ID, Methodology: p.Methodology, Agent: p.Agent, Goal: p.Goal,
		Status: string(p.Status), StartedAt: p.CreatedAt}))
	if _, err := e.Graph.PutObjects(ctx, p.ChangeID, writes); err != nil && !errors.Is(err, changeapi.ErrConflict) {
		e.log().Debug("run not recorded on its change", "process", p.ID, "change", p.ChangeID, "err", err)
		return
	}
	p.Recorded = &RunRecorded{Change: p.ChangeID, Status: p.Status}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// supertypesOf is the ancestry of the node types for the conditions (`x.types`): the catalogue in force, else the
// types the methodology was resolved with.
func (e *Engine) supertypesOf(m *methodology.Compiled) map[string][]string {
	if e.Types != nil {
		if t := e.Types(); t != nil {
			return t.Supertypes()
		}
	}
	return m.Supertypes()
}
