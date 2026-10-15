package methodology

import (
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/engine/condition"
)

// AssessmentView says how the assessments of the subjects its pattern matches read (ADR 0116): the states they go
// through, the first that holds wins. Subject is a subject key (description, title, intent, impact:<key>,
// item:<key>) or a glob over them (impact:*, item:*, *). A subject no view settles follows the platform's defaults
// (condition.DefaultAssessmentViews).
type AssessmentView struct {
	Subject string            `yaml:"subject" json:"subject"`
	States  []AssessmentState `yaml:"states" json:"states"`
}

// AssessmentState is one state of an assessment: it holds when each condition has the value given (a scoped
// condition read on the subject) and When, a CEL expression over the conditions' variables plus `assessment`,
// `subject` and `conditions` (condition.AssessmentEnv), is true; neither: it always holds. Tone is how it is shown
// (ok, warn, danger, info, neutral), Label what it says, Open whether the bubble opens by itself while it holds.
type AssessmentState struct {
	When       string          `yaml:"when,omitempty" json:"when,omitempty"`
	Conditions map[string]bool `yaml:"conditions,omitempty" json:"conditions,omitempty"`
	Tone       string          `yaml:"tone" json:"tone"`
	Label      string          `yaml:"label,omitempty" json:"label,omitempty"`
	Open       bool            `yaml:"open,omitempty" json:"open,omitempty"`
}

func (v AssessmentView) rules() condition.SubjectView {
	out := condition.SubjectView{Subject: v.Subject}
	for _, s := range v.States {
		out.States = append(out.States, condition.StateRule{When: s.When, Conditions: s.Conditions, Tone: s.Tone, Label: s.Label, Open: s.Open})
	}
	return out
}

// compileAssessments checks and compiles the assessment views (every condition is known by then).
func (s *compileState) compileAssessments() {
	if len(s.m.Assessments) == 0 {
		return
	}
	views := make([]condition.SubjectView, len(s.m.Assessments))
	for i, v := range s.m.Assessments {
		views[i] = v.rules()
	}
	compiled, issues := condition.CompileAssessmentViews(views, func(name string) bool { return s.known[name] })
	for _, is := range issues {
		s.add("assessments"+is.Path, "%s", is.Message)
	}
	s.assessments = compiled
}

// AssessmentStates is how the assessment of each subject of the change reads now, given the evaluation of the
// conditions on the same blackboard.
func (c *Compiled) AssessmentStates(bb domain.Blackboard, res condition.Result) []condition.AssessmentState {
	var v *condition.AssessmentViews
	if c != nil {
		v = c.assessments
	}
	return v.States(bb, res)
}
