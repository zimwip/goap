package graph

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/events"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/lib/outbox"
)

// changeSink records the change events delivered by the relay, and fails while failing is set.
type changeSink struct {
	mu      sync.Mutex
	failing bool
	evs     []domain.ChangeEvent
	ids     []string
}

func (s *changeSink) Deliver(_ context.Context, m outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failing {
		return errors.New("bus down")
	}
	if !strings.HasPrefix(m.Subject, "goap.change.") {
		return nil
	}
	var ev domain.ChangeEvent
	if err := json.Unmarshal(m.Data, &ev); err != nil {
		return err
	}
	if events.ChangeSubject(string(ev.Change.ID), ev.Type) != m.Subject {
		return errors.New("subject " + m.Subject + " for event " + ev.Type)
	}
	s.evs, s.ids = append(s.evs, ev), append(s.ids, m.EventID())
	return nil
}

func (s *changeSink) types(id domain.ChangeID) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, ev := range s.evs {
		if ev.Change.ID == id {
			out = append(out, ev.Type)
		}
	}
	return out
}

// The graph emits the events of a change itself, from the operation that writes their fact, whoever calls it (ADR 0136).
func TestGraphEmitsTheEventsOfAChange(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		g := New(repo)
		g.EndUser = func(context.Context) string { return "alice" }
		sink := &changeSink{}
		g.Observe(sink)
		head, err := g.BranchHead(ctx, "", domain.MainBranch)
		if err != nil {
			t.Fatal(err)
		}
		c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "t", Intent: "i", BaselineID: head.ID, OwnBranch: true})
		if err != nil {
			t.Fatal(err)
		}
		title := "t2"
		if _, err := g.UpdateChange(ctx, c.ID, ChangePatch{Title: &title}); err != nil {
			t.Fatal(err)
		}
		if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Type: domain.TypeSignal, Target: "ping"}}); err != nil {
			t.Fatal(err)
		}
		cn, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "N-1", Type: "Item", Rationale: "r"})
		if err != nil {
			t.Fatal(err)
		}
		if err := g.acceptImpact(ctx, c.ID, cn.ID, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := g.apply(ctx, c.ID, ""); err != nil {
			t.Fatal(err)
		}
		want := []string{events.ChangeCreated, events.ChangeEdited, events.ChangeItemAdded, events.ChangeSignal, events.ChangeCommitted, events.ChangeApplied}
		if got := sink.types(c.ID); !slices.Equal(got, want) {
			t.Fatalf("events %v, want %v", got, want)
		}
		for _, ev := range sink.evs {
			if ev.EndUser != "alice" || ev.Change.Items != nil {
				t.Fatalf("event %s: end user %q, items %v", ev.Type, ev.EndUser, ev.Change.Items)
			}
			if ev.Type == events.ChangeEdited && ev.Fields["title"].To != "t2" {
				t.Fatalf("edited fields %v", ev.Fields)
			}
			if ev.Type == events.ChangeApplied && (ev.Baseline == nil || ev.Baseline.Nodes != nil) {
				t.Fatalf("applied baseline %+v", ev.Baseline)
			}
		}
		if !strings.HasPrefix(sink.ids[0], OutboxOrigin+":") || sink.ids[0] == sink.ids[1] {
			t.Fatalf("event ids %v", sink.ids)
		}

		// a write refused emits nothing; a purge emits change.purged
		d, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "d", BaselineID: head.ID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.AddItems(ctx, d.ID, []domain.ChangeItem{{Type: domain.TypeFlow}}); err == nil {
			t.Fatal("a flow item is refused")
		}
		if _, err := g.PurgeChange(ctx, d.ID); err != nil {
			t.Fatal(err)
		}
		if got := sink.types(d.ID); !slices.Equal(got, []string{events.ChangeCreated, events.ChangePurged}) {
			t.Fatalf("events of the purged change %v", got)
		}
	})
}

// An event the bus did not take stays in the outbox and is delivered, in order, by the next drain.
func TestOutboxKeepsWhatTheBusRefused(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		g := New(repo)
		sink := &changeSink{failing: true}
		relay := g.Observe(sink)
		c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "t"})
		if err != nil {
			t.Fatal("the write does not depend on the bus:", err)
		}
		title := "t2"
		if _, err := g.UpdateChange(ctx, c.ID, ChangePatch{Title: &title}); err != nil {
			t.Fatal(err)
		}
		if got := sink.types(c.ID); len(got) != 0 {
			t.Fatalf("delivered while the bus is down: %v", got)
		}
		sink.mu.Lock()
		sink.failing = false
		sink.mu.Unlock()
		if err := relay.Drain(ctx); err != nil {
			t.Fatal(err)
		}
		if got := sink.types(c.ID); !slices.Equal(got, []string{events.ChangeCreated, events.ChangeEdited}) {
			t.Fatalf("events %v", got)
		}
		if err := relay.Drain(ctx); err != nil {
			t.Fatal(err)
		}
		if got := sink.types(c.ID); len(got) != 2 {
			t.Fatalf("delivered twice: %v", got)
		}
	})
}

// carriedSink records the subjects of every message, in order.
type carriedSink struct {
	mu   sync.Mutex
	subs []string
	data []string
}

func (s *carriedSink) Deliver(_ context.Context, m outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs, s.data = append(s.subs, m.Subject), append(s.data, string(m.Data))
	return nil
}

// A writer's own events ride on the commit of its commit (ADR 0136): after change.applied, in its transaction; none
// when nothing commits (a refused commit); a subject of the graph refused.
func TestCommitCarriesTheWritersEvents(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		g := New(repo)
		sink := &carriedSink{}
		g.Observe(sink)
		ev := graphapi.CarriedEvent{Subject: "goap.registry.thing.saved", Payload: json.RawMessage(`{"name":"N-1"}`)}
		edit := []NodeEdit{{Key: "N-1", Type: "Thing"}}
		if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Namespace: "carry", Title: "First", Edits: edit, Events: []graphapi.CarriedEvent{ev}}); err != nil {
			t.Fatal(err)
		}
		i := slices.Index(sink.subs, ev.Subject)
		// PostgreSQL stores a payload as jsonb, which spaces it its own way
		if i < 1 || !strings.HasSuffix(sink.subs[i-1], ".applied") || strings.ReplaceAll(sink.data[i], " ", "") != `{"name":"N-1"}` {
			t.Fatalf("carried event not after change.applied: %d %q", i, sink.data[max(i, 0)])
		}
		n := len(sink.subs)
		if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Namespace: "carry", Title: "Again", Edits: edit, Events: []graphapi.CarriedEvent{ev}}); err == nil {
			t.Fatal("creating N-1 again is refused")
		}
		if slices.Contains(sink.subs[n:], ev.Subject) {
			t.Fatal("an event carried by a commit that committed nothing")
		}
		bad := graphapi.CarriedEvent{Subject: "goap.change.x.applied", Payload: json.RawMessage(`{}`)}
		if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Namespace: "carry", Title: "Bad", Edits: []NodeEdit{{Key: "N-2", Type: "Thing"}}, Events: []graphapi.CarriedEvent{bad}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a subject of the graph: %v", err)
		}
	})
}
