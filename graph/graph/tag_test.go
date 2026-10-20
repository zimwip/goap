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
	commit := func(title, key, status string) (landed, domain.Node) {
		edit := NodeEdit{Key: key, Type: "Doc", Props: map[string]any{"status": status}}
		if n, err := g.NodeByKey(ctx, domain.DefaultNamespace, key); err == nil {
			ref := n.Ref()
			edit = NodeEdit{Pre: &ref, Props: edit.Props}
		}
		res, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: title, Baseline: must[domain.Baseline](t)(g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)).ID,
			By: "test", Edits: []NodeEdit{edit}})
		if err != nil {
			t.Fatal(err)
		}
		return landed{res.Change, res.Baseline}, must[domain.Node](t)(g.NodeByKey(ctx, domain.DefaultNamespace, key))
	}
	// a tag names one exact version (ADR 0143 §6)
	_, v1 := commit("First", "T-1", "draft")
	_, v2 := commit("Second", "T-1", "final")
	if v1.ID != v2.ID || v1.Version == v2.Version {
		t.Fatalf("two versions of T-1: %s, %s", v1.Ref(), v2.Ref())
	}
	rel := must[domain.Tag](t)(g.TagVersion(ctx, v1.Ref(), "release"))
	if rel.Ref() != v1.Ref() || rel.Namespace != domain.DefaultNamespace {
		t.Fatalf("tag = %+v", rel)
	}
	// not unique: the same name on another version, and several names on one version
	for _, name := range []string{"release", "stable"} {
		if _, err := g.TagVersion(ctx, v2.Ref(), name); err != nil {
			t.Fatal(err)
		}
	}
	if again := must[domain.Tag](t)(g.TagVersion(ctx, v2.Ref(), "release")); again.Ref() != v2.Ref() {
		t.Fatalf("repeating a name on the same version: %+v", again)
	}
	if got := must[[]domain.Tag](t)(g.Tags(ctx, domain.TagFilter{Name: "release"})); len(got) != 2 {
		t.Fatalf("release labels two versions, got %+v", got)
	}
	if got := must[[]domain.Tag](t)(g.Tags(ctx, domain.TagFilter{Node: v1.ID})); len(got) != 3 {
		t.Fatalf("three tags on T-1, got %+v", got)
	}
	if got := must[[]domain.Tag](t)(g.Tags(ctx, domain.TagFilter{Namespace: "elsewhere"})); len(got) != 0 {
		t.Fatalf("no tag in another namespace, got %+v", got)
	}
	// a version that does not exist, a draft and an empty name are refused
	if _, err := g.TagVersion(ctx, domain.NodeRef{ID: v1.ID, Version: v2.Version + 5}, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tagging a missing version: %v", err)
	}
	if _, err := g.TagVersion(ctx, domain.NodeRef{ID: v1.ID}, "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tagging a draft: %v", err)
	}
	if _, err := g.TagVersion(ctx, v1.Ref(), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a tag needs a name: %v", err)
	}
	// deleting a tag leaves the version
	if err := g.DeleteTag(ctx, rel.ID); err != nil {
		t.Fatal(err)
	}
	if err := g.DeleteTag(ctx, rel.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: %v", err)
	}
	if _, err := g.Node(ctx, v1.Ref()); err != nil {
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
