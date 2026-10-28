package graph

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/operations"
)

// Deleting a node and restoring it (ADR 0172). A change deletes a node by marking its draft deleted: the commit writes
// a tombstone, a version that keeps the last properties and holds no outgoing link, and the node leaves the state of
// the branch (the selection of ADR 0143 drops a node whose last version deletes it). The node keeps its id, its key
// and its versions: a change that creates a node of the key of a deleted one, or checks the deleted node out, restores
// it, an impact of intent modified whose pre is the tombstone.

// ImpactNodeDelete deletes a node in a change: its draft (the node checked out first when it has none) is marked
// deleted and loses its outgoing links, an updated event with the patch {"deleted": true, "removeLinks": [...]}. The
// draft is not edited any more; cancelling it takes the deletion back. A node the change creates is not deleted: its
// creation is cancelled.
//
//goap:operation impact.delete doer
func (g *Graph) ImpactNodeDelete(ctx context.Context, id domain.ChangeID, in NodeCheckout) (cn domain.ChangeImpact, err error) {
	if err := g.allow(ctx, id, operations.ImpactDelete); err != nil {
		return cn, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.resolve(ctx, tx, id, checkoutTarget(in))
		if err != nil {
			return err
		}
		if w.cn.Intent == domain.IntentCreated && w.cn.Committed == nil {
			return invalidf("%s is created by the change: cancel its creation rather than delete it", draftOf(w.cn))
		}
		if own, err := g.ownDraftOf(ctx, tx, id, w.cn.ID, w.flow); err != nil {
			return err
		} else if own != nil && own.Deleted {
			ref := own.Ref()
			cn = w.seenAs(&ref)
			return nil // deleted already
		}
		// the node is checked out first, the draft the deletion marks
		if err := g.ensureDraft(ctx, tx, w, in.Execution); err != nil {
			return err
		}
		d, err := g.working(ctx, tx, w)
		if err != nil {
			return err
		}
		patch := map[string]any{"deleted": true}
		if len(d.Links) > 0 {
			removes := make([]any, 0, len(d.Links))
			for _, l := range d.Links {
				removes = append(removes, removeLinkPatch(l))
			}
			patch["removeLinks"] = removes
		}
		ref := d.Ref()
		if err := g.emitUpdated(ctx, tx, w, ref, in.Execution, patch); err != nil {
			return err
		}
		cn = w.seenAs(&ref)
		return nil
	})
	return
}

// tombstone is the version that deleted a node in the reference commit of change c (ADR 0172): the node's last
// commit on the commit's branch up to its order, else, past the fork of the commit, on the branch it was forked from,
// when that commit deletes the node; nil otherwise. One indexed read of that node per branch of the chain, never the
// history of the namespace.
func (g *Graph) tombstone(ctx context.Context, tx Tx, c domain.Change, nid domain.NodeID) (*domain.Node, error) {
	if c.ForkCommit == "" {
		return nil, nil
	}
	b, err := headerOf(ctx, tx, c.ForkCommit)
	if err != nil {
		return nil, err
	}
	for {
		got, err := tx.BranchCommits(ctx, b.Namespace, domain.BranchOf(b.Branch), b.Seq, []domain.NodeID{nid})
		if err != nil {
			return nil, err
		}
		if cv, ok := got[nid]; ok && !cv.Revert {
			if !cv.Deleted {
				return nil, nil
			}
			n, err := tx.Node(ctx, domain.NodeRef{ID: nid, Version: cv.Version})
			if err != nil {
				return nil, fmt.Errorf("node %s: %w", nid, err)
			}
			return &n, nil
		}
		if b.Fork == "" {
			return nil, nil
		}
		if b, err = headerOf(ctx, tx, b.Fork); err != nil {
			return nil, err
		}
	}
}
