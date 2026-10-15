package methodology

import (
	"maps"
	"slices"
	"sort"
	"strings"
)

// Level kinds. Process -> Step -> Method -> MethodStep -> Action: below the last step, an agent operates the actions.
const (
	LevelAgent   = "agent"
	LevelAction  = "action" // the actions that do a leaf step: its internal steps
	LevelProcess = "process"
	LevelMethod  = "method"
	LevelOfStep  = "step"
)

// Gap kinds.
const (
	// GapBlocked: a step of the level can never be entered, whatever the order its siblings run in.
	GapBlocked = "blocked"
	// GapOutput: the level's exit criteria are not all reached by its steps.
	GapOutput = "output"
	// GapInner: a step of the level is not resolved inside (one of its own levels is broken): the level cannot run
	// through it until it is reworked.
	GapInner = "inner"
	// GapNoop: a condition is both an input and an output with the same value, so nothing is done about it: an output
	// must differ from the input. Step is the direct step, empty for the parent itself.
	GapNoop = "noop"
	// GapExternal: a condition the steps of a process or method need and nothing in it establishes; it must hold
	// before the run (a prerequisite), so it is reported but does not break the chain.
	GapExternal = "external"
)

// LevelCheck is the coherence of one level of a process or method: the direct steps of a parent, chained through
// their entry conditions and exit criteria from the parent's inputs to its outputs. The scheduler orders the steps by
// the actual state; this only says that an order exists.
type LevelCheck struct {
	// Path of the parent: the process or method name, or the path of a step with sub-steps.
	Path string
	Kind string
	// Inputs are the entry conditions of the parent (empty for a process or method: it takes none, see GapExternal),
	// Outputs its exit criteria.
	Inputs, Outputs map[string]bool
	// Order lists the direct steps in the order the conditions allow, with the layer each can start in (steps of one
	// layer are independent of each other).
	Order []LevelStep
	Gaps  []LevelGap
	// Agent and Goal: who operates the level (kind agent) and the goal it plans towards.
	Agent, Goal string
	// Steps are the direct steps of the level with their entry and exit, Edges the links their conditions draw
	// between them: the system of interest the flow shows.
	Steps []LevelNode
	Edges []GraphEdge
}

// LevelNode is a direct step of a level.
type LevelNode struct {
	Name, Path, Method, Target, Capability string
	// Foreach and GroupBy: the step runs once per element (or group) of the list, in parallel streams (ADR 0050).
	Foreach, GroupBy string
	Entry, Exit      map[string]bool
	// Composite: the step has sub-steps, a level of its own. Broken: that level, or one below it, is not resolved.
	Composite, Broken bool
	// SubSteps counts the direct sub-steps.
	SubSteps int
}

// LevelStep is a direct step of a level and the layer it can start in.
type LevelStep struct {
	Name  string
	Layer int
}

// LevelGap is a break in the chain of a level.
type LevelGap struct {
	// Step is the direct step concerned (empty: the outputs of the level).
	Step    string
	Kind    string
	Missing []string
	Message string
}

// OK says whether the level has no gap that breaks the chain.
func (l LevelCheck) OK() bool {
	for _, g := range l.Gaps {
		if g.Kind != GapExternal {
			return false
		}
	}
	return true
}

