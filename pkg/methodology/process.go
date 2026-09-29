package methodology

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/condition"
)

// Process describes how an objective (the realization of a change) is reached, as a tree of steps (ADR 0034): phases,
// steps and sub-steps, each described with the precision the methodology has for it. A step is done by one method:
// sub-steps, an action, an agent (which plans), another process (nested), or by hand (only described). A process is
// run by an agent of its own name, whose goal (also of its name) is that every top step is done; a step that runs an
// agent or a process starts it as a sub-agent on the same change, so processes nest.
type Process struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Examples    []string `yaml:"examples,omitempty" json:"examples,omitempty"`
	// Parallel lets the top steps run in any order (default: in declaration order).
	Parallel bool   `yaml:"parallel,omitempty" json:"parallel,omitempty"`
	Steps    []Step `yaml:"steps" json:"steps"`
}

// Step is a step of a process. Its method is at most one of Steps, Action (or Actions), Agent and Process; none: a
// manual step, a human task that shows Instructions (or Description).
type Step struct {
	Name         string `yaml:"name" json:"name"`
	Description  string `yaml:"description,omitempty" json:"description,omitempty"`
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	// Pre are the entry conditions of the step, on top of the steps before it being done.
	Pre map[string]bool `yaml:"pre,omitempty" json:"pre,omitempty"`
	// Done are the exit criteria. Default: the effects of the action, the goal of the agent, the exit criteria of the
	// sub-steps; for a nested process or a manual step, the step is done once it has run (a step_done artifact).
	Done map[string]bool `yaml:"done,omitempty" json:"done,omitempty"`
	// After names earlier sibling steps this one waits for, when the parent runs its steps in any order (Parallel);
	// in a sequence, a step waits for the one before it.
	After []string `yaml:"after,omitempty" json:"after,omitempty"`
	// Parallel lets the sub-steps run in any order (default: in declaration order).
	Parallel bool   `yaml:"parallel,omitempty" json:"parallel,omitempty"`
	Steps    []Step `yaml:"steps,omitempty" json:"steps,omitempty"`
	// Action of the methodology that does the step, or Actions: alternatives the planner chooses among (the cheapest
	// first, another one when it fails).
	Action  string   `yaml:"action,omitempty" json:"action,omitempty"`
	Actions []string `yaml:"actions,omitempty" json:"actions,omitempty"`
	// Agent of the methodology that does the step, planning towards Goal (default: its only goal).
	Agent string `yaml:"agent,omitempty" json:"agent,omitempty"`
	Goal  string `yaml:"goal,omitempty" json:"goal,omitempty"`
	// Process nested in the step: "<process>" of this methodology or "<methodology>/<process>".
	Process string `yaml:"process,omitempty" json:"process,omitempty"`
}

// Step methods.
const (
	MethodSteps   = "steps"
	MethodAction  = "action"
	MethodAgent   = "agent"
	MethodProcess = "process"
	MethodManual  = "manual"
)

// BuiltinStep is the builtin of the actions generated for the steps done by an agent or a nested process.
const BuiltinStep = "process.step"

// ArtifactStepDone is the type of the artifact recorded when a step run by an agent, a nested process or a person
// ends: {step: <path>, process: <process id>}.
const ArtifactStepDone = "step_done"

// Method returns how the step is done.
func (s Step) Method() string {
	switch {
	case len(s.Steps) > 0:
		return MethodSteps
	case s.Action != "" || len(s.Actions) > 0:
		return MethodAction
	case s.Agent != "":
		return MethodAgent
	case s.Process != "":
		return MethodProcess
	}
	return MethodManual
}

// StepCondition is the name of the generated condition of a step done once it has run.
func StepCondition(path string) string { return "step:" + path }

// NestedProcess returns the methodology (empty: the one declaring it) and the process a step nests.
func (s Step) NestedProcess() (methodologyName, process string) {
	if i := strings.Index(s.Process, "/"); i >= 0 {
		return s.Process[:i], s.Process[i+1:]
	}
	return "", s.Process
}

// ProcessOf returns the process an agent runs ("" for a declared agent).
func (ag Agent) ProcessOf() string { return ag.process }

// ProcessByName returns a process by name.
func (m *Methodology) ProcessByName(name string) (Process, bool) {
	for _, p := range m.Processes {
		if p.Name == name {
			return p, true
		}
	}
	return Process{}, false
}

