package domain

import "testing"

func TestEditedFields(t *testing.T) {
	before := Change{Title: "a", Intent: "x", Goal: "g", Data: map[string]any{"k": 1}}
	if f := EditedFields(before, before); f != nil {
		t.Fatalf("nothing edited: %v", f)
	}
	after := before
	after.Intent, after.Data = "y", map[string]any{"k": 2, "n": true}
	f := EditedFields(before, after)
	if len(f) != 3 || f["intent"].From != "x" || f["intent"].To != "y" || f["data.k"].To != 2 || f["data.n"].From != nil {
		t.Fatalf("edited %v", f)
	}
}
