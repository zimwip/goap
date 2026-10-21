package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/operations"
)

// Rebasing a change (ADR 0101, 0102, 0103): what it committed on its branch is brought onto the head of the branch it was
// forked from (main, a release branch, the branch of its parent change), the nodes changed on both sides merged
// three-way (domain.MergeDrafts, rebaseCommittedTx); the fields changed on both sides are conflicts the change settles by
// editing its draft, and its merge is then a fast-forward. A change that committed nothing has nothing to rebase.

// RebaseChange brings what a change committed up to date with the head of the branch it was forked from (the branch of
// its parent change for a sub-change, ADR 0103).
//
//goap:operation change.rebase doer
func (g *Graph) RebaseChange(ctx context.Context, id domain.ChangeID) (res RebaseResult, err error) {
	if err := g.allow(ctx, id, operations.ChangeRebase); err != nil {
		return res, err
	}
	err = g.changeTx(ctx, id, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		res, err = g.rebaseTx(ctx, tx, c, false)
		return err
	})
	return
}

// rebaseTx is RebaseChange inside a transaction; merging: for the merge that follows (rebaseCommittedTx).
func (g *Graph) rebaseTx(ctx context.Context, tx Tx, c domain.Change, merging bool) (RebaseResult, error) {
	switch c.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned:
		return RebaseResult{Parent: c.ParentID}, fmt.Errorf("change %s is %s: %w", c.ID, c.Status, ErrConflict)
	}
	own, ok, err := ownBranch(ctx, tx, c)
	if err != nil || !ok || !hasCommits(c) {
		return RebaseResult{Parent: c.ParentID}, err
	}
	res, err := g.rebaseCommittedTx(ctx, tx, c, own, merging)
	res.Parent = c.ParentID
	return res, err
}

// numberLinks gives the links a merge brought (they have no id) one.
func (g *Graph) numberLinks(d *domain.Draft) {
	for i := range d.Links {
		if d.Links[i].ID == "" {
			d.Links[i].ID = domain.LinkID(g.newID())
		}
	}
}

// rebasedNode is a node a rebase merges three-way (ADR 0102): the version the change committed (ours, as a draft), the
// merge, the pre it moves to and the conflicts it leaves.
type rebasedNode struct {
	cn        domain.ChangeImpact
	ours      domain.Draft
	merged    domain.Draft
	pre       *domain.NodeRef
	conflicts []string
}

// installRebased gives each merged node a draft (ADR 0144): checked out from the version the change committed (the
// event names it, with the version it is merged with and the pre it moves to) and updated with the difference the merge
// makes (DraftDiff, with the rebase and its conflicts), never a copy of the merge; then the merge is reviewed: the
// clean ones accepted by a review of graph.merge, the conflicting ones sent back to proposed (why) for a person.
func (g *Graph) installRebased(ctx context.Context, tx Tx, c domain.Change, nodes []rebasedNode, rebased map[string]any, why string) error {
	var entries []domain.ReviewEntry
	var accepted, reopened []domain.ImpactEvent
	for _, n := range nodes {
		event := g.newID()
		base := *n.ours.Base
		mergedWith := *n.merged.MergedWith
		ref := domain.DraftRef(base.ID)
		checkout := domain.ImpactEvent{ID: event, Change: c.ID, Impact: n.cn.ID, Op: domain.ImpactCheckedOut, Execution: n.ours.Execution, Post: &ref, Pre: n.pre,
			FromBase: &domain.BaseCopy{}}
		// the draft the checkout makes: the committed version with the ids its links take in the copy
		copied := n.ours.Clone()
		copied.Change, copied.Impact, copied.Flow, copied.MergedWith = c.ID, n.cn.ID, "", &mergedWith
		for i := range copied.Links {
			copied.Links[i].ID = domain.BaseLinkID(event, copied.Links[i].ID)
		}
		checkout.Draft = &copied
		merged := n.merged
		for i := range merged.Links {
			merged.Links[i].ID = domain.LinkID(g.newID()) // a link the merge adds is its own
		}
		patch := domain.DraftDiff(copied, merged)
		if patch == nil {
			patch = map[string]any{}
		}
		patch["rebased"], patch["conflicts"] = rebased, n.conflicts
		if err := g.emit(ctx, tx, checkout, domain.ImpactEvent{Change: c.ID, Impact: n.cn.ID, Op: domain.ImpactUpdated, Execution: n.ours.Execution, Post: &ref, Patch: patch}); err != nil {
			return err
		}
		if len(n.conflicts) == 0 {
			entries = append(entries, domain.ReviewEntry{Impact: string(n.cn.ID), Comment: why, Outcome: domain.ReviewOutcomeAccept})
			accepted = append(accepted, domain.ImpactEvent{Change: c.ID, Impact: n.cn.ID, Op: domain.ImpactReviewed})
		} else if n.cn.Review != domain.ReviewProposed {
			r := domain.Review{Status: domain.ReviewProposed, By: g.caller(ctx), At: g.now(), Comment: why}
			reopened = append(reopened, domain.ImpactEvent{Change: c.ID, Impact: n.cn.ID, Op: domain.ImpactReviewed, By: r.By, Review: &r})
		}
	}
	if len(entries) > 0 {
		key, cause, err := g.putReviewRecord(ctx, tx, c.ID, "graph.merge", why, entries)
		if err != nil {
			return err
		}
		for i := range accepted {
			r := domain.Review{Status: domain.ReviewAccepted, By: "graph.merge", Comment: why, At: g.now(), ReviewID: key}
			accepted[i].By, accepted[i].Review, accepted[i].Causation = "graph.merge", &r, cause
		}
		if err := g.emit(ctx, tx, accepted...); err != nil {
			return err
		}
	}
	return g.emit(ctx, tx, reopened...)
}

