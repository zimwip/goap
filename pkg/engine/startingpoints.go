package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
)

// Starting points (ADR 0097): the steps of the methodology of a change that are possible NOW towards its goal, the end
// point of the process. A step is possible when ALL its entry conditions hold, its exit criteria do not, and nothing
// already carries it out; a step whose entry does not hold is never proposed (the methodology is responsible for the
// sequencing, by the conditions of its steps). The proposals are steps and method steps, never the actions behind
// them: starting one hands it to the scheduler, which plans and sequences the actions inside it.

// Kinds of starting points.
const (
	PointStep   = "step"   // a step of a process
	PointMethod = "method" // the method a step names as a capability, or a step of that method
)

// Launch is what starts a starting point: the fields of StartRequest (and of the StartProcess RPC) that run exactly
// this point. The agent plans towards Goal (the exit criteria of the step, or the goal of the method) on the change;
// the scheduler selects and sequences the actions.
type Launch struct {
	Methodology string          `json:"methodology"`
	Agent       string          `json:"agent"`
	Goal        string          `json:"goal"`
	ChangeID    domain.ChangeID `json:"changeId"`
}

// StartingPoint is a step (or method step) that is possible now.
type StartingPoint struct {
	// ID is the path of the step ("<process-or-method>/<step>/..."), unique in the methodology.
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Process is the process (or method) the step belongs to; Parent, for a step of a method, the step of the process
	// that names the capability.
	Process string `json:"process"`
	Parent  string `json:"parent,omitempty"`
	Name    string `json:"name"`
	// Method and Capability: the method that applies (a step naming a capability) and the capability it provides.
	Method      string `json:"method,omitempty"`
	Capability  string `json:"capability,omitempty"`
	Description string `json:"description,omitempty"`
	Guidance    string `json:"guidance,omitempty"`
	// Why are the entry conditions that hold ("name" / "!name"); Produces the exit criteria the step reaches.
	Why      []string `json:"why,omitempty"`
	Produces []string `json:"produces,omitempty"`
	// Responsible and Accountable are the roles in force for the step (ADR 0035 §2).
	Responsible string `json:"responsible,omitempty"`
	Accountable string `json:"accountable,omitempty"`
	// Launch starts it. MayRun says whether the caller may (the rule Start applies to its initiator, on the project
	// of the change); NeedRoles the roles the agent declares. Running: a process already carries it out.
	Launch    Launch   `json:"launch"`
	MayRun    bool     `json:"mayRun"`
	NeedRoles []string `json:"needRoles,omitempty"`
	Running   bool     `json:"running,omitempty"`
}

// BlockedStep is a step towards the goal that is not possible yet: what its entry still misses. It is not proposed.
type BlockedStep struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Missing []string `json:"missing"`
}

// StartingPoints is the answer of Engine.StartingPoints.
type StartingPoints struct {
	Methodology string `json:"methodology,omitempty"`
	Goal        string `json:"goal,omitempty"`
	// Reason says why there is nothing to start, when there is nothing and it is not an error (no methodology, no
	// goal, goal reached, change closed).
	Reason string          `json:"reason,omitempty"`
	Points []StartingPoint `json:"points"`
	// Blocked lists (capped) the steps that wait for conditions; BlockedCount counts them all.
	Blocked      []BlockedStep `json:"blocked,omitempty"`
	BlockedCount int           `json:"blockedCount,omitempty"`
}

// StartingPointsOptions tunes StartingPoints.
type StartingPointsOptions struct {
	// Methodology overrides the methodology of the change.
	Methodology string
	// Project is the project the caller would start on (empty: the one of the change).
	Project string
	// MaxPoints and MaxBlocked cap the lists (default 20 and 8).
	MaxPoints, MaxBlocked int
}

