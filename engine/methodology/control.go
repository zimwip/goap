package methodology

import (
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/engine/condition"
)

// The second input of an activity (ADR 0162): beside what it consumes (requires), what governs it (controlled_by).
// A control is a condition of the methodology carrying a Control: its expression says that the rules were collected
// (the exercise was done: the output of the activity collecting them, waited for by every activity it controls), its
// Control which rules it covers: the nodes of the domain of some types, linked to the subject of the execution or
// selected by a filter, and the general rules the methodology writes itself. No rule found once collected is a valid
// answer: the activity runs under an empty control.

// Control is what a control condition covers.
type Control struct {
	// Types are the node types of the domain the rules are (alm@ArchitectureDecision, alm@CodingRule); a node of a
	// subtype is not one of them unless named.
	Types []string `yaml:"types,omitempty" json:"types,omitempty"`
	// Via is the link type from a rule to the subject of the execution (alm@governs): only the rules linked to it
	// govern it. Empty: the rules of the types the filter keeps, linked or not.
	Via string `yaml:"via,omitempty" json:"via,omitempty"`
	// Where (CEL: self the rule, subject the subject, change the change) keeps the rules that govern the subject.
	Where string `yaml:"where,omitempty" json:"where,omitempty"`
	// Rules are the general rules of the methodology: they govern every execution the condition controls.
	Rules []ControlRule `yaml:"rules,omitempty" json:"rules,omitempty"`
}

// ControlRule is a rule: one written by the methodology, or one read from the domain.
type ControlRule struct {
	// ID names the rule (the key of its node for a rule of the domain).
	ID string `yaml:"id" json:"id"`
	// Statement is the rule in one imperative sentence: all an agent is given of it.
	Statement string `yaml:"statement" json:"statement"`
	// Severity: veto (default), objective or advice.
	Severity string `yaml:"severity,omitempty" json:"severity,omitempty"`
	// Check measures the rule; nil: the rule guides, it is not measured.
	Check *ControlCheck `yaml:"check,omitempty" json:"check,omitempty"`
	// Fix tells how to fix a violation, in one line.
	Fix string `yaml:"fix,omitempty" json:"fix,omitempty"`
}

// ControlCheck is how a rule is measured: a query or a command, whose rows or lines are the violations.
type ControlCheck struct {
	// Language is cypher (a query over the view of the change, each row a violation) or shell (a command in the
	// workspace of the change, SARIF or `file:line: message` lines, the exit status ignored).
	Language string `yaml:"language" json:"language"`
	Code     string `yaml:"code" json:"code"`
}

// Severities of a rule.
const (
	SeverityVeto      = "veto"
	SeverityObjective = "objective"
	SeverityAdvice    = "advice"
)

// Severities are the severities of a rule, from the heaviest.
var Severities = []string{SeverityVeto, SeverityObjective, SeverityAdvice}

// The languages of a check.
const (
	CheckCypher = "cypher"
	CheckShell  = "shell"
)

// CheckLanguages are the languages a check is written in.
var CheckLanguages = []string{CheckCypher, CheckShell}

// SeverityOf is the severity of a rule, veto when it states none.
func (r ControlRule) SeverityOf() string {
	if r.Severity == "" {
		return SeverityVeto
	}
	return r.Severity
}

// ControlInfo is a control condition as compiled.
type ControlInfo struct {
	// Condition is the condition: it holds once the rules were collected.
	Condition   string
	Description string
	Types       []string
	Via         string
	Where       *condition.RuleFilter
	Rules       []ControlRule
}

// ControlCondition is the control a condition carries.
func (c *Compiled) ControlCondition(name string) (ControlInfo, bool) {
	ci, ok := c.controls[name]
	return ci, ok
}

