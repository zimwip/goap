package graph

import (
	"context"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// stateOf reads the state of a commit header that apply or CommitChange answered (they never carry it, ADR 0137):
// the stored commit, or the head of the branch of a change for a commit, which no commit holds (ADR 0140).
func (g *Graph) stateOf(t *testing.T, ctx context.Context, b domain.Commit) map[domain.NodeID]domain.Version {
	t.Helper()
	if b.ID != "" {
		return must[domain.Commit](t)(g.Commit(ctx, b.ID)).Nodes
	}
	var nodes map[domain.NodeID]domain.Version
	if err := g.repo.InTx(ctx, func(tx Tx) error {
		br, err := tx.Branch(ctx, string(b.Namespace), b.Branch)
		if err != nil {
			return err
		}
		nodes, err = branchVersions(ctx, tx, br, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return nodes
}

// withState is b with the state stateOf reads.
func (g *Graph) withState(t *testing.T, ctx context.Context, b domain.Commit) domain.Commit {
	t.Helper()
	b.Nodes = g.stateOf(t, ctx, b)
	return b
}
