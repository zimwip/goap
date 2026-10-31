package methodology

import (
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/def"
)

// Maturity is the axis of maturity of the changes of a methodology (ADR 0179): the phases a change goes through, in
// order. The methodology states them itself instead of naming the lifecycle of a domain (Lifecycle): the lifecycle of
// its changes is then derived from the phases and from the gates of its processes, each gate a transition between two
// phases (Compiled.Lifecycle). The guard of a transition is the entry of its gate and its checklist; the move is the
// recorded outcome of the gate's decision.
type Maturity struct {
	Description string  `yaml:"description,omitempty" json:"description,omitempty"`
	Phases      []Phase `yaml:"phases" json:"phases"`
}

// Phase is a phase of the maturity of a change: a state of the derived lifecycle.
type Phase struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Final: the change is done (or given up) in this phase; a change merges and closes only in a final phase.
	Final bool `yaml:"final,omitempty" json:"final,omitempty"`
	// NotCommittable: the change does not commit while in this phase (ADR 0078, the floor of the guardian of commits).
	NotCommittable bool `yaml:"notCommittable,omitempty" json:"notCommittable,omitempty"`
	// Exit names a transition into this phase from every phase that is not final (abandon), taken by a person with
	// no gate: a way out of the process at any time.
	Exit string `yaml:"exit,omitempty" json:"exit,omitempty"`
}

// HasLifecycle says whether the changes of the methodology follow a lifecycle: one of a domain, or the one derived
// from its maturity.
func (m *Methodology) HasLifecycle() bool { return m.Lifecycle != "" || m.Maturity != nil }

// LifecycleName is the name of the lifecycle the changes of the methodology follow: the lifecycle of a domain it
// names, else the methodology's own name when it derives one from its maturity (stable across its versions, so a
// change upgrades to another version keeping its state). Empty: none.
func (m *Methodology) LifecycleName() string {
	switch {
	case m.Lifecycle != "":
		return m.Lifecycle
	case m.Maturity != nil:
		return m.Name
	}
	return ""
}

// phase returns a phase of the maturity of m and its index (-1: none).
func (m *Methodology) phase(name string) (Phase, int) {
	if m.Maturity == nil {
		return Phase{}, -1
	}
	i := slices.IndexFunc(m.Maturity.Phases, func(p Phase) bool { return p.Name == name })
	if i < 0 {
		return Phase{}, -1
	}
	return m.Maturity.Phases[i], i
}

// checkMaturity validates the maturity of the methodology: named phases, unique, at least one not final (the first,
// where a change starts) and one final, exit transitions well named.
func (s *compileState) checkMaturity() {
	m := s.m
	if m.Maturity == nil {
		return
	}
	add := s.add
	if m.Lifecycle != "" {
		add("maturity", "a methodology names the lifecycle of a domain (lifecycle) or states its own maturity (maturity), not both")
		return
	}
	phases := m.Maturity.Phases
	if len(phases) == 0 {
		add("maturity.phases", "name the phases a change goes through, in order")
		return
	}
	seen := map[string]bool{}
	final := false
	for i, p := range phases {
		pp := fmt.Sprintf("maturity.phases[%d]", i)
		switch {
		case p.Name == "":
			add(pp+".name", "name required")
			continue
		case !def.NameRE.MatchString(p.Name):
			add(pp+".name", "name must be lowercase letters, digits, '-' or '_' and start with a letter")
			continue
		case seen[p.Name]:
			add(pp+".name", "duplicate phase %s", p.Name)
			continue
		}
		seen[p.Name] = true
		final = final || p.Final
		if p.Exit != "" {
			if !def.NameRE.MatchString(p.Exit) {
				add(pp+".exit", "a transition is named with lowercase letters, digits, '-' or '_'")
			}
			if !p.Final {
				add(pp+".exit", "a way out of the process leads to a final phase: flag %s final", p.Name)
			}
		}
	}
	if phases[0].Final {
		add("maturity.phases[0].final", "a change starts in the first phase: it cannot be final")
	}
	if !final {
		add("maturity.phases", "a change ends: flag at least one phase final")
	}
}

