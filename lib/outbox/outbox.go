// Package outbox carries the events of a service from its database to the bus (ADR 0136): a service writes its events
// in an outbox table in the transaction that writes their fact, and a Relay delivers them afterwards, in order, at
// least once. A service owns its events: no caller emits one on its behalf.
package outbox

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/zimwip/goap/lib/platform"
)

// HeaderEventID carries the id of an event (Message.EventID) on the bus, so a consumer that may see it twice can tell.
const HeaderEventID = platform.HeaderEventID

// Message is an event of the outbox: its subject, its JSON payload and the headers its context gave it (trace,
// actor, command). Seq is its rank in the outbox, 0 before it is stored; Origin is the service whose outbox holds it.
type Message struct {
	Seq       int64
	Origin    string
	Subject   string
	Data      []byte
	Headers   map[string]string
	CreatedAt time.Time
}

// EventID is the id of the event across services: <origin>:<seq>.
func (m Message) EventID() string { return m.Origin + ":" + strconv.FormatInt(m.Seq, 10) }

// New builds the message of an event published from ctx: v as JSON, the trace context and the stamp of ctx as
// headers.
func New(ctx context.Context, subject string, v any, at time.Time) (Message, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return Message{}, err
	}
	h := map[string]string{}
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(h))
	if st := platform.StampOf(ctx); st.Command != "" {
		h[platform.HeaderActor], h[platform.HeaderCommand] = st.Actor, st.Command
	}
	return Message{Subject: subject, Data: data, Headers: h, CreatedAt: at}, nil
}

// Context is ctx carrying what m says: its trace context, its stamp and its event id (platform.EventIDOf).
func Context(ctx context.Context, m Message) context.Context {
	ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(m.Headers))
	if m.Seq != 0 {
		ctx = platform.WithEventID(ctx, m.EventID())
	}
	if c := m.Headers[platform.HeaderCommand]; c != "" {
		ctx = platform.WithStamp(ctx, platform.Stamp{Actor: m.Headers[platform.HeaderActor], Command: c})
	}
	return ctx
}

// EventID is the id of the event ctx handles (Context, or a durable consumer of the bus), "" when none: a consumer that
// may see an event twice tells it by this id.
func EventID(ctx context.Context) string { return platform.EventIDOf(ctx) }

// Store is the outbox of a service as its relay reads it.
type Store interface {
	// Pending returns at most limit messages not yet published, oldest first.
	Pending(ctx context.Context, limit int) ([]Message, error)
	// MarkPublished records that the messages were delivered.
	MarkPublished(ctx context.Context, seqs []int64, at time.Time) error
	// PurgePublished removes the messages published before t.
	PurgePublished(ctx context.Context, before time.Time) error
}

// Sink receives the messages of a relay. An error stops the delivery: the message and the ones after it are delivered
// again later, so the order of a service's events is kept.
type Sink interface {
	Deliver(ctx context.Context, m Message) error
}

// SinkFunc is a Sink.
type SinkFunc func(ctx context.Context, m Message) error

// Deliver implements Sink.
func (f SinkFunc) Deliver(ctx context.Context, m Message) error { return f(ctx, m) }

// Sinks delivers to every sink of the list (nil ones skipped); the first error is returned once all were tried.
type Sinks []Sink

// Deliver implements Sink.
func (s Sinks) Deliver(ctx context.Context, m Message) error {
	var first error
	for _, k := range s {
		if k == nil {
			continue
		}
		if err := k.Deliver(ctx, m); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Typed hands a sink the payload of the messages decoded by decode (nil, false: not for it). It adapts the in-process
// receivers that take typed events.
func Typed(decode func(subject string, data []byte) (any, bool), fn func(ctx context.Context, subject string, v any) error) Sink {
	return SinkFunc(func(ctx context.Context, m Message) error {
		v, ok := decode(m.Subject, m.Data)
		if !ok {
			return nil
		}
		return fn(ctx, m.Subject, v)
	})
}

// NATS publishes on the GOAP stream of the bus, deduplicated by the event id (Nats-Msg-Id).
func NATS(e *platform.Events) Sink {
	if e == nil {
		return nil
	}
	return SinkFunc(func(ctx context.Context, m Message) error {
		h := make(map[string]string, len(m.Headers)+1)
		for k, v := range m.Headers {
			h[k] = v
		}
		h[HeaderEventID] = m.EventID()
		return e.PublishMessage(ctx, m.EventID(), m.Subject, m.Data, h)
	})
}

// Relay delivers the messages of an outbox to its sink. Drain delivers what is pending at once (after a commit);
// Run keeps delivering what Drain could not, and purges what was published.
type Relay struct {
	// Origin names the service in the event ids.
	Origin string
	Store  Store
	Sink   Sink
	// Poll is the interval at which Run looks for pending messages (1 s by default), Retention how long a published
	// message is kept (24 h by default).
	Poll, Retention time.Duration
	Log             *slog.Logger
	// Now is the clock (time.Now by default).
	Now func() time.Time
	// Lease, when set, makes one relay of the replicas sharing the outbox deliver at a time: a drain that does not
	// hold it leaves the delivery to the holder (ErrBusy). Without it the replicas' relays may deliver one message
	// twice, which the bus deduplicates by its id.
	Lease Lease

	mu    sync.Mutex
	again atomic.Bool
	once  sync.Once
	kick  chan struct{}
}

const batch = 100

// Lease is a lock shared by the replicas of a service, held for a while and renewed by its holder.
type Lease interface {
	// Hold takes the lease, or renews it when this replica holds it; false while another replica holds it.
	Hold(ctx context.Context) (bool, error)
	// Let lets it go.
	Let(ctx context.Context)
}

// ErrBusy is returned by Drain when another drain of the relay is at work (it delivers what this one would have).
var ErrBusy = errors.New("outbox relay busy")

func (r *Relay) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Relay) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *Relay) init() { r.once.Do(func() { r.kick = make(chan struct{}, 1) }) }

