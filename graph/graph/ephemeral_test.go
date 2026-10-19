package graph

import (
	"context"
	"fmt"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

func TestEphemeralBranchStoresNoCheckpoint(t *testing.T) { forEachRepo(t, testEphemeralBranch) }

// testEphemeralBranch: the branch of a change is ephemeral, its headers are never checkpointed however many it has,
// and its state still reads right (ADR 0140).
func testEphemeralBranch(t *testing.T, repo Repo) {
	if DefaultMaterializeEvery != 0 {
		t.Skip("GOAP_MATERIALIZE_EVERY stores entries")
	}
	ctx := context.Background()
	g := New(repo)
	g.CheckpointEvery = 2
	if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: "seed", By: "t", Edits: []NodeEdit{{Key: "N-0", Type: "Need"}}}); err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, NewChange{Title: "c", OwnBranch: true, ProjectID: "PROJ-ROOT"})
	if err != nil {
		t.Fatal(err)
	}
	var heads []domain.BaselineID
	for i := range 5 {
		cn, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: fmt.Sprintf("M-%d", i), Type: "Need", Rationale: "r"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.ImpactNodeReviewBatch(ctx, c.ID, domain.ReviewBatch{By: "t", Verdicts: []domain.ImpactVerdict{{Impact: cn.ID, Status: domain.ReviewAccepted, Comment: "ok"}}}); err != nil {
			t.Fatal(err)
		}
		b, err := g.commitChange(ctx, c.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		g.WaitCheckpoints()
		heads = append(heads, b.ID)
	}
	if err := repo.InTx(ctx, func(tx Tx) error {
		own, err := tx.Branch(ctx, c.Namespace, changeBranchName(c.ID))
		if err != nil {
			return err
		}
		if !own.Ephemeral {
			t.Fatalf("the branch of change %s is not ephemeral", c.ID)
		}
		for _, id := range heads {
			h, err := tx.BaselineHeader(ctx, id)
			if err != nil {
				return err
			}
			if h.Gap == 0 {
				t.Fatalf("baseline %s of the change's branch is stored", id)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, err := g.Baseline(ctx, heads[len(heads)-1])
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Nodes) != 6 {
		t.Fatalf("the last commit holds %d nodes, want 6", len(b.Nodes))
	}
}

func TestBaselineNodesOfAChange(t *testing.T) { forEachRepo(t, testBaselineNodesOfAChange) }

// testBaselineNodesOfAChange: the nodes of a baseline scoped by a change are the nodes its impacts act on, whatever
// else the baseline holds (ADR 0140).
func testBaselineNodesOfAChange(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	var edits []NodeEdit
	for i := range 5 {
		edits = append(edits, NodeEdit{Key: fmt.Sprintf("N-%d", i), Type: "Need"})
	}
	if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: "seed", By: "t", Edits: edits}); err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, NewChange{Title: "c", OwnBranch: true, ProjectID: "PROJ-ROOT"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "M-1", Type: "Need", Rationale: "r"})
	if err != nil {
		t.Fatal(err)
	}
	n2, err := g.NodeByKey(ctx, domain.DefaultNamespace, "N-2")
	if err != nil {
		t.Fatal(err)
	}
	ref := n2.Ref()
	added, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &ref, Rationale: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: added[0].ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ImpactNodeUpdate(ctx, c.ID, added[0].ID, NodeUpdate{Properties: map[string]any{"title": "x"}}); err != nil {
		t.Fatal(err)
	}
	batch := domain.ReviewBatch{By: "t"}
	for _, id := range []domain.ChangeImpactID{created.ID, added[0].ID} {
		batch.Verdicts = append(batch.Verdicts, domain.ImpactVerdict{Impact: id, Status: domain.ReviewAccepted, Comment: "ok"})
	}
	if _, err := g.ImpactNodeReviewBatch(ctx, c.ID, batch); err != nil {
		t.Fatal(err)
	}
	b, err := g.Apply(ctx, c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	page, err := g.BaselineNodes(ctx, b.ID, NodeQuery{Change: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, n := range page.Nodes {
		keys = append(keys, fmt.Sprintf("%s@%d", n.Key, n.Version))
	}
	if fmt.Sprint(keys) != "[M-1@1 N-2@2]" || page.Total != 2 {
		t.Fatalf("nodes of the change %v (total %d), want [M-1@1 N-2@2]", keys, page.Total)
	}
}
