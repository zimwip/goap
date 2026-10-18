package prefssvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/platform/prefssvc"
)

func pending(t *testing.T, st prefssvc.Store) []outbox.Message {
	t.Helper()
	ms, err := st.(outbox.Store).Pending(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// The store writes the events with the document, and none when the write is refused or there is nothing to delete.
func TestStoreOutbox(t *testing.T) {
	ctx := context.Background()
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			m, err := outbox.New(ctx, "goap.preferences.alice.updated", map[string]any{"subject": "alice"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.Update(ctx, "alice", func(prefssvc.Prefs) (prefssvc.Prefs, []outbox.Message, error) {
				return prefssvc.Prefs{"theme": "dark"}, []outbox.Message{m}, nil
			}); err != nil {
				t.Fatal(err)
			}
			if ms := pending(t, st); len(ms) != 1 || ms[0].Subject != m.Subject || string(ms[0].Data) != string(m.Data) {
				t.Fatalf("pending = %+v", ms)
			}
			refused := errors.New("refused")
			if _, err := st.Update(ctx, "alice", func(prefssvc.Prefs) (prefssvc.Prefs, []outbox.Message, error) {
				return nil, []outbox.Message{m}, refused
			}); !errors.Is(err, refused) {
				t.Fatalf("refused: %v", err)
			}
			if ms := pending(t, st); len(ms) != 1 {
				t.Fatalf("a refused write wrote %d events", len(ms)-1)
			}
			reset := m
			reset.Subject = "goap.preferences.bob.reset"
			if err := st.Delete(ctx, "bob", reset); err != nil { // bob has no document
				t.Fatal(err)
			}
			if ms := pending(t, st); len(ms) != 1 {
				t.Fatalf("deleting nothing wrote %d events", len(ms)-1)
			}
			reset.Subject = "goap.preferences.alice.reset"
			if err := st.Delete(ctx, "alice", reset); err != nil {
				t.Fatal(err)
			}
			ms := pending(t, st)
			if len(ms) != 2 || ms[1].Subject != reset.Subject || ms[1].Seq <= ms[0].Seq {
				t.Fatalf("pending = %+v", ms)
			}
			if err := st.(outbox.Store).MarkPublished(ctx, []int64{ms[0].Seq, ms[1].Seq}, time.Now()); err != nil {
				t.Fatal(err)
			}
			if ms := pending(t, st); len(ms) != 0 {
				t.Fatalf("after publication = %+v", ms)
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

func (r *recorder) wait(t *testing.T, n int) []outbox.Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		got := slices.Clone(r.msgs)
		r.mu.Unlock()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The service emits, in order, the keys changed by each write (never the values), nothing for a write changing
// nothing or refused, and reset when a document is forgotten.
func TestServiceEmits(t *testing.T) {
	ctx := context.Background()
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			s := &prefssvc.Service{Store: st}
			relay := s.Emit(rec)
			if relay == nil || (&prefssvc.Service{Store: st}).Emit(nil) != nil {
				t.Fatal("Emit")
			}
			user := "alice@example.org"
			steps := []prefssvc.Prefs{
				{"theme": "dark", "voiceEnabled": false},
				{"theme": "dark"},                               // nothing changes
				{"theme": "light", "voiceEnabled": nil, "n": 1}, // changed, removed, added
				{"n": 1.0}, // the same number
			}
			for _, p := range steps {
				if _, err := s.Set(ctx, user, p); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Set(ctx, user, prefssvc.Prefs{"f": func() {}}); !errors.Is(err, prefssvc.ErrInvalid) {
				t.Fatalf("invalid: %v", err)
			}
			if err := s.Reset(ctx, "bob"); err != nil { // nothing to forget
				t.Fatal(err)
			}
			if err := s.Reset(ctx, user); err != nil {
				t.Fatal(err)
			}
			rec.wait(t, 3)
			_ = relay.Drain(ctx) // whatever a background drain left (ErrBusy: it is still at work)
			time.Sleep(20 * time.Millisecond)
			got := rec.wait(t, 3)
			type ev struct {
				subject string
				keys    []string
			}
			want := []ev{
				{"goap.preferences.alice@example_org.updated", []string{"theme", "voiceEnabled"}},
				{"goap.preferences.alice@example_org.updated", []string{"n", "theme", "voiceEnabled"}},
				{"goap.preferences.alice@example_org.reset", nil},
			}
			if len(got) != len(want) {
				t.Fatalf("got %d events: %+v", len(got), got)
			}
			for i, m := range got {
				var e prefssvc.Event
				if err := json.Unmarshal(m.Data, &e); err != nil {
					t.Fatal(err)
				}
				if m.Subject != want[i].subject || !slices.Equal(e.Keys, want[i].keys) || e.Subject != user || e.At.IsZero() || m.Origin != prefssvc.OutboxOrigin {
					t.Fatalf("event %d = %s %+v (origin %q)", i, m.Subject, e, m.Origin)
				}
				var raw map[string]any
				_ = json.Unmarshal(m.Data, &raw)
				for k := range raw {
					if k != "subject" && k != "keys" && k != "at" {
						t.Fatalf("event %d carries %q", i, k)
					}
				}
			}
		})
	}
}

// Without Emit the service writes no event.
func TestServiceWithoutOutbox(t *testing.T) {
	ctx := context.Background()
	st := prefssvc.NewMemoryStore()
	s := &prefssvc.Service{Store: st}
	if _, err := s.Set(ctx, "alice", prefssvc.Prefs{"theme": "dark"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if ms := pending(t, st); len(ms) != 0 {
		t.Fatalf("pending = %+v", ms)
	}
}
