package methodology

import "context"

// Source resolves methodologies (the registry): the latest published version of one, or of every methodology. The
// engine reads them through it; the registry implements it without importing the engine (ADR 0098 §10).
type Source interface {
	Methodology(ctx context.Context, name string) (*Compiled, error)
	List(ctx context.Context) ([]*Compiled, error)
}

// ErrUnknown is returned for an unknown methodology.
type ErrUnknown struct{ Name string }

func (e ErrUnknown) Error() string { return "unknown methodology " + e.Name }
