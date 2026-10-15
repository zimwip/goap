package domain

import (
	"cmp"
	"slices"
)

// Dependencies between changes (ADR 0108): a change may depend on others, whose work must have reached the branch it
// merges into before it merges. The relation is stored on the dependent change (Change.DependsOn); it orders the
// merges into an integrator (DependencyOrder) and is shown to the guards of the change (FacetDependencies).

// Dependency is a change another change depends on (or, read the other way, depends on it), as seen from that other
// change.
type Dependency struct {
	ID          ChangeID     `json:"id"`
	Title       string       `json:"title,omitempty"`
	Methodology string       `json:"methodology,omitempty"`
	Status      ChangeStatus `json:"status"`
	State       string       `json:"state,omitempty"`
	ParentID    ChangeID     `json:"parentId,omitempty"`
	// Satisfied: the work of the dependency has reached the branch the dependent change merges into (main, every
	// change of its chain applied; or the dependent's parent, the dependency merged into it).
	Satisfied bool `json:"satisfied"`
	// Reason says why it is not satisfied.
	Reason string `json:"reason,omitempty"`
}

// DependencyOrder is the order in which changes merge given their dependencies: each after the changes of the list it
// depends on, ties (and a cycle, which the graph never stores) by creation then id. Dependencies outside the list are
// ignored.
func DependencyOrder(cs []Change) []Change {
	in := make(map[ChangeID]bool, len(cs))
	for _, c := range cs {
		in[c.ID] = true
	}
	rest := slices.Clone(cs)
	slices.SortStableFunc(rest, func(a, b Change) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	done := make(map[ChangeID]bool, len(cs))
	out := make([]Change, 0, len(cs))
	for len(rest) > 0 {
		i := slices.IndexFunc(rest, func(c Change) bool {
			for _, d := range c.DependsOn {
				if in[d] && !done[d] {
					return false
				}
			}
			return true
		})
		if i < 0 {
			i = 0 // a cycle: the oldest first
		}
		done[rest[i].ID] = true
		out = append(out, rest[i])
		rest = slices.Delete(rest, i, i+1)
	}
	return out
}
