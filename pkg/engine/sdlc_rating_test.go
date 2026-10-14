package engine_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

// The description of a new sdlc change is rated once, by nobody's request but its creation: no run starts again on
// its own (wired like goap-dev: the graph's events and the process events reach the triggers). A rating that misses
// its effect (here, about the wrong subject) is not tried again by itself (ADR 0112 §6): the rating falls to a person.
func TestSDLCDescriptionIsRatedOnce(t *testing.T) {
	for _, key := range []string{"description", "intent"} {
		t.Run(key, func(t *testing.T) { ratedOnce(t, key) })
	}
}

func ratedOnce(t *testing.T, key string) {
	ctx := devCtx
	e, g, base := sdlcSetup(t)
	broker := engine.NewBroker()
	e.Events = broker
	tm := &engine.TriggerManager{Engine: e}
	e.Graph = engine.EventingGraph{GraphPort: g, OnEvent: func(ctx context.Context, ev domain.ChangeEvent) { tm.Handle(ctx, engine.TriggerEventOf(ev)) }}
	wctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tm.WatchProcesses(wctx, broker)
	time.Sleep(20 * time.Millisecond)

	var mu sync.Mutex
	var prompts []string
	e.Executors[methodology.KindLLM] = engine.LLMExecutor{Client: llm.ClientFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		prompts = append(prompts, r.Messages[len(r.Messages)-1].Content)
		return llm.Response{Text: `{"items":[{"kind":"assessment","data":{"key":"` + key + `","score":2,"verdict":"unclear","findings":[{"text":"no scope","severity":"major"}],"suggestion":"Allow payment in 3 installments with no fees, for card payments of the web shop","reply":"say the scope"}}]}`}, nil
	})}
	c, err := e.Graph.CreateChange(ctx, graph.NewChange{Title: "Installments", Intent: "Allow payment in 3 installments", Methodology: "sdlc", Namespace: "alm", BaselineID: base, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	settle := func() {
		for range 3 {
			time.Sleep(150 * time.Millisecond)
			e.Drain()
		}
	}
	settle()
	// the person starts the delivery on the change: nothing in it rates the description again
	p, err := e.Start(ctx, engine.StartRequest{Methodology: "sdlc", Goal: "software_delivery", ChangeID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	settle()
	mu.Lock()
	defer mu.Unlock()
	rated := 0
	for _, p := range prompts {
		if strings.Contains(p, "You review the description of a change") {
			rated++
		}
	}
	if rated != 1 {
		all, _ := e.Store.List(ctx)
		for _, p := range all {
			if p.ChangeID == c.ID {
				t.Logf("process %s %s/%s goal %s status %s runs %d", p.ID, p.Methodology, p.Agent, p.Goal, p.Status, p.Runs)
				for _, s := range p.Steps {
					t.Logf("  step %s %s", s.Action, s.Error)
				}
			}
		}
		t.Fatalf("the description is rated %d times (%d model calls)", rated, len(prompts))
	}
	if key != "description" {
		all, _ := e.Store.List(ctx)
		for _, p := range all {
			if p.ChangeID == c.ID && p.Agent == "description_rating" && (p.Pending == nil || p.Pending.Action != "rate_description_by_hand") {
				t.Fatalf("the rating falls to a person: %+v", p.Pending)
			}
		}
	}
}
