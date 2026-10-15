package engine

import "testing"

// A model told to write an assessment may wrap it as an artifact of that type: it gets its kind back (ADR 0110).
func TestUnwrapKinds(t *testing.T) {
	items := []ItemInput{
		{Type: "artifact", Target: "assessment", Data: map[string]any{"key": "description", "score": 2}},
		{Type: "artifact", Target: "comment", Data: map[string]any{"about": "description", "text": "ok"}},
		{Type: "artifact", Target: "question", Data: map[string]any{"text": "why?"}},
		{Type: "assessment", Data: map[string]any{"key": "title", "score": 4}},
	}
	unwrapTypes(items)
	want := [][2]string{{"assessment", ""}, {"comment", ""}, {"artifact", "question"}, {"assessment", ""}}
	for i, w := range want {
		if items[i].Type != w[0] || items[i].Target != w[1] {
			t.Errorf("item %d: %s/%s, want %s/%s", i, items[i].Type, items[i].Target, w[0], w[1])
		}
	}
}