// Kick wakes Run.
func (r *Relay) Kick() {
	r.init()
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Drain delivers the pending messages, in order, until none is left or one fails. A drain already at work (in
// another goroutine, or the one delivering the message whose handling wrote again) is left to deliver them: Drain then
// returns ErrBusy at once, so a sink may write to the service it listens to.
func (r *Relay) Drain(ctx context.Context) error {
	if r == nil || r.Store == nil || r.Sink == nil {
		return nil
	}
	if !r.mu.TryLock() {
		// the drain at work looks again before it stops, so what this one would deliver is not left behind
		r.again.Store(true)
		r.Kick()
		return ErrBusy
	}
	defer r.mu.Unlock()
	ctx = context.WithoutCancel(ctx) // a delivery outlives the request that wrote the event
	if r.Lease != nil {
		defer r.Lease.Let(ctx)
	}
	for {
		if r.Lease != nil {
			if held, err := r.Lease.Hold(ctx); err != nil || !held {
				return cmp.Or(err, ErrBusy)
			}
		}
		r.again.Store(false)
		msgs, err := r.Store.Pending(ctx, batch)
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			if r.again.Load() {
				continue
			}
			return nil
		}
		var done []int64
		var failed error
		for _, m := range msgs {
			m.Origin = r.Origin
			if err := r.Sink.Deliver(Context(ctx, m), m); err != nil {
				failed = err
				break
			}
			done = append(done, m.Seq)
		}
		if len(done) > 0 {
			if err := r.Store.MarkPublished(ctx, done, r.now()); err != nil {
				return err
			}
		}
		if failed != nil {
			return failed
		}
	}
}

// Run delivers the pending messages when kicked or every Poll, backing off while the sink fails, and purges the
// published ones; it returns when ctx is done.
func (r *Relay) Run(ctx context.Context) {
	if r == nil || r.Store == nil || r.Sink == nil {
		return
	}
	r.init()
	poll, retention := r.Poll, r.Retention
	if poll <= 0 {
		poll = time.Second
	}
	if retention <= 0 {
		retention = 24 * time.Hour
	}
	wait, purged := poll, time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.kick:
		case <-time.After(wait):
		}
		switch err := r.Drain(ctx); {
		case err == nil, errors.Is(err, ErrBusy):
			wait = poll
		default:
			r.log().Warn("outbox delivery failed, retried", "origin", r.Origin, "err", err)
			wait = min(2*wait, time.Minute)
		}
		if now := r.now(); now.Sub(purged) > time.Hour {
			if err := r.Store.PurgePublished(ctx, now.Add(-retention)); err != nil {
				r.log().Warn("outbox purge", "origin", r.Origin, "err", err)
			}
			purged = now
		}
	}
}

// Token makes s one token of a subject: the separators and wildcards of NATS (".", "*", ">") and blanks become "_"
// (a subject or an email holds dots). The payload of an event carries the exact value.
func Token(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '.' || r == '*' || r == '>' || unicode.IsSpace(r) || unicode.IsControl(r):
			return '_'
		}
		return r
	}, s)
}

// Start runs a relay of a composition until ctx is done, in the background, configured from the environment
// (GOAP_OUTBOX_POLL, 1 s; GOAP_OUTBOX_RETENTION, 24 h): the retries of what a delivery missed and the purge. A nil
// relay (a service emitting nothing) is left alone.
func Start(ctx context.Context, r *Relay, log *slog.Logger) *Relay {
	if r == nil {
		return nil
	}
	r.Log = log
	r.Poll = platform.EnvDuration("GOAP_OUTBOX_POLL", time.Second)
	r.Retention = platform.EnvDuration("GOAP_OUTBOX_RETENTION", 24*time.Hour)
	go r.Run(ctx)
	return r
}
