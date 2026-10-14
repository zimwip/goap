package condition

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/assess"
	"github.com/zimwip/goap/pkg/domain"
)

func assessedBoard(score int) domain.Blackboard {
	desc := "Login\n\nlet users sign in"
	return domain.Blackboard{Change: domain.Change{ID: "c", Title: "Login", Intent: "let users sign in",
		Nodes: []domain.ChangeImpact{
			{ID: "i1", Key: "REQ-1", Type: "alm@Requirement", Intent: domain.IntentModified, Review: domain.ReviewAccepted},
			{ID: "i2", Key: "REQ-2", Type: "alm@Requirement", Intent: domain.IntentModified, Review: domain.ReviewProposed},
		},
		Items: []domain.ChangeItem{
			{ID: "a1", Kind: assess.KindAssessment, Data: map[string]any{"key": assess.SubjectDescription, "score": score, "rated": desc}},
			{ID: "a2", Kind: assess.KindAssessment, Data: map[string]any{"key": assess.ImpactPrefix + "REQ-1", "score": 4, "verdict": "confirmed"}},
			{ID: "a3", Kind: assess.KindAssessment, Data: map[string]any{"key": assess.ImpactPrefix + "REQ-2", "score": 1, "verdict": "refuted"}},
		}}}
}

func stateOf(t *testing.T, states []AssessmentState, subject string) AssessmentState {
	t.Helper()
	for _, s := range states {
		if s.Subject == subject {
			return s
		}
	}
	t.Fatalf("no state for %s in %+v", subject, states)
	return AssessmentState{}
}

func TestDefaultAssessmentStates(t *testing.T) {
	var none *AssessmentViews
	states := none.States(assessedBoard(4), Result{})
	if s := stateOf(t, states, "description"); s.Tone != ToneOK || s.Label != "accepted" || s.Open || s.Score != 4 {
		t.Fatalf("description %+v", s)
	}
	if s := stateOf(t, states, "impact:REQ-1"); s.Tone != ToneOK || s.Label != "processed" {
		t.Fatalf("decided impact %+v", s)
	}
	if s := stateOf(t, states, "impact:REQ-2"); s.Tone != ToneWarn || !s.Open {
		t.Fatalf("impact waiting for its review %+v", s)
	}
	if s := stateOf(t, none.States(assessedBoard(2), Result{}), "description"); s.Tone != ToneWarn || s.Label != "to be improved" || !s.Open {
		t.Fatalf("low score %+v", s)
	}
	bb := assessedBoard(4)
	bb.Change.Intent = "let users sign in with SSO"
	if s := stateOf(t, none.States(bb, Result{}), "description"); s.Label != "changed since its rating" || s.Current {
		t.Fatalf("stale %+v", s)
	}
}

func TestDeclaredAssessmentStates(t *testing.T) {
	set, err := Compile([]Definition{
		{Name: "gate", Expr: `assessments.exists(a, a.key == "description" && a.score >= 4)`},
		{Name: "accepted", Scope: ScopeImpact, Expr: `self.review == "accepted"`},
	})
	if err != nil {
		t.Fatal(err)
	}
	views, issues := CompileAssessmentViews([]SubjectView{
		{Subject: "description", States: []StateRule{
			{Conditions: map[string]bool{"gate": true}, Tone: ToneOK, Label: "through the gate"},
			{When: "assessment.score <= 1 || subject.kind != 'description'", Tone: ToneDanger, Label: "rewrite"},
		}},
		{Subject: "impact:*", States: []StateRule{
			{Conditions: map[string]bool{"accepted": true}, Tone: ToneInfo, Label: "accepted on its subject"},
			{When: "assessment.verdict == 'refuted' && subject.id == 'REQ-2' && !conditions.gate", Tone: ToneNeutral, Label: "refuted"},
		}},
	}, set.Has)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	bb := assessedBoard(4)
	states := views.States(bb, set.Evaluate(bb))
	if s := stateOf(t, states, "description"); s.Tone != ToneOK || s.Label != "through the gate" {
		t.Fatalf("description %+v", s)
	}
	if s := stateOf(t, states, "impact:REQ-1"); s.Tone != ToneInfo {
		t.Fatalf("scoped condition read on the subject %+v", s)
	}
	bb = assessedBoard(3)
	states = views.States(bb, set.Evaluate(bb))
	// no declared state holds: the defaults decide
	if s := stateOf(t, states, "description"); s.Tone != ToneOK || s.Label != "accepted" {
		t.Fatalf("falls back on the defaults %+v", s)
	}
	if s := stateOf(t, states, "impact:REQ-2"); s.Tone != ToneNeutral || s.Label != "refuted" {
		t.Fatalf("when over the subject and the conditions %+v", s)
	}
}

func TestAssessmentViewIssues(t *testing.T) {
	_, issues := CompileAssessmentViews([]SubjectView{
		{Subject: "", States: []StateRule{{Tone: ToneOK}}},
		{Subject: "description", States: []StateRule{
			{Tone: "green"},
			{Conditions: map[string]bool{"nope": true}, Tone: ToneOK},
			{When: "assessment.score +", Tone: ToneOK},
			{When: "assessment.score", Tone: ToneOK},
		}},
		{Subject: "title"},
	}, func(string) bool { return false })
	var paths []string
	for _, is := range issues {
		paths = append(paths, is.Path)
	}
	got := strings.Join(paths, " ")
	for _, want := range []string{"[0].subject", "[1].states[0].tone", "[1].states[1].conditions.nope", "[1].states[2].when", "[2].states"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing issue at %s: %v", want, issues)
		}
	}
}
