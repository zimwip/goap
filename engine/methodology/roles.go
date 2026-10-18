package methodology

import (
	"fmt"
	"slices"

	"github.com/zimwip/goap/lib/def"
)

// Roles of processes, methods, agents and actions (ADR 0136). A process and a method name the role responsible for
// them; their steps inherit what they leave unsaid; the agent running a process or a method acts as its responsible
// role unless it names its own; an action acts as the role of the element it refines (the step it is planned for, the
// method whose pool lists it), else as the role of the action it specializes.

// Over returns r with what it leaves unsaid taken from parent (nil r: parent): sub-steps inherit the roles of the
// steps containing them, then of their process or method, field by field.
func (r *Responsibilities) Over(parent *Responsibilities) *Responsibilities {
	switch {
	case r == nil:
		return parent
	case parent == nil:
		return r
	}
	out := *r
	if out.Responsible == "" {
		out.Responsible = parent.Responsible
	}
	if out.Accountable == "" {
		out.Accountable = parent.Accountable
	}
	if out.Consulted == nil {
		out.Consulted = parent.Consulted
	}
	if out.Informed == nil {
		out.Informed = parent.Informed
	}
	return &out
}

// responsible is the responsible role ("" for nil).
func (r *Responsibilities) responsible() string {
	if r == nil {
		return ""
	}
	return r.Responsible
}

// OwnerRoles returns the roles of the process or method of name (nil when there is none).
func (m *Methodology) OwnerRoles(name string) *Responsibilities {
	if p, ok := m.ProcessByName(name); ok {
		return p.Roles
	}
	for _, me := range m.Methods {
		if me.Name == name {
			return me.Roles
		}
	}
	return nil
}

// stepRoles returns the roles in force for every step of the processes and methods, by path.
func (m *Methodology) stepRoles() map[string]*Responsibilities {
	out := map[string]*Responsibilities{}
	var walk func(steps []Step, prefix string, roles *Responsibilities)
	walk = func(steps []Step, prefix string, roles *Responsibilities) {
		for _, s := range steps {
			path := prefix + "/" + s.Name
			eff := s.Roles.Over(roles)
			out[path] = eff
			walk(s.Steps, path, eff)
		}
	}
	for _, p := range m.Processes {
		walk(p.Steps, p.Name, p.Roles)
	}
	for _, me := range m.Methods {
		walk(me.Steps, me.Name, me.Roles)
	}
	return out
}

// MissingResponsible is the message of a process or a method naming no responsible role.
const MissingResponsible = "name the role responsible for it (roles.responsible): the agent running it acts as that role, and its steps and actions inherit it"

// checkResponsibles requires a responsible role of every process and method (ADR 0136). A stored methodology must
// have them (refused at publication); a draft being written is warned.
func (s *compileState) checkResponsibles(stored bool) {
	severity := def.SeverityWarning
	if stored {
		severity = def.SeverityError
	}
	missing := func(path string, r *Responsibilities) {
		if r.responsible() == "" {
			s.issues = append(s.issues, def.Issue{Path: path + ".roles.responsible", Message: MissingResponsible, Severity: severity})
		}
	}
	for i, p := range s.m.Processes {
		missing(fmt.Sprintf("processes[%d]", i), p.Roles)
	}
	for i, me := range s.m.Methods {
		missing(fmt.Sprintf("methods[%d]", i), me.Roles)
	}
}

// propagateRoles gives the actions and the declared agents that name no role the role they act as (ADR 0136): a
// planned action the responsible role of its step; a declared action the one responsible role of the steps it is
// planned for and the methods whose pool lists it (none when they differ: it depends on where it runs); a
// specialization of an action of this methodology the role of that action; a declared agent the one role of its
// actions.
func (s *compileState) propagateRoles() {
	steps := s.m.stepRoles()
	refiners := map[string][]string{} // declared action -> responsible roles of what refines it
	note := func(action, role string) {
		if role != "" && !slices.Contains(refiners[action], role) {
			refiners[action] = append(refiners[action], role)
		}
	}
	for name, a := range s.actions {
		if a.Step == "" {
			continue
		}
		role := steps[a.Step].responsible()
		if a.Implements != "" {
			note(a.Implements, role)
		}
		if a.Role == "" {
			a.Role = role
			s.actions[name] = a
		}
	}
	for _, me := range s.m.Methods {
		for _, x := range me.Actions {
			note(x, me.Roles.responsible())
		}
	}
	for name, a := range s.actions {
		if a.Step == "" && a.Role == "" && len(refiners[name]) == 1 {
			a.Role = refiners[name][0]
			s.actions[name] = a
		}
	}
	for name, a := range s.actions {
		if !a.IsSpecialization() || a.Role != "" {
			continue
		}
		if target, local := a.SpecializedAction(s.m.Name); local {
			a.Role = s.actions[target].Role
			s.actions[name] = a
		}
	}
	for i, a := range s.procs.actions {
		s.procs.actions[i].Role = s.actions[a.Name].Role
	}
	for name, ag := range s.agents {
		if ag.Role != "" || ag.process != "" {
			continue
		}
		var roles []string
		for _, x := range ag.Actions {
			if r := s.actions[x].Role; r != "" && !slices.Contains(roles, r) {
				roles = append(roles, r)
			}
		}
		if len(roles) == 1 {
			ag.Role = roles[0]
			s.agents[name] = ag
		}
	}
}
