package graph

import "context"

// ChangeDefaults is what the graph asks of the registry when a change is created: the methodologies are not known to
// the graph. The lifecycle of a change, its state and its transitions are the guardian's (ADR 0098), not the graph's.
type ChangeDefaults interface {
	// DefaultGoal returns the main goal the changes of a methodology start with (ADR 0096); "" when the methodology is
	// unknown or has none.
	DefaultGoal(ctx context.Context, methodology string) (string, error)
}
