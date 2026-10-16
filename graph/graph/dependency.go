package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/operations"
)

// Dependencies between changes (ADR 0108): a change names the changes whose work must have reached the branch it
// merges into before it merges (domain.Change.DependsOn). The merge refuses a change with a dependency not satisfied
// (checkMerge), the guards see them (domain.FacetDependencies), and the open sub-changes of a change merge in their
// dependency order (domain.DependencyOrder, ChangeDependencies).

// HeaderDependsOn is the field of a change.updated log entry recording a change of the dependencies of a change.
const HeaderDependsOn = "dependsOn"

// SetChangeDependencies replaces the changes a change depends on. The change is open; each dependency exists, is
// another change, neither one of its ancestors nor one of its sub-changes (a change already waits for its sub-changes),
// and the relation never makes a cycle (ErrInvalid). An abandoned dependency is refused (ErrConflict): it never lands.
// The change is journaled in a change.updated entry (dependsOn {from, to}); no entry when nothing changes.
//
//goap:operation change.depend doer
func (g *Graph) SetChangeDependencies(ctx context.Context, id domain.ChangeID, deps []domain.ChangeID) (c domain.Change, err error) {
	if err := g.allow(ctx, id, operations.ChangeDepend); err != nil {
		return domain.Change{}, err
	}
	var want []domain.ChangeID
	for _, d := range deps {
		if d != "" && !slices.Contains(want, d) {
			want = append(want, d)
		}
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		switch c.Status {
		case domain.ChangeDraft, domain.ChangeActive:
		default:
			return fmt.Errorf("change %s is %s: its dependencies no longer change: %w", c.ID, c.Status, ErrConflict)
		}
		if slices.Equal(c.DependsOn, want) {
			return nil
		}
		ancestors, err := ancestorIDs(ctx, tx, c)
		if err != nil {
			return err
		}
		for _, d := range want {
			if err := g.checkDependency(ctx, tx, c, ancestors, d); err != nil {
				return err
			}
		}
		if err := g.headerEntry(ctx, tx, c.ID, map[string]domain.HeaderValue{
			HeaderDependsOn: {From: idStrings(c.DependsOn), To: idStrings(want)}}); err != nil {
			return err
		}
		c.DependsOn = want
		return tx.PutChange(ctx, c)
	})
	return
}

// checkDependency checks that change c may depend on change d.
func (g *Graph) checkDependency(ctx context.Context, tx Tx, c domain.Change, ancestors []domain.ChangeID, d domain.ChangeID) error {
	if d == c.ID {
		return fmt.Errorf("a change does not depend on itself: %w", ErrInvalid)
	}
	if slices.Contains(ancestors, d) {
		return fmt.Errorf("change %s is an ancestor of %s: it merges after it anyway: %w", d, c.ID, ErrInvalid)
	}
	dc, err := tx.Change(ctx, d)
	if err != nil {
		return err
	}
	if dc.Status == domain.ChangeAbandoned {
		return fmt.Errorf("change %s is abandoned: it never lands: %w", d, ErrConflict)
	}
	up, err := ancestorIDs(ctx, tx, dc)
	if err != nil {
		return err
	}
	if slices.Contains(up, c.ID) {
		return fmt.Errorf("change %s is a sub-change of %s: a change already waits for its sub-changes: %w", d, c.ID, ErrInvalid)
	}
	// no cycle: c is not reached from d by the dependencies already stored
	seen := map[domain.ChangeID]bool{}
	todo := []domain.ChangeID{d}
	for len(todo) > 0 {
		x := todo[0]
		todo = todo[1:]
		if x == c.ID {
			return fmt.Errorf("change %s depends, directly or not, on %s: a dependency never makes a cycle: %w", d, c.ID, ErrInvalid)
		}
		if seen[x] {
			continue
		}
		seen[x] = true
		xc, err := tx.Change(ctx, x)
		if err != nil {
			return err
		}
		todo = append(todo, xc.DependsOn...)
	}
	return nil
}

// ancestorIDs are the ancestors of a change, its parent first.
func ancestorIDs(ctx context.Context, tx Tx, c domain.Change) ([]domain.ChangeID, error) {
	var out []domain.ChangeID
	for a := c.ParentID; a != "" && !slices.Contains(out, a); {
		out = append(out, a)
		ac, err := tx.Change(ctx, a)
		if err != nil {
			return nil, err
		}
		a = ac.ParentID
	}
	return out, nil
}

