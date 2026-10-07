package journal_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/journal"
)

// forEachRepo runs f on the in-memory and the SQLite repository of the graph (the PostgreSQL one is exercised by the
// graph's own suite).
func forEachRepo(t *testing.T, f func(t *testing.T, g *graph.Graph, c domain.Change)) {
	run := func(t *testing.T, repo graph.Repo) {
		ctx := context.Background()
		g := graph.New(repo)
		base, err := g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)
		if err != nil {
			t.Fatal(err)
		}
		c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "c", BaselineID: base.ID})
		if err != nil {
			t.Fatal(err)
		}
		f(t, g, c)
	}
	t.Run("memory", func(t *testing.T) { run(t, graph.NewMemory()) })
	t.Run("sqlite", func(t *testing.T) {
		ctx := context.Background()
		db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := platform.MigrateSQLite(ctx, db, "graph", graph.SQLiteMigrations, "migrations_sqlite"); err != nil {
			t.Fatal(err)
		}
		run(t, graph.NewSQLite(db))
	})
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func TestJournal(t *testing.T) { forEachRepo(t, testJournal) }

func testJournal(t *testing.T, g *graph.Graph, c domain.Change) {
	ctx := context.Background()
	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	met := true
	recs := []journal.Record{
		{ID: uuid.NewString(), ChangeID: c.ID, ProcessID: "p1", Seq: 2, Kind: journal.KindAction, Action: "a", EffectsMet: &met, StartedAt: t0.Add(time.Second),
			ModelCalls: []journal.ModelCall{{Model: "fast", InputTokens: 10, OutputTokens: 5}}},
		{ID: uuid.NewString(), ChangeID: c.ID, ProcessID: "p1", Seq: 1, Kind: journal.KindTick, Plan: []string{"a", "b"}, StartedAt: t0},
		{ID: uuid.NewString(), ChangeID: c.ID, ProcessID: "p2", Seq: 1, Kind: journal.KindTick, StartedAt: t0.Add(2 * time.Second)},
	}
	if err := journal.Append(ctx, g, recs); err != nil {
		t.Fatal(err)
	}
	all := must[[]journal.Record](t)(journal.Read(ctx, g, journal.Filter{ChangeID: c.ID}))
	if len(all) != 3 || all[1].Kind != journal.KindTick || all[0].ModelCalls[0].InputTokens != 10 || !*all[0].EffectsMet {
		t.Fatalf("journal: %+v", all)
	}
	p2 := must[[]journal.Record](t)(journal.Read(ctx, g, journal.Filter{ProcessIDs: []string{"p2"}}))
	if len(p2) != 1 || p2[0].ProcessID != "p2" {
		t.Fatalf("by process: %+v", p2)
	}
	if err := journal.Append(ctx, g, []journal.Record{{ID: uuid.NewString(), ChangeID: domain.ChangeID(uuid.NewString()), ProcessID: "p", Kind: "tick"}}); !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("unknown change: %v", err)
	}
	if err := journal.Append(ctx, g, []journal.Record{{ChangeID: c.ID, ProcessID: "p"}}); !errors.Is(err, journal.ErrInvalid) {
		t.Fatalf("no kind: %v", err)
	}
	if _, err := journal.Read(ctx, g, journal.Filter{}); !errors.Is(err, journal.ErrInvalid) {
		t.Fatalf("empty filter: %v", err)
	}
}

func TestModelExchange(t *testing.T) { forEachRepo(t, testModelExchange) }

// The prompts of a record's model calls are entries of the log of the change, not part of the record.
func testModelExchange(t *testing.T, g *graph.Graph, c domain.Change) {
	ctx := context.Background()
	long := strings.Repeat("x", journal.MaxExchangeText+10)
	id := uuid.NewString()
	rec := journal.Record{ID: id, ChangeID: c.ID, ProcessID: "p1", Seq: 1, Step: 4, Kind: journal.KindAction, Action: "a", StartedAt: time.Now(),
		ModelCalls: []journal.ModelCall{
			{Model: "fast", InputTokens: 10, OutputTokens: 5, Exchange: &journal.ModelExchange{System: "sys", Response: "ok",
				Messages: []journal.ModelMessage{{Role: "user", Content: "hello"}}}},
			{Model: "fast", InputTokens: 1},
			{Model: "fast", Exchange: &journal.ModelExchange{Response: long}},
		}}
	if err := journal.Append(ctx, g, []journal.Record{rec}); err != nil {
		t.Fatal(err)
	}
	if rec.ModelCalls[0].Exchange == nil {
		t.Fatal("Append cleared the caller's exchange")
	}
	got := must[[]journal.Record](t)(journal.Read(ctx, g, journal.Filter{ChangeID: c.ID}))
	if len(got) != 1 || len(got[0].ModelCalls) != 3 || got[0].ModelCalls[0].InputTokens != 10 {
		t.Fatalf("journal: %+v", got)
	}
	for _, m := range got[0].ModelCalls {
		if m.Exchange != nil {
			t.Fatalf("the record holds a prompt: %+v", m)
		}
	}
	entries, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID, Types: []string{journal.StreamModel + "."}, Execution: id})
	if err != nil || len(entries) != 2 {
		t.Fatalf("model entries: %v %v", entries, err)
	}
	first, err := journal.DecodeExchange(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	third, err := journal.DecodeExchange(entries[1])
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Process != "p1" || entries[0].Subject != "0" || first.Step != 4 || first.System != "sys" || first.Messages[0].Content != "hello" || first.Response != "ok" || first.Truncated {
		t.Fatalf("first exchange: %+v %+v", entries[0], first)
	}
	if entries[1].Subject != "2" || third.Call != 2 || !third.Truncated || len(third.Response) > journal.MaxExchangeText {
		t.Fatalf("third exchange: %+v (%d)", third, len(third.Response))
	}
	// the journal stream stays the records
	if j, _, _ := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID, Types: []string{journal.StreamJournal + "."}}); len(j) != 1 {
		t.Fatalf("journal entries: %v", j)
	}
}
