package graph

import (
	"context"

	"github.com/zimwip/goap/contracts/domain"
)

// What a change modifies (ADR 0102): a change impact is a modification when its draft differs from the version it was
// checked out from (or creates a node). A candidate (an impact with no draft) or a draft left as it was checked out is
// not: it cannot be accepted and is not committed, whoever works on the change.

// draftModifies reports whether a draft is a modification of the version it was checked out from.
func (g *Graph) draftModifies(ctx context.Context, tx Tx, d *domain.Draft) (bool, error) {
	if d == nil {
		return false, nil
	}
	if d.Base == nil {
		return true, nil
	}
	base, err := g.storedDraft(ctx, tx, *d.Base)
	if err != nil {
		return false, err
	}
	return domain.DraftChanged(&base, *d), nil
}

// prefetchBases reads in one batch the versions the drafts of impacts on a flow were checked out from, with their links:
// what an operation over them compares the drafts with (draftModifies).
func (g *Graph) prefetchBases(ctx context.Context, tx Tx, id domain.ChangeID, flow string, impacts []domain.ChangeImpactID) error {
	st, err := g.draftState(ctx, tx, id)
	if err != nil {
		return err
	}
	var refs []domain.NodeRef
	for _, impact := range impacts {
		if d, ok := st.draft(impact, flow); ok && d.Base != nil {
			refs = append(refs, *d.Base)
		}
	}
	return prefetchVersions(ctx, tx, refs)
}

// ImpactModifies reports whether a change impact of the main flow holds a modification of its node (ADR 0102).
func (g *Graph) ImpactModifies(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID) (ok bool, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		st, err := g.draftState(ctx, tx, id)
		if err != nil {
			return err
		}
		var d *domain.Draft
		if row, found := st.draft(impact, ""); found {
			row = row.Clone()
			d = &row
		}
		ok, err = g.draftModifies(ctx, tx, d)
		return err
	})
	return
}
