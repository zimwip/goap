package outbox

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// A sink whose handling writes again (a trigger writing to the service it listens to) gets the new event from the
// drain at work, after the one it handles, in order.
func TestDrainDeliversWhatItsSinkWrote(t *testing.T) {
	ctx := context.Background()
	store := &Memory{}
	var got []string
	var r *Relay
	write := func(subject string) {
		m, err := New(ctx, subject, map[string]string{"s": subject}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		store.Append(m)
	}
	r = &Relay{Origin: "svc", Store: store, Sink: SinkFunc(func(ctx context.Context, m Message) error {
		got = append(got, m.Subject+"@"+m.EventID())
		if m.Subject == "a" {
			write("b")
			if err := r.Drain(ctx); !errors.Is(err, ErrBusy) {
				t.Errorf("nested drain: %v", err)
			}
		}
		return nil
	})}
	write("a")
	if err := r.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if want := []string{"a@svc:1", "b@svc:2"}; !slices.Equal(got, want) {
		t.Fatalf("delivered %v, want %v", got, want)
	}
	if p, _ := store.Pending(ctx, 10); len(p) != 0 {
		t.Fatalf("still pending: %v", p)
	}
}

// A failed delivery keeps the message and the ones after it, for the next drain.
func TestDrainStopsAtTheFirstFailure(t *testing.T) {
	ctx := context.Background()
	store := &Memory{}
	for _, s := range []string{"a", "b"} {
		m, _ := New(ctx, s, nil, time.Now())
		store.Append(m)
	}
	fail := true
	var got []string
	r := &Relay{Store: store, Sink: SinkFunc(func(_ context.Context, m Message) error {
		if fail && m.Subject == "a" {
			return errors.New("down")
		}
		got = append(got, m.Subject)
		return nil
	})}
	if err := r.Drain(ctx); err == nil {
		t.Fatal("the failure is returned")
	}
	fail = false
	if err := r.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("delivered %v", got)
	}
}
