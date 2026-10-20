package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
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

// A state is the selection of the versions landed (ADR 0143): the graph and a store read the same, whatever the cache.
func TestBaselinesAreSelections(t *testing.T) { forEachRepo(t, testBaselinesAreSelections) }

func testBaselinesAreSelections(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
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
	// a store selects the state without the graph
	if err := repo.InTx(ctx, func(tx Tx) error {
		for i, b := range written {
			r, err := tx.Baseline(ctx, b.ID)
			if err != nil {
				return err
			}
			if !maps.Equal(r.Nodes, b.Nodes) {
				t.Errorf("baseline %d read by the store: %v, want %v", i, r.Nodes, b.Nodes)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// read: every one holds what was written, through a graph with no cache
	fresh := New(repo)
	for i, want := range written {
		got := must[domain.Baseline](t)(fresh.Baseline(ctx, want.ID))
		if !maps.Equal(got.Nodes, want.Nodes) {
			t.Fatalf("baseline %d: read %v, wrote %v", i, got.Nodes, want.Nodes)
		}
		if st := must[domain.Baseline](t)(fresh.StateAfter(ctx, want.ChangeID)); !maps.Equal(st.Nodes, want.Nodes) {
			t.Fatalf("state after the change of baseline %d: %v, want %v", i, st.Nodes, want.Nodes)
		}
		if nodes, _ := must2(t)(fresh.BaselineGraph(ctx, want.ID)); len(nodes) != len(want.Nodes) {
			t.Fatalf("baseline %d: graph of %d nodes, want %d", i, len(nodes), len(want.Nodes))
		}
	}
	// browsing a baseline by type
	page := must[NodePage](t)(New(repo).BaselineNodes(ctx, written[3].ID, NodeQuery{Type: "Design"}))
	if page.Total != 4 {
		t.Fatalf("baseline 3: %d Design nodes listed, want 4", page.Total)
	}
}
