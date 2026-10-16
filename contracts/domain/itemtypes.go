package domain

import (
	"maps"
	"slices"
)

// CoreItemTypes are the types of item ChangeItem.Validate knows without a registration: the change mechanism. A domain
// declares them like any other (the built-in methodology domain, ADR 0128).
func CoreItemTypes() []ItemType {
	return []ItemType{TypeArtifact, TypeDecision, TypeDecisionPoint, TypeFlow, TypeReview, TypeSignal, TypeTransition}
}

// RegisteredItemTypes lists the types of item a use case gave a validator (RegisterItemType), by name.
func RegisteredItemTypes() []ItemType {
	itemTypes.mu.RLock()
	defer itemTypes.mu.RUnlock()
	return slices.Sorted(maps.Keys(itemTypes.m))
}

// HasItemCode reports whether this process gives the items of a type a meaning beyond their data: a core type, or a
// validator a use case registered (RegisterItemType).
func HasItemCode(t ItemType) bool {
	return slices.Contains(CoreItemTypes(), t) || itemTypes.get(t) != nil
}
