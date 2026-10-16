package condition

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

func scopedBoard() domain.Blackboard {
	return domain.Blackboard{Change: domain.Change{ID: "c", Nodes: []domain.ChangeImpact{
		{ID: "i1", Key: "REQ-1", Type: "alm@Requirement", Intent: domain.IntentModified, Review: domain.ReviewAccepted, ProducedBy: "scope"},
		{ID: "i2", Key: "REQ-2", Type: "alm@Requirement", Intent: domain.IntentModified, Review: domain.ReviewProposed, ProducedBy: "propagate"},
		{ID: "i3", Key: "CMP-1", Type: "alm@Component", Intent: domain.IntentModified, Review: domain.ReviewProposed, ProducedBy: "propagate"},
	}}}
}

func TestScopedConditionsAndAggregates(t *testing.T) {
	set, err := Compile([]Definition{
		{Name: "accepted", Scope: ScopeImpact, Expr: `self.review == "accepted"`},
		{Name: "requirement", Scope: ScopeImpact, Where: `self.type == "alm@Requirement"`, Expr: `self.conditions.accepted || self.producedBy == "scope"`},
		{Name: "all_accepted", All: "accepted"},
		{Name: "requirements_ok", All: "requirement"},
		{Name: "any_accepted", Any: "accepted"},
		{Name: "none_accepted", None: "accepted"},
		{Name: "suspected_accepted", All: "accepted", Where: `self.producedBy == "propagate"`},
		{Name: "no_tests", All: "accepted", Where: `self.type == "alm@TestCase"`},
		{Name: "no_tests_ok", All: "accepted", Where: `self.type == "alm@TestCase"`, Empty: true},
		{Name: "reads", Expr: `changeImpacts.filter(i, i.conditions.accepted).size() == 1`},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := set.Evaluate(scopedBoard())
	want := map[string]bool{"all_accepted": false, "requirements_ok": false, "any_accepted": true, "none_accepted": false,
		"suspected_accepted": false, "no_tests": false, "no_tests_ok": true, "reads": true}
	for k, v := range want {
		if got, ok := res.State[k]; !ok || got != v {
			t.Errorf("%s = %v (known %v), want %v; errors %v", k, got, ok, v, res.Errors)
		}
	}
	if _, ok := res.State["accepted"]; ok {
		t.Error("a scoped condition is not in the world of the planner")
	}
	subs := res.Subjects[ScopeImpact]
	if len(subs) != 3 || subs[0].ID != "REQ-1" || !subs[0].Values["requirement"] || subs[1].Values["requirement"] {
		t.Fatalf("subjects %+v", subs)
	}
	if _, ok := subs[2].Values["requirement"]; ok {
		t.Error("a subject outside the where has no value")
	}
	ex, ok := set.Explain("suspected_accepted", scopedBoard())
	if !ok || ex.Value || !strings.Contains(ex.Inputs["failing"], "REQ-2") || !strings.Contains(ex.Inputs["failing"], "CMP-1") {
		t.Fatalf("explain %+v", ex)
	}
	if e := (SubjectEntry(scopedBoard(), ScopeImpact, "CMP-1")); e == nil || e["key"] != "CMP-1" {
		t.Fatalf("entry %v", e)
	}
}

func TestScopedCompileErrors(t *testing.T) {
	for name, defs := range map[string][]Definition{
		"cycle":       {{Name: "a", Scope: ScopeImpact, Expr: "self.conditions.b"}, {Name: "b", Scope: ScopeImpact, Expr: "self.conditions.a"}},
		"other scope": {{Name: "a", Scope: ScopeImpact, Expr: "true"}, {Name: "b", Scope: ScopeItem, Expr: "self.conditions.a"}},
		"unknown":     {{Name: "x", All: "nope"}},
		"self":        {{Name: "x", Expr: "self.review == 'x'"}},
		"scope":       {{Name: "x", Scope: "galaxy", Expr: "true"}},
		"empty any":   {{Name: "a", Scope: ScopeImpact, Expr: "true"}, {Name: "x", Any: "a", Empty: true}},
	} {
		if _, err := Compile(defs); err == nil {
			t.Errorf("%s: compiled", name)
		}
	}
}