// CheckLevels checks, level by level, that a process (or method composing its own steps) can be planned: for the
// root and each step with sub-steps, a chain exists from the inputs of the parent, through the entry conditions and
// exit criteria of its direct steps, to the outputs of the parent. ok is false when the methodology has no such
// process or method.
func (c *Compiled) CheckLevels(name string) (levels []LevelCheck, ok bool) {
	kind := LevelProcess
	steps := c.ProcessSteps(name)
	if steps == nil {
		kind, steps = LevelMethod, c.MethodSteps(name)
	}
	if steps == nil {
		// a method of actions alone: its agent operates them
		me, ok := c.MethodByName(name)
		if !ok || len(me.Actions) == 0 {
			return nil, false
		}
		lc, ok := c.operatorLevel(name, nil, me.ActorAgent(), me.Name, nil, nil)
		if !ok {
			return nil, false
		}
		lc.Kind = LevelMethod
		return []LevelCheck{lc}, true
	}
	for i := range steps {
		steps[i] = lift(steps[i])
	}
	root := StepInfo{Path: name, Steps: steps, Exit: map[string]bool{}}
	for _, s := range steps {
		maps.Copy(root.Exit, s.Exit)
	}
	var walk func(parent StepInfo, kind string)
	walk = func(parent StepInfo, kind string) {
		levels = append(levels, checkLevel(parent, kind))
		for _, s := range parent.Steps {
			if len(s.Steps) > 0 {
				walk(s, LevelOfStep)
			} else if lc, ok := c.capabilityLevel(s); ok {
				levels = append(levels, lc)
			} else if lc, ok := c.actionLevel(s); ok {
				levels = append(levels, lc)
			}
		}
	}
	walk(root, kind)
	if me, ok := c.MethodByName(name); ok && kind == LevelMethod {
		levels[0].Agent, levels[0].Goal = me.ActorAgent(), me.Name // the agent performs the activities that compose it
	}
	// what is not resolved inside a step surfaces in the level above: the process cannot run through it
	byPath := map[string]*LevelCheck{}
	for i := range levels {
		byPath[levels[i].Path] = &levels[i]
	}
	// a step done by an action or an agent opens on the actions: they are its level
	for i := range levels {
		for j, n := range levels[i].Steps {
			if below, ok := byPath[n.Path]; ok && !n.Composite {
				levels[i].Steps[j].Composite = true
				levels[i].Steps[j].SubSteps = len(below.Steps)
			}
		}
	}
	broken := map[string]bool{}
	var isBroken func(path string) bool
	isBroken = func(path string) bool {
		if v, ok := broken[path]; ok {
			return v
		}
		broken[path] = false
		l, ok := byPath[path]
		if !ok {
			return false
		}
		bad := !l.OK()
		for _, n := range l.Steps {
			if n.Composite && isBroken(n.Path) {
				bad = true
			}
		}
		broken[path] = bad
		return bad
	}
	for i := range levels {
		l := &levels[i]
		for j, n := range l.Steps {
			if n.Composite && isBroken(n.Path) {
				l.Steps[j].Broken = true
				l.Gaps = append(l.Gaps, LevelGap{Step: n.Name, Kind: GapInner,
					Message: "not resolved inside: rework it, the process cannot run through it"})
			}
		}
	}
	return levels, true
}

// lift gives every step with sub-steps, as entry, what it demands from its context: its own entry conditions and what
// its sub-steps need that none of them establishes (the preconditions of an action, say). The level above then chains
// on those demands, so a condition a sub-step takes from a sibling of its parent is not lost.
func lift(s StepInfo) StepInfo {
	if len(s.Steps) == 0 {
		return s
	}
	kids := make([]StepInfo, len(s.Steps))
	made := map[string]bool{}
	for i, c := range s.Steps {
		kids[i] = lift(c)
		for k := range c.Exit {
			made[k] = true
		}
	}
	s.Steps = kids
	entry := maps.Clone(s.Entry)
	if entry == nil {
		entry = map[string]bool{}
	}
	for _, c := range kids {
		for k, v := range c.Entry {
			if _, ok := entry[k]; !ok && !made[k] {
				entry[k] = v
			}
		}
	}
	s.Entry = entry
	return s
}

// MethodVariant is the method of a LevelNode that stands for a method specializing a capability.
const MethodVariant = "variant"

