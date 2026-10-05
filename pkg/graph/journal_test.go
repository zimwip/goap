package graph

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/domain"
)

func TestJournal(t *testing.T) { forEachRepo(t, testJournal) }

func testJournal(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "c", BaselineID: f.base.ID}))
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	met := true
	recs := []domain.ExecutionRecord{
		{ID: uuid.NewString(), ChangeID: c.ID, ProcessID: "p1", Seq: 2, Kind: domain.ExecAction, Action: "a", EffectsMet: &met, StartedAt: t0.Add(time.Second),
			ModelCalls: []domain.ModelCall{{Model: "fast", InputTokens: 10, OutputTokens: 5}}},
		{ID: uuid.NewString(), ChangeID: c.ID, ProcessID: "p1", Seq: 1, Kind: domain.ExecTick, Plan: []string{"a", "b"}, StartedAt: t0},
		{ID: uuid.NewString(), ChangeID: c.ID, ProcessID: "p2", Seq: 1, Kind: domain.ExecTick, StartedAt: t0.Add(2 * time.Second)},
	}
	if err := g.Record(ctx, recs); err != nil {
		t.Fatal(err)
	}
	all := must[[]domain.ExecutionRecord](t)(g.Journal(ctx, domain.ExecutionFilter{ChangeID: c.ID}))
	if len(all) != 3 || all[1].Kind != domain.ExecTick || all[0].ModelCalls[0].InputTokens != 10 || !*all[0].EffectsMet {
		t.Fatalf("journal: %+v", all)
	}
	p2 := must[[]domain.ExecutionRecord](t)(g.Journal(ctx, domain.ExecutionFilter{ProcessIDs: []string{"p2"}}))
	if len(p2) != 1 || p2[0].ProcessID != "p2" {
		t.Fatalf("by process: %+v", p2)
	}
	if err := g.Record(ctx, []domain.ExecutionRecord{{ID: uuid.NewString(), ChangeID: domain.ChangeID(uuid.NewString()), ProcessID: "p", Kind: "tick"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown change: %v", err)
	}
	if _, err := g.Journal(ctx, domain.ExecutionFilter{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty filter: %v", err)
	}
}

func TestModelExchange(t *testing.T) { forEachRepo(t, testModelExchange) }

// The prompts of a record's model calls are entries of the log of the change, not part of the record.
func testModelExchange(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "c", BaselineID: f.base.ID}))
	long := strings.Repeat("x", domain.MaxExchangeText+10)
	id := uuid.NewString()
	rec := domain.ExecutionRecord{ID: id, ChangeID: c.ID, ProcessID: "p1", Seq: 1, Step: 4, Kind: domain.ExecAction, Action: "a", StartedAt: time.Now(),
		ModelCalls: []domain.ModelCall{
			{Model: "fast", InputTokens: 10, OutputTokens: 5, Exchange: &domain.ModelExchange{System: "sys", Response: "ok",
				Messages: []domain.ModelMessage{{Role: "user", Content: "hello"}}}},
			{Model: "fast", InputTokens: 1},
			{Model: "fast", Exchange: &domain.ModelExchange{Response: long}},
		}}
	if err := g.Record(ctx, []domain.ExecutionRecord{rec}); err != nil {
		t.Fatal(err)
	}
	if rec.ModelCalls[0].Exchange == nil {
		t.Fatal("Record cleared the caller's exchange")
	}
	got := must[[]domain.ExecutionRecord](t)(g.Journal(ctx, domain.ExecutionFilter{ChangeID: c.ID}))
	if len(got) != 1 || len(got[0].ModelCalls) != 3 || got[0].ModelCalls[0].InputTokens != 10 {
		t.Fatalf("journal: %+v", got)
	}
	for _, m := range got[0].ModelCalls {
		if m.Exchange != nil {
			t.Fatalf("the record holds a prompt: %+v", m)
		}
	}
	entries, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID, Types: []string{domain.LogModel + "."}, Execution: id})
	if err != nil || len(entries) != 2 {
		t.Fatalf("model entries: %v %v", entries, err)
	}
	var first, third domain.ModelExchange
	if err := json.Unmarshal(entries[0].Payload, &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(entries[1].Payload, &third); err != nil {
		t.Fatal(err)
	}
	if entries[0].Process != "p1" || entries[0].Subject != "0" || first.Step != 4 || first.System != "sys" || first.Messages[0].Content != "hello" || first.Response != "ok" || first.Truncated {
		t.Fatalf("first exchange: %+v %+v", entries[0], first)
	}
	if entries[1].Subject != "2" || third.Call != 2 || !third.Truncated || len(third.Response) > domain.MaxExchangeText {
		t.Fatalf("third exchange: %+v (%d)", third, len(third.Response))
	}
	// the journal stream stays the records
	if j, _, _ := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID, Types: []string{domain.LogJournal + "."}}); len(j) != 1 {
		t.Fatalf("journal entries: %v", j)
	}
}
