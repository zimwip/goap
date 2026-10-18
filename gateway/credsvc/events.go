package credsvc

import (
	"context"
	"fmt"
	"time"

	"github.com/zimwip/goap/lib/outbox"
)

// OutboxOrigin names the credentials service in the ids of its events (ADR 0136).
const OutboxOrigin = "credentials"

// The kinds of the events of the credentials service, the last token of their subject
// goap.credentials.<subject>.<kind> (EventSubject).
const (
	// EventRegistered: a subject registered, its credential was created (Register).
	EventRegistered = "registered"
	// EventPasswordChanged: the password of a subject was set again (SetPassword).
	EventPasswordChanged = "password_changed"
	// EventSessionStarted: a sign-in session opened (StartSession).
	EventSessionStarted = "session_started"
	// EventSessionEnded: a sign-in session ended (EndSession, EndSessions, SetPassword): one event per session
	// ended, so a consumer reads every ending alike whatever ended it.
	EventSessionEnded = "session_ended"
)

// EventSubject is the subject of an event of kind about a user subject: goap.credentials.<subject>.<kind>, the
// subject made one token (outbox.Token; the payload carries it exactly).
func EventSubject(subject, kind string) string {
	return "goap.credentials." + outbox.Token(subject) + "." + kind
}

// Event is the payload of every event of the credentials service. It names who and when, and the session for a
// session event; never a password, its hash or a token.
type Event struct {
	// Subject is the user subject the event is about.
	Subject string `json:"subject"`
	// Session is the id of the session started or ended (session events only).
	Session string `json:"session,omitempty"`
	// At is when it happened.
	At time.Time `json:"at"`
}

// Emit makes the service write its events in the outbox of its store, in the transaction of their fact (ADR 0136),
// and returns the relay delivering them to sink, in order, at least once: after each write, in the background, and
// again by Relay.Run, which the caller runs, for what a delivery missed. A nil sink returns nil and the service
// writes no event. The store must be an outbox.Store (it panics otherwise).
func (s *Service) Emit(sink outbox.Sink) *outbox.Relay {
	if sink == nil {
		return nil
	}
	st, ok := s.Store.(outbox.Store)
	if !ok {
		panic(fmt.Sprintf("credsvc: the store %T keeps no outbox", s.Store))
	}
	s.relay = &outbox.Relay{Origin: OutboxOrigin, Store: st, Sink: sink}
	return s.relay
}

// Relay is the relay of the service's outbox (nil until Emit).
func (s *Service) Relay() *outbox.Relay { return s.relay }

// Wait waits for the deliveries the writes started (tests, shutdown).
func (s *Service) Wait() { s.pending.Wait() }

// event is the message of an event of kind, nil when the service emits none.
func (s *Service) event(ctx context.Context, kind string, ev Event) ([]outbox.Message, error) {
	if s.relay == nil {
		return nil, nil
	}
	m, err := outbox.New(ctx, EventSubject(ev.Subject, kind), ev, ev.At)
	if err != nil {
		return nil, err
	}
	return []outbox.Message{m}, nil
}

// ended gives the events of the sessions a revocation ends, nil when the service emits none.
func (s *Service) ended(ctx context.Context) Ended {
	if s.relay == nil {
		return nil
	}
	return func(sessions []Session) ([]outbox.Message, error) {
		var out []outbox.Message
		for _, sess := range sessions {
			m, err := s.event(ctx, EventSessionEnded, Event{Subject: sess.Subject, Session: sess.ID, At: sess.Revoked})
			if err != nil {
				return nil, err
			}
			out = append(out, m...)
		}
		return out, nil
	}
}

// deliver hands what a write put in the outbox to the relay, in the background: the caller does not wait for the
// consumers.
func (s *Service) deliver(ctx context.Context) {
	if s.relay == nil {
		return
	}
	relay := s.relay
	s.pending.Add(1)
	go func() {
		defer s.pending.Done()
		_ = relay.Drain(context.WithoutCancel(ctx))
	}()
}