// ControlsOf are the controls of an execution (ADR 0162): those of the action, then those of the steps it runs for,
// each step with the steps containing it (a step's controls govern its sub-steps), in that order, without repeating
// one. paths are step paths ("<process-or-method>/<step>/...").
func (c *Compiled) ControlsOf(action string, paths ...string) []ControlInfo {
	var names []string
	addAll := func(cs []string) {
		for _, n := range cs {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	if a, ok := c.Action(action); ok {
		addAll(a.Controls)
		if d, ok := c.actions[a.Implements]; ok && a.Implements != "" {
			addAll(d.Controls)
		}
	}
	for _, p := range paths {
		parts := strings.Split(p, "/")
		for i := len(parts); i >= 2; i-- {
			if s, ok := c.StepByPath(strings.Join(parts[:i], "/")); ok {
				addAll(s.Controls)
			}
		}
	}
	out := make([]ControlInfo, 0, len(names))
	for _, n := range names {
		if ci, ok := c.controls[n]; ok {
			out = append(out, ci)
		}
	}
	return out
}

// controlsPre are the controls of an activity as entry conditions: an activity waits for its rules to be collected.
func controlsPre(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// compileControls checks the control conditions and compiles what they cover, then checks that the activities
// naming controls name control conditions.
func (s *compileState) compileControls() {
	s.controls = map[string]ControlInfo{}
	for i, c := range s.m.Conditions {
		if c.Control == nil {
			continue
		}
		path := fmt.Sprintf("conditions[%d].control", i)
		d := c.Definition()
		if d.IsScoped() || d.IsAggregate() {
			s.add(path, "a control is about the change: its condition says the rules were collected (no scope, no aggregate)")
			continue
		}
		ct := c.Control
		if len(ct.Types) == 0 && len(ct.Rules) == 0 {
			s.add(path, "a control covers rules: types of the domain, rules of its own, or both")
		}
		for j, t := range ct.Types {
			if msg := s.m.checkTypeRef(t, false); msg != "" {
				s.add(fmt.Sprintf("%s.types[%d]", path, j), "%s", msg)
			}
		}
		if ct.Via != "" {
			if len(ct.Types) == 0 {
				s.add(path+".via", "via links the rules of the domain to the subject: name their types")
			}
			if msg := s.m.checkTypeRef(ct.Via, true); msg != "" {
				s.add(path+".via", "%s", msg)
			}
		}
		where, err := condition.CompileRuleFilter(ct.Where)
		if err != nil {
			s.add(path+".where", "%v", err)
			continue
		}
		ids := map[string]bool{}
		for j, r := range ct.Rules {
			rp := fmt.Sprintf("%s.rules[%d]", path, j)
			switch {
			case r.ID == "":
				s.add(rp+".id", "id required")
			case ids[r.ID]:
				s.add(rp+".id", "duplicate rule %s", r.ID)
			}
			ids[r.ID] = true
			if strings.TrimSpace(r.Statement) == "" {
				s.add(rp+".statement", "statement required: the rule in one sentence, all an agent is given of it")
			}
			if r.Severity != "" && !slices.Contains(Severities, r.Severity) {
				s.add(rp+".severity", "unknown severity %q (one of %s)", r.Severity, strings.Join(Severities, ", "))
			}
			if r.Check != nil {
				if !slices.Contains(CheckLanguages, r.Check.Language) {
					s.add(rp+".check.language", "unknown language %q (one of %s)", r.Check.Language, strings.Join(CheckLanguages, ", "))
				}
				if strings.TrimSpace(r.Check.Code) == "" {
					s.add(rp+".check.code", "code required: the query or the command measuring the rule")
				}
			}
		}
		s.controls[c.Name] = ControlInfo{Condition: c.Name, Description: c.Description, Types: ct.Types, Via: ct.Via, Where: where, Rules: ct.Rules}
	}
	check := func(path string, names []string) {
		for j, n := range names {
			p := fmt.Sprintf("%s.controls[%d]", path, j)
			switch {
			case !s.known[n]:
				s.add(p, "unknown condition %s: a control is a condition of the methodology carrying a control", n)
			case !s.isControl(n):
				s.add(p, "condition %s carries no control: declare what it covers (control: types, via, where, rules)", n)
			}
		}
	}
	for i, a := range s.m.Actions {
		check(fmt.Sprintf("actions[%d]", i), a.Controls)
	}
	var walk func(path string, steps []Step)
	walk = func(path string, steps []Step) {
		for i, st := range steps {
			sp := fmt.Sprintf("%s.steps[%d]", path, i)
			check(sp, st.Controls)
			walk(sp, st.Steps)
		}
	}
	for i, p := range s.m.Processes {
		walk(fmt.Sprintf("processes[%d]", i), p.Steps)
	}
	for i, me := range s.m.Methods {
		walk(fmt.Sprintf("methods[%d]", i), me.Steps)
	}
}

// isControl tells a control condition (compiled, or declared with a control that has an issue).
func (s *compileState) isControl(name string) bool {
	if _, ok := s.controls[name]; ok {
		return true
	}
	for _, c := range s.m.Conditions {
		if c.Name == name {
			return c.Control != nil
		}
	}
	return false
}