// HeaderBaselineID is the field of a change.updated log entry that records the move of a rebase (ADR 0101).
const HeaderBaselineID = "baselineId"

// storedDraft is a stored version seen as a draft, with its outgoing links: what a three-way merge compares.
func (g *Graph) storedDraft(ctx context.Context, tx Tx, ref domain.NodeRef) (domain.Draft, error) {
	return g.storedVersion(ctx, tx, ref, true)
}

// storedVersion is storedDraft, its links read only when withLinks: a merge reads the links of a side only when its
// link set differs from the one the sides started from (ADR 0146).
func (g *Graph) storedVersion(ctx context.Context, tx Tx, ref domain.NodeRef, withLinks bool) (domain.Draft, error) {
	n, err := tx.Node(ctx, ref)
	if err != nil {
		return domain.Draft{}, err
	}
	b := n.Ref()
	out := domain.Draft{Node: n.ID, Key: n.Key, Type: n.Type, State: n.State, Owner: n.Owner, Properties: cloneMap(n.Properties), Base: &b}
	if !withLinks {
		return out, nil
	}
	links, err := tx.OutLinks(ctx, n.Ref())
	if err != nil {
		return domain.Draft{}, err
	}
	for _, l := range links {
		out.Links = append(out.Links, domain.DraftLink{ID: l.ID, Type: l.Type, To: l.To, Target: l.Target, Properties: cloneMap(l.Properties)})
	}
	return out, nil
}

// ImpactNodeResolve settles the conflicts a rebase left on a change impact by keeping the change's values (ADR 0082 §2):
// an updated event with {"resolved": true}. The impact is then reviewed as any.
//
//goap:operation impact.resolve doer
func (g *Graph) ImpactNodeResolve(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, execution string) (cn domain.ChangeImpact, err error) {
	if err := g.allow(ctx, id, operations.ImpactResolve); err != nil {
		return cn, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.workOn(ctx, tx, id, domain.MainFlow)
		if err != nil {
			return err
		}
		if err := g.impact(ctx, tx, w, impact); err != nil {
			return err
		}
		d, err := g.working(ctx, tx, w)
		if err != nil {
			return err
		}
		st, err := g.draftState(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(st.conflicts[impact]) == 0 {
			return fmt.Errorf("change impact %s (%s) has no conflict to settle: %w", impact, w.cn.Key, ErrConflict)
		}
		ref := d.Ref()
		if err := g.emitUpdated(ctx, tx, w, ref, execution, map[string]any{"resolved": true}); err != nil {
			return err
		}
		cn = w.seenAs(&ref)
		return nil
	})
	return
}

// checkSettled refuses to accept a change impact whose conflicts from a rebase are not settled (ADR 0082 §2, ADR 0101 §2).
func (g *Graph) checkSettled(ctx context.Context, tx Tx, c domain.Change, impact domain.ChangeImpactID) error {
	st, err := g.draftState(ctx, tx, c.ID)
	if err != nil {
		return err
	}
	if pending := st.conflicts[impact]; len(pending) > 0 {
		return fmt.Errorf("change impact %s has conflicts with the branch it was forked from to settle first (edit the fields, or keep them with ImpactNodeResolve): %s: %w",
			impact, strings.Join(pending, ", "), ErrConflict)
	}
	return nil
}

// RebaseState reads where a change stands against the branch it was forked from. A change that committed nothing, or a
// closed one, has nothing behind.
func (g *Graph) RebaseState(ctx context.Context, id domain.ChangeID) (st RebaseState, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		st.Parent = c.ParentID
		view, err := g.draftState(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		for _, cn := range c.Nodes {
			if pending := slices.Clone(view.conflicts[cn.ID]); len(pending) > 0 {
				st.Conflicts = append(st.Conflicts, ImpactConflicts{Impact: cn.ID, Key: cn.Key, Conflicts: pending})
			}
		}
		if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned {
			return nil
		}
		own, ok, err := ownBranch(ctx, tx, c)
		if err != nil || !ok || !hasCommits(c) {
			return err
		}
		return g.committedBehind(ctx, tx, c, own, &st)
	})
	return
}
