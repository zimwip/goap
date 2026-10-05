package methodology

import (
	"fmt"
	"github.com/zimwip/goap/pkg/domain/def"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/condition"
)

// Process describes how an objective (the realization of a change) is reached, as a tree of steps (ADR 0034): phases,
// steps and sub-steps, each described with the precision the methodology has for it. A step is done by one method:
// sub-steps, an action, an agent (which plans), another process (nested), or by hand (only described). A process is
// run by an agent of its own name, whose goal (also of its name) is that every step is done; a step that runs an agent
// or a process starts it as a sub-agent on the same change, so processes nest.
//
// Steps are not ordered by their position: the planner sequences them by their conditions. A step can be planned once
// its entry conditions hold — those of the steps containing it, its own (Pre), and those of what it runs (the
// preconditions of its action, the prerequisites of a nested process) — and it makes its exit criteria (Done) true.
type Process struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Examples    []string `yaml:"examples,omitempty" json:"examples,omitempty"`
	// References are the reference documents that describe the process.
	References []Reference `yaml:"references,omitempty" json:"references,omitempty"`
	Steps      []Step      `yaml:"steps" json:"steps"`
}

// Reference points at a document of the documentary repository that describes a process or a step: a document of the
// graph ("doc:<key>"), a file of a document repository reached through an MCP ("<mcp>:<path>", e.g.
// "document-repository:procedures/delivery.md"), or a URL; Section narrows it to a part of the document.
type Reference struct {
	Title   string `yaml:"title,omitempty" json:"title,omitempty"`
	Ref     string `yaml:"ref" json:"ref"`
	Section string `yaml:"section,omitempty" json:"section,omitempty"`
}

// String is the reference as a person reads it.
func (r Reference) String() string {
	out := r.Ref
	if r.Title != "" {
		out = r.Title + " (" + r.Ref + ")"
	}
	if r.Section != "" {
		out += ", " + r.Section
	}
	return out
}

// Step is a step of a process. Its method is at most one of Steps, Action (or Actions), Agent and Process; none: a
// manual step, a human task that shows Instructions (or Description).
type Step struct {
	Name         string `yaml:"name" json:"name"`
	Description  string `yaml:"description,omitempty" json:"description,omitempty"`
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	// References are the reference documents that describe the step.
	References []Reference `yaml:"references,omitempty" json:"references,omitempty"`
	// Guidance (markdown) tells whoever does the step, person or agent, what it is for and how to go about it; the
	// Checklist lists what a person checks before marking it done; Deliverables name what it produces (document
	// types, ADR 0035 §5).
	Guidance     string   `yaml:"guidance,omitempty" json:"guidance,omitempty"`
	Checklist    []string `yaml:"checklist,omitempty" json:"checklist,omitempty"`
	Deliverables []string `yaml:"deliverables,omitempty" json:"deliverables,omitempty"`
	// Roles assign the step, RACI style (ADR 0035 §2); sub-steps without their own inherit them.
	Roles *Responsibilities `yaml:"roles,omitempty" json:"roles,omitempty"`
	// Pre are the entry conditions of the step (and of its sub-steps). A condition "step:<path>" is true once the
	// step of that path, done once it has run, has run.
	Pre map[string]bool `yaml:"pre,omitempty" json:"pre,omitempty"`
	// Done are the exit criteria. Default: the effects of the action, the goal of the agent, the exit criteria of the
	// sub-steps or of the nested process; for a manual step or a process of another methodology, the step is done
	// once it has run (the "step:<path>" condition, over a step_done artifact).
	Done  map[string]bool `yaml:"done,omitempty" json:"done,omitempty"`
	Steps []Step          `yaml:"steps,omitempty" json:"steps,omitempty"`
	// Action of the methodology that does the step, or Actions: alternatives the planner chooses among (the cheapest
	// first, another one when it fails).
	Action  string   `yaml:"action,omitempty" json:"action,omitempty"`
	Actions []string `yaml:"actions,omitempty" json:"actions,omitempty"`
	// Process nested in the step: "<process>" of this methodology or "<methodology>/<process>".
	Process string `yaml:"process,omitempty" json:"process,omitempty"`
	// Capability names what the step needs done; the methods providing it (`for`) say how, per context, and which
	// agent acts (ADR 0035 §1). YAML: `method: <capability>`.
	Capability string `yaml:"method,omitempty" json:"method,omitempty"`
	// Foreach (ADR 0050) is a CEL expression on the blackboard returning a list, on a step naming a capability: the
	// step is carried out once per element, in parallel streams, each element bound to `vars.item` and given the most
	// specific method for it (a method's `when` can read `vars.item`). The step is done when every stream is. Unless
	// the step states its exit criteria (`done`, which may read `vars.item` for what each stream reaches), it is done
	// once it has run.
	Foreach string `yaml:"foreach,omitempty" json:"foreach,omitempty"`
	// GroupBy is a CEL expression evaluated for each element of Foreach (the element is `vars.item`) whose result is
	// the key of its group: one stream per group instead of one per element, so that what belongs together is done
	// together (the Java components in one build, the Go ones in another). In a stream, `vars.item` is the group
	// `{key, items}`.
	GroupBy string `yaml:"groupBy,omitempty" json:"groupBy,omitempty"`
}

