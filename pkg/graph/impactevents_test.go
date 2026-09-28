package graph

import (
	"context"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestMigrateImpactEvents(t *testing.T) { forEachRepo(t, testMigrateImpactEvents) }

// Change impacts stored before the log (ADR 0029 §6) get one imported event each, once.
func testMigrateImpactEvents(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	n, err := g.CreateNode(ctx, NewNode{Key: "REQ-1", Type: "Requirement"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.CreateBaseline(ctx, "", "B", []domain.NodeRef{n.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, NewChange{Title: "t", BaselineID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	pre := n.Ref()
	legacy := domain.ChangeImpact{ID: "11111111-1111-1111-1111-111111111111", Key: "REQ-1", Type: "Requirement", Intent: domain.IntentModified,
		Rationale: "before the log", Pre: &pre, Review: domain.ReviewAccepted, CreatedAt: g.now(),
		Reviews: []domain.Review{{Status: domain.ReviewAccepted, By: "someone", Comment: "ok", At: g.now()}}}
	// written the way the store was written before ADR 0029: the row alone
	if err := repo.InTx(ctx, func(tx Tx) error { return tx.PutChangeImpact(ctx, c.ID, legacy) }); err != nil {
		t.Fatal(err)
	}
	for want := 1; want >= 0; want-- { // the second run finds nothing to do
		got, err := g.MigrateImpactEvents(ctx)
		if err != nil || got != want {
			t.Fatalf("migrated %d changes (%v), want %d", got, err, want)
		}
	}
	evs, err := g.ChangeEvents(ctx, c.ID)
	if err != nil || len(evs) != 1 || evs[0].Op != domain.ImpactImported || evs[0].State.Review != domain.ReviewAccepted {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	// the log goes on from there (checkImpactLogs replays it after the test)
	cn, err := g.WriteNode(ctx, c.ID, legacy.ID, NodeWrite{Properties: map[string]any{"title": "after"}})
	if err != nil || cn.Post == nil || cn.Post.Version != 2 {
		t.Fatalf("write after the migration: %+v, %v", cn, err)
	}
	if evs, _ := g.ChangeEvents(ctx, c.ID); len(evs) != 2 || evs[1].Op != domain.ImpactWritten {
		t.Fatalf("events = %+v", evs)
	}
}
