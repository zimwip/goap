package engine

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/engine/methodology"
)

// The current state of a change (ADR 0112 §4): where each process of its methodology stands NOW, whatever the
// number of runs it took to get there. The steps are judged against the change as it is (its conditions and its
// history), then what the live runs are doing is laid over them: a step is done because its exit holds on the change,
// not because some run did it once; it is in progress or waiting because a live run is at it. A step whose entry does
// not hold says which conditions it still needs, with what each means.

// Need is a condition a step waits for: its name, the value it needs and what it means.
type Need struct {
	Condition   string `json:"condition"`
	Want        bool   `json:"want"`
	Description string `json:"description,omitempty"`
	// ProducedBy are the actions that make it hold (an effect that is it, or implies it).
	ProducedBy []string `json:"producedBy,omitempty"`
	// Requires are the conditions it cannot hold without (ADR 0112 §5) that do not hold yet: what is to be reached
	// first, each with the actions producing it.
	Requires []Need `json:"requires,omitempty"`
	// Facts are the terms of its expression with whether each holds now (ADR 0112 §5): what it really asks of the
	// change. The first that does not hold carries the actions establishing it.
	Facts []Fact `json:"facts,omitempty"`
}

// Fact is one term of the expression of a condition (condition.Fact) with whether it holds now.
type Fact struct {
	Text  string `json:"text"`
	Holds bool   `json:"holds"`
	Error string `json:"error,omitempty"`
	// Over is the list an exists term ranges over, Parts its predicate part by part.
	Over  string `json:"over,omitempty"`
	Parts []Fact `json:"parts,omitempty"`
	// Missing marks the first term that does not hold; ProducedBy the actions establishing it.
	Missing    bool     `json:"missing,omitempty"`
	ProducedBy []string `json:"producedBy,omitempty"`
}

// NeedState is a condition a step waits for, bound to the change (ADR 0126 step 4): the value it needs, its
// prerequisites that do not hold yet and its facts. What it means and the actions producing it are the compiled view's
// (methodology.View.Conditions).
type NeedState struct {
	Condition string   `json:"condition"`
	Want      bool     `json:"want"`
	Requires  []string `json:"requires,omitempty"`
	Facts     []Fact   `json:"facts,omitempty"`
}

// StepState is where a step of the compiled view stands on a change (ADR 0126 step 4): the binding only, by the path
// of the step; what the step is (its name, guidance, roles, conditions) is the view's.
type StepState struct {
	Path    string      `json:"path"`
	State   string      `json:"state"`
	Missing []string    `json:"missing,omitempty"`
	Needs   []NeedState `json:"needs,omitempty"`
	Runs    int         `json:"runs,omitempty"`
	// ChildProcessIDs are the sub-agents it started, Lanes the streams of a step with foreach.
	ChildProcessIDs []string `json:"childProcessIds,omitempty"`
	Lanes           []Lane   `json:"lanes,omitempty"`
	// Waiting: the kind of the task and the permission an approval needs; Chosen the method chosen for a capability.
	Waiting    string      `json:"waiting,omitempty"`
	Permission string      `json:"permission,omitempty"`
	Chosen     string      `json:"chosen,omitempty"`
	Steps      []StepState `json:"steps,omitempty"`
}

// ProcessState is where one process of the methodology of a change stands now: the binding of a process of the
// compiled view (its description and whether it reacts to events are the view's).
type ProcessState struct {
	Process string `json:"process"`
	// State is the state of the process as a whole: done (its goal holds on the change), or the most advanced state
	// of its steps (active, waiting, blocked, ready, todo).
	State string `json:"state"`
	// Runs counts the executions of the process on the change (each run, and each time a kept run ran again);
	// Current is the run at work now (else the latest), Status its status, Error its error.
	Runs    int         `json:"runs"`
	Current string      `json:"current,omitempty"`
	Status  Status      `json:"status,omitempty"`
	Error   string      `json:"error,omitempty"`
	Steps   []StepState `json:"steps"`
	// Done counts the steps that run something (no sub-steps) done or skipped, out of Total.
	Done, Total int
}

// ChangeState is the current state of a change in its methodology: the binding of its compiled view (Methodology at
// Version, ADR 0113) to the change.
type ChangeState struct {
	ChangeID    domain.ChangeID `json:"changeId"`
	Methodology string          `json:"methodology,omitempty"`
	Version     string          `json:"version,omitempty"`
	Goal        string          `json:"goal,omitempty"`
	GoalReached bool            `json:"goalReached,omitempty"`
	Processes   []ProcessState  `json:"processes"`
}

