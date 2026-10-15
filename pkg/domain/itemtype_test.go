package domain

import (
	"errors"
	"testing"
)

// A kind of item the core does not know is refused until a use case registers it (ADR 0065); registering is
// idempotent and the last validation of a kind wins.
func TestRegisterItemType(t *testing.T) {
	const kind ItemType = "test-kind"
	if err := (ChangeItem{Type: kind}).Validate(); err == nil {
		t.Fatal("an unregistered kind must be refused")
	}
	boom := errors.New("boom")
	RegisterItemType(kind, func(ChangeItem) error { return boom })
	if err := (ChangeItem{Type: kind}).Validate(); !errors.Is(err, boom) {
		t.Fatalf("registered validation: %v", err)
	}
	RegisterItemType(kind, nil)
	if err := (ChangeItem{Type: kind}).Validate(); err != nil {
		t.Fatalf("re-registered, accepting: %v", err)
	}
}

// The catalogue lists the core kinds and the registered ones, described, with the permission they ask.
func TestItemTypes(t *testing.T) {
	const kind ItemType = "test-listed"
	RegisterItemType(kind, nil)
	DescribeItemType(kind, "test", "A kind of test.")
	RequireItemPermission(kind, ItemPermission{Permission: "thing:sign"})
	byKind := map[ItemType]ItemTypeInfo{}
	for _, k := range ItemTypes() {
		byKind[k.Type] = k
	}
	if k := byKind[TypeArtifact]; k.Origin != OriginCore || k.Description == "" {
		t.Errorf("core kind: %+v", k)
	}
	if k := byKind[kind]; k.Origin != "test" || k.Description != "A kind of test." || k.Permission == nil || k.Permission.Permission != "thing:sign" {
		t.Errorf("registered kind: %+v", k)
	}
}
