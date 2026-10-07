package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestTags(t *testing.T) { forEachRepo(t, testTags) }

func testTags(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	commit := func(title, key, name string) landed {
		res, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: title, Baseline: must[domain.Baseline](t)(g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)).ID,
			By: "test", BaselineName: name, Edits: []NodeEdit{{Key: key, Type: "Doc", Props: map[string]any{"status": "draft"}}}})
		if err != nil {
			t.Fatal(err)
		}
		return landed{res.Change, res.Baseline}
	}
	// a name other than the title tags the state the change leaves; the title itself is no tag
	a := commit("First", "T-1", "release")
	commit("Second", "T-2", "Second")
	tags := must[[]domain.Tag](t)(g.Tags(ctx, domain.TagFilter{}))
	if len(tags) != 1 || tags[0].Name != "release" || tags[0].ChangeID != a.change || tags[0].BaselineID != a.baseline.ID {
		t.Fatalf("tags = %+v", tags)
	}
	// not unique: the same name on another change, and several names on one change
	b := commit("Third", "T-3", "")
	for _, name := range []string{"release", "stable"} {
		if _, err := g.TagChange(ctx, b.change, name, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if again := must[domain.Tag](t)(g.TagChange(ctx, b.change, "release", "me")); again.ChangeID != b.change {
		t.Fatalf("repeating a name on the same change: %+v", again)
	}
	if got := must[[]domain.Tag](t)(g.Tags(ctx, domain.TagFilter{Name: "release"})); len(got) != 2 {
		t.Fatalf("release labels two changes, got %+v", got)
	}
	if got := must[[]domain.Tag](t)(g.Tags(ctx, domain.TagFilter{Change: b.change})); len(got) != 2 {
		t.Fatalf("two names on the third change, got %+v", got)
	}
	// the state named is the one the change left
	if s := must[domain.Baseline](t)(g.StateAfter(ctx, b.change)); s.ID != b.baseline.ID {
		t.Fatalf("state after = %s, want %s", s.ID, b.baseline.ID)
	}
	// a draft has no state to name
	draft := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "Draft", BaselineID: b.baseline.ID}))
	if _, err := g.TagChange(ctx, draft.ID, "x", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("tagging a draft: %v", err)
	}
	if _, err := g.StateAfter(ctx, draft.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("state after a draft: %v", err)
	}
	if _, err := g.TagChange(ctx, b.change, "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a tag needs a name: %v", err)
	}
	// deleting a tag leaves the change
	if err := g.DeleteTag(ctx, tags[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := g.DeleteTag(ctx, tags[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: %v", err)
	}
	if _, err := g.Change(ctx, a.change); err != nil {
		t.Fatal(err)
	}
}

// landed is what a commit of the test leaves: its change and the baseline it produced.
type landed struct {
	change   domain.ChangeID
	baseline domain.Baseline
}

// A state is computed from the log of the changes; materialising one only stores it (ADR 0056).
func TestVirtualBaselines(t *testing.T) { forEachRepo(t, testVirtualBaselines) }

func testVirtualBaselines(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	g.MaterializeEvery = 3
	head := must[domain.Baseline](t)(g.BranchHead(ctx, "", domain.MainBranch))
	var written []domain.Baseline
	for i := range 7 {
		edits := []NodeEdit{{Key: fmt.Sprintf("DES-%d", i), Type: "Design"}}
		if i == 4 {
			edits = append(edits, NodeEdit{Pre: &domain.NodeRef{ID: f.test.ID, Version: head.Nodes[f.test.ID]}, Props: map[string]any{"obsolete": true}})
		}
		head = commitOn(t, g, "", head.ID, edits...)
		written = append(written, head)
	}
	// stored: a header only between two snapshots
	raw := map[domain.BaselineID]domain.Baseline{}
	if err := repo.InTx(ctx, func(tx Tx) error {
		for _, b := range written {
			r, err := tx.Baseline(ctx, b.ID)
			if err != nil {
				return err
			}
			raw[b.ID] = r
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	gaps := make([]int, len(written))
	for i, b := range written {
		gaps[i] = raw[b.ID].Gap
		if raw[b.ID].Gap > 0 && len(raw[b.ID].Nodes) != 0 {
			t.Fatalf("baseline %d is a header only, yet holds %d nodes", i, len(raw[b.ID].Nodes))
		}
	}
	virtual := 0
	for _, gap := range gaps {
		if gap > 0 {
			virtual++
		}
		if gap >= 3 {
			t.Fatalf("gaps %v: a snapshot at least every 3 baselines", gaps)
		}
	}
	if virtual == 0 {
		t.Fatalf("gaps %v: no baseline is computed", gaps)
	}
	// read: every one holds what was written, computed or stored, through a graph with no cache
	fresh := New(repo)
	for i, want := range written {
		got := must[domain.Baseline](t)(fresh.Baseline(ctx, want.ID))
		if !maps.Equal(got.Nodes, want.Nodes) || got.Gap != gaps[i] {
			t.Fatalf("baseline %d (gap %d): read %v, wrote %v", i, gaps[i], got.Nodes, want.Nodes)
		}
		if st := must[domain.Baseline](t)(fresh.StateAfter(ctx, want.ChangeID)); !maps.Equal(st.Nodes, want.Nodes) {
			t.Fatalf("state after the change of baseline %d: %v, want %v", i, st.Nodes, want.Nodes)
		}
		if nodes, _ := must2(t)(fresh.BaselineGraph(ctx, want.ID)); len(nodes) != len(want.Nodes) {
			t.Fatalf("baseline %d: graph of %d nodes, want %d", i, len(nodes), len(want.Nodes))
		}
	}
	// browsing a computed baseline by type materialises it
	for i, b := range written {
		if gaps[i] == 0 {
			continue
		}
		page := must[NodePage](t)(fresh.BaselineNodes(ctx, b.ID, NodeQuery{Type: "Design"}))
		if page.Total == 0 {
			t.Fatalf("baseline %d: no Design node listed", i)
		}
		if got := must[domain.Baseline](t)(fresh.Baseline(ctx, b.ID)); got.Gap != 0 {
			t.Fatalf("baseline %d was browsed by type, it is stored now: gap %d", i, got.Gap)
		}
		break
	}
	// a snapshot on demand
	for i, b := range written {
		if gaps[i] == 0 {
			continue
		}
		if err := fresh.Materialize(ctx, b.ID); err != nil {
			t.Fatal(err)
		}
		got := must[domain.Baseline](t)(New(repo).Baseline(ctx, b.ID))
		if got.Gap != 0 || !maps.Equal(got.Nodes, b.Nodes) {
			t.Fatalf("baseline %d after Materialize: gap %d, %v", i, got.Gap, got.Nodes)
		}
	}
}
