package adapter

import (
	"slices"

	"github.com/zimwip/goap/pkg/mcp"
)

// Restriction is what the adapter instances of a unit's chain leave of an MCP (ADR 0028). Every
// instance of the chain adds its restrictions: a unit narrows what its ancestors allow and hands
// the result down to its sub-units, which cannot widen it.
type Restriction struct {
	// Disabled: an instance of the chain removes the MCP.
	Disabled bool
	// Allow, when not nil, is the intersection of the allow-lists of the chain.
	Allow []string
	// Deny is the union of the deny-lists of the chain.
	Deny []string
	// ReadOnly: an instance of the chain keeps only the read-only tools.
	ReadOnly bool
	// By lists the units whose instance restricts, nearest first.
	By []string
}

// Add adds the restrictions of an instance of the chain.
func (r *Restriction) Add(a Instance) {
	if !a.Restricts() {
		return
	}
	r.By = append(r.By, a.Unit)
	r.Disabled = r.Disabled || a.Disabled
	r.ReadOnly = r.ReadOnly || a.ReadOnly
	for _, t := range a.Deny {
		if !slices.Contains(r.Deny, t) {
			r.Deny = append(r.Deny, t)
		}
	}
	if len(a.Tools) > 0 {
		if r.Allow == nil {
			r.Allow = slices.Clone(a.Tools)
		} else {
			r.Allow = slices.DeleteFunc(r.Allow, func(t string) bool { return !slices.Contains(a.Tools, t) })
		}
	}
}

// Allows reports whether a tool of the MCP is allowed.
func (r Restriction) Allows(t mcp.Tool) bool {
	switch {
	case r.Disabled, r.ReadOnly && !t.ReadOnly, slices.Contains(r.Deny, t.Name):
		return false
	case r.Allow != nil:
		return slices.Contains(r.Allow, t.Name)
	}
	return true
}

// Apply returns the definition with only the allowed tools.
func (r Restriction) Apply(d mcp.Def) mcp.Def {
	out := d
	out.Tools = nil
	for _, t := range d.Tools {
		if r.Allows(t) {
			out.Tools = append(out.Tools, t)
		}
	}
	return out
}
