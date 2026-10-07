package methodology

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
)

// Hints returns the quality remarks on a compiled methodology: things that do not stop it from being published but that
// an author wants to know (ADR 0097). They are never compile issues.
//
// A well-made methodology sequences its activities by their conditions, so that something can start on an empty
// change: when no step (or action, with no process) working towards the main goal has its entry conditions satisfied
// there, the change could never take its first step and the hint, tied to the path `goal`, says what the first ones wait for.
func (c *Compiled) Hints() def.Issues {
	name := c.MainGoal()
	g, ok := c.Goal(name)
	if !ok || len(g.Pre) == 0 {
		return nil
	}
	world := c.Conditions.Evaluate(domain.Blackboard{}).State
	world["change_bound"] = true
	holds := func(want map[string]bool) bool {
		for k, v := range want {
			if world[k] != v {
				return false
			}
		}
		return true
	}
	if holds(g.Pre) {
		return nil
	}
	needed := c.NeededBy(g.Pre)
	towards := func(effects map[string]bool) bool {
		for k := range effects {
			if needed[k] {
				return true
			}
		}
		return false
	}
	waits := map[string]bool{}
	possible := false
	if len(c.Processes) > 0 {
		nested := map[string]bool{}
		for _, p := range c.Processes {
			var walk func([]StepInfo)
			walk = func(steps []StepInfo) {
				for _, s := range steps {
					if s.Process != "" {
						if other, proc := s.NestedProcess(); other == "" || other == c.Name {
							nested[proc] = true
						}
					}
					walk(s.Steps)
				}
			}
			walk(c.ProcessSteps(p.Name))
		}
		var visit func(StepInfo)
		visit = func(s StepInfo) {
			if !towards(s.Exit) || (len(s.Exit) > 0 && holds(s.Exit)) {
				return
			}
			if miss := missing(world, s.Entry, s.Exit); len(miss) > 0 {
				for _, m := range miss {
					waits[m] = true
				}
				return
			}
			possible = true
		}
		for _, p := range c.Processes {
			if nested[p.Name] {
				continue
			}
			for _, s := range c.ProcessSteps(p.Name) {
				visit(s)
			}
		}
	} else {
		for _, a := range c.actions {
			if a.IsSpecialization() || !towards(a.Effects) || holds(a.Effects) {
				continue
			}
			if miss := missing(world, a.Pre, a.Effects); len(miss) > 0 {
				for _, m := range miss {
					waits[m] = true
				}
				continue
			}
			possible = true
		}
	}
	if possible {
		return nil
	}
	var first []string
	for k := range waits {
		first = append(first, k)
	}
	sort.Strings(first)
	if len(first) > 5 {
		first = first[:5]
	}
	msg := fmt.Sprintf("nothing can start on an empty change towards the goal %q: every step that works towards it waits for a condition", name)
	if len(first) > 0 {
		msg += " (first: " + strings.Join(first, ", ") + ")"
	}
	return def.Issues{{Path: "goal", Message: msg + "; give a first step no precondition, or a condition an empty change satisfies"}}
}

// missing lists the entry conditions that do not hold in a world, leaving out the activity's own "not done yet" guards.
func missing(world, entry, exit map[string]bool) []string {
	var out []string
	for k, v := range entry {
		if w, own := exit[k]; own && w != v {
			continue
		}
		if have, ok := world[k]; !ok || have != v {
			if !v {
				k = "!" + k
			}
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
