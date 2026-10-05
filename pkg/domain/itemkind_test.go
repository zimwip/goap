package domain

import (
	"errors"
	"testing"
)

// A kind of item the core does not know is refused until a use case registers it (ADR 0065); registering is
// idempotent and the last validation of a kind wins.
func TestRegisterItemKind(t *testing.T) {
	const kind ItemKind = "test-kind"
	if err := (ChangeItem{Kind: kind}).Validate(); err == nil {
		t.Fatal("an unregistered kind must be refused")
	}
	boom := errors.New("boom")
	RegisterItemKind(kind, func(ChangeItem) error { return boom })
	if err := (ChangeItem{Kind: kind}).Validate(); !errors.Is(err, boom) {
		t.Fatalf("registered validation: %v", err)
	}
	RegisterItemKind(kind, nil)
	if err := (ChangeItem{Kind: kind}).Validate(); err != nil {
		t.Fatalf("re-registered, accepting: %v", err)
	}
}
