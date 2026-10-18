package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zimwip/goap/lib/outbox"
)

// OutboxOrigin names the engine in the ids of its events (ADR 0136).
const OutboxOrigin = "engine"

// Emit makes the engine write the events of its processes in the outbox of its store, in the transaction that writes
// the process (ADR 0136), and returns the relay delivering them to sink, in order, at least once: after each write,
// and again by Relay.Run, which the caller runs, for what a delivery missed. The store must be an EventStore (it
// panics otherwise); a nil sink keeps the direct publication. With shared locks (Locks), one replica delivers at a
// time. Call it once Locks is set. Events then serves the log lines of the runs only, a
// live display nothing keeps.
func (e *Engine) Emit(sink outbox.Sink) *outbox.Relay {
	if sink == nil {
		return nil
	}
	st, ok := e.Store.(EventStore)
	if !ok {
		panic(fmt.Sprintf("engine: the process store %T keeps no outbox", e.Store))
	}
	e.outbox = &outbox.Relay{Origin: OutboxOrigin, Store: st, Sink: sink, Log: e.Log}
	if e.Locks != nil {
		// the replicas share the outbox of their store: one delivers at a time, in order
		e.outbox.Lease = outboxLease{e}
	}
	return e.outbox
}

// outboxLease is the lease of the outbox among the replicas, a shared lock of the engine (ADR 0124).
type outboxLease struct{ e *Engine }

const outboxLock = "outbox"

func (l outboxLease) Hold(ctx context.Context) (bool, error) {
	held, _, err := l.e.Locks.Acquire(ctx, outboxLock, l.e.lockTTL())
	return held, err
}

func (l outboxLease) Let(ctx context.Context) { _ = l.e.Locks.Release(ctx, outboxLock) }

// Relay is the relay of the engine's outbox (nil until Emit).
func (e *Engine) Relay() *outbox.Relay { return e.outbox }

// emit writes p (when not nil) with its event: through the outbox once Emit was called, else the process then a direct
// publication. The delivery of an outboxed event runs in the background: a run does not wait for its consumers.
func (e *Engine) emit(ctx context.Context, p *Process, subject string, ev ProcessEvent) error {
	if e.outbox == nil {
		if p != nil {
			if err := e.Store.Put(ctx, p); err != nil {
				return err
			}
		}
		if e.Events != nil {
			if err := e.Events.Publish(ctx, subject, ev); err != nil {
				e.log().Warn("publish failed", "err", err)
			}
		}
		return nil
	}
	m, err := outbox.New(ctx, subject, ev, ev.Time)
	if err != nil {
		return err
	}
	if err := e.Store.(EventStore).PutEmitting(ctx, p, m); err != nil {
		return err
	}
	relay := e.outbox
	e.background(func() { _ = relay.Drain(context.WithoutCancel(ctx)) })
	return nil
}

// ProcessEvents hands the process events of the engine's outbox to fn (the live broker, the triggers in process),
// the log lines aside.
func ProcessEvents(fn func(ctx context.Context, ev ProcessEvent)) outbox.Sink {
	return outbox.SinkFunc(func(ctx context.Context, m outbox.Message) error {
		if !strings.HasPrefix(m.Subject, "goap.process.") {
			return nil
		}
		var ev ProcessEvent
		if err := json.Unmarshal(m.Data, &ev); err != nil {
			return fmt.Errorf("process event %s: %w", m.Subject, err)
		}
		if ev.Event != "log" {
			fn(ctx, ev)
		}
		return nil
	})
}
