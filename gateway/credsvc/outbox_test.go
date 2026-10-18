package credsvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zimwip/goap/gateway/credsvc"
	"github.com/zimwip/goap/lib/outbox"
)

func pendingSubjects(t *testing.T, st credsvc.Store) []string {
	t.Helper()
	msgs, err := st.(outbox.Store).Pending(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		out = append(out, m.Subject)
	}
	return out
}

// A store writes the events of a fact with it, and none when the write is refused or ends nothing (ADR 0136).
func TestStoreOutbox(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			msg := func(subject string) outbox.Message {
				m, err := outbox.New(ctx, subject, credsvc.Event{Subject: "alice"}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			if err := st.Create(ctx, "alice", "h1", msg("created")); err != nil {
				t.Fatal(err)
			}
			if err := st.Create(ctx, "alice", "h2", msg("refused")); !errors.Is(err, credsvc.ErrExists) {
				t.Fatalf("duplicate = %v", err)
			}
			if err := st.Set(ctx, "nobody", "h", msg("refused")); !errors.Is(err, credsvc.ErrNotFound) {
				t.Fatalf("set unknown = %v", err)
			}
			if err := st.Set(ctx, "alice", "h3", msg("set")); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			sess := credsvc.Session{ID: "s1", Subject: "alice", Created: now, Expires: now.Add(time.Hour)}
			if err := st.CreateSession(ctx, sess, now.Add(-time.Hour), msg("session")); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateSession(ctx, sess, now.Add(-time.Hour), msg("refused")); !errors.Is(err, credsvc.ErrExists) {
				t.Fatalf("duplicate session = %v", err)
			}
			var ended [][]string
			endedFn := func(ss []credsvc.Session) ([]outbox.Message, error) {
				var ids []string
				var out []outbox.Message
				for _, s := range ss {
					ids = append(ids, s.ID)
					out = append(out, msg("ended:"+s.ID))
				}
				ended = append(ended, ids)
				return out, nil
			}
			if err := st.RevokeSession(ctx, "unknown", now, endedFn); err != nil {
				t.Fatal(err)
			}
			// refused by its events: the session stays open, nothing written
			if err := st.RevokeSession(ctx, "s1", now, func([]credsvc.Session) ([]outbox.Message, error) { return nil, errors.New("no") }); err == nil {
				t.Fatal("a revocation whose events fail is refused")
			}
			if got, _ := st.Session(ctx, "s1"); !got.Revoked.IsZero() {
				t.Fatal("a refused revocation ends nothing")
			}
			if err := st.RevokeSession(ctx, "s1", now, endedFn); err != nil {
				t.Fatal(err)
			}
			if err := st.RevokeSession(ctx, "s1", now, endedFn); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"s2", "s3"} {
				s := credsvc.Session{ID: id, Subject: "alice", Created: time.Now(), Expires: now.Add(time.Hour)}
				if err := st.CreateSession(ctx, s, now.Add(-time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.RevokeSessions(ctx, "alice", now, endedFn); err != nil {
				t.Fatal(err)
			}
			if err := st.RevokeSessions(ctx, "alice", now, endedFn); err != nil {
				t.Fatal(err)
			}
			if want := [][]string{{"s1"}, {"s2", "s3"}}; !slices.EqualFunc(ended, want, slices.Equal) {
				t.Fatalf("ended = %v, want %v", ended, want)
			}
			want := []string{"created", "set", "session", "ended:s1", "ended:s2", "ended:s3"}
			if got := pendingSubjects(t, st); !slices.Equal(got, want) {
				t.Fatalf("outbox = %v, want %v", got, want)
			}
		})
	}
}

type recorder struct {
	mu   sync.Mutex
	msgs []outbox.Message
}

func (r *recorder) Deliver(_ context.Context, m outbox.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	return nil
}

// The service emits the events of what it writes, in order, through its relay; never a password or its hash.
func TestServiceEmits(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := &credsvc.Service{Store: st}
			rec := &recorder{}
			if s.Emit(nil) != nil {
				t.Fatal("no sink, no relay")
			}
			if s.Emit(rec) == nil {
				t.Fatal("Emit returns the relay")
			}
			const subject = "alice@example.com"
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(s.Register(ctx, subject, "correct horse"))
			if err := s.Register(ctx, subject, "correct horse"); !errors.Is(err, credsvc.ErrExists) {
				t.Fatalf("duplicate = %v", err)
			}
			a1, err := s.StartSession(ctx, subject, time.Hour)
			must(err)
			a2, err := s.StartSession(ctx, subject, time.Hour)
			must(err)
			must(s.EndSession(ctx, a1))
			must(s.EndSession(ctx, a1)) // already ended: no event
			must(s.SetPassword(ctx, subject, "battery staple"))
			must(s.EndSessions(ctx, subject)) // none open: no event
			s.Wait()
			must(s.Relay().Drain(ctx))

			tok := "goap.credentials.alice@example_com."
			want := []struct{ subject, session string }{
				{tok + credsvc.EventRegistered, ""},
				{tok + credsvc.EventSessionStarted, a1},
				{tok + credsvc.EventSessionStarted, a2},
				{tok + credsvc.EventSessionEnded, a1},
				{tok + credsvc.EventPasswordChanged, ""},
				{tok + credsvc.EventSessionEnded, a2},
			}
			rec.mu.Lock()
			defer rec.mu.Unlock()
			if len(rec.msgs) != len(want) {
				var got []string
				for _, m := range rec.msgs {
					got = append(got, m.Subject)
				}
				t.Fatalf("events = %v, want %d", got, len(want))
			}
			for i, m := range rec.msgs {
				var ev credsvc.Event
				must(json.Unmarshal(m.Data, &ev))
				if m.Subject != want[i].subject || ev.Subject != subject || ev.Session != want[i].session || ev.At.IsZero() {
					t.Errorf("event %d = %s %+v, want %s session %q", i, m.Subject, ev, want[i].subject, want[i].session)
				}
				if m.Origin != credsvc.OutboxOrigin || m.Seq == 0 {
					t.Errorf("event %d id = %s", i, m.EventID())
				}
				var raw map[string]any
				must(json.Unmarshal(m.Data, &raw))
				for k := range raw {
					if k != "subject" && k != "session" && k != "at" {
						t.Errorf("event %d carries %q", i, k)
					}
				}
			}
		})
	}
}

// Without Emit the service writes no event.
func TestServiceWithoutEmitWritesNoEvent(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := &credsvc.Service{Store: st}
			if err := s.Register(ctx, "bob", "correct horse"); err != nil {
				t.Fatal(err)
			}
			id, err := s.StartSession(ctx, "bob", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.EndSession(ctx, id); err != nil {
				t.Fatal(err)
			}
			if got := pendingSubjects(t, st); len(got) != 0 {
				t.Fatalf("outbox = %v", got)
			}
		})
	}
}