// ChangeState computes where the processes of the methodology of a change stand now. It reads the change, the
// methodology and the runs; nothing is stored.
func (e *Engine) ChangeState(ctx context.Context, changeID domain.ChangeID) (*ChangeState, error) {
	bb, err := e.blackboard(ctx, changeID, "")
	if err != nil {
		return nil, err
	}
	out := &ChangeState{ChangeID: changeID, Methodology: bb.Change.Methodology, Goal: bb.Change.Goal, Processes: []ProcessState{}}
	if out.Methodology == "" {
		return out, nil
	}
	m, err := e.Methodologies.Methodology(ctx, bb.Change.MethodologyRef()) // the version the change follows (ADR 0113)
	if err != nil {
		return nil, err
	}
	out.Version = m.Version()
	bb.Supertypes = e.supertypesOf(m)
	world := m.Conditions.Evaluate(bb).State
	if out.Goal == "" {
		out.Goal = m.MainGoal()
	}
	if g, ok := m.Goal(out.Goal); ok && len(g.Pre) > 0 {
		out.GoalReached = satisfiesAll(world, g.Pre)
	}
	all, err := e.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	var runs []*Process
	for _, p := range all {
		if p.ChangeID == changeID && p.Methodology == m.Name() {
			runs = append(runs, p)
		}
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].CreatedAt.Before(runs[j].CreatedAt) })
	for _, proc := range m.View().Processes {
		if proc.Nested {
			continue // shown in the step that nests it
		}
		out.Processes = append(out.Processes, e.processState(ctx, m, proc, &bb, world, runs))
	}
	return out, nil
}

// processState lays the live runs of a process over its steps judged against the change.
func (e *Engine) processState(ctx context.Context, m *methodology.Compiled, proc methodology.ActivityView, bb *domain.Blackboard, world map[string]bool, runs []*Process) ProcessState {
	ps := ProcessState{Process: proc.Name, Steps: []StepState{}}
	// the runs of the process (its agent; a nested process or a method has its own)
	var mine []*Process
	for _, p := range runs {
		if ag, ok := m.Agent(p.Agent); ok && ag.ProcessOf() == proc.Name {
			mine = append(mine, p)
		}
	}
	// the change as it is: every step run so far, no run at work
	now := &Process{World: world}
	for _, p := range mine {
		now.Steps = append(now.Steps, p.Steps...)
		ps.Runs += 1 + p.Runs
	}
	steps := proc.Steps
	progress := make([]StepProgress, 0, len(steps))
	for _, s := range steps {
		progress = append(progress, stepProgress(now, s))
	}
	var live *Process
	for _, p := range mine {
		if p.Status.Terminal() {
			continue
		}
		live = p
		for i, s := range steps {
			overlay(&progress[i], stepProgress(p, s))
		}
		if err := e.addLanes(ctx, p, progress); err != nil {
			e.log().Warn("change state lanes", "process", p.ID, "err", err)
		}
	}
	switch {
	case live != nil:
		ps.Current, ps.Status, ps.Error = live.ID, live.Status, live.Error
	case len(mine) > 0:
		last := mine[len(mine)-1]
		ps.Current, ps.Status, ps.Error = last.ID, last.Status, last.Error
	}
	ps.Done, ps.Total = countSteps(progress)
	if len(proc.Done) > 0 && satisfiesAll(world, proc.Done) {
		ps.State = StepDone
	} else {
		ps.State = aggregate(progress)
	}
	ps.Steps = bindSteps(m, world, bb, progress)
	return ps
}

// bindSteps keeps of the progress of steps what binds them to the change, their needs bound too.
func bindSteps(m *methodology.Compiled, world map[string]bool, bb *domain.Blackboard, steps []StepProgress) []StepState {
	out := make([]StepState, 0, len(steps))
	for _, s := range steps {
		st := StepState{Path: s.Path, State: s.State, Missing: s.Missing, Runs: s.Runs, ChildProcessIDs: s.ChildProcessIDs, Lanes: s.Lanes,
			Waiting: s.Waiting, Permission: s.Permission, Chosen: s.Chosen}
		for _, c := range s.Missing {
			st.Needs = append(st.Needs, bindNeed(m, world, bb, c))
		}
		if len(s.Steps) > 0 {
			st.Steps = bindSteps(m, world, bb, s.Steps)
		}
		out = append(out, st)
	}
	return out
}

