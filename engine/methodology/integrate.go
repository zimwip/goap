package methodology

import (
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/guard"
)

// Methodologies integrating methodologies (ADR 0106): a methodology declares the methodologies whose changes its
// changes integrate (`integrates`), what it requires of those changes (`guardian`), and reacts to their events (`on`
// with `from`). The levels are derived from the relation, never declared: a methodology that integrates others is one
// level above them (a release above the changes it delivers, a portfolio above its releases).

// Attach modes of an integration: how a candidate change becomes a sub-change of a change of the integrator.
const (
	// AttachDecision: the integrator's process proposes, a decision point of its change settles (default).
	AttachDecision = "decision"
	// AttachAuto: the integrator attaches at once.
	AttachAuto = "auto"
)

// Origins of a subscription (`on[].from`).
const (
	// FromSelf: the events of the change the methodology runs on (default; a transverse methodology, ADR 0036 §3).
	FromSelf = "self"
	// FromIntegrated: the events of the changes attached to a change of the methodology through `integrates`.
	FromIntegrated = "integrated"
	// FromIntegrator: the events of the change of an integrator a change of the methodology is attached to.
	FromIntegrator = "integrator"
	// FromDependency: the events of the changes a change of the methodology depends on (ADR 0108).
	FromDependency = "dependency"
)

// AnyMethodology in Integration.Methodologies integrates every other methodology (IntegrationOf); a wildcard is not
// followed by the check of integration cycles.
const AnyMethodology = "*"

// Integration names methodologies whose changes the changes of this one integrate as sub-changes.
type Integration struct {
	Methodologies []string `yaml:"methodologies" json:"methodologies"`
	// Exclusive: in a project where this methodology is in force, those changes land only through one of its changes
	// (shorthand for a guardian.merge requiring an attached integrator).
	Exclusive bool `yaml:"exclusive,omitempty" json:"exclusive,omitempty"`
	// Attach is how a candidate becomes a sub-change: AttachDecision (default) or AttachAuto.
	Attach string `yaml:"attach,omitempty" json:"attach,omitempty"`
}

// Guardian is what a methodology requires of the changes it integrates, when it is an outer guardian of their chain
// (ADR 0106): CEL guards in the environment of the guards of a change lifecycle, plus `integrator`.
type Guardian struct {
	// Merge must hold for the change to merge.
	Merge string `yaml:"merge,omitempty" json:"merge,omitempty"`
	// Transitions adds a guard to transitions of the change's own lifecycle, by transition name.
	Transitions map[string]string `yaml:"transitions,omitempty" json:"transitions,omitempty"`
}

// IntegratesChangesOf reports whether the changes of m integrate the changes of the methodology named other.
func (m *Methodology) IntegratesChangesOf(other string) bool {
	return m.IntegrationOf(other) != nil
}

// IntegrationOf returns the integration of m naming the methodology other (nil: none). AnyMethodology matches every
// other methodology.
func (m *Methodology) IntegrationOf(other string) *Integration {
	if other == m.Name {
		return nil
	}
	for i, in := range m.Integrates {
		if slices.Contains(in.Methodologies, other) || slices.Contains(in.Methodologies, AnyMethodology) {
			return &m.Integrates[i]
		}
	}
	return nil
}

// MergeGuard is the guard m adds to the merge of a change it integrates: guardian.merge, and an attached integrator
// when the integration is exclusive. "" when it adds none.
func (m *Methodology) MergeGuard(of string) string {
	var parts []string
	if in := m.IntegrationOf(of); in != nil && in.Exclusive {
		parts = append(parts, "integrator.attached")
	}
	if m.Guardian != nil && strings.TrimSpace(m.Guardian.Merge) != "" {
		parts = append(parts, "("+m.Guardian.Merge+")")
	}
	return strings.Join(parts, " && ")
}

// TransitionGuard is the guard m adds to a transition of the lifecycle of a change it integrates ("" when none).
func (m *Methodology) TransitionGuard(name string) string {
	if m.Guardian == nil {
		return ""
	}
	return m.Guardian.Transitions[name]
}

// checkIntegration checks integrates, guardian and the origins of the subscriptions.
func (s *compileState) checkIntegration() {
	m := s.m
	for i, in := range m.Integrates {
		path := fmt.Sprintf("integrates[%d]", i)
		if len(in.Methodologies) == 0 {
			s.add(path+".methodologies", "name the methodologies whose changes this one integrates (or %q)", AnyMethodology)
		}
		for j, other := range in.Methodologies {
			p := fmt.Sprintf("%s.methodologies[%d]", path, j)
			switch {
			case other == AnyMethodology:
			case !def.NameRE.MatchString(other):
				s.add(p, "integrates names methodologies")
			case other == m.Name:
				s.add(p, "a methodology does not integrate itself")
			}
		}
		switch in.Attach {
		case "", AttachDecision, AttachAuto:
		default:
			s.add(path+".attach", "attach must be %s or %s", AttachDecision, AttachAuto)
		}
	}
	if g := m.Guardian; g != nil {
		if len(m.Integrates) == 0 {
			s.add("guardian", "a guardian applies to the changes the methodology integrates: declare integrates")
		}
		if _, err := guard.Compile(g.Merge); err != nil {
			s.add("guardian.merge", "%v", err)
		}
		for name, expr := range g.Transitions {
			if _, err := guard.Compile(expr); err != nil {
				s.add("guardian.transitions."+name, "%v", err)
			}
		}
	}
	for i, sub := range m.On {
		path := fmt.Sprintf("on[%d].from", i)
		switch sub.From {
		case "", FromSelf, FromIntegrator, FromDependency:
		case FromIntegrated:
			if len(m.Integrates) == 0 {
				s.add(path, "from %q needs integrates: the methodology integrates no change", FromIntegrated)
			}
		default:
			s.add(path, "from must be %s, %s, %s or %s", FromSelf, FromIntegrated, FromIntegrator, FromDependency)
		}
	}
}
