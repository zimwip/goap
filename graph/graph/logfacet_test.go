package graph

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/zimwip/goap/contracts/domain"
)

func TestLogFacets(t *testing.T) { forEachRepo(t, testLogFacets) }

// A log facet is folded from the entries of its types for the blackboard of a change, and for the one its merge reads
// (the history of ADR 0109 is one).
func testLogFacets(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newFlowWorld(t, repo)
	g, c := w.g, w.change
	var folded []domain.LogEntry
	g.LogFacets = map[string]LogFacet{"seen": {Types: []string{"journal.step.ended"},
		Fold: func(_ context.Context, ch domain.Change, es []domain.LogEntry) (any, error) {
			if ch.ID != c.ID {
				t.Fatalf("folded for %s", ch.ID)
			}
			folded = es
			return len(es), nil
		}}}
	entry := func(kind string) domain.LogEntry {
		id := uuid.NewString()
		return domain.LogEntry{ID: id, Change: c.ID, Type: "journal." + kind, Execution: id, At: g.now(), Payload: []byte(`{}`)}
	}
	if err := g.AppendLog(ctx, []domain.LogEntry{entry("tick"), entry("step.ended"), entry("step.ended")}); err != nil {
		t.Fatal(err)
	}
	bb, err := g.Blackboard(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := bb.Facets["seen"].(int); n != 2 || len(folded) != 2 {
		t.Fatalf("the facet folds the entries of its types: %v %d", bb.Facets["seen"], len(folded))
	}
}
