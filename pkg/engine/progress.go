package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/risk"
)

// Step states of a process's progress (ADR 0035 §3).
const (
	StepDone    = "done"    // its exit criteria hold, and it ran
	StepSkipped = "skipped" // its entry holds and its exit criteria held without it running: nothing to do
	StepActive  = "active"  // being carried out (its action runs, or its sub-agent / nested process works)
	StepWaiting = "waiting" // waiting for a person (a task to submit, an action to approve), or for conditions another process or person establishes
	StepReady   = "ready"   // its entry holds: the planner may take it
	StepTodo    = "todo"    // its entry does not hold yet (Missing says what is missing)
	StepBlocked = "blocked" // the process is stuck (nothing established outside it would unblock it) or failed before it was done
)

// ProcessProgress is where a process run stands in the steps of its process: a read model computed from the
// definition and the run, never stored.
type ProcessProgress struct {
	ProcessID   string         `json:"processId"`
	Methodology string         `json:"methodology"`
	Process     string         `json:"process"`
	Description string         `json:"description,omitempty"`
	Status      Status         `json:"status"`
	Error       string         `json:"error,omitempty"`
	Steps       []StepProgress `json:"steps"`
	// Done counts the steps that run something (no sub-steps) done or skipped, out of Total.
	Done, Total int
}

// Lane is one stream of a step with foreach: the element it is carried out for, the method chosen for it and where
// its agent instance stands.
type Lane struct {
	Item      string `json:"item"`
	Method    string `json:"method,omitempty"`
	ProcessID string `json:"processId"`
	Status    Status `json:"status"`
}

// StepProgress is the state of one step, with its sub-steps.
type StepProgress struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Method: steps, action, agent, process or manual; Target what it runs (action names, agent, process).
	Method string `json:"method"`
	Target string `json:"target,omitempty"`
	// Chosen is the method chosen for a step that names a capability.
	Chosen string `json:"chosen,omitempty"`
	// Roles in force for the step (ADR 0035 §2).
	Roles *methodology.Responsibilities `json:"roles,omitempty"`
	State string                        `json:"state"`
	// Missing are the entry conditions that do not hold ("name" when expected true, "!name" when expected false).
	Missing []string `json:"missing,omitempty"`
	// Runs counts the executions of the step's actions; ChildProcessIDs are the sub-agents it started.
	Runs            int      `json:"runs,omitempty"`
	ChildProcessIDs []string `json:"childProcessIds,omitempty"`
	// Waiting: the kind of the task (input, approval, agent, condition) and the permission an approval needs.
	// Lanes are the parallel streams of a step with foreach (ADR 0050), one per element or group.
	Lanes      []Lane                  `json:"lanes,omitempty"`
	Waiting    string                  `json:"waiting,omitempty"`
	Permission string                  `json:"permission,omitempty"`
	Guidance   string                  `json:"guidance,omitempty"`
	Checklist  []string                `json:"checklist,omitempty"`
	References []methodology.Reference `json:"references,omitempty"`
	Steps      []StepProgress          `json:"steps,omitempty"`
}

// ErrNotAProcess is returned for a run whose agent does not run a process.
var ErrNotAProcess = fmt.Errorf("the run does not follow a process: %w", ErrInvalidState)

// Progress computes where the run id stands in the steps of the process its agent runs.
func (e *Engine) Progress(ctx context.Context, id string) (*ProcessProgress, error) {
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	m, err := e.Methodologies.Methodology(ctx, p.Methodology)
	if err != nil {
		return nil, err
	}
	ag, ok := m.Agent(p.Agent)
	if !ok || ag.ProcessOf() == "" {
		return nil, ErrNotAProcess
	}
	proc, _ := m.ProcessByName(ag.ProcessOf())
	out := &ProcessProgress{ProcessID: p.ID, Methodology: p.Methodology, Process: proc.Name, Description: proc.Description, Status: p.Status, Error: p.Error}
	for _, s := range m.ProcessSteps(proc.Name) {
		out.Steps = append(out.Steps, stepProgress(p, s))
	}
	if err := e.addLanes(ctx, p, out.Steps); err != nil {
		return nil, err
	}
	var count func([]StepProgress)
	count = func(steps []StepProgress) {
		for _, s := range steps {
			if len(s.Steps) > 0 {
				count(s.Steps)
				continue
			}
			out.Total++
			if s.State == StepDone || s.State == StepSkipped {
				out.Done++
			}
		}
	}
	count(out.Steps)
	return out, nil
}

