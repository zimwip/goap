package condition

import (
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/risk"
)

func TestRiskConditions(t *testing.T) {
	set, err := Compile(MustLibrary(LibraryRisks))
	if err != nil {
		t.Fatal(err)
	}
	item := func(id string, kind domain.ItemType, data map[string]any) domain.ChangeItem {
		return domain.ChangeItem{ID: domain.ItemID(id), Type: kind, Status: domain.ItemProposed, Data: data}
	}
	var bb domain.Blackboard
	bb.Change.Items = []domain.ChangeItem{
		item("1", risk.TypeRisk, map[string]any{"key": "RSK-1", "title": "high", "probability": 3.0, "impact": 4.0}),
		item("2", risk.TypeRisk, map[string]any{"key": "RSK-2", "title": "low", "probability": 1.0, "impact": 2.0}),
	}
	state := set.Evaluate(bb).State
	if !state["open_risks"] || !state["unmitigated_risks"] || state["risks_under_control"] || state["open_actions"] {
		t.Fatalf("a high risk without action: %v", state)
	}
	bb.Change.Items = append(bb.Change.Items,
		item("3", risk.TypeAction, map[string]any{"key": "ACT-1", "title": "mitigate", "for": "RSK-1"}),
		item("4", risk.TypeRisk, map[string]any{"key": "RSK-2", "status": "closed", "title": "low"}))
	state = set.Evaluate(bb).State
	if state["unmitigated_risks"] || !state["risks_under_control"] || !state["open_actions"] || !state["open_risks"] {
		t.Fatalf("the high risk has an action: %v", state)
	}
	if rs := risk.Risks(bb.Change); len(rs) != 2 || rs[1].Status != "closed" || rs[1].Probability != 1 || rs[1].Versions != 2 {
		t.Fatalf("register %+v", rs)
	}
}

func TestDependencyConditions(t *testing.T) {
	set, err := Compile(MustLibrary(LibraryDependencies))
	if err != nil {
		t.Fatal(err)
	}
	var bb domain.Blackboard
	if state := set.Evaluate(bb).State; !state["dependencies_met"] || state["dependencies_pending"] {
		t.Fatalf("no dependency: %v", state)
	}
	bb = bb.WithFacet(domain.FacetDependencies, []domain.Dependency{{ID: "a", Satisfied: true}, {ID: "b", Reason: "not merged yet"}})
	if state := set.Evaluate(bb).State; state["dependencies_met"] || !state["dependencies_pending"] {
		t.Fatalf("one dependency pending: %v", state)
	}
	ok, err := CheckGuard(`dependencies.size() == 2 && change.dependsOn.size() == 0`, bb, nil, "", "")
	if err != nil || !ok {
		t.Fatalf("the guards see the dependencies: %v %v", ok, err)
	}
}

// A proof of concept covers its risks while it runs; concluded, its verdict settles them and the risk the POC
// confirmed needs another mitigation (ADR 0131).
func TestConcludedPOCNoLongerCovers(t *testing.T) {
	risk.Register()
	set, err := Compile(MustLibrary(LibraryRisks))
	if err != nil {
		t.Fatal(err)
	}
	item := func(id string, kind domain.ItemType, data map[string]any) domain.ChangeItem {
		return domain.ChangeItem{ID: domain.ItemID(id), Type: kind, Status: domain.ItemProposed, Data: data}
	}
	bb := domain.Blackboard{Change: domain.Change{Items: []domain.ChangeItem{
		item("1", risk.TypeRisk, map[string]any{"key": "RSK-1", "title": "sdk", "probability": 4, "impact": 4}),
		item("2", risk.TypeAction, map[string]any{"key": "ACT-1", "title": "poc", "kind": "poc", "risks": []any{"RSK-1"}}),
	}}}
	if st := set.Evaluate(bb).State; !st["risks_under_control"] || !st["pocs_pending"] {
		t.Fatalf("a running POC covers its risk: %v", st)
	}
	bb.Change.Items = append(bb.Change.Items, item("3", risk.TypeAction, map[string]any{"key": "ACT-1", "title": "poc", "status": "done"}))
	if st := set.Evaluate(bb).State; st["risks_under_control"] || st["pocs_pending"] {
		t.Fatalf("a concluded POC confirming the risk no longer covers it: %v", st)
	}
}
