package registrysvc

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/outbox"
)

// The registry's events about a version ride on the graph commit that writes it, delivered by the graph's outbox
// (ADR 0136): in order, once, and none for a refused write or a write the graph holds already.
func TestMethodologyEventsRideOnTheGraphCommit(t *testing.T) {
	g := graph.New(graph.NewMemory())
	var mu sync.Mutex
	var got []string
	g.Observe(outbox.SinkFunc(func(_ context.Context, m outbox.Message) error {
		if !strings.HasPrefix(m.Subject, "goap.registry.methodology.") {
			return nil
		}
		var ev MethodologyEvent
		if err := json.Unmarshal(m.Data, &ev); err != nil {
			return err
		}
		mu.Lock()
		got = append(got, strings.TrimPrefix(m.Subject, "goap.registry.methodology.")+" "+ev.Name+"@"+ev.Version+" "+ev.Status)
		mu.Unlock()
		return nil
	}))
	direct := 0
	s := &Service{Store: NewGraphStore(g), Events: publisherFunc(func() { direct++ })}
	withALM(t, s)
	ctx := as("admin")
	m := example(t)
	if _, _, err := s.Save(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(ctx, m.Name, m.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(ctx, m.Name, m.Version); err == nil {
		t.Fatal("a published version is published once")
	}
	if err := s.Delete(ctx, m.Name, m.Version); err != nil {
		t.Fatal(err)
	}
	k := m.Name + "@" + m.Version
	want := []string{"saved " + k + " draft", "published " + k + " published", "deleted " + k + " published"}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if direct != 0 {
		t.Fatalf("the service published %d events itself", direct)
	}
}

type publisherFunc func()

func (f publisherFunc) Publish(context.Context, string, any) error { f(); return nil }
