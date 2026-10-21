package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
)

// A write records the position it left the log of the change at; the change is read at that position, a read may
// require it and is refused when its view does not reach it, and the change is read as it stood at an earlier one
// (ADR 0153).
func TestChangeViewPositions(t *testing.T) { forEachRepo(t, testChangeViewPositions) }

func testChangeViewPositions(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "positions", BaselineID: f.base.ID, OwnBranch: true}))
	pre := f.req.Ref()

	wctx, w := graphapi.WithWatermark(ctx)
	mod := must[[]domain.ChangeImpact](t)(g.ProposeImpact(wctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "edit"}}))
	first := w.Of(c.ID)
	if first <= 0 {
		t.Fatalf("the write recorded no position: %v", w.All())
	}
	must[domain.ChangeImpact](t)(g.edit(wctx, c.ID, mod[0].ID, edit{Properties: map[string]any{"title": "edited"}}))
	second := w.Of(c.ID)
	if second <= first {
		t.Fatalf("the edit left the log at %d, after %d", second, first)
	}

	head := must[domain.Change](t)(g.Change(graphapi.AtLeast(ctx, c.ID, second), c.ID))
	if head.Seq != second {
		t.Fatalf("the change is read at %d, its last write left the log at %d", head.Seq, second)
	}
	if _, err := g.Change(graphapi.AtLeast(ctx, c.ID, second+1000), c.ID); !errors.Is(err, ErrBehind) {
		t.Fatalf("a read asked past the head: %v, want ErrBehind", err)
	}

	// at the first position the impact holds its draft as proposed, not the edit
	at := must[domain.Change](t)(g.ChangeAt(ctx, c.ID, first))
	if at.Seq != first || len(at.Nodes) != 1 {
		t.Fatalf("at %d: seq %d, %d impacts", first, at.Seq, len(at.Nodes))
	}
	var title any
	err := g.repo.InTx(ctx, func(tx Tx) error {
		entries, err := tx.Log(ctx, domain.LogFilter{Change: c.ID, Types: []string{domain.LogImpact + ".", domain.LogFact + "."}})
		if err != nil {
			return err
		}
		var upTo []domain.LogEntry
		for _, l := range entries {
			if l.Seq <= first {
				upTo = append(upTo, l)
			}
		}
		st := (&draftState{change: c.ID}).copy()
		if _, err := st.foldEntries(ctx, tx, upTo); err != nil {
			return err
		}
		if d, ok := st.draft(mod[0].ID, ""); ok {
			title = d.Properties["title"]
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if title == "edited" {
		t.Fatalf("the view at %d holds the edit made after it", first)
	}
	if got := must[domain.Change](t)(g.ChangeAt(ctx, c.ID, second+1000)); got.Seq != second {
		t.Fatalf("a position past the head reads the head: %d, want %d", got.Seq, second)
	}
}

// A log facet is read from the log at each call (ADR 0153): every read gives the entries written so far, nothing kept
// from an earlier one.
func TestLogFacetEntriesFollowTheLog(t *testing.T) { forEachRepo(t, testLogFacetEntriesFollowTheLog) }

func testLogFacetEntriesFollowTheLog(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	g.LogFacets = map[string]LogFacet{"notes": {Types: []string{"journal.note"}}}
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "facets", BaselineID: f.base.ID, OwnBranch: true}))
	read := func() int {
		t.Helper()
		var n int
		err := g.repo.InTx(ctx, func(tx Tx) error {
			m, err := g.logEntries(ctx, tx, c.ID)
			n = len(m["notes"])
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for i := 1; i <= 3; i++ {
		if err := g.AppendLog(ctx, []domain.LogEntry{{Change: c.ID, Type: "journal.note", Payload: []byte(`{}`)}}); err != nil {
			t.Fatal(err)
		}
		if got := read(); got != i {
			t.Fatalf("after %d notes the facet reads %d", i, got)
		}
		if got := read(); got != i {
			t.Fatalf("read again after %d notes the facet reads %d", i, got)
		}
	}
}
