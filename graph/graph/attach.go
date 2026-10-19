package graph

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/events"
	"github.com/zimwip/goap/contracts/operations"
)

// Attaching and detaching a change (ADR 0106): a methodology may integrate the changes of others (`integrates`), a
// change of it becoming the parent of the changes it integrates. A change created elsewhere is attached to it later:
// it becomes a sub-change (ADR 0103), forked from the parent's branch (what it committed rebased onto the head of
// that branch) and merged into it, its guardian chain the parent's plus its own methodology. Detached, it is a root
// change again, its chain recomputed for its project.

// Fields of a change.updated log entry that record an attachment or a detachment.
const (
	HeaderParentID  = "parentId"
	HeaderGuardians = "guardians"
)

// AttachChange makes a root change a sub-change of another change, whose methodology integrates its own (ADR 0106):
// both open, in one namespace, the change acting in the parent's project or one below it, held by the parent's unit
// or one below it. Its branch is forked again from the head of the parent's branch: what it committed is rebased
// there (ErrConflict when that leaves conflicts: nothing changes then), and its guardian chain becomes the parent's
// plus its own methodology.
//
//goap:operation change.attach doer
func (g *Graph) AttachChange(ctx context.Context, id, parent domain.ChangeID) (domain.Change, error) {
	if err := g.allow(ctx, parent, operations.ChangeAttach); err != nil {
		return domain.Change{}, err
	}
	c, p, err := g.attachable(ctx, id, parent)
	if err != nil {
		return c, err
	}
	if g.Lifecycles != nil && c.Methodology != "" {
		ok, err := g.Lifecycles.Integrates(ctx, p.MethodologyRef(), c.Methodology)
		if err != nil {
			return c, err
		}
		if !ok {
			return c, fmt.Errorf("methodology %q does not integrate the changes of %q: %w", p.Methodology, c.Methodology, ErrInvalid)
		}
	}
	err = g.changeTx(ctx, id, func(tx Tx) error {
		// the change joins the parent's tree: both locks
		if err := tx.LockChange(ctx, parent); err != nil {
			return err
		}
		cur, pc, err := g.attachableTx(ctx, tx, id, parent)
		if err != nil {
			return err
		}
		if pc, err = g.parentBranch(ctx, tx, pc); err != nil {
			return err
		}
		chain := chainUnder(pc, cur.Methodology, ownRef(cur))
		if err := g.headerEntry(ctx, tx, cur.ID, map[string]domain.HeaderValue{
			HeaderParentID: {To: string(pc.ID)}, HeaderGuardians: {From: cur.Guardians, To: chain}}); err != nil {
			return err
		}
		cur.ParentID, cur.Guardians = pc.ID, chain
		if c, err = g.reparent(ctx, tx, cur, pc.Branch); err != nil {
			return err
		}
		g.raise(ctx, tx, events.ChangeAttached, c, nil)
		return nil
	})
	return c, err
}

// DetachChange gives a sub-change back as a root change (ADR 0106: deferred, rejected by the change integrating it),
// still open and not merged: its branch is forked again from the head of the branch its root ancestor merges into
// (what it committed rebased there; ErrConflict when that leaves conflicts), its guardian chain recomputed for its
// methodology and project.
//
//goap:operation change.detach doer
func (g *Graph) DetachChange(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	c, err := g.Change(ctx, id)
	if err != nil {
		return c, err
	}
	if c.ParentID == "" {
		return c, fmt.Errorf("change %s is not a sub-change: %w", id, ErrInvalid)
	}
	if err := g.allow(ctx, c.ParentID, operations.ChangeDetach); err != nil {
		return c, err
	}
	chain, err := g.guardianChain(ctx, NewChange{Methodology: c.Methodology, ProjectID: c.ProjectID}, c.MethodologyVersion)
	if err != nil {
		return c, err
	}
	err = g.changeTx(ctx, id, func(tx Tx) error {
		cur, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		if cur.ParentID == "" {
			return fmt.Errorf("change %s is not a sub-change: %w", id, ErrConflict)
		}
		if err := openToMove(cur); err != nil {
			return err
		}
		target, err := g.rootTarget(ctx, tx, cur)
		if err != nil {
			return err
		}
		if err := g.headerEntry(ctx, tx, cur.ID, map[string]domain.HeaderValue{
			HeaderParentID: {From: string(cur.ParentID), To: ""}, HeaderGuardians: {From: cur.Guardians, To: chain}}); err != nil {
			return err
		}
		left := cur.ParentID
		cur.ParentID, cur.Guardians = "", chain
		if c, err = g.reparent(ctx, tx, cur, target); err != nil {
			return err
		}
		// the event names the change it left (ADR 0106)
		g.raise(ctx, tx, events.ChangeDetached, c, func(ev *domain.ChangeEvent) { ev.Change.ParentID = left })
		return nil
	})
	return c, err
}