// capabilityLevel is the level below a step that names a capability: the methods that specialize it, alternatives that
// each reach their own goal from the step's entry. Their steps are not part of this level (a specialization adds
// variability and cuts the traceability): each method has a graph of its own.
func (c *Compiled) capabilityLevel(s StepInfo) (LevelCheck, bool) {
	if s.Method() != MethodCapability {
		return LevelCheck{}, false
	}
	methods := c.Methodology.methodsFor(s.Capability, c.methods.goals)
	if len(methods) == 0 {
		return LevelCheck{}, false
	}
	lc := LevelCheck{Path: s.Path, Kind: LevelOfStep, Inputs: maps.Clone(s.Entry), Outputs: maps.Clone(s.Exit)}
	for _, me := range methods {
		target := "always"
		if me.When != "" {
			target = "when " + me.When
		}
		lc.Steps = append(lc.Steps, LevelNode{Name: me.Name, Path: s.Path + "/" + me.Name, Method: MethodVariant, Target: target, Capability: s.Capability,
			Entry: maps.Clone(s.Entry), Exit: maps.Clone(c.methods.goals[me.Name].Pre)})
		lc.Order = append(lc.Order, LevelStep{Name: me.Name})
	}
	lc.Agent = ""
	return lc, true
}

// actionLevel is the level below a leaf step done by an action or by alternative actions: the actions themselves are
// its internal steps (the smallest activity has no flow of its own, it is a node).
func (c *Compiled) actionLevel(s StepInfo) (LevelCheck, bool) {
	if s.Method() != MethodAction {
		return LevelCheck{}, false
	}
	lc, ok := c.operatorLevel(s.Path, s.Actions, "", "", s.Entry, s.Exit, s.Action)
	if !ok {
		return LevelCheck{}, false
	}
	lc.Kind = LevelAction
	return lc, true
}

// operatorLevel is the level below a step done by an action, alternative actions or an agent (or a method naming an
// agent): the actions themselves, which the agent carries out in the order that reaches the goal.
func (c *Compiled) operatorLevel(path string, alternatives []string, agent, goal string, entry, exit map[string]bool, action ...string) (LevelCheck, bool) {
	type act struct {
		name        string
		pre, effect map[string]bool
	}
	var acts []act
	var parent = StepInfo{Path: path, Entry: entry, Exit: exit}
	switch {
	case agent != "":
		ag, ok := c.Agent(agent)
		if !ok {
			return LevelCheck{}, false
		}
		for _, a := range c.AgentActions(ag) {
			acts = append(acts, act{a.Name, a.Pre, a.Effects})
		}
		if goal == "" && len(ag.Goals) == 1 {
			goal = ag.Goals[0]
		}
		if g, ok := c.Goal(goal); ok && parent.Exit == nil {
			parent.Exit = g.Pre
		}
	default:
		names := alternatives
		if len(action) > 0 && action[0] != "" {
			names = []string{action[0]}
		}
		for _, n := range names {
			if a, ok := c.Action(n); ok {
				acts = append(acts, act{a.Name, a.Pre, a.Effects})
			}
		}
	}
	if len(acts) == 0 {
		return LevelCheck{}, false
	}
	for _, a := range acts {
		parent.Steps = append(parent.Steps, StepInfo{Step: Step{Name: a.name, Action: a.name}, Path: path + "/" + a.name, Entry: a.pre, Exit: a.effect})
	}
	lc := checkLevel(parent, LevelAgent)
	lc.Agent, lc.Goal = agent, goal
	return lc, true
}

