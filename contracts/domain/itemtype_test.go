package domain

import (
	"errors"
	"slices"
	"testing"
)

// A type no use case gave a validator passes ChangeItem.Validate (whether a domain declares it is the graph's
// question, ADR 0128); registering is idempotent and the last validation of a type wins.
func TestRegisterItemType(t *testing.T) {
	const typ ItemType = "testType"
	if err := (ChangeItem{Type: typ}).Validate(); err != nil {
		t.Fatalf("a type with no validator: %v", err)
	}
	if HasItemCode(typ) {
		t.Fatal("no code yet")
	}
	boom := errors.New("boom")
	RegisterItemType(typ, func(ChangeItem) error { return boom })
	if err := (ChangeItem{Type: typ}).Validate(); !errors.Is(err, boom) {
		t.Fatalf("registered validation: %v", err)
	}
	RegisterItemType(typ, nil)
	if err := (ChangeItem{Type: typ}).Validate(); err != nil {
		t.Fatalf("re-registered, accepting: %v", err)
	}
	if !HasItemCode(typ) || !HasItemCode(TypeArtifact) || !slices.Contains(RegisteredItemTypes(), typ) {
		t.Fatal("registered and core types have code")
	}
}
