package enginee2e

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/assess"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/llm"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
)

// The description of a new sdlc change is rated once, by nobody's request but its creation: no run starts again on
// its own (wired in process: the graph's events and the process events reach the triggers). A rating that misses
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
	// the graph emits the events of the changes, whoever writes them (ADR 0136)
	g.Observe(engine.ChangeEvents(func(ctx context.Context, ev domain.ChangeEvent) { tm.Handle(ctx, engine.TriggerEventOf(ev)) }))
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
		return llm.Response{Text: `{"items":[{"type":"assessment","data":{"key":"` + key + `","score":2,"verdict":"unclear","findings":[{"text":"no scope","severity":"major"}],"suggestion":"Allow payment in 3 installments with no fees, for card payments of the web shop","reply":"say the scope"}}]}`}, nil
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

// The description edited again while its rating is in flight (ADR 0110, supersede): the model call is interrupted,
// the rating of the text before is dropped, and the run rates the description as it is now.
func TestSDLCEditDuringRatingRestartsIt(t *testing.T) {
	ctx := devCtx
	e, g, base := sdlcSetup(t)
	broker := engine.NewBroker()
	e.Events = broker
	tm := &engine.TriggerManager{Engine: e}
	// the graph emits the events of the changes, whoever writes them (ADR 0136)
	g.Observe(engine.ChangeEvents(func(ctx context.Context, ev domain.ChangeEvent) { tm.Handle(ctx, engine.TriggerEventOf(ev)) }))
	wctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tm.WatchProcesses(wctx, broker)
	time.Sleep(20 * time.Millisecond)

	started := make(chan struct{}, 1)
	var mu sync.Mutex
	var prompts []string
	var interrupted int
	e.Executors[methodology.KindLLM] = engine.LLMExecutor{Client: llm.ClientFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
		mu.Lock()
		prompt := r.Messages[len(r.Messages)-1].Content
		prompts = append(prompts, prompt)
		first := len(prompts) == 1
		mu.Unlock()
		if first { // the first rating waits for its answer until it is interrupted
			started <- struct{}{}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				return llm.Response{}, errors.New("never interrupted")
			}
			mu.Lock()
			interrupted++
			mu.Unlock()
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{Text: `{"items":[{"type":"assessment","data":{"key":"description","score":4,"verdict":"clear","reply":"fine"}}]}`}, nil
	})}
	c, err := e.Graph.CreateChange(ctx, graph.NewChange{Title: "Installments", Intent: "Allow payment in 3 installments", Methodology: "sdlc", Namespace: "alm", BaselineID: base, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the rating did not start")
	}
	text := "Allow payment in 3 installments with no fees, for card payments of the web shop"
	if _, err := e.Graph.UpdateChange(ctx, c.ID, graph.ChangePatch{Intent: &text}); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		time.Sleep(150 * time.Millisecond)
		e.Drain()
	}
	mu.Lock()
	defer mu.Unlock()
	if interrupted != 1 || len(prompts) != 2 {
		t.Fatalf("interrupted %d, %d model calls, want 1 and 2", interrupted, len(prompts))
	}
	if !strings.Contains(prompts[1], "with no fees") {
		t.Fatalf("the new run rates the text before: %s", prompts[1])
	}
	all, _ := e.Store.List(ctx)
	for _, p := range all {
		if p.ChangeID != c.ID || p.Agent != "description_rating" {
			continue
		}
		if p.Status != engine.StatusCompleted && p.Status != engine.StatusCancelled {
			t.Fatalf("rating process %s status %s pending %+v", p.ID, p.Status, p.Pending)
		}
	}
	bb, err := g.Blackboard(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	cur := 0
	for _, it := range bb.Change.Items {
		if it.Type == "assessment" && it.Data["key"] == "description" {
			cur++
			if it.Data["rated"] != assess.FingerprintOf(bb, "description") {
				t.Fatalf("the rating is of another text: %v", it.Data["rated"])
			}
		}
	}
	if cur != 1 {
		t.Fatalf("%d ratings, want 1", cur)
	}
}
