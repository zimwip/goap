package graph

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
)

// MaxDeltaNodes caps what CommitDelta reads: past it, a reader reads the later commit whole.
const MaxDeltaNodes = 5000

// CommitDelta is what going from a commit to a later one of its branch changed (graphapi.CommitDelta).
type CommitDelta = graphapi.CommitDelta

// CommitDelta reads the nodes committed on the branch of to after from, at their version in to: a view of the head
// (access, hub, configuration snapshots) follows the head this way, reading what moved rather than the namespace
// whole. Refused (ErrInvalid) when from is empty, the commits are not of one branch, to is before from or more than
// MaxDeltaNodes nodes moved: the reader then reads to whole.
func (g *Graph) CommitDelta(ctx context.Context, from, to domain.CommitID) (d CommitDelta, err error) {
	if from == "" || to == "" {
		return d, fmt.Errorf("a delta goes from a commit to another: %w", ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if d.From, err = tx.CommitHeader(ctx, from); err != nil {
			return err
		}
		if d.To, err = tx.CommitHeader(ctx, to); err != nil {
			return err
		}
		if d.From.Namespace != d.To.Namespace || domain.BranchOf(d.From.Branch) != domain.BranchOf(d.To.Branch) || d.To.Seq < d.From.Seq {
			return fmt.Errorf("commit %s does not follow %s on its branch: %w", to, from, ErrInvalid)
		}
		moved, err := tx.CommittedVersions(ctx, d.To.Namespace, domain.BranchOf(d.To.Branch), d.From.Seq, d.To.Seq, nil)
		if err != nil {
			return err
		}
		if len(moved) > MaxDeltaNodes {
			return fmt.Errorf("%d nodes moved between %s and %s, read the commit: %w", len(moved), from, to, ErrInvalid)
		}
		ids := slices.Sorted(func(yield func(domain.NodeID) bool) {
			for id := range moved {
				if !yield(id) {
					return
				}
			}
		})
		// the selection the state of to makes, so that the delta and a whole read agree
		at, err := versionsAt(ctx, tx, to, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, ok := at[id]; !ok {
				d.Removed = append(d.Removed, id)
			}
		}
		refs := refsOf(at)
		if len(refs) == 0 {
			return nil
		}
		if d.Nodes, err = tx.NodesAt(ctx, refs, ""); err != nil {
			return err
		}
		d.Links, err = tx.LinksFrom(ctx, refs)
		return err
	})
	return d, err
}