// attachable checks an attachment outside the transaction (the methodologies are asked between the two reads).
func (g *Graph) attachable(ctx context.Context, id, parent domain.ChangeID) (c, p domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, p, err = g.attachableTx(ctx, tx, id, parent)
		return err
	})
	return
}

func (g *Graph) attachableTx(ctx context.Context, tx Tx, id, parent domain.ChangeID) (domain.Change, domain.Change, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return c, domain.Change{}, err
	}
	p, err := tx.Change(ctx, parent)
	if err != nil {
		return c, p, err
	}
	switch {
	case c.ID == p.ID:
		return c, p, fmt.Errorf("a change is not attached to itself: %w", ErrInvalid)
	case c.ParentID != "":
		return c, p, fmt.Errorf("change %s is already a sub-change of %s: detach it first: %w", c.ID, c.ParentID, ErrConflict)
	case domain.NamespaceOf(c.Namespace) != domain.NamespaceOf(p.Namespace):
		return c, p, fmt.Errorf("a sub-change acts on the namespace of its parent (%s), change %s acts on %s: %w",
			domain.NamespaceOf(p.Namespace), c.ID, domain.NamespaceOf(c.Namespace), ErrInvalid)
	}
	if err := openToMove(c); err != nil {
		return c, p, err
	}
	switch p.Status {
	case domain.ChangeDraft, domain.ChangeActive:
	default:
		return c, p, fmt.Errorf("parent change %s is %s: %w", p.ID, p.Status, ErrConflict)
	}
	// the parent must not be under the change: an attachment never makes a cycle
	for a, seen := p.ParentID, map[domain.ChangeID]bool{}; a != "" && !seen[a]; {
		if a == c.ID {
			return c, p, fmt.Errorf("change %s is an ancestor of %s: %w", c.ID, p.ID, ErrInvalid)
		}
		seen[a] = true
		ac, err := tx.Change(ctx, a)
		if err != nil {
			return c, p, err
		}
		a = ac.ParentID
	}
	// a dependency never joins an ancestor and its sub-change (ADR 0108)
	up, err := ancestorIDs(ctx, tx, p)
	if err != nil {
		return c, p, err
	}
	for _, a := range append([]domain.ChangeID{p.ID}, up...) {
		ac, err := tx.Change(ctx, a)
		if err != nil {
			return c, p, err
		}
		if slices.Contains(c.DependsOn, a) || slices.Contains(ac.DependsOn, c.ID) {
			return c, p, fmt.Errorf("change %s and %s depend on one another: remove the dependency before attaching: %w", c.ID, a, ErrInvalid)
		}
	}
	if ok, err := g.within(ctx, tx, domain.StructureProject, c.ProjectID, p.ProjectID); err != nil {
		return c, p, err
	} else if !ok {
		return c, p, fmt.Errorf("project %s is not part of %s, the project of change %s: %w", c.ProjectID, p.ProjectID, p.ID, ErrInvalid)
	}
	if ok, err := g.within(ctx, tx, domain.StructureOrganisation, c.OwnerOrg, p.OwnerOrg); err != nil {
		return c, p, err
	} else if !ok {
		return c, p, fmt.Errorf("unit %s is not part of %s, the unit of change %s: %w", c.OwnerOrg, p.OwnerOrg, p.ID, ErrInvalid)
	}
	return c, p, nil
}

