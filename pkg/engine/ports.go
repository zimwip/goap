package engine

import (
	"context"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

// GraphPort is what the engine needs from the graph service. *graph.Graph
// implements it in-process; the engine service uses a connect client.
type GraphPort interface {
	CreateChange(ctx context.Context, in graph.NewChange) (domain.ChangeSet, error)
	UpdateChange(ctx context.Context, id domain.ChangeID, p graph.ChangePatch) (domain.ChangeSet, error)
	AddItems(ctx context.Context, id domain.ChangeID, items []domain.ChangeItem) ([]domain.ChangeItem, error)
	Blackboard(ctx context.Context, id domain.ChangeID) (domain.Blackboard, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
}

// MethodologyPort resolves methodologies (the registry).
type MethodologyPort interface {
	Methodology(ctx context.Context, name string) (*methodology.Compiled, error)
}

// Publisher publishes process events (NATS in services).
type Publisher interface {
	Publish(ctx context.Context, subject string, v any) error
}

// NopPublisher discards events.
type NopPublisher struct{}

// Publish implements Publisher.
func (NopPublisher) Publish(context.Context, string, any) error { return nil }

// StaticMethodologies serves compiled methodologies from memory.
type StaticMethodologies map[string]*methodology.Compiled

// Methodology implements MethodologyPort.
func (s StaticMethodologies) Methodology(_ context.Context, name string) (*methodology.Compiled, error) {
	m, ok := s[name]
	if !ok {
		return nil, ErrUnknownMethodology{Name: name}
	}
	return m, nil
}

// ErrUnknownMethodology is returned for unknown methodologies.
type ErrUnknownMethodology struct{ Name string }

func (e ErrUnknownMethodology) Error() string { return "unknown methodology " + e.Name }
