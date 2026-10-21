package graph

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/operations"
)

// A change created with no methodology (a creation path that names none: an RPC, a tool) takes one later: every change
// is performed by a methodology (ADR 0096), and the person working in the change picks it among those of its project.

// HeaderMethodology is the field of a change.updated log entry recording the methodology a change took.
const HeaderMethodology = "methodology"

// MethodologyGate is asked before a change takes a methodology (SetChangeMethodology), outside any transaction (it
// reads the organisation): whether the methodology applies to the project of the change. Nil accepts every methodology.
type MethodologyGate func(ctx context.Context, c domain.Change, methodology string) error

// SetChangeMethodology gives an open change (draft or active) that follows no methodology the one named, as a creation
// would have: the latest published version, its main goal when the change has none, its lifecycle starting in its
// initial state, the guardian chain of its project (or under its parent). A change that already follows a methodology
// keeps it (ErrConflict: UpgradeChange moves it to another version); an unknown methodology is ErrInvalid when a
// registry resolves the versions. MethodologyGate (when set) must accept it. The choice is journaled in a
// change.updated entry (methodology, methodologyVersion, guardians {from, to}).
//
//goap:operation change.methodology doer
func (g *Graph) SetChangeMethodology(ctx context.Context, id domain.ChangeID, methodology string) (domain.Change, error) {
	if err := g.allow(ctx, id, operations.ChangeMethodology); err != nil {
		return domain.Change{}, err
	}
	if methodology == "" {
		return domain.Change{}, fmt.Errorf("a methodology is required: %w", ErrInvalid)
	}
	c, err := g.Change(ctx, id)
	if err != nil {
		return c, err
	}
	if err := adoptable(c); err != nil {
		return c, err
	}
	if g.MethodologyGate != nil {
		if err := g.MethodologyGate(ctx, c, methodology); err != nil {
			return c, err
		}
	}
	// what a creation resolves, asked before the transaction: the registry reads the graph
	var version, goal, lifecycle, initial string
	goal = c.Goal
	if g.Lifecycles != nil {
		if version, err = g.Lifecycles.MethodologyVersion(ctx, methodology); err != nil {
			return c, err
		}
		if version == "" {
			return c, fmt.Errorf("methodology %s is unknown: %w", methodology, ErrInvalid)
		}
		ref := domain.MethodologyRef(methodology, version)
		if goal == "" {
			if goal, err = g.Lifecycles.DefaultGoal(ctx, ref); err != nil {
				return c, err
			}
		}
		lc, err := g.Lifecycles.Lifecycle(ctx, ref)
		if err != nil {
			return c, err
		}
		if lc != nil {
			lifecycle, initial = lc.Name, lc.Initial
		}
	}
	guardians, err := g.guardianChain(ctx, NewChange{Methodology: methodology, ParentID: c.ParentID, ProjectID: c.ProjectID.String()}, version)
	if err != nil {
		return c, err
	}
	err = g.changeTx(ctx, id, func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		if err := adoptable(c); err != nil {
			return err
		}
		fields := map[string]domain.HeaderValue{HeaderMethodology: {From: "", To: methodology}}
		if version != "" {
			fields[HeaderMethodologyVersion] = domain.HeaderValue{From: c.MethodologyVersion, To: version}
		}
		if len(guardians) > 0 {
			fields[HeaderGuardians] = domain.HeaderValue{From: c.Guardians, To: guardians}
		}
		if err := g.headerEntry(ctx, tx, c.ID, fields); err != nil {
			return err
		}
		c.Methodology, c.MethodologyVersion, c.Goal, c.Guardians = methodology, version, goal, guardians
		if c.Lifecycle == "" {
			c.Lifecycle, c.State = lifecycle, initial
		}
		return tx.PutChange(ctx, c)
	})
	return c, err
}

// adoptable checks that a change may take a methodology: open, and following none yet.
func adoptable(c domain.Change) error {
	if c.Methodology != "" {
		return fmt.Errorf("change %s follows methodology %s already: %w", c.ID, c.Methodology, ErrConflict)
	}
	switch c.Status {
	case domain.ChangeDraft, domain.ChangeActive:
		return nil
	}
	return fmt.Errorf("change %s is %s: it no longer takes a methodology: %w", c.ID, c.Status, ErrConflict)
}
