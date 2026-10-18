package prefssvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/zimwip/goap/lib/outbox"
)

// ErrInvalid is returned for a document the service does not accept.
var ErrInvalid = errors.New("invalid")

// ErrAnonymous is returned when the caller is not identified: preferences belong to a declared user.
var ErrAnonymous = errors.New("preferences need an identified user")

// MaxDocumentBytes bounds the JSON encoding of a subject's document: the service keeps it opaque, so the only thing it
// protects is its own storage.
const MaxDocumentBytes = 64 << 10

// check refuses a document that is not a JSON object the service can keep: values of other kinds than JSON's, or
// larger than MaxDocumentBytes. What the keys mean, and which values they accept, is the business of the interface
// reading them (web/src/lib/stores/preferences.svelte.ts), never of this service (ADR 0068).
func check(doc Prefs) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("%w: not a JSON object: %v", ErrInvalid, err)
	}
	if len(b) > MaxDocumentBytes {
		return fmt.Errorf("%w: the document is %d bytes, at most %d", ErrInvalid, len(b), MaxDocumentBytes)
	}
	return nil
}

// Service reads and writes the preferences of a subject.
type Service struct {
	Store Store
	// Now is the clock of the events (time.Now by default).
	Now func() time.Time

	relay *outbox.Relay
}

// OutboxOrigin names the preferences service in the ids of its events (ADR 0136).
const OutboxOrigin = "preferences"

// The events of the preferences (ADR 0136), on goap.preferences.<subject>.<event> (the subject made a token by
// outbox.Token; the payload carries it exactly).
const (
	EventUpdated = "updated"
	EventReset   = "reset"
)

// Event is the payload of an event of the preferences: whose document, which top-level keys changed (added, changed
// or removed; "updated" only) and when. It never carries a value: a reader who needs one asks the owner.
type Event struct {
	Subject string    `json:"subject"`
	Keys    []string  `json:"keys,omitempty"`
	At      time.Time `json:"at"`
}

// Subject is the bus subject of an event of the preferences of a user.
func Subject(user, event string) string {
	return "goap.preferences." + outbox.Token(user) + "." + event
}

// Emit makes the service write its events in the outbox of its store, in the transaction of the document (ADR 0136),
// and returns the relay delivering them to sink, in order, at least once: after each write, and again by Relay.Run,
// which the caller runs. A nil sink: no outbox, no event (nil relay). The store must be an outbox.Store (it panics
// otherwise). Call it before serving.
func (s *Service) Emit(sink outbox.Sink) *outbox.Relay {
	if sink == nil {
		return nil
	}
	st, ok := s.Store.(outbox.Store)
	if !ok {
		panic(fmt.Sprintf("prefssvc: the store %T keeps no outbox", s.Store))
	}
	s.relay = &outbox.Relay{Origin: OutboxOrigin, Store: st, Sink: sink}
	return s.relay
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// event is the message of an event, nil without an outbox.
func (s *Service) event(ctx context.Context, subject, event string, keys []string) ([]outbox.Message, error) {
	if s.relay == nil {
		return nil, nil
	}
	at := s.now().UTC()
	m, err := outbox.New(ctx, Subject(subject, event), Event{Subject: subject, Keys: keys, At: at}, at)
	if err != nil {
		return nil, err
	}
	return []outbox.Message{m}, nil
}

// deliver hands what the write left in the outbox to the relay, in the background: the caller does not wait for the
// consumers.
func (s *Service) deliver(ctx context.Context) {
	if relay := s.relay; relay != nil {
		go func() { _ = relay.Drain(context.WithoutCancel(ctx)) }()
	}
}

// changedKeys lists, sorted, the top-level keys whose value differs between two documents (as JSON).
func changedKeys(before, after Prefs) []string {
	var keys []string
	for k, a := range after {
		b, ok := before[k]
		if !ok || !sameJSON(a, b) {
			keys = append(keys, k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

func sameJSON(a, b any) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

// Get returns the preferences of a subject.
func (s *Service) Get(ctx context.Context, subject string) (Prefs, error) {
	if subject == "" {
		return nil, ErrAnonymous
	}
	return s.Store.Get(ctx, subject)
}

// Set merges a patch into the document of a subject: a nil value clears the key. Any key is kept; the result must
// stay a JSON object of at most MaxDocumentBytes. The event "updated" names the keys whose value changed; a patch
// changing nothing emits none.
func (s *Service) Set(ctx context.Context, subject string, patch Prefs) (Prefs, error) {
	if subject == "" {
		return nil, ErrAnonymous
	}
	emitted := false
	out, err := s.Store.Update(ctx, subject, func(cur Prefs) (Prefs, []outbox.Message, error) {
		out := maps.Clone(cur)
		if out == nil {
			out = Prefs{}
		}
		for k, v := range patch {
			if v == nil {
				delete(out, k)
			} else {
				out[k] = v
			}
		}
		if err := check(out); err != nil {
			return nil, nil, err
		}
		keys := changedKeys(cur, out)
		if len(keys) == 0 {
			return out, nil, nil
		}
		msgs, err := s.event(ctx, subject, EventUpdated, keys)
		emitted = len(msgs) > 0
		return out, msgs, err
	})
	if err == nil && emitted {
		s.deliver(ctx)
	}
	return out, err
}

// Reset forgets the preferences of a subject; the event "reset" is emitted when there was a document to forget.
func (s *Service) Reset(ctx context.Context, subject string) error {
	if subject == "" {
		return ErrAnonymous
	}
	msgs, err := s.event(ctx, subject, EventReset, nil)
	if err != nil {
		return err
	}
	if err := s.Store.Delete(ctx, subject, msgs...); err != nil {
		return err
	}
	if len(msgs) > 0 {
		s.deliver(ctx)
	}
	return nil
}
