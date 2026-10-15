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

// The catalogue lists the core kinds and the registered ones, described, with the permission they ask.
func TestItemKinds(t *testing.T) {
	const kind ItemKind = "test-listed"
	RegisterItemKind(kind, nil)
	DescribeItemKind(kind, "test", "A kind of test.")
	RequireItemPermission(kind, ItemPermission{Permission: "thing:sign"})
	byKind := map[ItemKind]ItemKindInfo{}
	for _, k := range ItemKinds() {
		byKind[k.Kind] = k
	}
	if k := byKind[KindArtifact]; k.Origin != OriginCore || k.Description == "" {
		t.Errorf("core kind: %+v", k)
	}
	if k := byKind[kind]; k.Origin != "test" || k.Description != "A kind of test." || k.Permission == nil || k.Permission.Permission != "thing:sign" {
		t.Errorf("registered kind: %+v", k)
	}
}
