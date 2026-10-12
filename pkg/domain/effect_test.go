package domain

import "testing"

// The effects of a change are a fold of its effect items in force (ADR 0107): the latest value of a name wins, null
// clears it, a record shares its effects across its versions, and a candidate of an open flow is not in force.
func TestEffects(t *testing.T) {
	mark := func(id, subject, target string, effects map[string]any) ChangeItem {
		it := EffectMark{Subject: subject, ID: target, Effects: effects}.Item()
		it.ID = ItemID(id)
		if err := it.Validate(); err != nil {
			t.Fatal(err)
		}
		return it
	}
	c := Change{Nodes: []ChangeImpact{{ID: "I1"}}, Items: []ChangeItem{
		{ID: "r1", Kind: "risk", Data: map[string]any{"key": "RSK-1"}},
		{ID: "a1", Kind: KindArtifact},
		mark("e1", EffectOnImpact, "I1", map[string]any{"formulated": true, "score": 3}),
		mark("e2", EffectOnItem, "r1", map[string]any{"assessed": true}),
		{ID: "r2", Kind: "risk", Data: map[string]any{"key": "RSK-1"}},
		mark("e3", EffectOnImpact, "I1", map[string]any{"score": nil, "checked": "yes"}),
		mark("e4", EffectOnItem, "a1", map[string]any{"read": false}),
		mark("e5", EffectOnItem, "missing", map[string]any{"lost": true}),
	}}
	fx := c.Effects()
	if got := EffectValues(fx.OfImpact("I1")); len(got) != 2 || got["formulated"] != true || got["checked"] != "yes" {
		t.Fatalf("impact: %v", got)
	}
	for _, it := range c.Items[:1] {
		if e := fx.OfItem(it)["assessed"]; e.Value != true || e.Item != "e2" || e.On != "r1" {
			t.Fatalf("record: %+v", e)
		}
	}
	if got := EffectValues(fx.OfRecord("risk", "RSK-1")); got["assessed"] != true {
		t.Fatalf("record by key: %v", got)
	}
	if got := EffectValues(fx.OfItem(c.Items[1])); got["read"] != false {
		t.Fatalf("artifact: %v", got)
	}
	if got := EffectValues(fx.OfImpact("I2")); len(got) != 0 {
		t.Fatalf("no effects: %v", got)
	}
	for _, bad := range []EffectMark{
		{Subject: "node", ID: "x", Effects: map[string]any{"a": true}},
		{Subject: EffectOnImpact, Effects: map[string]any{"a": true}},
		{Subject: EffectOnImpact, ID: "x"},
		{Subject: EffectOnImpact, ID: "x", Effects: map[string]any{"a.b": true}},
		{Subject: EffectOnImpact, ID: "x", Effects: map[string]any{"a": map[string]any{}}},
	} {
		if bad.Validate() == nil {
			t.Fatalf("%+v must be refused", bad)
		}
	}
}
