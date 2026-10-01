package methodology

import "slices"

// RankByPriority is the generic Activity-specialization mechanism (ADR 0009 §5 for an abstract action, ADR 0035
// §1 for a method providing a capability; architecture plan "Activity concept": specializes names an Activity's
// generic Activity and the condition - priority included - that makes it the one to use in context): among a set
// of candidates each specializing the same, more generic Activity, the ones whose own condition holds on the
// blackboard, highest priority first (ties keep declaration order - a stable sort). Different layers (Action,
// Method, and any future one) go from generic to specialized through this one algorithm, not independent copies
// of it.
func RankByPriority[T any](items []T, applicable func(T) bool, priority func(T) int) []T {
	out := make([]T, 0, len(items))
	for _, it := range items {
		if applicable(it) {
			out = append(out, it)
		}
	}
	slices.SortStableFunc(out, func(a, b T) int { return priority(b) - priority(a) })
	return out
}
