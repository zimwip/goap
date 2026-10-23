package methodology

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/engine/condition"
	"gopkg.in/yaml.v3"
)

// The second input of an activity (ADR 0162): beside what it consumes (requires), what governs it (controlled_by).
// A condition is never a control by itself: it becomes one by its use, an activity naming it among its controls. The
// condition says that the rules were collected (the exercise was done: the output of the activity collecting them),
// and the use says which rules govern the activity: the nodes of the domain of some types, linked to the subject of
// the execution or selected by a filter, and the general rules the methodology writes itself. The activity waits for
// the condition; no rule found once collected is a valid answer: the activity runs under an empty control.

// Control is the use of a condition as a control of an activity, and what it covers. Written as a name alone, it uses
// the condition with nothing to cover (an issue); stored, it is a controlled_by link carrying the rest.
type Control struct {
	// Condition is the condition the activity waits for: it holds once the rules were collected.
	Condition string `yaml:"condition" json:"condition"`
	// Types are the node types of the domain the rules are (alm@ArchitectureDecision, alm@CodingRule); a node of a
	// subtype is not one of them unless named.
	Types []string `yaml:"types,omitempty" json:"types,omitempty"`
	// Via is the link type from a rule to the subject of the execution (alm@governs): only the rules linked to it
	// govern it. Empty: the rules of the types the filter keeps, linked or not.
	Via string `yaml:"via,omitempty" json:"via,omitempty"`
	// Where (CEL: self the rule, subject the subject, change the change) keeps the rules that govern the subject.
	Where string `yaml:"where,omitempty" json:"where,omitempty"`
	// Rules are the general rules of the methodology: they govern every execution the use controls.
	Rules []ControlRule `yaml:"rules,omitempty" json:"rules,omitempty"`
}

// UnmarshalYAML reads a control written as the name of its condition, or in full.
func (c *Control) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*c = Control{}
		return n.Decode(&c.Condition)
	}
	type plain Control
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*c = Control(p)
	return nil
}

// UnmarshalJSON reads a control written as the name of its condition, or in full.
func (c *Control) UnmarshalJSON(b []byte) error {
	var name string
	if json.Unmarshal(b, &name) == nil {
		*c = Control{Condition: name}
		return nil
	}
	type plain Control
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*c = Control(p)
	return nil
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

// ControlInfo is a control of an activity as compiled.
type ControlInfo struct {
	// Condition is the condition: it holds once the rules were collected.
	Condition   string
	Description string
	Types       []string
	Via         string
	Where       *condition.RuleFilter
	Rules       []ControlRule
}

// actionControls and stepControls key the controls of an action ("action:<name>") and of a step ("step:<path>").
func actionControls(name string) string { return "action:" + name }
func stepControls(path string) string   { return "step:" + path }

// ControlsOf are the controls of an execution (ADR 0162): those of the action, then those of the action it
// implements, then those of the steps it runs for, each step with the steps containing it (a step's controls govern
// its sub-steps). paths are step paths ("<process-or-method>/<step>/...").
func (c *Compiled) ControlsOf(action string, paths ...string) []ControlInfo {
	var out []ControlInfo
	out = append(out, c.controls[actionControls(action)]...)
	if a, ok := c.Action(action); ok && a.Implements != "" {
		out = append(out, c.controls[actionControls(a.Implements)]...)
	}
	for _, p := range paths {
		parts := strings.Split(p, "/")
		for i := len(parts); i >= 2; i-- {
			out = append(out, c.controls[stepControls(strings.Join(parts[:i], "/"))]...)
		}
	}
	return out
}

// controlsPre are the controls of an activity as entry conditions: an activity waits for its rules to be collected.
func controlsPre(cs []Control) map[string]bool {
	if len(cs) == 0 {
		return nil
	}
	out := make(map[string]bool, len(cs))
	for _, c := range cs {
		if c.Condition != "" {
			out[c.Condition] = true
		}
	}
	return out
}

// compileControls checks the controls of the actions and of the steps of the processes and methods, and compiles
// what each covers.
func (s *compileState) compileControls() {
	s.controls = map[string][]ControlInfo{}
	conds := map[string]Condition{}
	for _, c := range s.m.Conditions {
		conds[c.Name] = c
	}
	compile := func(key, path string, cs []Control) {
		for j, ct := range cs {
			cp := fmt.Sprintf("%s.controls[%d]", path, j)
			if ct.Condition == "" {
				s.add(cp+".condition", "condition required: the condition saying the rules were collected")
				continue
			}
			c, declared := conds[ct.Condition]
			switch {
			case !s.known[ct.Condition]:
				s.add(cp+".condition", "unknown condition %s", ct.Condition)
				continue
			case declared && (c.Definition().IsScoped() || c.Definition().IsAggregate()):
				s.add(cp+".condition", "a control waits for a condition about the change: %s is scoped or an aggregate", ct.Condition)
				continue
			}
			if len(ct.Types) == 0 && len(ct.Rules) == 0 {
				s.add(cp, "a control covers rules: types of the domain, rules of its own, or both")
			}
			for k, t := range ct.Types {
				if msg := s.m.checkTypeRef(t, false); msg != "" {
					s.add(fmt.Sprintf("%s.types[%d]", cp, k), "%s", msg)
				}
			}
			if ct.Via != "" {
				if len(ct.Types) == 0 {
					s.add(cp+".via", "via links the rules of the domain to the subject: name their types")
				}
				if msg := s.m.checkTypeRef(ct.Via, true); msg != "" {
					s.add(cp+".via", "%s", msg)
				}
			}
			where, err := condition.CompileRuleFilter(ct.Where)
			if err != nil {
				s.add(cp+".where", "%v", err)
				continue
			}
			ids := map[string]bool{}
			for k, r := range ct.Rules {
				rp := fmt.Sprintf("%s.rules[%d]", cp, k)
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
			s.controls[key] = append(s.controls[key], ControlInfo{Condition: ct.Condition, Description: c.Description,
				Types: ct.Types, Via: ct.Via, Where: where, Rules: ct.Rules})
		}
	}
	for i, a := range s.m.Actions {
		compile(actionControls(a.Name), fmt.Sprintf("actions[%d]", i), a.Controls)
	}
	var walk func(prefix, path string, steps []Step)
	walk = func(prefix, path string, steps []Step) {
		for i, st := range steps {
			sp := fmt.Sprintf("%s.steps[%d]", path, i)
			compile(stepControls(prefix+"/"+st.Name), sp, st.Controls)
			walk(prefix+"/"+st.Name, sp, st.Steps)
		}
	}
	for i, p := range s.m.Processes {
		walk(p.Name, fmt.Sprintf("processes[%d]", i), p.Steps)
	}
	for i, me := range s.m.Methods {
		walk(me.Name, fmt.Sprintf("methods[%d]", i), me.Steps)
	}
}
