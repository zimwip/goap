package condition

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/risk"
)

// Conditions read the effects of the impacts, the items and the risk register (ADR 0107).
func TestEffectConditions(t *testing.T) {
	set, err := Compile([]Definition{
		{Name: "formulated", Expr: `changeImpacts.size() > 0 && changeImpacts.all(i, "formulated" in i.effects && i.effects.formulated == true)`},
		{Name: "assessed", Expr: `risks.size() > 0 && risks.all(r, has(r.effects.assessed))`},
		{Name: "noted", Expr: `artifacts.exists(a, a.effects.score >= 3)`},
	})
	if err != nil {
		t.Fatal(err)
	}
	mark := func(id, subject, target string, effects map[string]any) domain.ChangeItem {
		it := domain.EffectMark{Subject: subject, ID: target, Effects: effects}.Item()
		it.ID = domain.ItemID(id)
		return it
	}
	var bb domain.Blackboard
	bb.Change.Nodes = []domain.ChangeImpact{{ID: "I1", Key: "REQ-1"}}
	bb.Change.Items = []domain.ChangeItem{
		{ID: "r1", Kind: risk.KindRisk, Data: map[string]any{"key": "RSK-1", "title": "t", "probability": 2.0, "impact": 2.0}},
		{ID: "a1", Kind: domain.KindArtifact, Type: "note"},
	}
	if s := set.Evaluate(bb).State; s["formulated"] || s["assessed"] || s["noted"] {
		t.Fatalf("no effect yet: %v", s)
	}
	bb.Change.Items = append(bb.Change.Items,
		mark("e1", domain.EffectOnImpact, "I1", map[string]any{"formulated": true}),
		mark("e2", domain.EffectOnItem, "r1", map[string]any{"assessed": "medium"}),
		mark("e3", domain.EffectOnItem, "a1", map[string]any{"score": 4}),
		domain.ChangeItem{ID: "r2", Kind: risk.KindRisk, Data: map[string]any{"key": "RSK-1", "status": "mitigating"}})
	if s := set.Evaluate(bb).State; !s["formulated"] || !s["assessed"] || !s["noted"] {
		t.Fatalf("effects set: %v", s)
	}
}
