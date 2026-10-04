package graph

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
)

// Tags (ADR 0056) name the state a change leaves. A baseline is the state after a change, computed from the history
// and possibly kept as a snapshot; naming one labels the change that produced it (and the snapshot, when there is
// one), it never makes a baseline. Tags are not unique: a name may label several changes, a change may carry several
// names.

// TagChange names the state change id leaves. The change must have landed (applied): a draft has no state yet. The
// same name on the same change is not repeated: the tag already there is returned.
func (g *Graph) TagChange(ctx context.Context, id domain.ChangeID, name, by string) (t domain.Tag, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		t, err = g.tagTx(ctx, tx, c, name, by)
		return err
	})
	return t, err
}

func (g *Graph) tagTx(ctx context.Context, tx Tx, c domain.Change, name, by string) (domain.Tag, error) {
	if name == "" {
		return domain.Tag{}, fmt.Errorf("a tag needs a name: %w", ErrInvalid)
	}
	if c.Status != domain.ChangeApplied {
		return domain.Tag{}, fmt.Errorf("change %s is %s: only the state of an applied change can be named: %w", c.ID, c.Status, ErrConflict)
	}
	have, err := tx.Tags(ctx, domain.TagFilter{Change: c.ID, Name: name})
	if err != nil {
		return domain.Tag{}, err
	}
	if len(have) > 0 {
		return have[0], nil
	}
	t := domain.Tag{ID: domain.TagID(g.newID()), Name: name, Namespace: domain.NamespaceOf(c.Namespace), ChangeID: c.ID, BaselineID: c.ResultBaselineID, By: by, CreatedAt: g.now()}
	return t, tx.PutTag(ctx, t)
}

// Tags lists the tags matching f, oldest first.
func (g *Graph) Tags(ctx context.Context, f domain.TagFilter) (out []domain.Tag, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { out, err = tx.Tags(ctx, f); return err })
	return out, err
}

// DeleteTag removes a tag; the change and its state are untouched.
func (g *Graph) DeleteTag(ctx context.Context, id domain.TagID) error {
	return g.repo.InTx(ctx, func(tx Tx) error { return tx.DeleteTag(ctx, id) })
}

// StateAfter returns the state of the namespace after change id landed: the baseline that change produced.
func (g *Graph) StateAfter(ctx context.Context, id domain.ChangeID) (b domain.Baseline, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		if c.Status != domain.ChangeApplied || c.ResultBaselineID == "" {
			return fmt.Errorf("change %s is %s: it has left no state: %w", id, c.Status, ErrNotFound)
		}
		b, err = tx.Baseline(ctx, c.ResultBaselineID)
		return err
	})
	return b, err
}
