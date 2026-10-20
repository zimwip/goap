package graph

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// Several people (or a person and an agent) write the same change at once (ADR 0127): the writers of a change are
// serialized, so the projection of its impacts stays the fold of its log, the drafts read after the writes hold every
// one of them, and a check made before a write (a second checkout) still holds when it is written.
func TestConcurrentWritersOfAChange(t *testing.T) { forEachRepo(t, testConcurrentWritersOfAChange) }

func testConcurrentWritersOfAChange(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "together", BaselineID: f.base.ID}))
	const n = 8
	parallel := func(fn func(i int) error) []error {
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() { defer wg.Done(); errs[i] = fn(i) }()
		}
		wg.Wait()
		return errs
	}

	// creations of distinct nodes: every one is an impact of the change
	for i, err := range parallel(func(i int) error {
		_, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: fmt.Sprintf("REQ-C%d", i), Type: "Requirement", Rationale: "new"})
		return err
	}) {
		if err != nil {
			t.Fatalf("creation %d: %v", i, err)
		}
	}
	if got := len(must[domain.Change](t)(g.Change(ctx, c.ID)).Nodes); got != n {
		t.Fatalf("%d impacts after %d concurrent creations", got, n)
	}

	// one checkout of a node, whoever asks first; the others are told it is checked out
	var won []domain.ChangeImpact
	var mu sync.Mutex
	for _, err := range parallel(func(int) error {
		cn, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "edit"})
		if err == nil {
			mu.Lock()
			won = append(won, cn)
			mu.Unlock()
		}
		return err
	}) {
		if err != nil && !errors.Is(err, ErrConflict) {
			t.Fatalf("a concurrent checkout fails with a conflict, not %v", err)
		}
	}
	if len(won) != 1 {
		t.Fatalf("%d concurrent checkouts of one node succeeded, want 1", len(won))
	}

	// edits of distinct properties of one draft: the draft holds them all
	cn := won[0]
	for i, err := range parallel(func(i int) error {
		_, err := g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{fmt.Sprintf("p%d", i): i}})
		return err
	}) {
		if err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
	}
	view := must[domain.NodeView](t)(g.ChangeNodeView(ctx, c.ID, "", *cn.Post))
	for i := range n {
		if _, ok := view.Properties[fmt.Sprintf("p%d", i)]; !ok {
			t.Fatalf("edit p%d is lost from the draft: %v", i, view.Properties)
		}
	}
}

// Changes merged into one branch at once all commit (ADR 0127): a merge reads the head of the target under the branch's
// lock, so the later one sees the earlier one committed and merges after it, and the head moves only from the baseline a
// merge built on: no merge leaves the work of another out of the branch.
func TestConcurrentMergesIntoABranch(t *testing.T) { forEachRepo(t, testConcurrentMergesIntoABranch) }

func testConcurrentMergesIntoABranch(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	const n = 4
	ids := make([]domain.ChangeID, n)
	for i := range n {
		c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: fmt.Sprintf("add %d", i), BaselineID: f.base.ID}))
		cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: fmt.Sprintf("REQ-M%d", i), Type: "Requirement", Rationale: "new"}))
		must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, cn.ID, "bob", "fine"))
		ids[i] = c.ID
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = g.apply(ctx, ids[i], "")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("merge %d: %v", i, err)
		}
	}
	head := must[domain.Baseline](t)(g.BranchHead(ctx, "", domain.MainBranch))
	for i := range n {
		key := fmt.Sprintf("REQ-M%d", i)
		if n, err := g.NodeByKeyOn(ctx, "", domain.MainBranch, key); err != nil || head.Nodes[n.ID] != n.Version {
			t.Fatalf("%s is not on main after the concurrent merges: %v", key, err)
		}
	}
}