// openToMove refuses a change that is no longer open (merged, abandoned, waiting for the resolutions of a merge).
func openToMove(c domain.Change) error {
	switch c.Status {
	case domain.ChangeDraft, domain.ChangeActive:
		return nil
	}
	return fmt.Errorf("change %s is %s: only an open change is attached or detached: %w", c.ID, c.Status, ErrConflict)
}

// parentBranch gives the parent the branch of its own its sub-changes are forked from and merged into.
func (g *Graph) parentBranch(ctx context.Context, tx Tx, p domain.Change) (domain.Change, error) {
	if _, ok, err := ownBranch(ctx, tx, p); err != nil || ok {
		return p, err
	}
	p, _, err := g.ensureOwnBranch(ctx, tx, p)
	return p, err
}

// rootTarget is the branch the root ancestor of a change merges into.
func (g *Graph) rootTarget(ctx context.Context, tx Tx, c domain.Change) (string, error) {
	root := c
	for seen := map[domain.ChangeID]bool{}; root.ParentID != "" && !seen[root.ParentID]; {
		seen[root.ParentID] = true
		p, err := tx.Change(ctx, root.ParentID)
		if err != nil {
			return "", err
		}
		root = p
	}
	own, ok, err := ownBranch(ctx, tx, root)
	if err != nil {
		return "", err
	}
	if !ok {
		return domain.BranchOf(root.Branch), nil
	}
	return domain.BranchOf(own.Parent), nil
}

// reparent forks the branch of a change again from the head of the branch target, the change's header (parent,
// chain) already set: what it committed is rebased there, conflicts refusing the move; a change that committed
// nothing is forked from that head; one without a branch of its own gets one.
func (g *Graph) reparent(ctx context.Context, tx Tx, c domain.Change, target string) (domain.Change, error) {
	head, err := branchHead(ctx, tx, c.Namespace, target)
	if err != nil {
		return c, err
	}
	own, ok, err := ownBranch(ctx, tx, c)
	if err != nil {
		return c, err
	}
	if !ok {
		own = domain.Branch{Ephemeral: true, Name: changeBranchName(c.ID), Namespace: c.Namespace, Origin: domain.ChangeBranchOrigin(c.ID),
			Intent: domain.IntentDerive, Status: domain.BranchOpen, CreatedAt: g.now()}
	}
	own.Parent = target
	if !hasCommits(c) {
		own.ForkBaseline, own.Head = head.ID, head.ID
		c.BaselineID = head.ID
	}
	if err := tx.PutBranch(ctx, own); err != nil {
		return c, err
	}
	c.Branch = own.Name
	if err := tx.PutChange(ctx, c); err != nil {
		return c, err
	}
	if !hasCommits(c) {
		return c, nil
	}
	res, err := g.rebaseCommittedTx(ctx, tx, c, own)
	if err != nil {
		return c, err
	}
	if res.Changed() {
		return c, fmt.Errorf("change %s %s: settle it on its own before it moves: %w", c.ID, res.Summary(), ErrConflict)
	}
	return tx.Change(ctx, c.ID)
}

// headerEntry journals an edit of the header of a change (a change.updated log entry).
func (g *Graph) headerEntry(ctx context.Context, tx Tx, id domain.ChangeID, fields map[string]domain.HeaderValue) error {
	e, err := domain.HeaderEntry(g.newID(), id, g.caller(ctx), g.now(), domain.HeaderEdit{Fields: fields})
	if err != nil {
		return err
	}
	_, err = tx.AppendLog(ctx, e)
	return err
}

// ownRef is the reference of the change's own methodology in its chain (its version kept), nil when absent.
func ownRef(c domain.Change) *domain.GuardianRef {
	if c.MethodologyVersion != "" {
		return &domain.GuardianRef{Methodology: c.Methodology, Version: c.MethodologyVersion}
	}
	for _, g := range c.Guardians {
		if g.Methodology == c.Methodology {
			return &g
		}
	}
	return nil
}
