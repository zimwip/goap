package methodology

import (
	"fmt"
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

// ProcessGraph builds the graph of a process (false when the methodology has no such process).
func (c *Compiled) ProcessGraph(name string) (ProcessGraph, bool) {
	p, ok := c.ProcessByName(name)
	if !ok {
		return ProcessGraph{}, false
	}
	g := ProcessGraph{Process: p.Name, Description: p.Description, References: p.References}
	var walk func(steps []StepInfo, parent string, depth int)
	agents := map[string]bool{}
	capabilities := map[string]bool{}
	walk = func(steps []StepInfo, parent string, depth int) {
		for _, s := range steps {
			gs := GraphStep{Path: s.Path, Name: s.Name, Description: s.Description, Parent: parent, Depth: depth, Leaf: len(s.Steps) == 0,
				Method: s.Method(), Entry: s.Entry, Exit: s.Exit, Roles: s.Effective, Guidance: s.Guidance, References: s.References,
				Process: s.Process, Capability: s.Capability}
			switch gs.Method {
			case MethodAction:
				gs.Target = s.Action
				if gs.Target == "" {
					gs.Target = fmt.Sprint(s.Actions)
				}
			case MethodAgent:
				gs.Target = s.Agent
				agents[s.Agent] = true
			case MethodProcess:
				gs.Target = s.Process
			case MethodCapability:
				gs.Target = s.Capability
				capabilities[s.Capability] = true
			}
			g.Steps = append(g.Steps, gs)
			walk(s.Steps, s.Path, depth+1)
		}
	}
	walk(c.ProcessSteps(name), "", 0)
	g.Edges = conditionEdges(g.Steps)
	for _, me := range c.Methodology.Methods {
		if capabilities[me.For] {
			g.Methods = append(g.Methods, GraphMeth{Method: me, AgentGoal: c.MethodGoal(me.Name)})
			agents[me.Agent] = true
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
