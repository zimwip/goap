package condition

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestRiskConditions(t *testing.T) {
	set, err := Compile(MustLibrary(LibraryRisks))
	if err != nil {
		t.Fatal(err)
	}
	item := func(id string, kind domain.ItemKind, data map[string]any) domain.ChangeItem {
		return domain.ChangeItem{ID: domain.ItemID(id), Kind: kind, Status: domain.ItemProposed, Data: data}
	}
	var bb domain.Blackboard
	bb.Change.Items = []domain.ChangeItem{
		item("1", domain.KindRisk, map[string]any{"key": "RSK-1", "title": "high", "probability": 3.0, "impact": 4.0}),
		item("2", domain.KindRisk, map[string]any{"key": "RSK-2", "title": "low", "probability": 1.0, "impact": 2.0}),
	}
	state := set.Evaluate(bb).State
	if !state["open_risks"] || !state["unmitigated_risks"] || state["risks_under_control"] || state["open_actions"] {
		t.Fatalf("a high risk without action: %v", state)
	}
	bb.Change.Items = append(bb.Change.Items,
		item("3", domain.KindAction, map[string]any{"key": "ACT-1", "title": "mitigate", "for": "RSK-1"}),
		item("4", domain.KindRisk, map[string]any{"key": "RSK-2", "status": "closed", "title": "low"}))
	state = set.Evaluate(bb).State
	if state["unmitigated_risks"] || !state["risks_under_control"] || !state["open_actions"] || !state["open_risks"] {
		t.Fatalf("the high risk has an action: %v", state)
	}
	if rs := bb.Change.Risks(); len(rs) != 2 || rs[1].Status != "closed" || rs[1].Probability != 1 || rs[1].Versions != 2 {
		t.Fatalf("register %+v", rs)
	}
}
