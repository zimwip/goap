package condition

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"

	"github.com/zimwip/goap/contracts/assess"
	"github.com/zimwip/goap/contracts/domain"
)

// How the assessment of a subject reads (ADR 0116): a methodology declares, per subject, the states its assessment
// goes through, each bound to conditions or a CEL expression, with the tone it is shown in, a label and whether the
// bubble opens by itself. The server evaluates them against the change; the web only renders the result.

// Tones of an assessment state.
const (
	ToneOK      = "ok"
	ToneWarn    = "warn"
	ToneDanger  = "danger"
	ToneInfo    = "info"
	ToneNeutral = "neutral"
)

// Tones lists the tones, in a stable order.
var Tones = []string{ToneOK, ToneWarn, ToneDanger, ToneInfo, ToneNeutral}

// StateRule is one state of the view of an assessment: it holds when every condition has the value given (a scoped
// condition read on the subject) and its when expression is true; neither: it always holds.
type StateRule struct {
	When       string
	Conditions map[string]bool
	Tone       string
	Label      string
	Open       bool
}

// SubjectView is the view of the assessments of the subjects its pattern matches (a subject key, or a glob:
// "impact:*", "*"): its states, the first that holds wins.
type SubjectView struct {
	Subject string
	States  []StateRule
}

// AssessmentState is how the assessment of one subject reads now.
type AssessmentState struct {
	Subject  string `json:"subject"`
	Score    int    `json:"score"`
	Verdict  string `json:"verdict,omitempty"`
	Current  bool   `json:"current"`
	Answered bool   `json:"answered"`
	Tone     string `json:"tone"`
	Label    string `json:"label,omitempty"`
	Open     bool   `json:"open,omitempty"`
	// Errors are the states that could not be evaluated (skipped).
	Errors []string `json:"errors,omitempty"`
}

// DefaultAssessmentViews are the views of the subjects no view of the methodology settles: an impact is processed
// once its review is decided; any other subject is accepted once it is rated as it reads, its last remark answered,
// with a score of AcceptableScore or more.
var DefaultAssessmentViews = []SubjectView{
	{Subject: assess.ImpactPrefix + "*", States: []StateRule{
		{When: `subject.entry != null && subject.entry.review in ["accepted", "rejected"]`, Tone: ToneOK, Label: "processed"},
		{Tone: ToneWarn, Label: "not processed yet"},
	}},
	{Subject: "*", States: []StateRule{
		{When: "!assessment.current", Tone: ToneWarn, Label: "changed since its rating"},
		{When: "!assessment.answered", Tone: ToneWarn, Label: "your remark awaits an answer"},
		{When: fmt.Sprintf("assessment.score >= %d", AcceptableScore), Tone: ToneOK, Label: "accepted"},
		{Tone: ToneWarn, Label: "to be improved"},
	}},
}

// AssessmentViews are compiled views.
type AssessmentViews struct {
	views []compiledView
}

type compiledView struct {
	subject string
	states  []compiledRule
}

type compiledRule struct {
	StateRule
	prog cel.Program
}

// AssessmentEnv is the CEL environment of the when of a state: the one of the conditions plus `assessment` (the
// rating, as an entry of assessments), `subject` ({key, kind, id, entry: the entry of the change, of changeImpacts or
// of items it is about, null when there is none; conditions: the scoped conditions on it}) and `conditions` (the
// conditions of the change).
func AssessmentEnv() (*cel.Env, error) {
	env, err := NewEnv()
	if err != nil {
		return nil, err
	}
	return env.Extend(
		cel.Variable("assessment", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("subject", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("conditions", cel.MapType(cel.StringType, cel.BoolType)),
	)
}

// CheckSubjectPattern checks the subject pattern of a view.
func CheckSubjectPattern(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("subject required: a subject key (description, title, intent, impact:<key>, item:<key>) or a glob (impact:*, *)")
	}
	_, err := path.Match(p, "")
	return err
}

// CompileAssessmentViews compiles views; known says whether a condition exists. The issues are given per view and
// state ("[i].states[j].when"), the views compiled without the states that have one.
func CompileAssessmentViews(views []SubjectView, known func(string) bool) (*AssessmentViews, []ViewIssue) {
	env, err := AssessmentEnv()
	if err != nil {
		return nil, []ViewIssue{{Message: err.Error()}}
	}
	var issues []ViewIssue
	out := &AssessmentViews{}
	for i, v := range views {
		at := fmt.Sprintf("[%d]", i)
		if err := CheckSubjectPattern(v.Subject); err != nil {
			issues = append(issues, ViewIssue{Path: at + ".subject", Message: err.Error()})
			continue
		}
		if len(v.States) == 0 {
			issues = append(issues, ViewIssue{Path: at + ".states", Message: "at least one state required"})
		}
		cv := compiledView{subject: v.Subject}
		for j, s := range v.States {
			sat := fmt.Sprintf("%s.states[%d]", at, j)
			ok := true
			if !slices.Contains(Tones, s.Tone) {
				issues = append(issues, ViewIssue{Path: sat + ".tone", Message: fmt.Sprintf("unknown tone %q (one of %s)", s.Tone, strings.Join(Tones, ", "))})
				ok = false
			}
			for _, k := range slices.Sorted(maps.Keys(s.Conditions)) {
				if known != nil && !known(k) {
					issues = append(issues, ViewIssue{Path: sat + ".conditions." + k, Message: fmt.Sprintf("unknown condition %q", k)})
					ok = false
				}
			}
			r := compiledRule{StateRule: s}
			if s.When != "" {
				p, err := compileBool(env, s.When)
				if err != nil {
					issues = append(issues, ViewIssue{Path: sat + ".when", Message: err.Error()})
					ok = false
				}
				r.prog = p
			}
			if ok {
				cv.states = append(cv.states, r)
			}
		}
		out.views = append(out.views, cv)
	}
	return out, issues
}

// ViewIssue is an issue of a view, at a path relative to the list of views.
type ViewIssue struct {
	Path    string
	Message string
}

func compileBool(env *cel.Env, expr string) (cel.Program, error) {
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	if ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
		return nil, fmt.Errorf("must return bool, got %s", ast.OutputType())
	}
	return env.Program(ast, cel.CostLimit(1_000_000))
}

