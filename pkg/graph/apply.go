package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/zimwip/goap/pkg/domain"
)

// Apply lands a change: the versions its accepted change impacts wrote on its branch
// (ADR 0024) are checked (properties, lifecycle, transitions) and merged into the
// branch it was forked from, giving the resulting baseline. A change without a branch
// of its own of accepted change impacts applies as an empty baseline.
func (g *Graph) Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	var result domain.Baseline
	err := g.repo.InTx(ctx, func(tx Tx) (err error) {
		if c, err := tx.Change(ctx, id); err != nil {
			return err
		} else if of := openFlows(c); len(of) > 0 {
			return fmt.Errorf("change %s has an open flow (%s): adopt or discard it first: %w", id, of[0].ID, ErrConflict)
		}
		if open, err := openSubChanges(ctx, tx, id); err != nil {
			return err
		} else if len(open) > 0 {
			return fmt.Errorf("change %s has %d open sub-change(s), apply or abandon them first: %w", id, len(open), ErrConflict)
		}
		if result, err = g.applyTx(ctx, tx, id, baselineName); err != nil {
			return err
		}
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		// A change with its own branch is merged into the branch it was forked
		// from once applied; conflicts leave it merge_pending (see MergeChange).
		own, ok, err := ownBranch(ctx, tx, c)
		if err != nil || !ok {
			return err
		}
		c, err = g.integrate(ctx, tx, c, own, nil)
		if err != nil {
			return err
		}
		if c.Status == domain.ChangeApplied {
			if result, err = tx.Baseline(ctx, c.ResultBaselineID); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

func (g *Graph) applyTx(ctx context.Context, tx Tx, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return domain.Baseline{}, err
	}
	if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned || c.Status == domain.ChangeMergePending {
		return domain.Baseline{}, fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
	}
	base, err := tx.Baseline(ctx, c.BaselineID)
	if err != nil {
		return domain.Baseline{}, err
	}
	ix, err := g.typesAt(ctx, tx, c.BaselineID)
	if err != nil {
		return domain.Baseline{}, err
	}
	target, parentBaseline := maps.Clone(base.Nodes), base.ID
	// A change on its own branch starts from the head of that branch: what its
	// sub-changes merged into it since the fork is part of the result.
	if _, isOwn, err := ownBranch(ctx, tx, c); err != nil {
		return domain.Baseline{}, err
	} else if isOwn {
		head, err := branchHead(ctx, tx, c.Namespace, c.Branch)
		if err != nil {
			return domain.Baseline{}, err
		}
		target, parentBaseline = maps.Clone(head.Nodes), head.ID
	}
	a := &applier{g: g, tx: tx, ctx: ctx, change: c, ix: ix, branch: domain.BranchOf(c.Branch), target: target}
	if err := a.prepareChangeImpacts(); err != nil {
		return domain.Baseline{}, err
	}
	if err := a.checkChangeImpacts(); err != nil {
		return domain.Baseline{}, err
	}
	if baselineName == "" {
		baselineName = c.Title
	}
	result := domain.Baseline{ID: domain.BaselineID(g.newID()), Name: baselineName, Namespace: c.Namespace, Branch: a.branch, ParentID: parentBaseline, ChangeID: c.ID, Nodes: a.target, CreatedAt: g.now()}
	if err := tx.PutBaseline(ctx, result); err != nil {
		return domain.Baseline{}, err
	}
	if err := g.advanceBranch(ctx, tx, c.Namespace, a.branch, result.ID); err != nil {
		return domain.Baseline{}, err
	}
	c.Status = domain.ChangeApplied
	c.ResultBaselineID = result.ID
	return result, tx.PutChange(ctx, c)
}

// advanceBranch moves the head of a branch (main is created on first use).
func (g *Graph) advanceBranch(ctx context.Context, tx Tx, namespace, name string, head domain.BaselineID) error {
	namespace = domain.NamespaceOf(namespace)
	b, err := tx.Branch(ctx, namespace, name)
	switch {
	case errors.Is(err, ErrNotFound) && name == domain.MainBranch:
		b = domain.Branch{Name: name, Namespace: namespace, Status: domain.BranchOpen, CreatedAt: g.now()}
	case err != nil:
		return err
	}
	b.Head = head
	return tx.PutBranch(ctx, b)
}

// nextVersion returns the next version number of a node (numbered across branches).
func nextVersion(ctx context.Context, tx Tx, id domain.NodeID) (domain.Version, error) {
	vs, err := tx.Versions(ctx, id)
	if err != nil {
		return 0, err
	}
	return domain.Version(len(vs) + 1), nil
}

// applier checks and gathers what a change lands: the versions its accepted change impacts wrote.
type applier struct {
	g      *Graph
	tx     Tx
	ctx    context.Context
	change domain.Change
	ix     *typeIndex
	branch string
	target map[domain.NodeID]domain.Version
	// cposts are the versions produced by the accepted change impacts (ADR 0024).
	cposts []cpost
}