func checkLevel(parent StepInfo, kind string) LevelCheck {
	lc := LevelCheck{Path: parent.Path, Kind: kind, Inputs: maps.Clone(parent.Entry), Outputs: maps.Clone(parent.Exit)}
	var nodes []GraphStep
	for _, s := range parent.Steps {
		n := LevelNode{Name: s.Name, Path: s.Path, Method: s.Method(), Capability: s.Capability, Foreach: s.Foreach, GroupBy: s.GroupBy, Entry: s.Entry, Exit: s.Exit,
			Composite: len(s.Steps) > 0, SubSteps: len(s.Steps)}
		switch n.Method {
		case MethodAction:
			n.Target = s.Action
			if n.Target == "" {
				n.Target = strings.Join(s.Actions, ", ")
			}
		case MethodProcess:
			n.Target = s.Process
		case MethodCapability:
			n.Target = s.Capability
		}
		lc.Steps = append(lc.Steps, n)
		nodes = append(nodes, GraphStep{Path: s.Path, Entry: s.Entry, Exit: s.Exit, Leaf: true})
	}
	lc.Edges = conditionEdges(nodes)
	// an output that is the input does nothing: the parent's, then each direct step's
	if same := unchanged(parent.Entry, parent.Exit); len(same) > 0 && kind != LevelProcess && kind != LevelMethod {
		lc.Gaps = append(lc.Gaps, LevelGap{Kind: GapNoop, Missing: same, Message: "inputs that are also outputs: an activity does not take what it makes true"})
	}
	for _, s := range parent.Steps {
		if same := unchanged(s.Entry, s.Exit); len(same) > 0 {
			lc.Gaps = append(lc.Gaps, LevelGap{Step: s.Name, Kind: GapNoop, Missing: same, Message: "inputs that are also outputs: an activity does not take what it makes true"})
		}
	}
	if kind == LevelProcess || kind == LevelMethod {
		lc.Inputs = nil // a process or method takes no declared input
	}
	avail := map[string]bool{}
	for k, v := range lc.Inputs {
		if v {
			avail[k] = true
		}
	}
	produced := map[string]bool{}
	for _, s := range parent.Steps {
		for k := range s.Exit {
			produced[k] = true
		}
	}
	// what no step of the level establishes and the parent does not give: it is demanded of the context (the level
	// above chains on it, the root takes it from the state of the change or reports it missing)
	external := map[string]bool{}
	for _, s := range parent.Steps {
		for k, v := range s.Entry {
			if _, in := lc.Inputs[k]; in || produced[k] {
				continue
			}
			if v {
				avail[k] = true
			}
			external[k] = v
		}
	}
	if len(external) > 0 {
		lc.Gaps = append(lc.Gaps, LevelGap{Kind: GapExternal, Missing: sortedKeys(external),
			Message: "needed by the steps and established by none at this level: the context must provide it"})
	}

	met := func(entry map[string]bool) (missing []string) {
		for k, v := range entry {
			if avail[k] != v {
				missing = append(missing, k)
			}
		}
		sort.Strings(missing)
		return missing
	}
	done := map[string]bool{}
	for layer := 0; len(done) < len(parent.Steps); layer++ {
		var ready []StepInfo
		for _, s := range parent.Steps {
			if !done[s.Name] && len(met(s.Entry)) == 0 {
				ready = append(ready, s)
			}
		}
		if len(ready) == 0 {
			break
		}
		// the steps of one layer all start from the same state
		for _, s := range ready {
			done[s.Name] = true
			lc.Order = append(lc.Order, LevelStep{Name: s.Name, Layer: layer})
		}
		for _, s := range ready {
			for k, v := range s.Exit {
				if v {
					avail[k] = true
				} else {
					delete(avail, k)
				}
			}
		}
	}
	for _, s := range parent.Steps {
		if done[s.Name] {
			continue
		}
		missing := met(s.Entry)
		var waits []string // the missing conditions only blocked siblings could establish
		for _, k := range missing {
			for _, o := range parent.Steps {
				if o.Name != s.Name && !done[o.Name] {
					if v, ok := o.Exit[k]; ok && v == s.Entry[k] {
						waits = append(waits, o.Name)
						break
					}
				}
			}
		}
		msg := "cannot be entered: " + strings.Join(missing, ", ") + " never holds at this level"
		if len(waits) > 0 {
			slices.Sort(waits)
			msg = "cannot be entered: waits for " + strings.Join(slices.Compact(waits), ", ") + ", itself blocked (cycle)"
		}
		lc.Gaps = append(lc.Gaps, LevelGap{Step: s.Name, Kind: GapBlocked, Missing: missing, Message: msg})
	}
	if missing := met(lc.Outputs); len(missing) > 0 {
		lc.Gaps = append(lc.Gaps, LevelGap{Kind: GapOutput, Missing: missing,
			Message: "outputs not reached by the steps: " + strings.Join(missing, ", ")})
	}
	return lc
}

func sortedKeys(m map[string]bool) []string {
	return slices.Sorted(maps.Keys(m))
}

// unchanged lists the conditions an activity takes as input and also gives as output, with whatever values: it cannot
// be both (the compiler refuses it, the flow draws it red).
func unchanged(entry, exit map[string]bool) []string { return overlap(entry, exit) }
