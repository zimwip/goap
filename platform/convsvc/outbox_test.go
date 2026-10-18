package convsvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/platform/convsvc"
)

func pending(t *testing.T, st convsvc.Store) []outbox.Message {
	t.Helper()
	ms, err := st.(outbox.Store).Pending(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// The store writes the events with their fact, and none when the write is refused.
func TestStoreOutbox(t *testing.T) {
	ctx := context.Background()
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			ev := func(subject string) outbox.Message {
				m, err := outbox.New(ctx, subject, map[string]string{"event": subject}, now)
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			c := convsvc.Conversation{ID: "C-1", Subject: "alice", Title: "t", CreatedAt: now, UpdatedAt: now}
			if err := st.Create(ctx, c, 1, ev("created")); err != nil {
				t.Fatal(err)
			}
			// refused writes: no event
			if err := st.Create(ctx, convsvc.Conversation{ID: "C-2", Subject: "alice", CreatedAt: now, UpdatedAt: now}, 1, ev("refused")); !errors.Is(err, convsvc.ErrFull) {
				t.Fatalf("second create: %v", err)
			}
			if _, err := st.Append(ctx, convsvc.Message{ID: "M-0", ConversationID: c.ID, Role: convsvc.RoleUser, Status: convsvc.StatusDone, CreatedAt: now}, 0, ev("refused")); !errors.Is(err, convsvc.ErrFull) {
				t.Fatalf("full append: %v", err)
			}
			if _, err := st.Rename(ctx, "C-404", "x", now, ev("refused")); !errors.Is(err, convsvc.ErrNotFound) {
				t.Fatalf("rename of none: %v", err)
			}
			m, err := st.Append(ctx, convsvc.Message{ID: "M-1", ConversationID: c.ID, Role: convsvc.RoleAssistant, Status: convsvc.StatusPending, CreatedAt: now}, 10, ev("appended"))
			if err != nil {
				t.Fatal(err)
			}
			boom := errors.New("boom")
			if _, err := st.UpdateMessage(ctx, m.ID, now, func(convsvc.Message) (convsvc.Message, error) { return convsvc.Message{}, boom },
				func(string, convsvc.Message, convsvc.Message) ([]outbox.Message, error) {
					return []outbox.Message{ev("refused")}, nil
				}); !errors.Is(err, boom) {
				t.Fatalf("refused update: %v", err)
			}
			var owner string
			if _, err := st.UpdateMessage(ctx, m.ID, now, func(m convsvc.Message) (convsvc.Message, error) { m.Status = convsvc.StatusDone; return m, nil },
				func(o string, before, after convsvc.Message) ([]outbox.Message, error) {
					owner = o
					if before.Status != convsvc.StatusPending || after.Status != convsvc.StatusDone {
						t.Errorf("before %q after %q", before.Status, after.Status)
					}
					return []outbox.Message{ev("updated")}, nil
				}); err != nil {
				t.Fatal(err)
			}
			if owner != "alice" {
				t.Fatalf("owner %q", owner)
			}
			if err := st.SetSummary(ctx, convsvc.Summary{ConversationID: c.ID, Text: "s", UpTo: 2, UpdatedAt: now}, ev("summarized")); err != nil {
				t.Fatal(err)
			}
			// a summary covering fewer messages is ignored, and so is its event
			if err := st.SetSummary(ctx, convsvc.Summary{ConversationID: c.ID, Text: "s", UpTo: 1, UpdatedAt: now}, ev("refused")); err != nil {
				t.Fatal(err)
			}
			if _, err := st.Rename(ctx, c.ID, "u", now, ev("renamed")); err != nil {
				t.Fatal(err)
			}
			if err := st.Delete(ctx, c.ID, ev("deleted")); err != nil {
				t.Fatal(err)
			}
			var got []string
			ms := pending(t, st)
			for _, m := range ms {
				got = append(got, m.Subject)
			}
			want := "created appended updated summarized renamed deleted"
			if strings.Join(got, " ") != want {
				t.Fatalf("pending = %v, want %s", got, want)
			}
			seqs := []int64{}
			for _, m := range ms {
				seqs = append(seqs, m.Seq)
			}
			if err := st.(outbox.Store).MarkPublished(ctx, seqs, now); err != nil {
				t.Fatal(err)
			}
			if p := pending(t, st); len(p) != 0 {
				t.Fatalf("published still pending: %v", p)
			}
			if err := st.(outbox.Store).PurgePublished(ctx, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// recorder is a sink recording what it gets.
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

func (r *recorder) got() []outbox.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]outbox.Message(nil), r.msgs...)
}

// The service emits the events of its writes, in order, naming the owner and never the content.
func TestServiceEmits(t *testing.T) {
	ctx := context.Background()
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s := newService(st)
			rec := &recorder{}
			relay := s.Emit(rec)
			c, err := s.Create(ctx, alice, "secret title")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Append(ctx, alice, c.ID, "", convsvc.Content{Text: "private question"}); err != nil {
				t.Fatal(err)
			}
			// refused: no event
			if _, err := s.Append(ctx, bob, c.ID, "", convsvc.Content{Text: "intrusion"}); !errors.Is(err, convsvc.ErrNotFound) {
				t.Fatalf("bob appends: %v", err)
			}
			a, err := s.Append(ctx, system, c.ID, convsvc.RoleAssistant, convsvc.Content{Status: convsvc.StatusPending})
			if err != nil {
				t.Fatal(err)
			}
			done := convsvc.Content{Text: "private answer", Status: convsvc.StatusDone,
				Actions: []convsvc.Action{{"type": "start_agent", "status": "proposed"}}}
			if _, err := s.UpdateMessage(ctx, system, a.ID, done); err != nil {
				t.Fatal(err)
			}
			// the same content again: nothing changed, no event
			if _, err := s.UpdateMessage(ctx, system, a.ID, done); err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateAction(ctx, system, a.ID, 0, func(x convsvc.Action) (convsvc.Action, error) {
				x["status"] = "starting"
				return x, nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateAction(ctx, system, a.ID, 0, func(convsvc.Action) (convsvc.Action, error) {
				return nil, errors.New("decided")
			}); err == nil {
				t.Fatal("refused action update accepted")
			}
			if err := s.SetSummary(ctx, system, c.ID, "private summary", 2); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Rename(ctx, alice, c.ID, "another secret"); err != nil {
				t.Fatal(err)
			}
			if err := s.Delete(ctx, alice, c.ID); err != nil {
				t.Fatal(err)
			}
			want := []string{"created", "message_appended", "message_appended", "message_updated", "action_updated", "summarized", "renamed", "deleted"}
			deadline := time.Now().Add(5 * time.Second)
			for len(rec.got()) < len(want) && time.Now().Before(deadline) {
				_ = relay.Drain(ctx)
				time.Sleep(5 * time.Millisecond)
			}
			got := rec.got()
			if len(got) != len(want) {
				t.Fatalf("delivered %d events, want %d: %v", len(got), len(want), got)
			}
			for i, m := range got {
				if m.Subject != convsvc.Subject(c.ID, want[i]) || m.EventID() == "" || !strings.HasPrefix(m.EventID(), convsvc.OutboxOrigin+":") {
					t.Fatalf("event %d: %s (%s), want %s", i, m.Subject, m.EventID(), want[i])
				}
				for _, secret := range []string{"secret", "private", "intrusion"} {
					if strings.Contains(string(m.Data), secret) {
						t.Fatalf("event %s carries the content: %s", m.Subject, m.Data)
					}
				}
				var ev convsvc.Event
				if err := json.Unmarshal(m.Data, &ev); err != nil {
					t.Fatal(err)
				}
				if ev.Event != want[i] || ev.ConversationID != c.ID || ev.Subject != "alice" || ev.At.IsZero() {
					t.Fatalf("payload %d: %+v", i, ev)
				}
				switch ev.Event {
				case "message_updated":
					if ev.MessageID != a.ID || ev.Status != convsvc.StatusDone {
						t.Fatalf("message_updated: %+v", ev)
					}
				case "action_updated":
					if ev.MessageID != a.ID || ev.ActionIndex == nil || *ev.ActionIndex != 0 || ev.Status != "starting" {
						t.Fatalf("action_updated: %+v", ev)
					}
				}
			}
		})
	}
}

// Without Emit the service writes no event.
func TestServiceWithoutEmit(t *testing.T) {
	ctx := context.Background()
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s := newService(st)
			if s.Emit(nil) != nil {
				t.Fatal("a nil sink makes a relay")
			}
			c, err := s.Create(ctx, alice, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Append(ctx, alice, c.ID, "", convsvc.Content{Text: "hi"}); err != nil {
				t.Fatal(err)
			}
			if p := pending(t, st); len(p) != 0 {
				t.Fatalf("events written without Emit: %v", p)
			}
		})
	}
}
