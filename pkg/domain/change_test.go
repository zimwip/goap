package domain

import "testing"

func TestChangeItemValidateSignal(t *testing.T) {
	valid := ChangeItem{Type: TypeSignal, Target: "review_needed", Recipient: "P1"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid signal: %v", err)
	}
	invalid := ChangeItem{Type: TypeSignal}
	if err := invalid.Validate(); err == nil {
		t.Fatal("signal item without type should fail validation")
	}
}