// compiledProcesses is what the processes add to a compiled methodology: an action per step that has a method other
// than sub-steps, a goal and an agent per process, and the conditions of the steps done once they have run.
type compiledProcesses struct {
	actions    []Action // in process then step order
	goals      []Goal
	agents     []Agent
	conditions []condition.Definition
}

// compileProcesses validates the processes and generates what runs them. actions are the compiled actions (effects
// include the expectations), known the condition names.
func (m *Methodology) compileProcesses(add func(path, format string, args ...any), actions map[string]Action, known map[string]bool, agents map[string]Agent) compiledProcesses {
	var out compiledProcesses
	names := map[string]bool{}
	for i, p := range m.Processes {
		path := fmt.Sprintf("processes[%d]", i)
		switch {
		case p.Name == "":
			add(path+".name", "name required")
			continue
		case !nameRE.MatchString(p.Name):
			add(path+".name", "name must be lowercase letters, digits, '-' or '_' and start with a letter")
			continue
		case names[p.Name]:
			add(path+".name", "duplicate process %s", p.Name)
			continue
		case agents[p.Name].Name != "":
			add(path+".name", "%s is already an agent: a process is run by an agent of its name", p.Name)
			continue
		case slices.ContainsFunc(m.Goals, func(g Goal) bool { return g.Name == p.Name }):
			add(path+".name", "%s is already a goal: a process reaches a goal of its name", p.Name)
			continue
		}
		names[p.Name] = true
	}
	if m.checkProcessCycles(add) {
		return out
	}
	w := &stepWalker{m: m, add: add, actions: actions, known: known, agents: agents, root: &out, done: map[string]map[string]bool{}}
	for i, p := range m.Processes {
		if names[p.Name] {
			w.process(i)
		}
	}
	// in declaration order
	slices.SortStableFunc(out.goals, func(a, b Goal) int { return m.processIndex(a.Name) - m.processIndex(b.Name) })
	slices.SortStableFunc(out.agents, func(a, b Agent) int { return m.processIndex(a.Name) - m.processIndex(b.Name) })
	return out
}

func (m *Methodology) processIndex(name string) int {
	return slices.IndexFunc(m.Processes, func(p Process) bool { return p.Name == name })
}

// process generates the process at index i once (a process nesting another one of the methodology needs its exit
// criteria first) and returns what it makes true once done.
func (w *stepWalker) process(i int) map[string]bool {
	p := w.m.Processes[i]
	if d, ok := w.done[p.Name]; ok {
		return d
	}
	w.done[p.Name] = nil
	path := fmt.Sprintf("processes[%d]", i)
	if len(p.Steps) == 0 {
		w.add(path+".steps", "a process needs at least one step")
		return nil
	}
	sub := *w
	sub.out = &compiledProcesses{}
	done := sub.walk(p.Steps, path+".steps", p.Name, nil, p.Parallel)
	w.root.conditions = append(w.root.conditions, sub.out.conditions...)
	w.root.actions = append(w.root.actions, sub.out.actions...)
	if len(done) == 0 {
		return nil
	}
	w.done[p.Name] = done
	var own []string
	for _, a := range sub.out.actions {
		own = append(own, a.Name)
	}
	w.root.goals = append(w.root.goals, Goal{Name: p.Name, Description: p.Description, Examples: p.Examples, Pre: done})
	w.root.agents = append(w.root.agents, Agent{Name: p.Name, Description: p.Description, Examples: p.Examples, Planner: PlannerGOAP,
		Actions: own, Goals: []string{p.Name}, process: p.Name})
	return done
}

type stepWalker struct {
	m       *Methodology
	add     func(path, format string, args ...any)
	actions map[string]Action
	known   map[string]bool
	agents  map[string]Agent
	out     *compiledProcesses // what the process being generated adds
	root    *compiledProcesses // what every process adds
	// done holds the exit criteria of the processes generated so far (nil while one is being generated)
	done map[string]map[string]bool
}

