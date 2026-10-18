package methodology

import (
	"maps"
	"slices"
	"sort"
)

// ProcessGraph is a process as a graph (ADR 0036 §4): its steps, the edges the conditions draw between them (a step
// whose exit criteria meet what another step needs to be entered), and the methods behind the steps that name a
// capability, with their agents and the agents' actions.
type ProcessGraph struct {
	Process     string       `json:"process"`
	Description string       `json:"description,omitempty"`
	Steps       []GraphStep  `json:"steps"`
	Edges       []GraphEdge  `json:"edges"`
	Methods     []GraphMeth  `json:"methods,omitempty"`
	References  []Reference  `json:"references,omitempty"`
	Agents      []GraphAgent `json:"agents,omitempty"`
}

// GraphStep is a step of the process graph: a leaf (it runs something) or a phase (sub-steps, Parent of its leaves).
type GraphStep struct {
	Path, Name, Description, Parent string
	Depth                           int
	Leaf                            bool
	// Method: steps, action, agent, process, method or manual; Target what it runs.
	Method, Target string
	Entry, Exit    map[string]bool
	Roles          *Responsibilities
	Guidance       string
	References     []Reference
	// Process is the nested process ("<process>" or "<methodology>/<process>"), Capability the capability.
	Process, Capability string
	// Foreach and GroupBy: the step runs once per element (or group) of the list (ADR 0050).
	Foreach, GroupBy string
	// Review: the step is a review of a subject, automatic when it starts by itself (ADR 0130).
	Review *Review
	// Gate: the step is a gate of its process (ADR 0132).
	Gate *Gate
}

// GraphEdge says that the exit criteria of From meet the entry of To, on the given conditions.
type GraphEdge struct {
	From, To   string
	Conditions []string
}

// GraphMeth is a method of a capability a step names, with the agent that acts.
type GraphMeth struct {
	Method
	AgentGoal string
}

// GraphAgent is an agent an agent step or a method names, with its actions.
type GraphAgent struct {
	Name, Description, Planner string
	Goals                      []string
	Actions                    []GraphAction
}

// GraphAction is an action an agent may plan with.
type GraphAction struct {
	Name, Kind, Description string
	Pre, Effects            map[string]bool
}

// ProcessGraph is the flow of a process, or of a method composing its own steps or actions (false when the
// methodology has neither): a projection of the compiled view (ADR 0126 step 3), its step tree flattened, the edges its
// conditions draw, the methods of the capabilities it names and their agents.
func (c *Compiled) ProcessGraph(name string) (ProcessGraph, bool) {
	v := c.View()
	a, ok := v.Process(name)
	if !ok {
		// a method composing its own steps has a graph of its own, apart from the process of the step that names its
		// capability: the method specializes that step, which cuts the traceability (it adds variability)
		if a, ok = v.Method(name); !ok || len(a.Steps) == 0 && len(a.Actions) == 0 {
			return ProcessGraph{}, false
		}
	}
	g := ProcessGraph{Process: a.Name, Description: a.Description, References: a.References}
	agents := map[string]bool{}
	capabilities := map[string]bool{}
	var walk func(steps []StepView, parent string, depth int)
	walk = func(steps []StepView, parent string, depth int) {
		for _, s := range steps {
			g.Steps = append(g.Steps, GraphStep{Path: s.Path, Name: s.Name, Description: s.Description, Parent: parent, Depth: depth, Leaf: len(s.Steps) == 0,
				Method: s.Method, Target: s.Target, Entry: s.Entry, Exit: s.Exit, Roles: s.Roles, Guidance: s.Guidance, References: s.References,
				Process: s.Process, Capability: s.Capability, Foreach: s.Foreach, GroupBy: s.GroupBy, Review: s.Review, Gate: s.Gate})
			if s.Method == MethodCapability {
				capabilities[s.Capability] = true
			}
			walk(s.Steps, s.Path, depth+1)
		}
	}
	walk(a.Steps, "", 0)
	g.Edges = conditionEdges(g.Steps)
	for _, me := range c.definition.Methods {
		if capabilities[me.For] {
			g.Methods = append(g.Methods, GraphMeth{Method: me, AgentGoal: c.MethodGoal(me.Name)})
			agents[me.ActorAgent()] = true
		}
	}
	names := slices.Sorted(maps.Keys(agents))
	for _, n := range names {
		ag, ok := c.Agent(n)
		if !ok {
			continue
		}
		ga := GraphAgent{Name: ag.Name, Description: ag.Description, Planner: ag.Planner, Goals: ag.Goals}
		for _, a := range c.AgentActions(ag) {
			full, _ := c.Action(a.Name)
			ga.Actions = append(ga.Actions, GraphAction{Name: a.Name, Kind: full.Kind, Description: full.Description, Pre: a.Pre, Effects: a.Effects})
		}
		g.Agents = append(g.Agents, ga)
	}
	return g, true
}

// conditionEdges draws an edge from a leaf step to another when its exit criteria meet what the other needs (same
// condition, same value), then drops the edges implied by a longer path (transitive reduction).
func conditionEdges(steps []GraphStep) []GraphEdge {
	var leaves []GraphStep
	for _, s := range steps {
		if s.Leaf {
			leaves = append(leaves, s)
		}
	}
	conds := map[[2]string][]string{}
	next := map[string][]string{}
	for _, a := range leaves {
		for _, b := range leaves {
			if a.Path == b.Path {
				continue
			}
			var on []string
			for k, v := range b.Entry {
				if w, ok := a.Exit[k]; !ok || w != v {
					continue
				}
				if w, ok := a.Entry[k]; ok && w == v {
					continue // a needs it already: it keeps it, it does not produce it
				}
				if w, own := b.Exit[k]; own && w != v {
					continue // b's own "not done yet" guard
				}
				on = append(on, k)
			}
			if len(on) > 0 {
				sort.Strings(on)
				conds[[2]string{a.Path, b.Path}] = on
				next[a.Path] = append(next[a.Path], b.Path)
			}
		}
	}
	// reachable without the direct edge
	reach := func(from, to string) bool {
		seen := map[string]bool{}
		stack := []string{}
		for _, n := range next[from] {
			if n != to {
				stack = append(stack, n)
			}
		}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if n == to {
				return true
			}
			if seen[n] {
				continue
			}
			seen[n] = true
			stack = append(stack, next[n]...)
		}
		return false
	}
	var out []GraphEdge
	for _, a := range leaves {
		for _, b := range next[a.Path] {
			if !reach(a.Path, b) {
				out = append(out, GraphEdge{From: a.Path, To: b, Conditions: conds[[2]string{a.Path, b}]})
			}
		}
	}
	return out
}
