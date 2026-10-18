package risk

import (
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// The probability and the impact of a risk are 1 to 5 when given (ADR 0036), and may be left out of a version that
// does not restate them.
func TestRiskScaleIsOneToFive(t *testing.T) {
	Register()
	risk := func(extra map[string]any) domain.ChangeItem {
		d := map[string]any{"key": "RSK-1", "title": "t"}
		for k, v := range extra {
			d[k] = v
		}
		return domain.ChangeItem{Type: TypeRisk, Data: d}
	}
	for name, c := range map[string]struct {
		data map[string]any
		ok   bool
	}{
		"absent":         {nil, true},
		"lowest":         {map[string]any{"probability": 1.0, "impact": 1.0}, true},
		"highest":        {map[string]any{"probability": 5, "impact": int64(5)}, true},
		"zero":           {map[string]any{"probability": 0.0}, false},
		"above the top":  {map[string]any{"impact": 6.0}, false},
		"negative":       {map[string]any{"probability": -1.0}, false},
		"not a number":   {map[string]any{"impact": "high"}, false},
		"one of the two": {map[string]any{"probability": 3.0, "impact": 9.0}, false},
	} {
		if err := risk(c.data).Validate(); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A proof of concept covers its risks while it runs, and no longer once concluded: its verdict settled them (ADR 0131).
func TestPOCCoversItsRisksWhileItRuns(t *testing.T) {
	Register()
	c := domain.Change{Items: []domain.ChangeItem{
		{ID: "1", Type: TypeAction, Data: map[string]any{"key": "ACT-1", "title": "poc", "kind": "poc", "for": "RSK-1", "risks": []any{"RSK-1", "RSK-2"}}},
		{ID: "2", Type: TypeAction, Data: map[string]any{"key": "ACT-2", "title": "patch", "for": "RSK-3"}},
	}}
	as := Actions(c)
	if as[0].Kind != KindPOC || as[0].Purpose != PurposeDerisk || as[1].Kind != KindMitigation {
		t.Fatalf("kinds %+v", as)
	}
	if !as[0].Covers("RSK-1") || !as[0].Covers("RSK-2") || as[0].Covers("RSK-3") || !as[1].Covers("RSK-3") {
		t.Fatalf("coverage %+v", as)
	}
	// the POC concluded, the same version scheme: the kind is kept, the coverage ends; a mitigation done still covers
	c.Items = append(c.Items, domain.ChangeItem{ID: "3", Type: TypeAction, Data: map[string]any{"key": "ACT-1", "title": "poc", "status": "done"}},
		domain.ChangeItem{ID: "4", Type: TypeAction, Data: map[string]any{"key": "ACT-2", "title": "patch", "status": "done"}})
	as = Actions(c)
	if as[0].Kind != KindPOC || as[0].Covers("RSK-1") || !as[1].Covers("RSK-3") {
		t.Fatalf("after the conclusion %+v", as)
	}
	for _, bad := range []map[string]any{{"kind": "spike"}, {"kind": "poc", "purpose": "fun"}} {
		d := map[string]any{"key": "ACT-9", "title": "t"}
		for k, v := range bad {
			d[k] = v
		}
		if err := (domain.ChangeItem{Type: TypeAction, Data: d}).Validate(); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	h := domain.ChangeItem{Type: TypeHypothesis, Data: map[string]any{"key": "HYP-1"}}
	if err := h.Validate(); err == nil {
		t.Error("a hypothesis without a statement is accepted")
	}
}