// bindNeed binds a missing entry condition ("name" / "!name") to the change: its prerequisites that do not hold yet,
// and its facts against the blackboard, for one wanted true.
func bindNeed(m *methodology.Compiled, world map[string]bool, bb *domain.Blackboard, missing string) NeedState {
	name, neg := strings.CutPrefix(missing, "!")
	n := NeedState{Condition: name, Want: !neg}
	if !n.Want {
		return n
	}
	for _, p := range m.View().Condition(name).Requires {
		if !world[p] {
			n.Requires = append(n.Requires, p)
		}
	}
	if bb != nil {
		n.Facts = factsOf(m, name, *bb)
	}
	return n
}

// overlay lays what a live run is doing at a step over its state on the change: a step the change has done stays
// done; one the run is at (active, waiting, blocked) takes the run's state.
func overlay(dst *StepProgress, run StepProgress) {
	for i := range dst.Steps {
		if i < len(run.Steps) {
			overlay(&dst.Steps[i], run.Steps[i])
		}
	}
	for _, c := range run.ChildProcessIDs {
		if !slices.Contains(dst.ChildProcessIDs, c) {
			dst.ChildProcessIDs = append(dst.ChildProcessIDs, c)
		}
	}
	if dst.State == StepDone || dst.State == StepSkipped {
		return
	}
	if len(dst.Steps) > 0 {
		dst.State = aggregate(dst.Steps)
		return
	}
	switch run.State {
	case StepActive, StepWaiting, StepBlocked:
		dst.State, dst.Waiting, dst.Permission = run.State, run.Waiting, run.Permission
		if run.Chosen != "" {
			dst.Chosen = run.Chosen
		}
	}
}

// countSteps counts the steps that run something (no sub-steps) done or skipped, and all of them.
func countSteps(steps []StepProgress) (done, total int) {
	for _, s := range steps {
		if len(s.Steps) > 0 {
			d, t := countSteps(s.Steps)
			done, total = done+d, total+t
			continue
		}
		total++
		if s.State == StepDone || s.State == StepSkipped {
			done++
		}
	}
	return done, total
}

// describeNeeds fills the needs of the steps from what they miss, with what each condition means, the actions
// producing it and, for one wanted true, the conditions it cannot hold without that do not hold yet: the conditions of
// the compiled view, bound to the world and the blackboard.
func describeNeeds(m *methodology.Compiled, world map[string]bool, bb *domain.Blackboard, steps []StepProgress) {
	v := m.View()
	for i := range steps {
		describeNeeds(m, world, bb, steps[i].Steps)
		steps[i].Needs = nil
		for _, c := range steps[i].Missing {
			b := bindNeed(m, world, bb, c)
			cv := v.Condition(b.Condition)
			n := Need{Condition: b.Condition, Want: b.Want, Description: cv.Description, Facts: b.Facts}
			if n.Want {
				n.ProducedBy = cv.ProducedBy
			}
			for _, p := range b.Requires {
				pv := v.Condition(p)
				n.Requires = append(n.Requires, Need{Condition: p, Want: true, Description: pv.Description, ProducedBy: pv.ProducedBy})
			}
			steps[i].Needs = append(steps[i].Needs, n)
		}
	}
}

// factsOf reads a condition into its terms against the blackboard, the first missing one with its producers.
func factsOf(m *methodology.Compiled, name string, bb domain.Blackboard) []Fact {
	if m.Conditions == nil {
		return nil
	}
	cf, ok := m.Conditions.Facts(name, bb)
	if !ok {
		return nil
	}
	miss, missing := condition.Missing(cf)
	var conv func([]condition.Fact) []Fact
	conv = func(fs []condition.Fact) []Fact {
		out := make([]Fact, 0, len(fs))
		for _, f := range fs {
			v := Fact{Text: f.Text, Holds: f.Holds, Error: f.Error, Over: f.Over, Parts: conv(f.Parts)}
			if missing && !f.Holds && f.Text == miss.Text && len(f.Parts) == 0 {
				v.Missing, missing = true, false
				v.ProducedBy = m.ProducersWhere(func(effect string) bool { return m.Conditions.Establishes(effect, miss) })
			}
			out = append(out, v)
		}
		return out
	}
	return conv(cf)
}
