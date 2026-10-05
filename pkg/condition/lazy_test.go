package condition

import (
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/criticality"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/risk"
	"github.com/zimwip/goap/pkg/verify"
)

// The variables of the decision and risk concepts are computed when an expression reads them (ADR 0064).
func TestActivationIsLazy(t *testing.T) {
	bb := domain.Blackboard{Facets: map[string]any{domain.FacetDecisionPoints: []domain.DecisionPoint{{ID: "p", Status: domain.PointBlocked}}}}
	act := Activation(bb)
	for _, name := range []string{"options", "decisionPoints", "questions", "risks", "actions", "verifications", "derogations"} {
		if _, ok := act[name].(func() any); !ok {
			t.Errorf("%s is computed eagerly", name)
		}
	}
	if pts, _ := Resolve(act["decisionPoints"]).([]any); len(pts) != 1 {
		t.Fatalf("decisionPoints %v", pts)
	}

	// an expression over one variable gets it, and a set that reads none of them never computes them
	set, err := Compile([]Definition{{Name: "pending", Expr: `decisionPoints.exists(d, d.status != "decided")`}, {Name: "plain", Expr: `items.size() == 0`}})
	if err != nil {
		t.Fatal(err)
	}
	if st := set.Evaluate(bb); !st.State["pending"] || !st.State["plain"] || len(st.Errors) > 0 {
		t.Fatalf("%v %v", st.State, st.Errors)
	}
}

func TestLibraries(t *testing.T) {
	for _, n := range LibraryNames {
		defs, ok := Library(n)
		if !ok || len(defs) == 0 {
			t.Fatalf("library %s", n)
		}
		if _, err := Compile(defs); err != nil {
			t.Fatalf("library %s: %v", n, err)
		}
	}
	if _, ok := Library("nope"); ok {
		t.Fatal("unknown library")
	}
}

func TestVerificationConditions(t *testing.T) {
	set, err := Compile(MustLibrary(LibraryVerification))
	if err != nil {
		t.Fatal(err)
	}
	it := func(id string, data map[string]any) domain.ChangeItem {
		return domain.ChangeItem{ID: domain.ItemID(id), Kind: verify.KindVerification, Data: data}
	}
	var bb domain.Blackboard
	if st := set.Evaluate(bb).State; !st["all_verified"] || st["unverified_effects"] || st["reserves_open"] {
		t.Fatalf("nothing produced: %v", st)
	}
	bb.Change.Items = []domain.ChangeItem{it("1", map[string]any{"state": "produced", "action": "a", "impacts": []any{"i1", "i2"}})}
	if st := set.Evaluate(bb).State; st["all_verified"] || !st["unverified_effects"] {
		t.Fatalf("produced: %v", st)
	}
	bb.Change.Items = append(bb.Change.Items,
		it("2", map[string]any{"state": "accepted", "action": "b", "impact": "i1"}),
		it("3", map[string]any{"state": "accepted_with_reserve", "action": "b", "impact": "i2"}))
	if st := set.Evaluate(bb).State; !st["all_verified"] || st["unverified_effects"] || !st["reserves_open"] {
		t.Fatalf("settled: %v", st)
	}
}

func TestDerogationConditions(t *testing.T) {
	set, err := Compile(MustLibrary(LibraryDerogations))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	drg := func(id, key, exp, status string) domain.ChangeItem {
		d := map[string]any{"key": key, "rule": "r", "target": "t", "reason": "why", "signatory": "alice", "expires": exp}
		if status != "" {
			d["status"] = status
		}
		return domain.ChangeItem{ID: domain.ItemID(id), Kind: risk.KindDerogation, Data: d}
	}
	bb := domain.Blackboard{At: at}
	if st := set.Evaluate(bb).State; !st["no_expired_derogation"] || st["derogation_debt"] {
		t.Fatalf("none: %v", st)
	}
	bb.Change.Items = []domain.ChangeItem{drg("1", "D1", "2030-01-02T00:00:00Z", ""), drg("2", "D2", "2030-01-02T00:00:00Z", ""),
		drg("3", "D3", "2030-01-02T00:00:00Z", "")}
	if st := set.Evaluate(bb).State; !st["no_expired_derogation"] || !st["derogation_debt"] {
		t.Fatalf("three open: %v", st)
	}
	bb.Change.Items = append(bb.Change.Items, drg("4", "D3", "2030-01-02T00:00:00Z", "closed"))
	if st := set.Evaluate(bb).State; st["derogation_debt"] {
		t.Fatalf("one closed: %v", st)
	}
	bb.At = at.Add(48 * time.Hour)
	if st := set.Evaluate(bb).State; st["no_expired_derogation"] {
		t.Fatalf("ran out: %v", st)
	}
	if _, ok := Activation(bb)["derogations"].(func() any); !ok {
		t.Error("derogations is computed eagerly")
	}
}

