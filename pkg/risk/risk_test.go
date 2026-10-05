package risk

import (
	"testing"

	"github.com/zimwip/goap/pkg/domain"
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
		return domain.ChangeItem{Kind: KindRisk, Data: d}
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