// StartingPoints computes the steps of the methodology of the change that are possible now towards the goal of the
// change. It reads the change and the methodology only; nothing is started.
func (e *Engine) StartingPoints(ctx context.Context, changeID domain.ChangeID, opts StartingPointsOptions) (*StartingPoints, error) {
	bb, err := e.Graph.Blackboard(ctx, changeID)
	if err != nil {
		return nil, err
	}
	ch := bb.Change
	out := &StartingPoints{Points: []StartingPoint{}, Methodology: ch.Methodology, Goal: ch.Goal}
	if opts.Methodology != "" {
		out.Methodology = opts.Methodology
	}
	switch {
	case ch.Status == domain.ChangeCommitted || ch.Status == domain.ChangeApplied || ch.Status == domain.ChangeAbandoned:
		out.Reason = fmt.Sprintf("the change is %s", ch.Status)
		return out, nil
	case out.Methodology == "":
		out.Reason = "the change has no methodology"
		return out, nil
	}
	m, err := e.Methodologies.Methodology(ctx, out.Methodology)
	if err != nil {
		return nil, err
	}
	// a change that predates the default goal (or a methodology chosen for a change that has none) is pulled by the end
	// point of the methodology
	if out.Goal == "" || opts.Methodology != "" && opts.Methodology != ch.Methodology {
		out.Goal = m.MainGoal()
	}
	if out.Goal == "" {
		out.Reason = "the methodology names no goal"
		return out, nil
	}
	goal, ok := m.Goal(out.Goal)
	if !ok {
		out.Reason = fmt.Sprintf("the goal %q is not known to the methodology", out.Goal)
		return out, nil
	}
	bb.Supertypes = e.supertypesOf(m)
	world := m.Conditions.Evaluate(bb).State
	world["change_bound"] = true
	if len(goal.Pre) > 0 && satisfiesAll(world, goal.Pre) {
		out.Reason = "the goal is reached"
		return out, nil
	}
	project := opts.Project
	if project == "" {
		project = ch.ProjectID
	}
	all, err := e.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	var active []*Process
	for _, p := range all {
		if p.ChangeID == changeID && !p.Status.Terminal() {
			active = append(active, p)
		}
	}
	w := &pointWalker{e: e, ctx: ctx, m: m, bb: bb, world: world, needed: m.NeededBy(goal.Pre), active: active, project: project,
		change: changeID, out: out, maxPoints: opts.MaxPoints, maxBlocked: opts.MaxBlocked}
	if w.maxPoints <= 0 {
		w.maxPoints = 20
	}
	if w.maxBlocked <= 0 {
		w.maxBlocked = 8
	}
	nested := nestedProcesses(m)
	for _, proc := range m.Processes {
		if nested[proc.Name] {
			continue // reached through the step that nests it
		}
		agent, ok := processAgent(m, proc.Name)
		if !ok {
			continue
		}
		for _, s := range m.ProcessSteps(proc.Name) {
			if err := w.visit(s, agent, PointStep, "", nil); err != nil {
				return nil, err
			}
		}
	}
	if len(out.Points) == 0 && out.Reason == "" {
		switch {
		case out.BlockedCount > 0:
			out.Reason = "no step is possible yet: the steps towards the goal wait for conditions"
		default:
			out.Reason = "no step of the methodology works towards the goal"
		}
	}
	return out, nil
}

// satisfiesAll reports whether every condition of want holds in the world.
func satisfiesAll(world, want map[string]bool) bool {
	for k, v := range want {
		if world[k] != v {
			return false
		}
	}
	return true
}

// processAgent is the agent generated for a process.
func processAgent(m *methodology.Compiled, process string) (string, bool) {
	for _, ag := range m.AgentList() {
		if ag.ProcessOf() == process {
			return ag.Name, true
		}
	}
	return "", false
}

// nestedProcesses are the processes of the methodology that a step of one of them nests: their steps are carried out
// through that step.
func nestedProcesses(m *methodology.Compiled) map[string]bool {
	out := map[string]bool{}
	var walk func([]methodology.StepInfo)
	walk = func(steps []methodology.StepInfo) {
		for _, s := range steps {
			if s.Process != "" {
				if other, proc := s.NestedProcess(); other == "" || other == m.Name {
					out[proc] = true
				}
			}
			walk(s.Steps)
		}
	}
	for _, p := range m.Processes {
		walk(m.ProcessSteps(p.Name))
	}
	return out
}

type pointWalker struct {
	e                     *Engine
	ctx                   context.Context
	m                     *methodology.Compiled
	bb                    domain.Blackboard
	world                 map[string]bool
	needed                map[string]bool
	active                []*Process
	project               string
	change                domain.ChangeID
	out                   *StartingPoints
	maxPoints, maxBlocked int
}