func stepProgress(p *Process, s methodology.StepInfo) StepProgress {
	sp := StepProgress{Path: s.Path, Name: s.Name, Description: s.Description, Method: s.Method(), Guidance: s.Guidance,
		Checklist: s.Checklist, References: s.References, Roles: s.Effective}
	switch sp.Method {
	case methodology.MethodAction:
		sp.Target = s.Action
		if sp.Target == "" {
			sp.Target = strings.Join(s.Actions, " | ")
		}
	case methodology.MethodProcess:
		sp.Target = s.Process
	case methodology.MethodCapability:
		sp.Target = s.Capability
	}
	sp.Missing = missing(p.World, s.Entry, s.Exit)
	done := len(s.Exit) > 0 && p.World.Satisfies(s.Exit)
	if len(s.Steps) > 0 {
		ran := false
		for _, c := range s.Steps {
			cp := stepProgress(p, c)
			ran = ran || cp.Runs > 0 || cp.State == StepDone
			sp.Steps = append(sp.Steps, cp)
		}
		switch {
		case done && ran:
			sp.State = StepDone
		case done && len(sp.Missing) == 0:
			sp.State = StepSkipped
		case len(sp.Missing) > 0 && !ran:
			sp.State = StepTodo // not entered yet, whatever its criteria
		default:
			sp.State = aggregate(sp.Steps)
		}
		return sp
	}
	for _, st := range p.Steps {
		if slices.Contains(s.Planned, st.Action) {
			sp.Runs++
			for _, c := range st.Children {
				if !slices.Contains(sp.ChildProcessIDs, c) {
					sp.ChildProcessIDs = append(sp.ChildProcessIDs, c)
				}
			}
		}
	}
	for i := len(p.Steps) - 1; i >= 0 && sp.Method == methodology.MethodCapability; i-- {
		if slices.Contains(s.Planned, p.Steps[i].Action) && p.Steps[i].Specialization != "" {
			sp.Chosen = p.Steps[i].Specialization // the method chosen for it
			break
		}
	}
	pending := p.Pending != nil && slices.Contains(s.Planned, p.Pending.Action)
	if pending && p.Pending.ChildProcessID != "" && !slices.Contains(sp.ChildProcessIDs, p.Pending.ChildProcessID) {
		sp.ChildProcessIDs = append(sp.ChildProcessIDs, p.Pending.ChildProcessID)
	}
	switch {
	case done && sp.Runs > 0:
		sp.State = StepDone
	case done && len(sp.Missing) == 0:
		sp.State = StepSkipped // its turn came, and there was nothing to do
	case pending && p.Pending.Kind == TaskAgent:
		sp.State = StepActive
	case pending:
		sp.State, sp.Waiting, sp.Permission = StepWaiting, p.Pending.Kind, p.Pending.Permission
	case p.WaitsForConditions() && slices.ContainsFunc(sp.Missing, func(c string) bool { return slices.Contains(p.Pending.Conditions, c) }):
		sp.State, sp.Waiting = StepWaiting, TaskCondition // its Missing say for what
	case p.Status == StatusRunning && len(p.Plan) > 0 && slices.Contains(s.Planned, p.Plan[0]):
		sp.State = StepActive
	case p.Status == StatusStuck || p.Status == StatusFailed:
		sp.State = StepBlocked
	case len(sp.Missing) == 0:
		sp.State = StepReady
	default:
		sp.State = StepTodo
	}
	return sp
}

// aggregate is the state of a step of sub-steps that is not done: the most advanced state among them.
func aggregate(steps []StepProgress) string {
	for _, st := range []string{StepWaiting, StepActive, StepBlocked, StepReady} {
		if slices.ContainsFunc(steps, func(s StepProgress) bool { return s.State == st }) {
			return st
		}
	}
	if slices.ContainsFunc(steps, func(s StepProgress) bool { return s.State == StepDone }) {
		return StepActive // started, not finished
	}
	return StepTodo
}

// missing lists the entry conditions that do not hold in a world (the one a run last observed, or the one of a
// change), leaving out the step's own "not done yet" guards (traced: false for a step done when traced).
func missing(world map[string]bool, entry, exit map[string]bool) []string {
	return entryConditions(world, entry, exit, false)
}

// satisfied lists the entry conditions that hold in the world, the counterpart of missing.
func satisfied(world map[string]bool, entry, exit map[string]bool) []string {
	return entryConditions(world, entry, exit, true)
}

func entryConditions(world map[string]bool, entry, exit map[string]bool, holding bool) []string {
	var out []string
	for k, v := range entry {
		if w, own := exit[k]; own && w != v {
			continue // the step's own "not done yet" guard
		}
		if have, ok := world[k]; (ok && have == v) == holding {
			if v {
				out = append(out, k)
			} else {
				out = append(out, "!"+k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// addLanes lists, for the steps that run for each element of a list, the streams started for them.
func (e *Engine) addLanes(ctx context.Context, p *Process, steps []StepProgress) error {
	for i := range steps {
		if err := e.addLanes(ctx, p, steps[i].Steps); err != nil {
			return err
		}
		prefix := steps[i].Path + foreachSuffix
		for _, key := range slices.Sorted(maps.Keys(p.Children)) {
			item, ok := strings.CutPrefix(key, prefix)
			if !ok {
				continue
			}
			c, err := e.Store.Get(ctx, p.Children[key])
			if err != nil {
				return err
			}
			lane := Lane{Item: item, ProcessID: c.ID, Status: c.Status}
			if c.Step != nil {
				lane.Method = c.Step.Method
			}
			steps[i].Lanes = append(steps[i].Lanes, lane)
		}
	}
	return nil
}

// ExplainCondition shows how a condition of the run's world state got its value against the change of the run:
// the formula, the value of each part of it and of the blackboard variables it reads.
// An engine's own condition (change_bound) has no formula: its Expr is empty. ok is false for an unknown name.
func (e *Engine) ExplainCondition(ctx context.Context, id, name string) (ex condition.Explanation, waived, ok bool, err error) {
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return ex, false, false, err
	}
	m, err := e.Methodologies.Methodology(ctx, p.Methodology)
	if err != nil {
		return ex, false, false, err
	}
	var bb domain.Blackboard
	if p.ChangeID == "" {
		bb = domain.Blackboard{Vars: p.Vars}
	} else {
		if bb, err = e.Graph.BlackboardIn(ctx, p.ChangeID, p.Flow); err != nil {
			return ex, false, false, err
		}
		bb.Vars = p.Vars
	}
	bb.Supertypes = e.supertypesOf(m)
	ex, ok = m.Conditions.Explain(name, bb)
	if !ok && name == "change_bound" {
		ex, ok = condition.Explanation{Name: name, Value: p.ChangeID != ""}, true
	}
	_, waived = risk.Waivers(bb.Change, p.ID)[name]
	return ex, waived, ok, nil
}
