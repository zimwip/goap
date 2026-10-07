package graph

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// scratchDrafts is the drafts of a change as one comparable text, from a from-scratch fold of its whole impact log.
func scratchDrafts(t *testing.T, g *Graph, id domain.ChangeID) string {
	t.Helper()
	var out string
	err := g.repo.InTx(context.Background(), func(tx Tx) error {
		events, err := impactEvents(context.Background(), tx, id)
		if err != nil {
			return err
		}
		out = norm(domain.FoldDrafts(events))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func norm(l []domain.Draft) string {
	if len(l) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(l)
	return string(b)
}

func readDrafts(t *testing.T, g *Graph, id domain.ChangeID) string {
	t.Helper()
	var out string
	err := g.repo.InTx(context.Background(), func(tx Tx) error {
		ds, err := g.drafts(context.Background(), tx, id)
		out = norm(ds)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// After any sequence of operations the drafts the graph reads (the cache extended event by event) are the ones a
// from-scratch fold of the log gives, and a read after new events folds only those (ADR 0079 §2).
func TestDraftCacheEqualsScratchFold(t *testing.T) { forEachRepo(t, testDraftCacheEqualsScratchFold) }

func testDraftCacheEqualsScratchFold(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "cache", BaselineID: f.base.ID, OwnBranch: true}))
	pre := f.req.Ref()
	mod := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "edit"}}))
	for i := 0; i < 20; i++ {
		must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, mod[0].ID, edit{Properties: map[string]any{"title": "t" + string(rune('a'+i))}}))
		if got, want := readDrafts(t, g, c.ID), scratchDrafts(t, g, c.ID); got != want {
			t.Fatalf("after %d edits the cached drafts differ from the fold\ncache: %s\nfold:  %s", i+1, got, want)
		}
	}
	// a read with nothing new folds nothing
	before := g.draftStates.folded.Load()
	readDrafts(t, g, c.ID)
	if n := g.draftStates.folded.Load() - before; n != 0 {
		t.Fatalf("a read with no new event folded %d events", n)
	}
	// one more edit: only its events are folded, not the 20 before
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, mod[0].ID, edit{Properties: map[string]any{"title": "last"}}))
	before = g.draftStates.folded.Load()
	if got, want := readDrafts(t, g, c.ID), scratchDrafts(t, g, c.ID); got != want {
		t.Fatalf("the cached drafts differ from the fold\ncache: %s\nfold:  %s", got, want)
	}
	if n := g.draftStates.folded.Load() - before; n < 1 || n > 2 {
		t.Fatalf("a read after one edit folded %d events, want the new one only", n)
	}
	// a cold graph on the same log folds everything and agrees
	if got, want := readDrafts(t, New(repo), c.ID), scratchDrafts(t, g, c.ID); got != want {
		t.Fatalf("cold read differs\ncold: %s\nfold: %s", got, want)
	}
}

// A transaction that wrote events and rolls back leaves no trace in the cache; inside it, the writes are read; another
// graph on the same store sees the events of the first.
func TestDraftCacheRollbackAndSharedLog(t *testing.T) {
	forEachRepo(t, testDraftCacheRollbackAndSharedLog)
}

func testDraftCacheRollbackAndSharedLog(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	other := New(repo) // a second graph (another process) on the same store
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "rollback", BaselineID: f.base.ID, OwnBranch: true}))
	pre := f.req.Ref()
	mod := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "edit"}}))
	co := must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, mod[0].ID, edit{Properties: map[string]any{"title": "kept"}}))
	readDrafts(t, g, c.ID) // warm the cache
	readDrafts(t, other, c.ID)

	boom := errors.New("boom")
	err := g.repo.InTx(ctx, func(tx Tx) error {
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: mod[0].ID, Op: domain.ImpactUpdated, Post: co.Post,
			Patch: map[string]any{"props": map[string]any{"title": "ghost"}}}); err != nil {
			return err
		}
		ds, err := g.drafts(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		if len(ds) != 1 || ds[0].Properties["title"] != "ghost" {
			t.Errorf("the transaction does not read its own event: %+v", ds)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("rollback: %v", err)
	}
	for name, gg := range map[string]*Graph{"writer": g, "other": other} {
		if got, want := readDrafts(t, gg, c.ID), scratchDrafts(t, g, c.ID); got != want {
			t.Fatalf("%s: a rolled-back write poisoned the cache\ncache: %s\nfold:  %s", name, got, want)
		}
	}
	// the cache never moved past the committed log
	var last int64
	_ = g.repo.InTx(ctx, func(tx Tx) error {
		es, _ := impactEvents(ctx, tx, c.ID)
		last = int64(es[len(es)-1].Seq)
		return nil
	})
	if s := g.draftStates.get(c.ID); s == nil || s.seq != last {
		t.Fatalf("cache at %v, committed log at %d", s, last)
	}

	// a committed write of the first graph reaches the second, which kept its cache
	must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, mod[0].ID, edit{Properties: map[string]any{"title": "seen"}}))
	var d domain.Draft
	_ = other.repo.InTx(ctx, func(tx Tx) error {
		ds, err := other.drafts(ctx, tx, c.ID)
		if err != nil || len(ds) != 1 {
			t.Fatalf("other graph: %+v %v", ds, err)
		}
		d = ds[0]
		return nil
	})
	if d.Properties["title"] != "seen" {
		t.Fatalf("the other graph does not see the edit: %+v", d.Properties)
	}
}

// A draft read from the cache is a copy: editing it does not change what the next read returns.
func TestDraftCacheReturnsCopies(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		f := newFixture(t, repo)
		g := f.g
		c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "copy", BaselineID: f.base.ID, OwnBranch: true}))
		pre := f.req.Ref()
		mod := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "edit"}}))
		must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, mod[0].ID, edit{Properties: map[string]any{"title": "x"}}))
		_ = g.repo.InTx(ctx, func(tx Tx) error {
			ds, _ := g.drafts(ctx, tx, c.ID)
			ds[0].Properties["title"] = "mutated"
			return nil
		})
		if got, want := readDrafts(t, g, c.ID), scratchDrafts(t, g, c.ID); got != want {
			t.Fatalf("a caller edited the cache\ncache: %s\nfold:  %s", got, want)
		}
	})
}

// BenchmarkDraftRead reads the drafts of a change with a long log: the cache folds the new events only.
func BenchmarkDraftRead(b *testing.B) {
	t := &testing.T{}
	ctx := context.Background()
	repo := NewMemory()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "bench", BaselineID: f.base.ID, OwnBranch: true}))
	pre := f.req.Ref()
	mod := must[[]domain.ChangeImpact](t)(g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "edit"}}))
	for i := 0; i < 200; i++ {
		must[domain.ChangeImpact](t)(g.edit(ctx, c.ID, mod[0].ID, edit{Properties: map[string]any{"title": "t"}}))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = g.repo.InTx(ctx, func(tx Tx) error { _, err := g.drafts(ctx, tx, c.ID); return err })
	}
}