// visit examines a step from the top: a step that does not work towards the goal, that is done, or whose entry does
// not hold is not proposed (the last one is reported as blocked) and its sub-steps are not looked at; a possible step
// is proposed as a whole, its sub-steps being sequenced by the scheduler. A step naming a capability is proposed
// through the method that applies: the method (a pool of actions), or the possible steps of the method.
// me is set for the steps of a method (kind method).
func (w *pointWalker) visit(s methodology.StepInfo, agent, kind, parent string, me *methodology.Method) error {
	if kind == PointStep && !w.towardsGoal(s) {
		return nil
	}
	if len(s.Exit) > 0 && satisfiesAll(w.world, s.Exit) {
		return nil // done, or skipped
	}
	if miss := missing(w.world, s.Entry, s.Exit); len(miss) > 0 {
		w.block(s, miss)
		return nil
	}
	if kind == PointStep && s.Method() == methodology.MethodCapability && s.Foreach == "" {
		choices := w.m.MethodsFor(s.Capability, w.bb)
		if len(choices) == 0 {
			w.block(s, []string{"method:" + s.Capability})
			return nil
		}
		choice := choices[0]
		if steps := w.m.MethodSteps(choice.Name); len(steps) > 0 {
			for _, ms := range steps {
				if err := w.visit(ms, choice.ActorAgent(), PointMethod, s.Path, &choice.Method); err != nil {
					return err
				}
			}
			return nil
		}
		return w.add(s, StartingPoint{Kind: PointMethod, Process: processOfPath(s.Path), Method: choice.Name, Capability: s.Capability,
			Launch: Launch{Agent: choice.ActorAgent(), Goal: choice.AgentGoal}, Responsible: roleOf(s, &choice.Method, true), Accountable: roleOf(s, &choice.Method, false)})
	}
	pt := StartingPoint{Kind: kind, Process: processOfPath(s.Path), Parent: parent,
		Launch: Launch{Agent: agent, Goal: s.Path}, Responsible: roleOf(s, me, true), Accountable: roleOf(s, me, false)}
	if me != nil {
		pt.Method, pt.Capability = me.Name, me.For
	}
	return w.add(s, pt)
}

// towardsGoal reports whether the step reaches something the goal needs.
func (w *pointWalker) towardsGoal(s methodology.StepInfo) bool {
	for k := range s.Exit {
		if w.needed[k] {
			return true
		}
	}
	return false
}

func processOfPath(path string) string {
	p, _, _ := strings.Cut(path, "/")
	return p
}

func roleOf(s methodology.StepInfo, me *methodology.Method, responsible bool) string {
	r := s.Effective
	if me != nil && me.Roles != nil {
		r = me.Roles
	}
	switch {
	case r == nil:
		return ""
	case responsible:
		return r.Responsible
	}
	return r.Accountable
}

func (w *pointWalker) block(s methodology.StepInfo, miss []string) {
	w.out.BlockedCount++
	if len(w.out.Blocked) < w.maxBlocked {
		w.out.Blocked = append(w.out.Blocked, BlockedStep{ID: s.Path, Name: s.Name, Missing: miss})
	}
}

// add completes and records a possible step.
func (w *pointWalker) add(s methodology.StepInfo, pt StartingPoint) error {
	if len(w.out.Points) >= w.maxPoints {
		return nil
	}
	pt.ID, pt.Name, pt.Description, pt.Guidance = s.Path, s.Name, s.Description, s.Guidance
	pt.Why = satisfied(w.world, s.Entry, s.Exit)
	for k, v := range s.Exit {
		if v {
			pt.Produces = append(pt.Produces, k)
		} else {
			pt.Produces = append(pt.Produces, "!"+k)
		}
	}
	slices.Sort(pt.Produces)
	pt.Launch.Methodology, pt.Launch.ChangeID = w.m.Name, w.change
	pt.Running = w.running(pt)
	ok, roles, err := w.e.MayRunAgent(w.ctx, w.m.Name, pt.Launch.Agent, w.project)
	if err != nil {
		return err
	}
	pt.MayRun, pt.NeedRoles = ok, roles
	w.out.Points = append(w.out.Points, pt)
	return nil
}

// running reports whether a live process of the change already carries the step out: it works towards the goal of the
// step, runs the step for its parent, runs a step inside it or around it, or runs the whole process the step belongs
// to.
func (w *pointWalker) running(pt StartingPoint) bool {
	for _, p := range w.active {
		switch {
		case p.Goal == pt.ID, p.Agent == pt.Launch.Agent && p.Goal == pt.Launch.Goal && p.Methodology == pt.Launch.Methodology:
			return true
		case p.Step != nil && (p.Step.Path == pt.ID || strings.HasPrefix(p.Step.Path, pt.ID+"/") || strings.HasPrefix(pt.ID, p.Step.Path+"/")):
			return true
		case p.Methodology == w.m.Name && p.Goal == pt.Process && p.Agent == pt.Process:
			return true // the whole process is being run towards its goal: its planner sequences the step
		}
	}
	return false
}
