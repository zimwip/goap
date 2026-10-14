package engine

import "testing"

// A model told to write an assessment may wrap it as an artifact of that type: it gets its kind back (ADR 0110).
func TestUnwrapKinds(t *testing.T) {
	items := []ItemInput{
		{Kind: "artifact", Type: "assessment", Data: map[string]any{"key": "description", "score": 2}},
		{Kind: "artifact", Type: "comment", Data: map[string]any{"about": "description", "text": "ok"}},
		{Kind: "artifact", Type: "question", Data: map[string]any{"text": "why?"}},
		{Kind: "assessment", Data: map[string]any{"key": "title", "score": 4}},
	}
	unwrapKinds(items)
	want := [][2]string{{"assessment", ""}, {"comment", ""}, {"artifact", "question"}, {"assessment", ""}}
	for i, w := range want {
		if items[i].Kind != w[0] || items[i].Type != w[1] {
			t.Errorf("item %d: %s/%s, want %s/%s", i, items[i].Kind, items[i].Type, w[0], w[1])
		}
	}
}
