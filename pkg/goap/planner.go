// Package goap implements a Goal Oriented Action Planner (A* over boolean
// world states), in the spirit of Embabel's planner.
package goap

import (
	"container/heap"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// WorldState maps condition names to their known truth value. A condition
// absent from the map is unknown: it satisfies no requirement.
type WorldState map[string]bool

// Satisfies reports whether every requirement holds in the state.
func (w WorldState) Satisfies(req map[string]bool) bool {
	for k, want := range req {
		if got, ok := w[k]; !ok || got != want {
			return false
		}
	}
	return true
}

// Unsatisfied counts the requirements that do not hold.
func (w WorldState) Unsatisfied(req map[string]bool) int {
	n := 0
	for k, want := range req {
		if got, ok := w[k]; !ok || got != want {
			n++
		}
	}
	return n
}

// Apply returns a copy of the state with effects applied.
func (w WorldState) Apply(effects map[string]bool) WorldState {
	out := maps.Clone(w)
	if out == nil {
		out = WorldState{}
	}
	maps.Copy(out, effects)
	return out
}

func (w WorldState) key() string {
	keys := slices.Sorted(maps.Keys(w))
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		if w[k] {
			b.WriteString("=1;")
		} else {
			b.WriteString("=0;")
		}
	}
	return b.String()
}

// Action is a planning operator.
type Action struct {
	Name    string
	Pre     map[string]bool
	Effects map[string]bool
	Cost    float64
}

// Goal is a set of conditions to reach.
type Goal struct {
	Name  string
	Pre   map[string]bool
	Value float64
}

// Plan is an ordered list of actions reaching a goal.
type Plan struct {
	Goal    Goal
	Actions []Action
	Cost    float64
}

// NetValue is the goal value minus the plan cost.
func (p *Plan) NetValue() float64 { return p.Goal.Value - p.Cost }

func (p *Plan) String() string {
	names := make([]string, len(p.Actions))
	for i, a := range p.Actions {
		names[i] = a.Name
	}
	return fmt.Sprintf("%s: [%s] cost=%.1f", p.Goal.Name, strings.Join(names, " -> "), p.Cost)
}

// ErrNoPlan is returned when the goal cannot be reached.
var ErrNoPlan = errors.New("no plan reaches the goal")

// Planner searches plans with A*.
type Planner struct {
	// MaxExpansions bounds the search (default 10000).
	MaxExpansions int
}

// Plan returns the cheapest action sequence reaching goal from start. An
// empty plan is returned when the goal already holds.
func (p Planner) Plan(start WorldState, actions []Action, goal Goal) (*Plan, error) {
	max := p.MaxExpansions
	if max <= 0 {
		max = 10000
	}
	// Admissible heuristic: when the goal does not hold, at least one more
	// action (of at least the minimum cost) is needed. Plans are optimal.
	minCost := 0.0
	for i, a := range actions {
		if c := cost(a); i == 0 || c < minCost {
			minCost = c
		}
	}
	h := func(w WorldState) float64 {
		if w.Satisfies(goal.Pre) {
			return 0
		}
		return minCost
	}

	type node struct {
		state  WorldState
		g      float64
		parent *node
		action *Action
	}
	open := &pq{}
	startN := &node{state: maps.Clone(start)}
	heap.Push(open, &pqItem{value: startN, priority: 0})
	best := map[string]float64{startN.state.key(): 0}
	expansions := 0

	for open.Len() > 0 {
		cur := heap.Pop(open).(*pqItem).value.(*node)
		if cur.state.Satisfies(goal.Pre) {
			var acts []Action
			for n := cur; n.action != nil; n = n.parent {
				acts = append(acts, *n.action)
			}
			slices.Reverse(acts)
			return &Plan{Goal: goal, Actions: acts, Cost: cur.g}, nil
		}
		if g, ok := best[cur.state.key()]; ok && g < cur.g {
			continue
		}
		expansions++
		if expansions > max {
			return nil, fmt.Errorf("search exceeded %d expansions: %w", max, ErrNoPlan)
		}
		for i := range actions {
			a := &actions[i]
			if !cur.state.Satisfies(a.Pre) {
				continue
			}
			next := cur.state.Apply(a.Effects)
			k := next.key()
			g := cur.g + cost(*a)
			if old, ok := best[k]; ok && old <= g {
				continue
			}
			best[k] = g
			f := g + h(next)
			heap.Push(open, &pqItem{value: &node{state: next, g: g, parent: cur, action: a}, priority: f, tie: a.Name})
		}
	}
	return nil, ErrNoPlan
}

// Best plans every goal and returns the plan with the highest net value
// (ties broken by lower cost, then goal name).
func (p Planner) Best(start WorldState, actions []Action, goals []Goal) (*Plan, error) {
	var plans []*Plan
	for _, g := range goals {
		pl, err := p.Plan(start, actions, g)
		if err == nil {
			plans = append(plans, pl)
		}
	}
	if len(plans) == 0 {
		return nil, ErrNoPlan
	}
	sort.SliceStable(plans, func(i, j int) bool {
		if plans[i].NetValue() != plans[j].NetValue() {
			return plans[i].NetValue() > plans[j].NetValue()
		}
		if plans[i].Cost != plans[j].Cost {
			return plans[i].Cost < plans[j].Cost
		}
		return plans[i].Goal.Name < plans[j].Goal.Name
	})
	return plans[0], nil
}

func cost(a Action) float64 {
	if a.Cost <= 0 {
		return 1
	}
	return a.Cost
}

type pqItem struct {
	value    any
	priority float64
	tie      string
	seq      int
}

type pq []*pqItem

func (q pq) Len() int { return len(q) }
func (q pq) Less(i, j int) bool {
	if q[i].priority != q[j].priority {
		return q[i].priority < q[j].priority
	}
	if q[i].tie != q[j].tie {
		return q[i].tie < q[j].tie
	}
	return q[i].seq < q[j].seq
}
func (q pq) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *pq) Push(x any) {
	it := x.(*pqItem)
	it.seq = len(*q)
	*q = append(*q, it)
}
func (q *pq) Pop() any {
	old := *q
	it := old[len(old)-1]
	*q = old[:len(old)-1]
	return it
}