// walk generates the steps of one level and returns what they make true once all done. ready is what a step of the
// level needs before its own entry conditions and predecessors.
func (w *stepWalker) walk(steps []Step, path, prefix string, ready map[string]bool, parallel bool) map[string]bool {
	all := map[string]bool{}
	done := map[string]map[string]bool{}
	// after holds what a step makes true once done together with what it needed: a step waits for the whole chain
	// before it, not only for the criteria of the previous step, which may already hold
	after := map[string]map[string]bool{}
	for i, s := range steps {
		sp := fmt.Sprintf("%s[%d]", path, i)
		if s.Name == "" || !nameRE.MatchString(s.Name) {
			w.add(sp+".name", "step name required: lowercase letters, digits, '-' or '_', starting with a letter")
			continue
		}
		if _, dup := done[s.Name]; dup {
			w.add(sp+".name", "duplicate step %s", s.Name)
			continue
		}
		stepPath := prefix + "/" + s.Name
		need := maps.Clone(ready)
		if need == nil {
			need = map[string]bool{}
		}
		w.merge(sp+".pre", need, s.Pre)
		for _, c := range []map[string]bool{s.Pre, s.Done} {
			for k := range c {
				if !w.known[k] {
					w.add(sp, "unknown condition %q", k)
				}
			}
		}
		var preds []string
		if !parallel && i > 0 && steps[i-1].Name != "" {
			preds = append(preds, steps[i-1].Name)
		}
		for _, a := range s.After {
			if !parallel {
				w.add(sp+".after", "after applies to the steps of a parallel level: in a sequence a step follows the one before it")
				break
			}
			if !slices.ContainsFunc(steps[:i], func(o Step) bool { return o.Name == a }) {
				w.add(sp+".after", "%q is not an earlier step of the same level", a)
				continue
			}
			preds = append(preds, a)
		}
		for _, p := range preds {
			w.merge(sp+".after", need, after[p])
		}
		d := w.step(s, sp, stepPath, need)
		done[s.Name] = d
		chain := maps.Clone(need)
		w.merge(sp+".done", chain, d)
		after[s.Name] = chain
		w.merge(sp+".done", all, d)
	}
	return all
}

