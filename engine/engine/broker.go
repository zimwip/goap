package engine

import (
	"context"
	"sync"
)

// Broker fans process events out to live subscribers (WatchEvents). With
// several engine replicas it is fed from NATS instead of local publications.
type Broker struct {
	mu   sync.Mutex
	subs map[int]chan ProcessEvent
	next int
}

// NewBroker returns an empty broker.
func NewBroker() *Broker { return &Broker{subs: map[int]chan ProcessEvent{}} }

// Publish implements Publisher (local delivery).
func (b *Broker) Publish(_ context.Context, _ string, v any) error {
	if ev, ok := v.(ProcessEvent); ok {
		b.Deliver(ev)
	}
	return nil
}

// Deliver sends an event to every subscriber; slow subscribers lose events
// rather than blocking the engine.
func (b *Broker) Deliver(ev ProcessEvent) {
	if ev.Process != nil {
		ev.Process = clone(ev.Process)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Subscribe returns a channel of events and its cancel function.
func (b *Broker) Subscribe(buffer int) (<-chan ProcessEvent, func()) {
	ch := make(chan ProcessEvent, buffer)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Publishers publishes to several publishers.
type Publishers []Publisher

// Publish implements Publisher.
func (ps Publishers) Publish(ctx context.Context, subject string, v any) error {
	var first error
	for _, p := range ps {
		if p == nil {
			continue
		}
		if err := p.Publish(ctx, subject, v); err != nil && first == nil {
			first = err
		}
	}
	return first
}
