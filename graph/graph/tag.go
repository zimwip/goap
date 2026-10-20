package graph

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
)

// Tags (ADR 0143 §6) name one exact node version: what that version stands for is read from it, its outgoing links
// naming exact versions. Tags are not unique: a name may label several versions, a version may carry several names.

// TagVersion names the node version ref for the caller. The version must exist (a draft has none). The same name on
// the same version is not repeated: the tag already there is returned.
func (g *Graph) TagVersion(ctx context.Context, ref domain.NodeRef, name string) (t domain.Tag, err error) {
	if name == "" {
		return domain.Tag{}, fmt.Errorf("a tag needs a name: %w", ErrInvalid)
	}
	if ref.ID == "" || ref.Version <= 0 {
		return domain.Tag{}, fmt.Errorf("a tag names a written version, not %s: %w", ref, ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		n, err := tx.Node(ctx, ref)
		if err != nil {
			return err
		}
		have, err := tx.Tags(ctx, domain.TagFilter{Node: ref.ID, Name: name})
		if err != nil {
			return err
		}
		for _, h := range have {
			if h.Version == ref.Version {
				t = h
				return nil
			}
		}
		t = domain.Tag{ID: domain.TagID(g.newID()), Name: name, Namespace: domain.NamespaceOf(n.Namespace), Node: ref.ID, Version: ref.Version, By: g.caller(ctx), CreatedAt: g.now()}
		return tx.PutTag(ctx, t)
	})
	return t, err
}

// Tags lists the tags matching f, oldest first.
func (g *Graph) Tags(ctx context.Context, f domain.TagFilter) (out []domain.Tag, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { out, err = tx.Tags(ctx, f); return err })
	return out, err
}

// DeleteTag removes a tag; the version it named is untouched.
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