// gatePhases resolves the phases a gate of a methodology with a maturity moves between: from (default, the phase
// before the one it enters) to enters, and back to backTo. It reports what is wrong through add.
func (w *stepWalker) gatePhases(sp string, g *Gate) (from, to string, ok bool) {
	m := w.m
	if g.Enters == "" {
		w.add(sp+".gate.enters", "name the phase of the maturity of the change the go enters")
		return "", "", false
	}
	enters, i := m.phase(g.Enters)
	if i < 0 {
		w.add(sp+".gate.enters", "unknown phase %q of the maturity", g.Enters)
		return "", "", false
	}
	from = g.From
	if from == "" {
		if i == 0 {
			w.add(sp+".gate.enters", "the go enters the first phase: name the phase it leaves (from)")
			return "", "", false
		}
		from = m.Maturity.Phases[i-1].Name
	}
	left, j := m.phase(from)
	switch {
	case j < 0:
		w.add(sp+".gate.from", "unknown phase %q of the maturity", from)
		return "", "", false
	case left.Final:
		w.add(sp+".gate.from", "the phase %s is final: no gate leaves it", from)
		return "", "", false
	case from == enters.Name:
		w.add(sp+".gate.enters", "the go enters another phase than the one it leaves")
		return "", "", false
	}
	if d := g.Decision; d != nil && d.BackTo != "" {
		if _, k := m.phase(d.BackTo); k < 0 {
			w.add(sp+".gate.decision.backTo", "unknown phase %q of the maturity", d.BackTo)
			return "", "", false
		}
		if d.BackTo == from {
			w.add(sp+".gate.decision.backTo", "back leads to another phase than the one the gate is in (%s)", from)
			return "", "", false
		}
	}
	return from, enters.Name, true
}

// buildLifecycle derives the lifecycle of the changes from the maturity and the gates (ADR 0179): the phases are its
// states, the first its initial state, each gate the transition of its go (from -> enters) and of its back (from ->
// backTo), each exit a transition from every phase not final. The criteria and freezes of the gates are merged later,
// as for the lifecycle of a domain (registrysvc.WithGates). nil: the methodology has no maturity.
func (c *Compiled) buildLifecycle() *domain.Lifecycle {
	m := c.definition
	if m.Maturity == nil || len(m.Maturity.Phases) == 0 {
		return nil
	}
	lc := &domain.Lifecycle{Name: m.LifecycleName(), Description: m.Maturity.Description, Initial: m.Maturity.Phases[0].Name}
	if lc.Description == "" {
		lc.Description = "The maturity of the changes of " + m.Name
	}
	for _, p := range m.Maturity.Phases {
		lc.States = append(lc.States, domain.LifecycleState{Name: p.Name, Description: p.Description, Final: p.Final, NotCommittable: p.NotCommittable})
	}
	for _, g := range c.gates {
		if g.From == "" || g.Enters == "" {
			continue
		}
		lc.Transitions = append(lc.Transitions, domain.Transition{Name: g.Transition, From: g.From, To: g.Enters,
			Description: fmt.Sprintf("The go of the gate %s", g.Path)})
		if g.Back != "" && g.BackTo != "" {
			lc.Transitions = append(lc.Transitions, domain.Transition{Name: g.Back, From: g.From, To: g.BackTo,
				Description: fmt.Sprintf("The decision of the gate %s sends the change back to %s", g.Path, g.BackTo)})
		}
	}
	for _, p := range m.Maturity.Phases {
		if p.Exit == "" {
			continue
		}
		for _, q := range m.Maturity.Phases {
			if !q.Final {
				lc.Transitions = append(lc.Transitions, domain.Transition{Name: p.Exit, From: q.Name, To: p.Name,
					Description: firstNonEmpty(p.Description, "Leave the process for "+p.Name)})
			}
		}
	}
	return lc
}

// Lifecycle is the lifecycle the compiled methodology derives from its maturity (ADR 0179), without the criteria of
// its gates (registrysvc.WithGates merges them); nil when it states no maturity (it names the lifecycle of a domain,
// or none). The returned lifecycle is a copy.
func (c *Compiled) Lifecycle() *domain.Lifecycle {
	if c.lifecycle == nil {
		return nil
	}
	lc := *c.lifecycle
	lc.States = slices.Clone(lc.States)
	lc.Transitions = slices.Clone(lc.Transitions)
	return &lc
}
