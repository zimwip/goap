package domain

import "testing"

func TestChangeItemValidateSignal(t *testing.T) {
	valid := ChangeItem{Kind: KindSignal, Type: "review_needed", Target: "P1"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid signal: %v", err)
	}
	invalid := ChangeItem{Kind: KindSignal}
	if err := invalid.Validate(); err == nil {
		t.Fatal("signal item without type should fail validation")
	}
}
