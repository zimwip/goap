package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/journal"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

// Every model call of the engine declares who asked and which call of which step it is, for the ledger of the gateway
// (ADR 0089): the same numbering as the journal, so that a ledger row finds its exchange in the log of the change.
func TestEngineStampsItsCallsForTheLedger(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	inner := scripted(t)
	var mu sync.Mutex
	var metas []llm.CallMeta
	e.Executors[methodology.KindLLM] = LLMExecutor{Client: llm.ClientFunc(func(ctx context.Context, req llm.Request) (llm.Response, error) {
		mu.Lock()
		metas = append(metas, llm.MetaFrom(ctx))
		mu.Unlock()
		return inner.Complete(ctx, req)
	})}
	p, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Intent: "The PSP changes its API, what does this break?", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusCompleted {
		t.Fatalf("run: %v %+v", err, p)
	}
	n := 0
	for _, st := range p.Steps {
		n += len(st.LLMCalls)
	}
	if n == 0 || len(metas) != n {
		t.Fatalf("%d calls recorded in the steps, %d stamped", n, len(metas))
	}
	seen := map[[2]int]llm.CallMeta{}
	for _, m := range metas {
		if m.Source != llm.SourceEngine || m.ProcessID != p.ID || m.ChangeID != string(p.ChangeID) || m.Agent != p.Agent || m.Action == "" {
			t.Fatalf("meta %+v", m)
		}
		seen[[2]int{m.Step, m.Call}] = m
	}
	entries, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: p.ChangeID, Types: []string{journal.StreamModel + ".call"}})
	if err != nil || len(entries) == 0 {
		t.Fatalf("exchanges: %v %d", err, len(entries))
	}
	for _, st := range p.Steps {
		for i := range st.LLMCalls {
			m, ok := seen[[2]int{st.Index, i}]
			if !ok || m.Action != st.Action {
				t.Fatalf("step %d call %d (%s): no matching stamp (%+v)", st.Index, i, st.Action, m)
			}
		}
	}
	// the exchange a ledger row names is found by process, step and call
	for k := range seen {
		found := false
		for _, en := range entries {
			var ex journal.ModelExchange
			if err := json.Unmarshal(en.Payload, &ex); err == nil && en.Process == p.ID && ex.Step == k[0] && ex.Call == k[1] {
				found = true
			}
		}
		if !found {
			t.Fatalf("no exchange for step %d call %d", k[0], k[1])
		}
	}
}

// The calls of the planner are the first calls of the step they choose (the step takes them over, newStep).
func TestPlannerCallsAreNumberedInTheirStep(t *testing.T) {
	var metas []llm.CallMeta
	rec := &planRecorder{client: llm.ClientFunc(func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		metas = append(metas, llm.MetaFrom(ctx))
		return llm.Response{Text: "{}"}, nil
	})}
	ctx := llm.WithMeta(context.Background(), llm.CallMeta{Source: llm.SourceEngine, ProcessID: "p", ChangeID: "c", Step: 3, Agent: "ag"})
	for range 2 {
		if _, err := rec.complete(ctx, llm.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(metas) != 2 || metas[0].Step != 3 || metas[0].Call != 0 || metas[1].Call != 1 || metas[1].Source != llm.SourceEngine || metas[1].Agent != "ag" {
		t.Fatalf("%+v", metas)
	}
}