// step generates one step and returns its exit criteria.
func (w *stepWalker) step(s Step, sp, path string, need map[string]bool) map[string]bool {
	methods := 0
	for _, set := range []bool{len(s.Steps) > 0, s.Action != "", len(s.Actions) > 0, s.Agent != "", s.Process != ""} {
		if set {
			methods++
		}
	}
	if methods > 1 {
		w.add(sp, "a step is done by one method: sub-steps, an action, an agent or a process")
		return nil
	}
	if s.Goal != "" && s.Agent == "" {
		w.add(sp+".goal", "goal applies to a step done by an agent")
	}
	done := maps.Clone(s.Done)
	gen := Action{Name: path, Description: s.Description, Pre: need, Cost: 1, Step: path}
	switch s.Method() {
	case MethodSteps:
		sub := w.walk(s.Steps, sp+".steps", path, need, s.Parallel)
		if done == nil {
			done = map[string]bool{}
		}
		w.merge(sp+".done", done, sub)
		return done
	case MethodAction:
		names, field := s.Actions, sp+".actions"
		if s.Action != "" {
			names, field = []string{s.Action}, sp+".action"
		}
		for _, name := range names {
			a, ok := w.actions[name]
			switch {
			case !ok:
				w.add(field, "unknown action %q", name)
				return nil
			case a.IsSpecialization():
				w.add(field, "%q is a specialization: name the action it specializes", name)
				return nil
			}
			if done == nil {
				done = maps.Clone(a.Effects)
			}
			alt := a
			alt.Name, alt.Step, alt.Implements, alt.Utility = path, path, a.Name, ""
			if len(names) > 1 {
				alt.Name = path + ":" + a.Name // one planned action per alternative
			}
			if s.Description != "" {
				alt.Description = s.Description
			}
			alt.Pre = maps.Clone(need)
			w.merge(field, alt.Pre, a.Pre)
			alt.Effects = maps.Clone(a.Effects)
			w.merge(sp+".done", alt.Effects, done)
			w.out.actions = append(w.out.actions, alt)
		}
		return done
	case MethodAgent:
		ag, ok := w.agents[s.Agent]
		if !ok || ag.process != "" {
			w.add(sp+".agent", "unknown agent %q", s.Agent)
			return nil
		}
		goal := s.Goal
		if goal == "" {
			if gs := w.agentGoals(ag); len(gs) == 1 {
				goal = gs[0].Name
			} else {
				w.add(sp+".goal", "agent %s has several goals: name the one the step reaches", ag.Name)
				return nil
			}
		}
		g, ok := w.goal(goal)
		if !ok || (len(ag.Goals) > 0 && !slices.Contains(ag.Goals, goal)) {
			w.add(sp+".goal", "%q is not a goal of agent %s", goal, ag.Name)
			return nil
		}
		if done == nil {
			done = maps.Clone(g.Pre)
		}
		gen.Kind, gen.Builtin = KindBuiltin, BuiltinStep
		gen.Params = map[string]any{"step": path, "agent": ag.Name, "goal": goal}
	case MethodProcess:
		other, proc := s.NestedProcess()
		switch {
		case !nameRE.MatchString(proc) || (other != "" && !nameRE.MatchString(other)):
			w.add(sp+".process", "process must be <process> or <methodology>/<process>")
			return nil
		case (other == "" || other == w.m.Name) && !slices.ContainsFunc(w.m.Processes, func(p Process) bool { return p.Name == proc }):
			w.add(sp+".process", "unknown process %q", proc)
			return nil
		}
		if other == w.m.Name {
			other = ""
		}
		if other == "" && done == nil {
			// a process of this methodology: done by its exit criteria, which the planner can then chain on
			done = maps.Clone(w.process(w.m.processIndex(proc)))
		}
		gen.Kind, gen.Builtin = KindBuiltin, BuiltinStep
		gen.Params = map[string]any{"step": path, "methodology": other, "agent": proc, "goal": proc}
	case MethodManual:
		gen.Kind = KindHuman
		gen.Instructions = s.Instructions
		if gen.Instructions == "" {
			gen.Instructions = s.Description
		}
		if gen.Description == "" {
			gen.Description = "Step " + path
		}
	}
	if done == nil {
		// done once it has run: the builtin or the human task records a step_done artifact
		name := StepCondition(path)
		if !w.known[name] {
			w.known[name] = true
			w.out.conditions = append(w.out.conditions, condition.Definition{Name: name,
				Expr: fmt.Sprintf("artifacts.exists(a, a.type == %q && a.data.step == %q)", ArtifactStepDone, path)})
		}
		done = map[string]bool{name: true}
	}
	if gen.Effects == nil {
		gen.Effects = maps.Clone(done)
	}
	w.out.actions = append(w.out.actions, gen)
	return done
}

func (w *stepWalker) agentGoals(ag Agent) []Goal {
	if len(ag.Goals) == 0 {
		return w.m.Goals
	}
	var out []Goal
	for _, name := range ag.Goals {
		if g, ok := w.goal(name); ok {
			out = append(out, g)
		}
	}
	return out
}

func (w *stepWalker) goal(name string) (Goal, bool) {
	for _, g := range w.m.Goals {
		if g.Name == name {
			return g, true
		}
	}
	return Goal{}, false
}

// merge adds src to dst, reporting a condition both require with opposite values.
func (w *stepWalker) merge(path string, dst, src map[string]bool) {
	for k, v := range src {
		if have, ok := dst[k]; ok && have != v {
			w.add(path, "condition %s is required both true and false", k)
			continue
		}
		dst[k] = v
	}
}

// checkProcessCycles reports processes of the methodology that nest themselves.
func (m *Methodology) checkProcessCycles(add func(path, format string, args ...any)) (found bool) {
	nested := map[string][]string{}
	var collect func(p string, steps []Step)
	collect = func(p string, steps []Step) {
		for _, s := range steps {
			if other, proc := s.NestedProcess(); s.Process != "" && (other == "" || other == m.Name) {
				nested[p] = append(nested[p], proc)
			}
			collect(p, s.Steps)
		}
	}
	for _, p := range m.Processes {
		collect(p.Name, p.Steps)
	}
	for i, p := range m.Processes {
		seen := map[string]bool{}
		stack := slices.Clone(nested[p.Name])
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if n == p.Name {
				add(fmt.Sprintf("processes[%d].steps", i), "process %s nests itself", p.Name)
				found = true
				break
			}
			if !seen[n] {
				seen[n] = true
				stack = append(stack, nested[n]...)
			}
		}
	}
	return found
}