var defaultViews = sync.OnceValue(func() *AssessmentViews {
	v, issues := CompileAssessmentViews(DefaultAssessmentViews, nil)
	if len(issues) > 0 {
		panic(fmt.Sprintf("default assessment views: %v", issues))
	}
	return v
})

// States evaluates the views (nil: the defaults only) against the blackboard and the evaluation of its conditions:
// one state per assessed subject, in the order of the assessments. The first view whose pattern matches the subject
// and one of whose states holds decides, then the defaults.
func (v *AssessmentViews) States(bb domain.Blackboard, res Result) []AssessmentState {
	as := assess.Assessments(bb)
	act := Activation(bb)
	records := Resolve(act["assessments"]).([]any)
	conds := map[string]bool{}
	for k, b := range res.State {
		conds[k] = b
	}
	out := make([]AssessmentState, 0, len(as))
	for i, a := range as {
		st := AssessmentState{Subject: a.Key, Score: a.Score, Verdict: a.Verdict, Current: a.Current, Answered: a.Answered, Tone: ToneNeutral}
		sub, scoped := subjectOf(act, res, a.Key)
		sact := maps.Clone(act)
		sact["assessment"] = records[i]
		sact["subject"] = sub
		sact["conditions"] = conds
		decided := false
		for _, set := range []*AssessmentViews{v, defaultViews()} {
			if set == nil || decided {
				continue
			}
			for _, cv := range set.views {
				if ok, _ := path.Match(cv.subject, a.Key); !ok {
					continue
				}
				if r, ok := cv.decide(sact, conds, scoped, &st); ok {
					st.Tone, st.Label, st.Open = r.Tone, r.Label, r.Open
					decided = true
					break
				}
			}
		}
		out = append(out, st)
	}
	return out
}

// decide returns the first state of the view that holds on the subject.
func (cv compiledView) decide(act map[string]any, conds, scoped map[string]bool, st *AssessmentState) (StateRule, bool) {
	for j, r := range cv.states {
		holds := true
		for k, want := range r.Conditions {
			got, ok := scoped[k]
			if !ok {
				got, ok = conds[k]
			}
			if !ok || got != want {
				holds = false
				break
			}
		}
		if holds && r.prog != nil {
			b, err := evalBool(r.prog, act)
			if err != nil {
				st.Errors = append(st.Errors, fmt.Sprintf("%s states[%d]: %s", cv.subject, j, err))
				continue
			}
			holds = b
		}
		if holds {
			return r.StateRule, true
		}
	}
	return StateRule{}, false
}

// subjectOf is the subject of an assessment as the when expressions see it, and the values of the scoped conditions
// about it.
func subjectOf(act map[string]any, res Result, key string) (map[string]any, map[string]bool) {
	kind, id, _ := strings.Cut(key, ":")
	var entry any
	type ref struct{ scope, id string }
	var refs []ref
	switch {
	case key == assess.SubjectDescription || key == assess.SubjectTitle || key == assess.SubjectIntent:
		entry = act["change"]
	case kind+":" == assess.ImpactPrefix:
		for _, e := range SubjectsOf(act, ScopeImpact) {
			if SubjectID(ScopeImpact, e) == id {
				entry = e
			}
		}
		refs = append(refs, ref{ScopeImpact, id})
	case kind+":" == assess.ItemPrefix:
		for _, e := range SubjectsOf(act, ScopeItem) {
			data, _ := e["data"].(map[string]any)
			if k, _ := data["key"].(string); e["id"] == id || (k != "" && k == id) {
				entry = e
				refs = append(refs, ref{ScopeItem, SubjectID(ScopeItem, e)})
			}
		}
		refs = append(refs, ref{ScopeRisk, id}, ref{ScopeAction, id})
	}
	scoped := map[string]bool{}
	for _, r := range refs {
		for _, s := range res.Subjects[r.scope] {
			if s.ID == r.id {
				maps.Copy(scoped, s.Values)
			}
		}
	}
	values := map[string]any{}
	for k, b := range scoped {
		values[k] = b
	}
	if kind == key {
		id = ""
	}
	return map[string]any{"key": key, "kind": kind, "id": id, "entry": entry, "conditions": values}, scoped
}