// dependencyOf is change d as a dependency of change c: whether its work has reached the branch c merges into. The
// work of an applied change is on the branch of its parent, that of an applied parent on its own parent's, and so on:
// it is satisfied when it reached main (every change of its chain applied) or c's parent.
func dependencyOf(ctx context.Context, tx Tx, c domain.Change, d domain.ChangeID) (domain.Dependency, error) {
	dc, err := tx.Change(ctx, d)
	if err != nil {
		return domain.Dependency{ID: d, Reason: "unknown change"}, err
	}
	out := domain.Dependency{ID: dc.ID, Title: dc.Title, Methodology: dc.Methodology, Status: dc.Status, State: dc.State, ParentID: dc.ParentID}
	switch dc.Status {
	case domain.ChangeAbandoned:
		out.Reason = "abandoned: it never lands"
		return out, nil
	case domain.ChangeApplied:
	default:
		out.Reason = "not merged yet"
		return out, nil
	}
	x := dc
	for x.Status == domain.ChangeApplied && x.ParentID != "" {
		if x.ParentID == c.ParentID {
			out.Satisfied = true // merged into the change c merges into
			return out, nil
		}
		if x, err = tx.Change(ctx, x.ParentID); err != nil {
			return out, err
		}
	}
	if x.Status == domain.ChangeApplied {
		out.Satisfied = true // landed on main
		return out, nil
	}
	out.Reason = fmt.Sprintf("merged into change %s (%s), which has not landed", x.ID, x.Title)
	return out, nil
}

// dependenciesOf are the dependencies of a change, as it sees them.
func dependenciesOf(ctx context.Context, tx Tx, c domain.Change) ([]domain.Dependency, error) {
	out := make([]domain.Dependency, 0, len(c.DependsOn))
	for _, d := range c.DependsOn {
		dep, err := dependencyOf(ctx, tx, c, d)
		if err != nil {
			return nil, err
		}
		out = append(out, dep)
	}
	return out, nil
}

// checkDependencies refuses the merge of a change whose dependencies are not all satisfied (ErrConflict).
func checkDependencies(c domain.Change, deps []domain.Dependency) error {
	var wait []string
	for _, d := range deps {
		if !d.Satisfied {
			wait = append(wait, fmt.Sprintf("%s (%s): %s", d.ID, d.Title, d.Reason))
		}
	}
	if len(wait) > 0 {
		return fmt.Errorf("change %s depends on changes whose work has not reached the branch it merges into: %s: %w",
			c.ID, strings.Join(wait, "; "), ErrConflict)
	}
	return nil
}

// ChangeDependencies reads the dependencies of a change, the open changes depending on it and the merge order of its
// open sub-changes.
func (g *Graph) ChangeDependencies(ctx context.Context, id domain.ChangeID) (out ChangeDependencies, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		if out.DependsOn, err = dependenciesOf(ctx, tx, c); err != nil {
			return err
		}
		all, err := tx.Changes(ctx)
		if err != nil {
			return err
		}
		var subs []domain.Change
		for _, x := range all {
			open := x.Status == domain.ChangeDraft || x.Status == domain.ChangeActive || x.Status == domain.ChangeCommitted
			if !open {
				continue
			}
			if slices.Contains(x.DependsOn, id) {
				dep, err := dependencyOf(ctx, tx, x, id)
				if err != nil {
					return err
				}
				dep.ID, dep.Title, dep.Methodology, dep.Status, dep.State, dep.ParentID = x.ID, x.Title, x.Methodology, x.Status, x.State, x.ParentID
				out.Dependents = append(out.Dependents, dep)
			}
			if x.ParentID == id {
				subs = append(subs, x)
			}
		}
		for _, s := range domain.DependencyOrder(subs) {
			deps, err := dependenciesOf(ctx, tx, s)
			if err != nil {
				return err
			}
			o := domain.Dependency{ID: s.ID, Title: s.Title, Methodology: s.Methodology, Status: s.Status, State: s.State, ParentID: s.ParentID, Satisfied: true}
			for _, d := range deps {
				if !d.Satisfied {
					o.Satisfied, o.Reason = false, "waits for "+string(d.ID)+" ("+d.Title+")"
					break
				}
			}
			out.Order = append(out.Order, o)
		}
		return nil
	})
	return
}

func idStrings(ids []domain.ChangeID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}