// change.criticality and criticalityPolicy are visible to conditions and guards: the facet of the organisation when the
// blackboard has one, else the compiled-in table of the level (ADR 0075 §3). `policy` stays a field of the decision points.
func TestCriticalityVariables(t *testing.T) {
	bb := domain.Blackboard{Change: domain.Change{Data: map[string]any{domain.DataCriticality: "C3"}}}
	set, err := Compile([]Definition{
		{Name: "critical", Expr: `change.criticality == "C3"`},
		{Name: "human_only", Expr: `criticalityPolicy.oracles == ["human"]`},
		{Name: "short", Expr: `criticalityPolicy.maxDerogationHours <= 168`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st := set.Evaluate(bb).State; !st["critical"] || !st["human_only"] || !st["short"] {
		t.Fatalf("defaults: %v", st)
	}
	if _, ok := Activation(bb)["criticalityPolicy"].(func() any); !ok {
		t.Error("criticalityPolicy is computed eagerly")
	}
	if Activation(domain.Blackboard{})["change"].(map[string]any)["criticality"] != "C2" {
		t.Error("a change that names no level is C2")
	}
	bb = bb.WithFacet(domain.FacetCriticalityPolicy, criticality.Policy{Oracles: []string{"tool"}, MaxDerogation: 1000 * time.Hour})
	if st := set.Evaluate(bb).State; st["human_only"] || st["short"] {
		t.Fatalf("the facet of the organisation wins: %v", st)
	}
	// a guard reads them the way a condition does
	ok, err := CheckGuard(`change.criticality == "C3" ? criticalityPolicy.oracles.exists(o, o == "tool") : true`, bb, nil, "t", "")
	if err != nil || !ok {
		t.Fatalf("guard: %v %v", ok, err)
	}
}

// CheckGate: vetos and objectives, an objective covered by the derogation of its name (ADR 0075 §3).
func TestCheckGate(t *testing.T) {
	at := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	drg := func(key, rule, exp string) domain.ChangeItem {
		return domain.ChangeItem{ID: domain.ItemID(key), Kind: risk.KindDerogation, Data: map[string]any{"key": key, "rule": rule, "target": "t", "reason": "why", "signatory": "alice", "expires": exp}}
	}
	tr := domain.Transition{Name: "ship",
		Vetos:      []domain.Criterion{{Name: "safe", Expr: `change.criticality != "C3" || world["reviewed"]`}},
		Objectives: []domain.Criterion{{Name: "docs", Expr: `world["docs"]`}, {Name: "perf", Expr: `world["perf"]`}}}
	bb := domain.Blackboard{At: at, Change: domain.Change{Data: map[string]any{domain.DataCriticality: "C3"}}}
	world := map[string]bool{"reviewed": false, "docs": false, "perf": true}
	res, err := CheckGate(tr, bb, world, "")
	if err != nil || res.Passed() || len(res.Vetoed) != 1 || res.Vetoed[0] != "safe" || len(res.Unmet) != 1 {
		t.Fatalf("veto: %+v %v", res, err)
	}
	world["reviewed"] = true
	if res, _ = CheckGate(tr, bb, world, ""); res.Passed() || len(res.Uncovered()) != 1 || res.Uncovered()[0] != "docs" {
		t.Fatalf("uncovered: %+v", res)
	}
	bb.Change.Items = []domain.ChangeItem{drg("D-old", "docs", "2029-12-31T00:00:00Z"), drg("D-other", "perf", "2030-02-01T00:00:00Z")}
	if res, _ = CheckGate(tr, bb, world, ""); res.Passed() {
		t.Fatalf("an expired derogation and one of another rule cover nothing: %+v", res)
	}
	bb.Change.Items = append(bb.Change.Items, drg("D-docs", "docs", "2030-02-01T00:00:00Z"))
	if res, _ = CheckGate(tr, bb, world, ""); !res.Passed() || !res.WithReserve() || res.Reserve["docs"] != "D-docs" {
		t.Fatalf("covered: %+v", res)
	}
	if _, err := CheckGate(domain.Transition{Vetos: []domain.Criterion{{Name: "x", Expr: `world["missing"]`}}}, bb, world, ""); err == nil {
		t.Fatal("a criterion that cannot be evaluated is an error")
	}
}
