package engine

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/events"
	"github.com/zimwip/goap/lib/outbox"
)

// ChangeEvents hands fn the events of the changes (goap.change.<id>.<event>, ADR 0136) among the messages of an
// outbox: what feeds the trigger manager in a single-process deployment, where the graph's outbox relay delivers in
// process. The graph emits them, whoever wrote the change; the engine emits none.
func ChangeEvents(fn func(ctx context.Context, ev domain.ChangeEvent)) outbox.Sink {
	return outbox.SinkFunc(func(ctx context.Context, m outbox.Message) error {
		if !strings.HasPrefix(m.Subject, "goap.change.") {
			return nil
		}
		var ev domain.ChangeEvent
		if json.Unmarshal(m.Data, &ev) == nil && ev.Type != "" {
			fn(ctx, ev)
		}
		return nil
	})
}

// TriggerEventOf converts a change event.
func TriggerEventOf(ev domain.ChangeEvent) TriggerEvent {
	c := ev.Change
	return TriggerEvent{Type: ev.Type, Change: &c, Items: ev.Items, Fields: ev.Fields, EndUser: ev.EndUser}
}

// WatchProcesses feeds process events of the broker to the trigger manager
// (process.completed, process.failed, process.stuck). It returns when ctx is done.
func (t *TriggerManager) WatchProcesses(ctx context.Context, b *Broker) {
	evs, cancel := b.Subscribe(256)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-evs:
			if !ok {
				return
			}
			if ev.Process == nil {
				continue
			}
			if typ, ok := events.FromBroker(ev.Event); ok {
				t.Handle(ctx, TriggerEvent{Type: typ, Process: ev.Process, Step: ev.Step})
				continue
			}
			switch ev.Process.Status {
			case StatusCompleted:
				if ev.Event == string(StatusCompleted) {
					t.Handle(ctx, TriggerEvent{Type: events.ProcessCompleted, Process: ev.Process})
				}
			case StatusFailed:
				if ev.Event == string(StatusFailed) {
					t.Handle(ctx, TriggerEvent{Type: events.ProcessFailed, Process: ev.Process})
				}
			case StatusStuck:
				if ev.Event == string(StatusStuck) {
					t.Handle(ctx, TriggerEvent{Type: events.ProcessStuck, Process: ev.Process})
				}
			}
		}
	}
}
