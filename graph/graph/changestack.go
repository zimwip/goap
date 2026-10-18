package graph

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
)

// ChangeStackQuery names what a change-stack is asked of (ADR 0133): a node version (Node, Version 0: its latest on
// main) or a baseline. Limit bounds the lineage (0: domain.DefaultStackLimit).
type ChangeStackQuery struct {
	Node     domain.NodeRef
	Baseline domain.BaselineID
	Limit    int
}

// ChangeStack returns the stack of changes producing a node version or a baseline (ADR 0133): its lineage and the
// changes it is made of, oldest first. Nothing is stored for it: it is read from what every version and baseline
// records (the change that wrote it, the merges that made it join a branch, the baselines it follows from).
func (g *Graph) ChangeStack(ctx context.Context, q ChangeStackQuery) (s domain.ChangeStack, err error) {
	if (q.Node.ID == "") == (q.Baseline == "") {
		return s, fmt.Errorf("a change-stack is of a node version or of a baseline: %w", ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if q.Baseline != "" {
			b, err := tx.Baseline(ctx, q.Baseline)
			if err != nil {
				return err
			}
			heads, err := tx.BaselineHeaders(ctx, b.Namespace)
			if err != nil {
				return err
			}
			s.Baselines, s.Truncated = domain.BaselineLineage(heads, b.ID, branchOfChange(ctx, tx), q.Limit)
		} else {
			n, err := tx.Node(ctx, q.Node)
			if err != nil {
				return err
			}
			vs, err := tx.Versions(ctx, n.ID)
			if err != nil {
				return err
			}
			joins, err := tx.VersionJoins(ctx, n.ID)
			if err != nil {
				return err
			}
			s.Versions, s.Truncated = domain.VersionLineage(vs, joins, n.Version, q.Limit)
		}
		s.Changes, err = stackChanges(ctx, tx, domain.StackChangeIDs(s.Versions, s.Baselines))
		return err
	})
	return s, err
}

// DiffStacks returns the changes that explain the difference between two baselines of a namespace (ADR 0133): those
// the stack of to holds and the one of from does not (added), and the reverse (removed).
func (g *Graph) DiffStacks(ctx context.Context, from, to domain.BaselineID) (added, removed []domain.StackChange, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		a, err := tx.Baseline(ctx, from)
		if err != nil {
			return err
		}
		heads, err := tx.BaselineHeaders(ctx, a.Namespace)
		if err != nil {
			return err
		}
		branchOf := branchOfChange(ctx, tx)
		fa, _ := domain.BaselineLineage(heads, from, branchOf, len(heads))
		fb, _ := domain.BaselineLineage(heads, to, branchOf, len(heads))
		if len(fb) == 0 {
			return fmt.Errorf("baseline %s is not of namespace %s: %w", to, a.Namespace, ErrInvalid)
		}
		ad, rm := domain.DiffStacks(domain.StackChangeIDs(nil, fa), domain.StackChangeIDs(nil, fb))
		if added, err = stackChanges(ctx, tx, ad); err != nil {
			return err
		}
		removed, err = stackChanges(ctx, tx, rm)
		return err
	})
	return added, removed, err
}

// branchOfChange names the own branch of a change ("" when it has none or the graph no longer holds it).
func branchOfChange(ctx context.Context, tx Tx) func(domain.ChangeID) string {
	seen := map[domain.ChangeID]string{}
	return func(id domain.ChangeID) string {
		b, ok := seen[id]
		if !ok {
			if c, err := tx.Change(ctx, id); err == nil {
				b = c.Branch
			}
			seen[id] = b
		}
		return b
	}
}

// stackChanges names the changes of a stack; a change the graph no longer holds (purged) keeps its id alone.
func stackChanges(ctx context.Context, tx Tx, ids []domain.ChangeID) ([]domain.StackChange, error) {
	out := make([]domain.StackChange, 0, len(ids))
	for _, id := range ids {
		sc := domain.StackChange{ID: id}
		c, err := tx.Change(ctx, id)
		switch {
		case err == nil:
			sc.Parent, sc.Title, sc.Status, sc.OwnerOrg = c.ParentID, c.Title, c.Status, c.OwnerOrg
		case !isNotFound(err):
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}
