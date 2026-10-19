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
