package graph

import (
	"context"
	"errors"
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
	c := must[domain.ChangeSet](t)(g.CreateChange(ctx, NewChange{Title: "c", BaselineID: f.base.ID}))
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