// Step methods.
const (
	MethodSteps   = "steps"
	MethodAction  = "action"
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
	case s.Process != "":
		return MethodProcess
	case s.Capability != "":
		return MethodCapability
	}
	return MethodManual
}

// StepCondition is the name of the generated condition of a step done once it has run.
func StepCondition(path string) string { return "step:" + path }

// StatePrefix starts the generated condition of a state of the lifecycle of the change (ADR 0058): a step that
// names "state:analysing" in its pre is active while the change is analysing.
const StatePrefix = "state:"

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
	// criteria are the entry and exit criteria of every step, by path
	criteria map[string]stepCriteria
	// extend gives an agent that performs a method the activities that compose it and their goal
	extend map[string]agentExt
}

type agentExt struct{ actions, goals []string }

type stepCriteria struct{ entry, done map[string]bool }

// compileProcesses validates the processes and generates what runs them. actions are the compiled actions (effects
// include the expectations), known the condition names.
func (m *Methodology) compileProcesses(add func(path, format string, args ...any), actions map[string]Action, known map[string]bool, agents map[string]Agent, methods compiledMethods, roles map[string]bool) compiledProcesses {
	out := compiledProcesses{criteria: map[string]stepCriteria{}}
	names := map[string]bool{}
	for i, p := range m.Processes {
		path := fmt.Sprintf("processes[%d]", i)
		switch {
		case p.Name == "":
			add(path+".name", "name required")
			continue
		case !def.NameRE.MatchString(p.Name):
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
		checkReferences(add, path+".references", p.References)
	}
	if m.checkProcessCycles(add) {
		return out
	}
	w := &stepWalker{m: m, add: add, actions: actions, known: known, agents: agents, methods: methods, roles: roles, root: &out, done: map[string]*processCriteria{}}
	for i, p := range m.Processes {
		if names[p.Name] {
			w.process(i)
		}
	}
	// the conditions a step names are checked once every step has its own: a step may name "step:<path>" of any other
	for _, r := range w.refs {
		if state, ok := strings.CutPrefix(r.cond, StatePrefix); ok && state != "" && !known[r.cond] {
			// "state:<name>" is generated (ADR 0058): true while the change is in that state of its lifecycle
			known[r.cond] = true
			out.conditions = append(out.conditions, condition.Definition{Name: r.cond, Expr: fmt.Sprintf("change.state == %q", state)})
		}
		if !known[r.cond] {
			add(r.path, "unknown condition %q", r.cond)
		}
	}
	// in declaration order
	slices.SortStableFunc(out.goals, func(a, b Goal) int { return m.processIndex(a.Name) - m.processIndex(b.Name) })
	slices.SortStableFunc(out.agents, func(a, b Agent) int { return m.processIndex(a.Name) - m.processIndex(b.Name) })
	return out
}

func checkReferences(add func(path, format string, args ...any), path string, refs []Reference) {
	for i, r := range refs {
		if strings.TrimSpace(r.Ref) == "" {
			add(fmt.Sprintf("%s[%d].ref", path, i), "a reference names its document (doc:<key>, <mcp>:<path> or a URL)")
		}
	}
}

func (m *Methodology) processIndex(name string) int {
	return slices.IndexFunc(m.Processes, func(p Process) bool { return p.Name == name })
}

// processCriteria is what a process makes true once done, and what it needs from outside to get there.
type processCriteria struct {
	done, needs map[string]bool
}

// process generates the process at index i once (a process nesting another one of the methodology needs its criteria
// first) and returns them (nil when the process is invalid).
func (w *stepWalker) process(i int) *processCriteria {
	p := w.m.Processes[i]
	if c, ok := w.done[p.Name]; ok {
		return c
	}
	w.done[p.Name] = nil
	path := fmt.Sprintf("processes[%d]", i)
	if len(p.Steps) == 0 {
		w.add(path+".steps", "a process needs at least one step")
		return nil
	}
	sub := *w
	sub.out = &compiledProcesses{}
	done := sub.walk(p.Steps, path+".steps", p.Name, nil)
	w.refs = sub.refs
	w.root.conditions = append(w.root.conditions, sub.out.conditions...)
	w.root.actions = append(w.root.actions, sub.out.actions...)
	if len(done) == 0 {
		return nil
	}
	c := &processCriteria{done: done, needs: prerequisites(sub.out.actions)}
	w.done[p.Name] = c
	var own []string
	for _, a := range sub.out.actions {
		own = append(own, a.Name)
	}
	w.root.goals = append(w.root.goals, Goal{Name: p.Name, Description: p.Description, Examples: p.Examples, Pre: done})
	w.root.agents = append(w.root.agents, Agent{Name: p.Name, Description: p.Description, Examples: p.Examples, Planner: PlannerGOAP,
		Actions: own, Goals: []string{p.Name}, process: p.Name})
	return c
}

// prerequisites are the preconditions of a process's steps that none of its steps establishes: what must hold before
// the process can make any progress on them. A condition required both true and false by two steps is left out.
func prerequisites(actions []Action) map[string]bool {
	produced := map[string]bool{}
	for _, a := range actions {
		for k := range a.Effects {
			produced[k] = true
		}
	}
	out, conflict := map[string]bool{}, map[string]bool{}
	for _, a := range actions {
		for k, v := range a.Pre {
			if produced[k] {
				continue
			}
			if have, ok := out[k]; ok && have != v {
				conflict[k] = true
			}
			out[k] = v
		}
	}
	for k := range conflict {
		delete(out, k)
	}
	return out
}

type stepWalker struct {
	m       *Methodology
	add     func(path, format string, args ...any)
	actions map[string]Action
	known   map[string]bool
	agents  map[string]Agent
	methods compiledMethods
	roles   map[string]bool
	out     *compiledProcesses // what the process being generated adds
	root    *compiledProcesses // what every process adds
	// done holds the criteria of the processes generated so far (nil while one is being generated)
	done map[string]*processCriteria
	// refs are the conditions the steps name, checked once every step condition exists
	refs []conditionRef
}

type conditionRef struct{ path, cond string }

// walk generates the steps of one level and returns what they make true once all done. inherited are the entry
// conditions of the steps containing the level.
func (w *stepWalker) walk(steps []Step, path, prefix string, inherited map[string]bool) map[string]bool {
	all := map[string]bool{}
	seen := map[string]bool{}
	for i, s := range steps {
		sp := fmt.Sprintf("%s[%d]", path, i)
		if s.Name == "" || !def.NameRE.MatchString(s.Name) {
			w.add(sp+".name", "step name required: lowercase letters, digits, '-' or '_', starting with a letter")
			continue
		}
		if seen[s.Name] {
			w.add(sp+".name", "duplicate step %s", s.Name)
			continue
		}
		seen[s.Name] = true
		checkReferences(w.add, sp+".references", s.References)
		checkResponsibilities(w.add, sp+".roles", s.Roles, w.roles)
		for field, c := range map[string]map[string]bool{"pre": s.Pre, "done": s.Done} {
			for k := range c {
				w.refs = append(w.refs, conditionRef{fmt.Sprintf("%s.%s.%s", sp, field, k), k})
			}
		}
		need := maps.Clone(inherited)
		if need == nil {
			need = map[string]bool{}
		}
		w.merge(sp+".pre", need, s.Pre)
		first := len(w.out.actions)
		d := w.step(s, sp, prefix+"/"+s.Name, need)
		w.root.criteria[prefix+"/"+s.Name] = stepCriteria{entry: need, done: d}
		reported := map[string]bool{}
		for _, k := range overlap(need, d) {
			reported[k] = true
			w.add(sp+".done."+k, "%s", inputAndOutput(k, "the step "+prefix+"/"+s.Name))
		}
		for _, a := range w.out.actions[first:] {
			if a.Step != prefix+"/"+s.Name || a.Implements == "" {
				continue
			}
			for _, k := range overlap(a.Pre, a.Effects) {
				if !reported[k] {
					reported[k] = true
					w.add(sp+".pre."+k, "%s", inputAndOutput(k, "the step "+prefix+"/"+s.Name+" (through its action "+a.Implements+")"))
				}
			}
		}
		w.merge(sp+".done", all, d)
	}
	return all
}

// step generates one step and returns its exit criteria.
func (w *stepWalker) step(s Step, sp, path string, need map[string]bool) map[string]bool {
	if s.GroupBy != "" && s.Foreach == "" {
		w.add(sp+".groupBy", "groupBy groups the elements of foreach: declare foreach")
		return nil
	}
	if s.Foreach != "" && s.Capability == "" {
		w.add(sp+".foreach", "foreach applies to a step naming a capability (method): each element is given the most specific method")
		return nil
	}
	methods := 0
	for _, set := range []bool{len(s.Steps) > 0, s.Action != "", len(s.Actions) > 0, s.Process != "", s.Capability != ""} {
		if set {
			methods++
		}
	}
	if methods > 1 {
		w.add(sp, "a step is made of one thing: sub-steps, an action, a process or a method")
		return nil
	}
	done := maps.Clone(s.Done)
	gen := Action{Name: path, Description: s.Description, Pre: need, Cost: 1, Step: path}
	switch s.Method() {
	case MethodSteps:
		sub := w.walk(s.Steps, sp+".steps", path, need)
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
	case MethodProcess:
		other, proc := s.NestedProcess()
		switch {
		case !def.NameRE.MatchString(proc) || (other != "" && !def.NameRE.MatchString(other)):
			w.add(sp+".process", "process must be <process> or <methodology>/<process>")
			return nil
		case (other == "" || other == w.m.Name) && !slices.ContainsFunc(w.m.Processes, func(p Process) bool { return p.Name == proc }):
			w.add(sp+".process", "unknown process %q", proc)
			return nil
		}
		if other == w.m.Name {
			other = ""
		}
		if other == "" {
			// a process of this methodology: done by its exit criteria, which the planner can then chain on, and
			// entered once what it needs from outside holds
			if c := w.process(w.m.processIndex(proc)); c != nil {
				if done == nil {
					done = maps.Clone(c.done)
				}
				gen.Pre = maps.Clone(need)
				w.merge(sp+".pre", gen.Pre, c.needs)
			}
		}
		gen.Kind, gen.Builtin = KindBuiltin, BuiltinStep
		gen.Params = map[string]any{"step": path, "methodology": other, "agent": proc, "goal": proc}
	case MethodCapability:
		candidates := w.m.methodsFor(s.Capability, w.methods.goals)
		if len(candidates) == 0 {
			w.add(sp+".method", "no valid method provides %q (methods[].for)", s.Capability)
			return nil
		}
		if s.Foreach != "" {
			if err := condition.CheckList(s.Foreach); err != nil {
				w.add(sp+".foreach", "%s", err)
				return nil
			}
			gen.Params = map[string]any{"step": path, "capability": s.Capability, "foreach": s.Foreach}
			if s.GroupBy != "" {
				if _, err := condition.CompileValue(s.GroupBy); err != nil {
					w.add(sp+".groupBy", "%s", err)
					return nil
				}
				gen.Params["groupBy"] = s.GroupBy
			}
		} else if done == nil {
			if done = sharedCriteria(candidates, w.methods.goals); len(done) == 0 {
				w.add(sp+".done", "the methods providing %q reach nothing in common: state the exit criteria of the step", s.Capability)
				return nil
			}
		}
		gen.Kind, gen.Builtin = KindBuiltin, BuiltinStep
		if gen.Params == nil {
			gen.Params = map[string]any{"step": path, "capability": s.Capability}
		}
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

// StepInfo is a step of a compiled process: its definition, its path, what it needs and makes true, and the planned
// actions it compiled to (none for a step of sub-steps).
type StepInfo struct {
	Step
	// Effective are the roles in force for the step: its own, else those of the steps containing it.
	Effective *Responsibilities
	Path      string
	Entry     map[string]bool
	Exit      map[string]bool
	Planned   []string
	Steps     []StepInfo
}

// Leaves returns the steps that run something (no sub-steps), depth first.
func (s StepInfo) Leaves() []StepInfo {
	if len(s.Steps) == 0 {
		return []StepInfo{s}
	}
	var out []StepInfo
	for _, c := range s.Steps {
		out = append(out, c.Leaves()...)
	}
	return out
}

// ProcessSteps returns the compiled step tree of a process (nil when the methodology has no such process).
func (c *Compiled) ProcessSteps(name string) []StepInfo {
	p, ok := c.ProcessByName(name)
	if !ok {
		return nil
	}
	return c.stepTree(p.Steps, name)
}

// MethodSteps returns the compiled step tree of a method composing its own steps (nil when it has none).
func (c *Compiled) MethodSteps(name string) []StepInfo {
	for _, me := range c.Methods {
		if me.Name == name && len(me.Steps) > 0 {
			return c.stepTree(me.Steps, name)
		}
	}
	return nil
}

// stepTree builds the compiled step tree under prefix (the name of the process or method).
func (c *Compiled) stepTree(top []Step, prefix string) []StepInfo {
	planned := map[string][]Action{}
	for _, a := range c.processes.actions {
		planned[a.Step] = append(planned[a.Step], a)
	}
	var build func(steps []Step, prefix string, roles *Responsibilities) []StepInfo
	build = func(steps []Step, prefix string, roles *Responsibilities) []StepInfo {
		out := make([]StepInfo, 0, len(steps))
		for _, s := range steps {
			path := prefix + "/" + s.Name
			cr := c.processes.criteria[path]
			eff := roles
			if s.Roles != nil {
				eff = s.Roles
			}
			info := StepInfo{Step: s, Effective: eff, Path: path, Entry: cr.entry, Exit: cr.done, Steps: build(s.Steps, path, eff)}
			info.Step.Steps = nil
			for i, a := range planned[path] {
				info.Planned = append(info.Planned, a.Name)
				if i == 0 {
					info.Entry = a.Pre // with the preconditions of what it runs
				}
			}
			out = append(out, info)
		}
		return out
	}
	return build(top, prefix, nil)
}

// StepByPath returns the step of a path ("<process>/<step>/<sub-step>").
func (c *Compiled) StepByPath(path string) (StepInfo, bool) {
	process, _, _ := strings.Cut(path, "/")
	var find func([]StepInfo) (StepInfo, bool)
	find = func(steps []StepInfo) (StepInfo, bool) {
		for _, s := range steps {
			if s.Path == path {
				return s, true
			}
			if strings.HasPrefix(path, s.Path+"/") {
				return find(s.Steps)
			}
		}
		return StepInfo{}, false
	}
	return find(c.ProcessSteps(process))
}

// StepCriteria returns the entry and exit criteria of a compiled step or method-step by its full path
// ("<process-or-method>/<step>/..."): unlike ProcessSteps/StepByPath, which walk a process's own step tree,
// this reads the shared criteria map directly, so it works for a step owned by a Method's Steps too (method
// steps compile into the same pool, architecture plan "Activity concept").
func (c *Compiled) StepCriteria(path string) (entry, exit map[string]bool, ok bool) {
	cr, ok := c.processes.criteria[path]
	return cr.entry, cr.done, ok
}
