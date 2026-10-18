package enginee2e

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
)

// The events of the processes go through the engine's outbox (ADR 0136) to the triggers: every change gets its
// reviewer, however close the runs (the rate limit is per trigger and change), and an event delivered again is handled
// once.
func TestTriggersFollowTheEngineOutbox(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, base := agentsSetup(t)
	// no rate limit to speak of: the redelivery below is told apart by its id alone
	tm := &engine.TriggerManager{Engine: e, MinInterval: time.Nanosecond}
	tm.Start(ctx)
	var mu sync.Mutex
	type delivered struct {
		id string
		ev engine.ProcessEvent
	}
	var completed []delivered
	var ids []string
	relay := e.Emit(engine.ProcessEvents(func(ctx context.Context, ev engine.ProcessEvent) {
		mu.Lock()
		ids = append(ids, outbox.EventID(ctx))
		if ev.Event == string(engine.StatusCompleted) && ev.Process.Agent == "test-designer" {
			completed = append(completed, delivered{outbox.EventID(ctx), ev})
		}
		mu.Unlock()
		tm.OnProcess(ctx, ev)
	}))
	if relay == nil {
		t.Fatal("no relay")
	}
	var designers []*engine.Process
	for range 2 {
		d, err := e.Start(ctx, engine.StartRequest{Methodology: "test-design", Agent: "test-designer", BaselineID: base, Intent: "concevoir", ProjectID: testProject})
		if err != nil {
			t.Fatal(err)
		}
		if d, _ = e.Run(ctx, d.ID); d.Status != engine.StatusCompleted {
			t.Fatalf("designer %s", d.Status)
		}
		designers = append(designers, d)
	}
	reviewers := func() map[domain.ChangeID]int {
		out := map[domain.ChangeID]int{}
		ps, _ := e.Store.List(ctx)
		for _, p := range ps {
			if p.Agent == "reviewer" {
				out[p.ChangeID]++
			}
		}
		return out
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(reviewers()) < 2 {
		e.Drain()
		time.Sleep(20 * time.Millisecond)
	}
	got := reviewers()
	if got[designers[0].ChangeID] != 1 || got[designers[1].ChangeID] != 1 {
		t.Fatalf("reviewers per change %v", got)
	}
	mu.Lock()
	if len(ids) == 0 || !strings.HasPrefix(ids[0], engine.OutboxOrigin+":") || len(completed) != 2 {
		t.Fatalf("event ids %v, completed %d", ids, len(completed))
	}
	again := completed[0]
	mu.Unlock()
	// the completion of the first designer delivered again, with its id: no second reviewer
	tm.OnProcess(platform.WithEventID(ctx, again.id), again.ev)
	e.Drain()
	if n := reviewers()[designers[0].ChangeID]; n != 1 {
		t.Fatalf("a redelivered event fired again: %d reviewers", n)
	}
	if p, _ := relay.Store.Pending(ctx, 10); len(p) != 0 {
		t.Fatalf("still pending: %d", len(p))
	}
}
