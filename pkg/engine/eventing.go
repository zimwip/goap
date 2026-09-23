package engine

import (
	"context"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// EventingGraph wraps an in-process GraphPort and reports change lifecycle
// events (single-process deployments, where the graph service does not
// publish them on NATS).
type EventingGraph struct {
	GraphPort
	OnEvent func(ctx context.Context, ev domain.ChangeEvent)
}

func (g EventingGraph) emit(ctx context.Context, typ string, id domain.ChangeID, b *domain.Baseline, items []domain.ChangeItem) {
	if g.OnEvent == nil {
		return
	}
	bb, err := g.GraphPort.Blackboard(ctx, id)
	if err != nil {
		return
	}
	c := bb.Change
	c.Items = nil
	g.OnEvent(ctx, domain.ChangeEvent{Type: typ, Change: c, Baseline: b, Items: items})
}

// CreateChange implements GraphPort.
func (g EventingGraph) CreateChange(ctx context.Context, in graph.NewChange) (domain.ChangeSet, error) {
	c, err := g.GraphPort.CreateChange(ctx, in)
	if err == nil {
		g.emit(ctx, "change.created", c.ID, nil, nil)
	}
	return c, err
}

// AddItems implements GraphPort.
func (g EventingGraph) AddItems(ctx context.Context, id domain.ChangeID, items []domain.ChangeItem) ([]domain.ChangeItem, error) {
	out, err := g.GraphPort.AddItems(ctx, id, items)
	if err == nil {
		g.emit(ctx, "change.item_added", id, nil, out)
	}
	return out, err
}

// Apply implements GraphPort.
func (g EventingGraph) Apply(ctx context.Context, id domain.ChangeID, name string) (domain.Baseline, error) {
	b, err := g.GraphPort.Apply(ctx, id, name)
	if err == nil {
		g.emit(ctx, "change.applied", id, &b, nil)
	}
	return b, err
}

// TriggerEventOf converts a change event.
func TriggerEventOf(ev domain.ChangeEvent) TriggerEvent {
	c := ev.Change
	return TriggerEvent{Type: ev.Type, Change: &c}
}

// WatchProcesses feeds process events of the broker to the trigger manager
// (process.completed, process.failed, process.stuck). It returns when ctx is done.
func (t *TriggerManager) WatchProcesses(ctx context.Context, b *Broker) {
	events, cancel := b.Subscribe(256)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.Process == nil {
				continue
			}
			switch ev.Process.Status {
			case StatusCompleted:
				if ev.Event == string(StatusCompleted) {
					t.Handle(ctx, TriggerEvent{Type: "process.completed", Process: ev.Process})
				}
			case StatusFailed:
				if ev.Event == string(StatusFailed) {
					t.Handle(ctx, TriggerEvent{Type: "process.failed", Process: ev.Process})
				}
			case StatusStuck:
				if ev.Event == string(StatusStuck) {
					t.Handle(ctx, TriggerEvent{Type: "process.stuck", Process: ev.Process})
				}
			}
		}
	}
}
