package mcpsvc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/zimwip/goap/lib/outbox"
)

// OutboxOrigin names the hub in the ids of its events (ADR 0136).
const OutboxOrigin = "mcp"

// The events of a connector, on goap.mcp.connector.<id>.<event> (ADR 0136).
const (
	// EventRegistered: a connector registered for the first time, or with another declaration (info or endpoint).
	EventRegistered = "registered"
	// EventLost: its lease expired without renewal.
	EventLost = "lost"
	// EventBack: it registered again after it was lost.
	EventBack = "back"
)

// ConnectorSubject is the subject of an event of a connector.
func ConnectorSubject(id, event string) string {
	return "goap.mcp.connector." + outbox.Token(id) + "." + event
}

// ConnectorEvent is the payload of an event of a connector: what it is and when, never a secret (the names of the
// secrets it needs are not sent either; a reader who needs its declaration asks the hub).
type ConnectorEvent struct {
	Event     string `json:"event"`
	Connector string `json:"connector"`
	Version   string `json:"version,omitempty"`
	// Endpoint is where the connector is reached (registered, back).
	Endpoint string `json:"endpoint,omitempty"`
	// Operations names the operations it exposes (registered).
	Operations []string `json:"operations,omitempty"`
	// LastSeen is its last registration or renewal before the event.
	LastSeen time.Time `json:"lastSeen,omitzero"`
	At       time.Time `json:"at"`
}

// Emit makes the hub write the events of its connectors in the outbox of its store, in the transaction that writes the
// registration (ADR 0136), and returns the relay delivering them to sink, in order, at least once: after each write,
// and again by Relay.Run, which the caller runs, for what a delivery missed. The loss of a connector is noticed by
// Watch, which the caller runs too. The store must be an outbox.Store (it panics otherwise); a nil sink: no event (nil).
func (s *Service) Emit(sink outbox.Sink) *outbox.Relay {
	if sink == nil {
		return nil
	}
	st, ok := s.Store.(outbox.Store)
	if !ok {
		panic(fmt.Sprintf("mcpsvc: the store %T keeps no outbox", s.Store))
	}
	s.relay = &outbox.Relay{Origin: OutboxOrigin, Store: st, Sink: sink}
	return s.relay
}

// Relay is the relay of the hub's outbox (nil until Emit).
func (s *Service) Relay() *outbox.Relay { return s.relay }

// deliver delivers what the hub wrote in its outbox, in the background: a registration does not wait for the bus.
func (s *Service) deliver(ctx context.Context) {
	relay := s.relay
	go func() { _ = relay.Drain(context.WithoutCancel(ctx)) }()
}

func (s *Service) event(ctx context.Context, event string, r ConnectorReg, at time.Time) (outbox.Message, error) {
	ev := ConnectorEvent{Event: event, Connector: r.Info.GetId(), Version: r.Info.GetVersion(), LastSeen: r.LastSeen, At: at}
	if event != EventLost {
		ev.Endpoint = r.Endpoint
	}
	if event == EventRegistered {
		for _, o := range r.Info.GetOperations() {
			ev.Operations = append(ev.Operations, o.Name)
		}
		ev.LastSeen = time.Time{}
	}
	return outbox.New(ctx, ConnectorSubject(ev.Connector, event), ev, at)
}

// registrationEvents are the events of reg replacing prev (found false: a first registration): registered when it is
// new or its declaration changed, back when prev was lost (lost first when its expiry was not reported yet), none for
// a renewal that changes nothing.
func (s *Service) registrationEvents(ctx context.Context, prev ConnectorReg, found bool, reg ConnectorReg) ([]outbox.Message, error) {
	var out []outbox.Message
	add := func(event string, r ConnectorReg, at time.Time) error {
		m, err := s.event(ctx, event, r, at)
		out = append(out, m)
		return err
	}
	if found && (!prev.LostAt.IsZero() || !s.liveAt(prev, reg.LastSeen)) {
		if prev.LostAt.IsZero() {
			// expired, not reported yet: the loss is reported at its expiry
			if err := add(EventLost, prev, prev.LastSeen.Add(s.lease())); err != nil {
				return nil, err
			}
		}
		if err := add(EventBack, ConnectorReg{Info: reg.Info, Endpoint: reg.Endpoint, LastSeen: prev.LastSeen}, reg.LastSeen); err != nil {
			return nil, err
		}
	}
	if !found || prev.Endpoint != reg.Endpoint || !proto.Equal(prev.Info, reg.Info) {
		if err := add(EventRegistered, reg, reg.LastSeen); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Sweep reports lost the registrations whose lease expired since they last registered, each with its event in its own
// transaction (MarkLost: one replica reports it, a renewal in between wins). Without Emit it does nothing.
func (s *Service) Sweep(ctx context.Context) error {
	if s.relay == nil {
		return nil
	}
	regs, err := s.Store.Connectors(ctx)
	if err != nil {
		return err
	}
	now, wrote := s.now(), false
	for _, r := range regs {
		if !r.LostAt.IsZero() || s.liveAt(r, now) {
			continue
		}
		m, err := s.event(ctx, EventLost, r, r.LastSeen.Add(s.lease()))
		if err != nil {
			return err
		}
		ok, err := s.Store.MarkLost(ctx, r.Info.Id, r.LastSeen, now, m)
		if err != nil {
			return err
		}
		wrote = wrote || ok
	}
	if wrote {
		s.deliver(ctx)
	}
	return nil
}

// Watch sweeps (Sweep) every interval (a fifth of the lease when not positive) until ctx is done. Without Emit it
// returns at once.
func (s *Service) Watch(ctx context.Context, interval time.Duration) {
	if s.relay == nil {
		return
	}
	if interval <= 0 {
		interval = s.lease() / 5
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
				slog.Default().Warn("connector sweep", "err", err)
			}
		}
	}
}
