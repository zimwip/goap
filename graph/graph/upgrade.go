package graph

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/operations"
)

// Versions of a methodology (ADR 0113): a change follows the version of its methodology it was created with
// (domain.Change.MethodologyVersion); a new version of the methodology serves the changes created after it. A change
// moves to another version only when someone upgrades it.

// HeaderMethodologyVersion is the field of a change.updated log entry recording an upgrade of the version of the
// methodology of a change.
const HeaderMethodologyVersion = "methodologyVersion"

// UpgradeChange moves an open change to another version of its methodology (ADR 0113): the version named, else the
// latest published one. The version must be one the registry holds published (ErrInvalid otherwise, and without a
// registry). The change keeps its state: the lifecycle of the new version must be the same lifecycle and know the
// state the change is in (ErrConflict otherwise). Its guardian chain names the new version of its own methodology. The
// upgrade is journaled in a change.updated entry (methodologyVersion {from, to}); no entry when the version is the one
// it follows already. Its sub-changes keep theirs, each is upgraded on its own; a run already started keeps the
// version it started with until it ends.
//
//goap:operation change.upgrade doer
func (g *Graph) UpgradeChange(ctx context.Context, id domain.ChangeID, version string) (domain.Change, error) {
	if err := g.allow(ctx, id, operations.ChangeUpgrade); err != nil {
		return domain.Change{}, err
	}
	c, err := g.Change(ctx, id)
	if err != nil {
		return c, err
	}
	if c.Methodology == "" {
		return c, fmt.Errorf("change %s follows no methodology: %w", id, ErrInvalid)
	}
	if g.Lifecycles == nil {
		return c, fmt.Errorf("no registry resolves the versions of methodology %s: %w", c.Methodology, ErrInvalid)
	}
	target, err := g.Lifecycles.MethodologyVersion(ctx, domain.MethodologyRef(c.Methodology, version))
	if err != nil {
		return c, err
	}
	if target == "" {
		return c, fmt.Errorf("methodology %s is unknown: %w", c.Methodology, ErrInvalid)
	}
	if target == c.MethodologyVersion {
		return c, nil
	}
	lc, err := g.Lifecycles.Lifecycle(ctx, domain.MethodologyRef(c.Methodology, target))
	if err != nil {
		return c, err
	}
	if err := sameLifecycle(c, lc, target); err != nil {
		return c, err
	}
	err = g.changeTx(ctx, id, func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		switch c.Status {
		case domain.ChangeDraft, domain.ChangeActive:
		default:
			return fmt.Errorf("change %s is %s: it no longer changes version: %w", c.ID, c.Status, ErrConflict)
		}
		if c.MethodologyVersion == target {
			return nil
		}
		fields := map[string]domain.HeaderValue{HeaderMethodologyVersion: {From: c.MethodologyVersion, To: target}}
		if chain := upgradedChain(c, target); !slices.Equal(chain, c.Guardians) {
			fields[HeaderGuardians] = domain.HeaderValue{From: c.Guardians, To: chain}
			c.Guardians = chain
		}
		if err := g.headerEntry(ctx, tx, c.ID, fields); err != nil {
			return err
		}
		c.MethodologyVersion = target
		return tx.PutChange(ctx, c)
	})
	return c, err
}

// sameLifecycle checks that the lifecycle of the target version of the methodology of a change keeps the change
// where it stands: the same lifecycle (or none for both), knowing its state.
func sameLifecycle(c domain.Change, lc *domain.Lifecycle, target string) error {
	switch {
	case lc == nil && c.Lifecycle == "":
		return nil
	case lc == nil || lc.Name != c.Lifecycle:
		name := ""
		if lc != nil {
			name = lc.Name
		}
		return fmt.Errorf("%s follows lifecycle %q, change %s follows %q: %w",
			domain.MethodologyRef(c.Methodology, target), name, c.ID, c.Lifecycle, ErrConflict)
	}
	if _, ok := lc.State(c.State); !ok && c.State != "" {
		return fmt.Errorf("the lifecycle %s of %s has no state %q, the state of change %s: %w",
			lc.Name, domain.MethodologyRef(c.Methodology, target), c.State, c.ID, ErrConflict)
	}
	return nil
}

// upgradedChain is the guardian chain of a change naming the version target of its own methodology.
func upgradedChain(c domain.Change, target string) []domain.GuardianRef {
	chain := slices.Clone(c.Guardians)
	for i := range chain {
		if chain[i].Methodology == c.Methodology {
			chain[i].Version = target
		}
	}
	return chain
}
