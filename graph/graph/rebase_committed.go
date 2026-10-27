package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
)

// Iterating on the branch of a change and merging it (ADR 0102). Committing a root change writes its versions on its own
// branch and leaves it open: it is edited and committed again as often as needed. Merging it takes the branch into the
// one it was forked from, once every impact of it is settled, and closes it. When the target moved since the fork, the
// merge first rebases the branch onto its head: the fork moves there, the nodes changed on both sides are merged
// three-way into drafts (the target's version as their second parent), committed when the rebase met no conflict, left to
// settle otherwise; the merge is then a fast-forward. A sub-change, which has no branch of its own (ADR 0081), is merged
// into its parent the same way: settled, rebased onto its parent, asked to the merge guards, then integrated.

// hasCommits reports whether a change committed versions on its own branch it has not merged yet: an impact of it
// committed there (its own commit, or the merge of a sub-change, ADR 0103).
func hasCommits(c domain.Change) bool {
	if c.Status != domain.ChangeDraft && c.Status != domain.ChangeActive {
		return false
	}
	for _, cn := range mainImpactsOf(c) {
		if cn.Committed != nil {
			return true
		}
	}
	return false
}

// uncommitted lists the change impacts whose draft holds a modification not committed yet (ADR 0102).
func (g *Graph) uncommitted(ctx context.Context, tx Tx, c domain.Change) ([]string, error) {
	draftOf, err := g.draftsOf(ctx, tx, c.ID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, cn := range mainImpactsOf(c) {
		if ok, err := g.draftModifies(ctx, tx, draftOf(cn.ID, "")); err != nil {
			return nil, err
		} else if ok {
			out = append(out, cn.Key)
		}
	}
	return out, nil
}

// withdrawnSince lists the nodes the branch of a root change changed that no impact of it holds any more: withdrawn
// after their commit, the next commit reverts them to the versions of the fork, returned too (ADR 0102).
func (g *Graph) withdrawnSince(ctx context.Context, tx Tx, c domain.Change) ([]domain.NodeID, map[domain.NodeID]domain.Version, error) {
	own, ok, err := ownBranch(ctx, tx, c)
	if err != nil || !ok {
		return nil, nil, err
	}
	held := map[domain.NodeID]bool{}
	for _, cn := range mainImpactsOf(c) {
		held[nodeOf(cn)] = true
	}
	// only a node the branch holds a version of can differ from the fork (ADR 0137)
	changed, err := tx.BranchNodeIDs(ctx, own.Name)
	if err != nil {
		return nil, nil, err
	}
	var ids []domain.NodeID
	for _, id := range changed {
		if !held[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil, nil
	}
	fork, err := forkVersions(ctx, tx, own, ids)
	if err != nil {
		return nil, nil, err
	}
	at, err := branchVersions(ctx, tx, own, ids)
	if err != nil {
		return nil, nil, err
	}
	slices.Sort(ids)
	var out []domain.NodeID
	for _, id := range ids {
		if hv, ok := at[id]; ok && fork[id] != hv {
			out = append(out, id)
		}
	}
	return out, fork, nil
}

// unsettled lists what holds the merge of a change (ADR 0102), one rule for every impact of its main flow: a rejected
// impact is to be reworked or withdrawn; a candidate (it modifies nothing and committed nothing) to be modified or
// withdrawn; a modification to be committed (a sub-change, which does not commit: to be accepted). An impact a merge or a
// split comes with (via) is settled with the impact of its parent. Then the open flows, decision points and sub-changes,
// and the withdrawals not committed yet.
func (g *Graph) unsettled(ctx context.Context, tx Tx, c domain.Change) ([]string, error) {
	var out []string
	if of := openFlows(c); len(of) > 0 {
		out = append(out, fmt.Sprintf("open flow %s (adopt or discard it)", of[0].ID))
	}
	if pd := pendingDecisions(c, g.now(), g.DecisionPolicy); len(pd) > 0 {
		out = append(out, fmt.Sprintf("pending decision point %q", pd[0].Question))
	}
	if open, err := openSubChanges(ctx, tx, c.ID); err != nil {
		return nil, err
	} else if len(open) > 0 {
		out = append(out, fmt.Sprintf("%d open sub-change(s) (merge or abandon them)", len(open)))
	}
	draftOf, err := g.draftsOf(ctx, tx, c.ID)
	if err != nil {
		return nil, err
	}
	for _, cn := range mainImpactsOf(c) {
		if cn.Via != "" {
			continue
		}
		modifies, err := g.draftModifies(ctx, tx, draftOf(cn.ID, ""))
		if err != nil {
			return nil, err
		}
		switch {
		case cn.Review == domain.ReviewRejected:
			out = append(out, cn.Key+" is rejected (rework or withdraw it)")
		case plannedCreation(cn):
			out = append(out, cn.Key+" is planned, not created yet (create or withdraw it)")
		case !modifies && cn.Committed == nil:
			out = append(out, cn.Key+" modifies nothing (modify or withdraw it)")
		case modifies && cn.Review != domain.ReviewAccepted:
			out = append(out, cn.Key+" awaits its review")
		case modifies:
			out = append(out, cn.Key+" is not committed")
		}
	}
	if w, _, err := g.withdrawnSince(ctx, tx, c); err != nil {
		return nil, err
	} else if len(w) > 0 {
		out = append(out, fmt.Sprintf("%d withdrawn node(s) still on the branch (commit to revert them)", len(w)))
	}
	return out, nil
}

// merge merges a change into what it was forked from (ADR 0102): settled, brought up to date (a root change: its branch
// rebased onto the head of its target; a sub-change: rebased onto its parent), asked to the merge guards, then integrated
// (a fast-forward; a target moving in between falls back to the three-way merge with the resolutions given).
// closing: the close of the change (ADR 0166), which needs it settled and leaves it applied; else the change stays open.
func (g *Graph) merge(ctx context.Context, id domain.ChangeID, resolutions map[domain.NodeID]Resolution, closing bool) (c domain.Change, err error) {
	// one transaction (ADR 0140): the guardians judge what is merged; a rebase that changed the branch is kept, the merge
	// waits for the change to be looked at again
	err = g.inChangeKeeping(ctx, id, func(ctx context.Context) error {
		var rebased error
		if c, rebased, err = g.mergeIn(ctx, id, resolutions, closing); err == nil && rebased != nil {
			err = keep(rebased)
		}
		return err
	})
	return c, err
}

func (g *Graph) mergeIn(ctx context.Context, id domain.ChangeID, resolutions map[domain.NodeID]Resolution, closing bool) (c domain.Change, rebased error, err error) {
	err = g.changeTx(ctx, id, func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		// what the change committed is merged whatever it still works on; its close needs everything settled (ADR 0166)
		if closing {
			if left, err := g.unsettled(ctx, tx, c); err != nil {
				return err
			} else if len(left) > 0 {
				return fmt.Errorf("change %s cannot be closed yet: %s: %w", id, strings.Join(left, "; "), ErrConflict)
			}
		}
		if !hasCommits(c) {
			return fmt.Errorf("change %s committed nothing on its branch: %w", id, ErrNothingToCommit)
		}
		return g.checkParentDrafts(ctx, tx, c)
	})
	if err != nil {
		return c, nil, err
	}
	var res RebaseResult
	err = g.changeTx(ctx, id, func(tx Tx) error {
		// the target is locked before the rebase reads its head (ADR 0127, 0140): the merge runs in one transaction, so no
		// other merge commits between the rebase and the fast-forward, which would turn it into a three-way merge
		if own, ok, err := ownBranch(ctx, tx, c); err != nil {
			return err
		} else if ok {
			if err := tx.LockBranch(ctx, c.Namespace, domain.BranchOf(own.Parent)); err != nil {
				return err
			}
		}
		res, err = g.rebaseTx(ctx, tx, c, true)
		return err
	})
	if err != nil {
		return c, nil, err
	}
	if res.Changed() {
		return c, fmt.Errorf("change %s %s: %w", id, res.Summary(), ErrConflict), nil
	}
	err = g.changeTx(ctx, id, func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		if err := g.checkMerge(ctx, tx, c, closing); err != nil {
			return err
		}
		own, ok, err := ownBranch(ctx, tx, c)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("change %s has no branch of its own: %w", id, ErrInvalid)
		}
		// the head of the target is read under its lock: a merge commit meanwhile is seen, and this one merges after it
		// (ADR 0127)
		if err := tx.LockBranch(ctx, c.Namespace, domain.BranchOf(own.Parent)); err != nil {
			return err
		}
		if c.ParentID == "" {
			c, err = g.integrate(ctx, tx, c, own, resolutions, closing)
			return err
		}
		// a sub-change fast-forwards the branch of its parent, which takes its work as its own (ADR 0103)
		if ff, err := g.fastForward(ctx, tx, c, own, closing); err != nil {
			return err
		} else if !ff {
			return fmt.Errorf("the branch of parent change %s moved meanwhile: merge %s again: %w", c.ParentID, id, ErrConflict)
		}
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		return g.recordInParent(ctx, tx, c)
	})
	return c, nil, err
}

// checkParentDrafts refuses the merge of a sub-change while its parent holds an uncommitted modification of a node the
// sub-change changed: the parent commits or cancels it first (its draft would no longer start from its branch).
func (g *Graph) checkParentDrafts(ctx context.Context, tx Tx, c domain.Change) error {
	if c.ParentID == "" {
		return nil
	}
	parent, err := tx.Change(ctx, c.ParentID)
	if err != nil {
		return err
	}
	draftOf, err := g.draftsOf(ctx, tx, parent.ID)
	if err != nil {
		return err
	}
	mine := map[domain.NodeID]bool{}
	for _, cn := range mainImpactsOf(c) {
		mine[nodeOf(cn)] = true
	}
	var held []string
	for _, cn := range mainImpactsOf(parent) {
		if !mine[nodeOf(cn)] {
			continue
		}
		if ok, err := g.draftModifies(ctx, tx, draftOf(cn.ID, "")); err != nil {
			return err
		} else if ok {
			held = append(held, cn.Key)
		}
	}
	if len(held) > 0 {
		return fmt.Errorf("parent change %s holds uncommitted modifications of %s: it commits or cancels them first: %w", parent.ID, strings.Join(held, ", "), ErrConflict)
	}
	return nil
}

// recordInParent gives the parent of a merged sub-change an impact for each node the sub-change committed on the parent's
// branch (ADR 0103): its own impact of the node, else a new one, accepted with the sub-change's review and committed as the
// version the sub-change committed. The parent then holds the work as its own, committed.
func (g *Graph) recordInParent(ctx context.Context, tx Tx, c domain.Change) error {
	parent, err := tx.Change(ctx, c.ParentID)
	if err != nil {
		return err
	}
	// the parent's acceptances are one review, recorded before them (ADR 0105)
	type committedIn struct {
		events []domain.ImpactEvent
		id     domain.ChangeImpactID
		ref    domain.NodeRef
		review domain.Review
	}
	var todo []committedIn
	var entries []domain.ReviewEntry
	held := map[domain.NodeID]domain.ChangeImpactID{} // the parent's impact of each node, the last one
	parentImpact := map[domain.ChangeImpactID]domain.ChangeImpact{}
	for _, p := range mainImpactsOf(parent) {
		held[nodeOf(p)] = p.ID
		parentImpact[p.ID] = p
	}
	for _, cn := range mainImpactsOf(c) {
		if cn.Committed == nil {
			continue
		}
		ref := *cn.Committed
		var events []domain.ImpactEvent
		id := held[ref.ID]
		if p, ok := parentImpact[id]; ok && p.Committed != nil && *p.Committed == ref {
			continue // merged before (ADR 0166): the parent holds it already
		}
		if id == "" {
			id = domain.ChangeImpactID(g.newID())
			st := domain.ChangeImpact{ID: id, Key: cn.Key, Type: cn.Type, Intent: cn.Intent, Rationale: cn.Rationale, Review: domain.ReviewProposed,
				ProducedBy: "graph.merge", DerivedFrom: []domain.ItemID{domain.ItemID(cn.ID)}, Execution: cn.Execution, CreatedAt: g.now()}
			if cn.Pre != nil {
				p := *cn.Pre
				st.Pre = &p
				events = append(events, domain.ImpactEvent{Change: parent.ID, Impact: id, Op: domain.ImpactProposed, State: &st})
			} else {
				events = append(events, domain.ImpactEvent{Change: parent.ID, Impact: id, Op: domain.ImpactCreated, State: &st, Post: &ref})
			}
		}
		last := domain.Review{}
		for _, r := range cn.Reviews {
			if r.Flow == "" && !r.Superseded {
				last = r
			}
		}
		r := domain.Review{Status: domain.ReviewAccepted, By: last.By, At: g.now(),
			Comment: strings.TrimSpace(fmt.Sprintf("merged from sub-change %s (%s): %s", c.Title, c.ID, last.Comment))}
		todo = append(todo, committedIn{events: events, id: id, ref: ref, review: r})
		entries = append(entries, domain.ReviewEntry{Impact: string(id), Comment: r.Comment, Outcome: domain.ReviewOutcomeAccept})
	}
	if len(todo) > 0 {
		key, cause, err := g.putReviewRecord(ctx, tx, parent.ID, "graph.merge", fmt.Sprintf("merge of sub-change %s (%s)", c.Title, c.ID), entries)
		if err != nil {
			return err
		}
		// by phase, so that the verdicts of the review and the versions of the commit are one entry each (ADR 0144)
		var phases [3][]domain.ImpactEvent
		for _, t := range todo {
			r := t.review
			r.ReviewID = key
			ref := t.ref
			phases[0] = append(phases[0], t.events...)
			phases[1] = append(phases[1], domain.ImpactEvent{Change: parent.ID, Impact: t.id, Op: domain.ImpactReviewed, By: r.By, Review: &r, Causation: cause})
			phases[2] = append(phases[2], domain.ImpactEvent{Change: parent.ID, Impact: t.id, Op: domain.ImpactCommitted, Committed: &ref, Branch: domain.BranchOf(parent.Branch)})
		}
		for _, events := range phases {
			if err := g.emit(ctx, tx, events...); err != nil {
				return err
			}
		}
	}
	// the parent's branch now holds the sub-change's commits, committed impacts of the parent: it has committed work to
	// merge (hasCommits)
	return nil
}

// rebaseCommittedTx rebases a change that committed on its own branch onto what that branch is forked from now (the
// head of its target branch; for a sub-change, the state of its parent's branch, ADR 0103): the fork of the branch and
// the change's reference baseline move there, and, for each node changed on both sides since the fork, a draft merges
// the two (base: the version at the fork; ours: the version the change committed; theirs: the version on the target),
// its Base the committed version and MergedWith the target's. The branch of a change holds no baseline (ADR 0140): the
// move is its fork's, journaled, and what the change committed stays on it. A rebase with no conflict keeps the reviews
// and commits the merged drafts; a conflict sends its impact back to proposed, named as ADR 0101 does, to be settled,
// accepted and committed. merging: for the merge that follows, which moves the fork itself when nothing merges
// (fastForward).
func (g *Graph) rebaseCommittedTx(ctx context.Context, tx Tx, c domain.Change, own domain.Branch, merging bool) (RebaseResult, error) {
	return g.rebaseOnto(ctx, tx, c, own, own, merging)
}

// rebaseOnto is rebaseCommittedTx for a branch given another parent (reparent): forked bears the fork the branch had,
// the base of the three-way merges; the rebase is then made whatever moved.
func (g *Graph) rebaseOnto(ctx context.Context, tx Tx, c domain.Change, own, forked domain.Branch, merging bool) (RebaseResult, error) {
	target := domain.BranchOf(own.Parent)
	res := RebaseResult{Branch: target, From: own.ForkBaseline}
	to, err := ontoOf(ctx, tx, own)
	if err != nil {
		return res, err
	}
	res.Onto = to.head
	if forked.Parent == own.Parent {
		if moved, err := to.moved(ctx, tx, own); err != nil || !moved {
			return res, err
		}
	}
	if keys, err := g.uncommitted(ctx, tx, c); err != nil {
		return res, err
	} else if len(keys) > 0 {
		return res, fmt.Errorf("change %s holds uncommitted modifications (%s): commit or cancel them before it is rebased: %w", c.ID, strings.Join(keys, ", "), ErrConflict)
	}
	impactOf := map[domain.NodeID]domain.ChangeImpact{}
	for _, cn := range mainImpactsOf(c) {
		impactOf[nodeOf(cn)] = cn
	}
	// the nodes read are the ones the branch holds a version of (only those can differ from the fork) and the ones the
	// impacts start from, never the whole states (ADR 0137)
	ids, err := tx.BranchNodeIDs(ctx, own.Name)
	if err != nil {
		return res, err
	}
	for _, cn := range mainImpactsOf(c) {
		if cn.Pre != nil {
			ids = append(ids, cn.Pre.ID)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	forkNodes, err := forkVersions(ctx, tx, forked, ids)
	if err != nil {
		return res, err
	}
	mineNodes, err := branchVersions(ctx, tx, forked, ids)
	if err != nil {
		return res, err
	}
	headNodes, err := to.versions(ctx, tx, ids)
	if err != nil {
		return res, err
	}
	// the link sets of the versions merged on both sides, read in one batch: a side whose set is the one the sides
	// started from left its links alone, and its links are not read (ADR 0146)
	var setRefs []domain.NodeRef
	for _, id := range ids {
		hv, inMine := mineNodes[id]
		fv, inFork := forkNodes[id]
		tv, inHead := headNodes[id]
		if inMine && inFork && inHead && fv != hv && tv != fv {
			setRefs = append(setRefs, domain.NodeRef{ID: id, Version: fv}, domain.NodeRef{ID: id, Version: hv}, domain.NodeRef{ID: id, Version: tv})
		}
	}
	sets, err := tx.LinkSetsOf(ctx, setRefs)
	if err != nil {
		return res, err
	}
	var merges []rebasedNode
	for _, id := range ids {
		hv, inMine := mineNodes[id]
		if !inMine {
			continue
		}
		fv, inFork := forkNodes[id]
		if inFork && fv == hv {
			continue // the change did not change it: the target's version stands
		}
		tv, inHead := headNodes[id]
		if inHead == inFork && tv == fv {
			continue // changed by the change only
		}
		if inHead && inFork {
			// a version over the target's already (a draft rebased at its commit, ADR 0165): nothing to merge
			if anc, err := CommonAncestor(ctx, tx, id, hv, tv); err == nil && anc.Version == tv {
				continue
			} else if err != nil && !errors.Is(err, ErrNotFound) {
				return res, err
			}
		}
		cn, ok := impactOf[id]
		if !ok {
			return res, fmt.Errorf("node %s changed on the branch of change %s by no impact of it: %w", id, c.ID, ErrConflict)
		}
		ours, err := g.storedDraft(ctx, tx, domain.NodeRef{ID: id, Version: hv})
		if err != nil {
			return res, err
		}
		oursRef, theirsRef := domain.NodeRef{ID: id, Version: hv}, domain.NodeRef{ID: id, Version: tv}
		var merged domain.Draft
		var conflicts []string
		if !inFork || !inHead {
			merged, conflicts = ours.Clone(), []string{domain.ConflictNode}
		} else {
			baseRef := domain.NodeRef{ID: id, Version: fv}
			setBase, okBase := sets[baseRef]
			setOurs, okOurs := sets[oursRef]
			setTheirs, okTheirs := sets[theirsRef]
			known := okBase && okOurs && okTheirs
			oursLinks := !known || setOurs.Of != setBase.Of
			theirsLinks := !known || setTheirs.Of != setBase.Of
			base, err := g.storedVersion(ctx, tx, baseRef, oursLinks && theirsLinks)
			if err != nil {
				return res, err
			}
			theirs, err := g.storedVersion(ctx, tx, theirsRef, theirsLinks)
			if err != nil {
				return res, err
			}
			switch {
			case !theirsLinks: // the target left the links alone: the change's stand
				base.Links, theirs.Links = ours.Links, ours.Links
			case !oursLinks: // the change left them alone: the target's stand
				base.Links = ours.Links
			}
			merged, conflicts = domain.MergeDrafts(base, ours, theirs)
			g.numberLinks(&merged)
		}
		if len(conflicts) == 0 && !domain.DraftChanged(&ours, merged) {
			continue // what the target changed, the change holds already
		}
		merged.Change, merged.Impact, merged.Flow = c.ID, cn.ID, ""
		merged.Base, merged.MergedWith = &oursRef, &theirsRef
		var pre *domain.NodeRef
		if cn.Pre != nil && inHead {
			pre = &theirsRef // the change now starts from the target's version
		}
		merges = append(merges, rebasedNode{cn: cn, ours: ours, merged: merged, pre: pre, conflicts: conflicts})
		res.Impacts = append(res.Impacts, RebasedImpact{Impact: cn.ID, Key: cn.Key, Changed: len(conflicts) > 0 && cn.Review != domain.ReviewProposed,
			Merged: true, Conflicts: conflicts})
	}
	// the impacts that committed nothing start from the target's version of their node too (to check again, ADR 0101)
	var repre []domain.ImpactEvent
	for _, cn := range mainImpactsOf(c) {
		if cn.Committed != nil || cn.Pre == nil {
			continue
		}
		if v, ok := headNodes[cn.Pre.ID]; ok && v != cn.Pre.Version {
			ref := domain.NodeRef{ID: cn.Pre.ID, Version: v}
			repre = append(repre, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactRebased, Pre: &ref})
		}
	}
	if merging && len(merges) == 0 && len(repre) == 0 {
		// no node changed on both sides: the merge commits what the branch committed over the target as it is, the fork
		// moved by the fast-forward (ADR 0140)
		return res, nil
	}
	// the rebase is one entry naming what the branch is brought onto, then the nodes it changes, then the review of the
	// merge, then its commit (ADR 0144)
	if err := g.moveFork(ctx, tx, c.ID, &own, to); err != nil {
		return res, err
	}
	rebased := map[string]any{"branch": target, "baseline": string(to.head)}
	if err := g.installRebased(ctx, tx, c, merges, rebased, "rebased onto "+target); err != nil {
		return res, err
	}
	if err := g.emit(ctx, tx, repre...); err != nil {
		return res, err
	}
	// a rebase that met no conflict commits its merges: the branch holds the work brought up to date
	var wrote []domain.Node
	if len(res.Impacts) > 0 && !res.Changed() {
		var err error
		if _, wrote, err = g.commitOnBranch(ctx, tx, c.ID, "", true, nil); err != nil {
			return res, err
		}
	}
	g.raiseRebased(ctx, tx, c.ID, own.Name, wrote)
	return res, nil
}

// moveFork moves the fork of the branch of a change onto what it is forked from now, with the change's reference
// baseline: a change.updated entry records the move (HeaderBaselineID, ADR 0101).
func (g *Graph) moveFork(ctx context.Context, tx Tx, id domain.ChangeID, own *domain.Branch, to onto) error {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return err
	}
	if err := to.forkOn(ctx, tx, own); err != nil {
		return err
	}
	e, err := domain.HeaderEntry(g.newID(), c.ID, g.caller(ctx), g.now(),
		domain.HeaderEdit{Fields: map[string]domain.HeaderValue{HeaderBaselineID: {From: string(c.BaselineID), To: string(own.ForkBaseline)}}})
	if err != nil {
		return err
	}
	if _, err := tx.AppendLog(ctx, e); err != nil {
		return err
	}
	if err := tx.PutBranch(ctx, *own); err != nil {
		return err
	}
	if c, err = tx.Change(ctx, id); err != nil {
		return err
	}
	c.BaselineID = own.ForkBaseline
	return tx.PutChange(ctx, c)
}

// committedBehind fills the rebase state of a change that committed on its branch: what it is forked from when that
// moved since the fork, and the impacts whose node changed on both sides.
func (g *Graph) committedBehind(ctx context.Context, tx Tx, c domain.Change, own domain.Branch, st *RebaseState) error {
	to, err := ontoOf(ctx, tx, own)
	if err != nil {
		return err
	}
	if moved, err := to.moved(ctx, tx, own); err != nil || !moved {
		return err
	}
	st.Branch, st.Onto = domain.BranchOf(own.Parent), to.head
	var ids []domain.NodeID
	for _, cn := range mainImpactsOf(c) {
		ids = append(ids, nodeOf(cn))
	}
	forkNodes, err := forkVersions(ctx, tx, own, ids)
	if err != nil {
		return err
	}
	mineNodes, err := branchVersions(ctx, tx, own, ids)
	if err != nil {
		return err
	}
	headNodes, err := to.versions(ctx, tx, ids)
	if err != nil {
		return err
	}
	for _, cn := range mainImpactsOf(c) {
		id := nodeOf(cn)
		hv, ok := mineNodes[id]
		if !ok || forkNodes[id] == hv {
			continue
		}
		if tv, ok := headNodes[id]; ok && tv != forkNodes[id] {
			st.Behind = append(st.Behind, cn.ID)
		}
	}
	return nil
}

// CommitState is what a change can commit and merge (ADR 0102): the accepted modifications the next commit writes, the
// withdrawn nodes it reverts, what holds the merge. The merge guards are asked when the merge is made, not here.
type CommitState struct {
	ToCommit  []string `json:"toCommit,omitempty"`
	Reverts   int      `json:"reverts,omitempty"`
	Unsettled []string `json:"unsettled,omitempty"`
	Commits   bool     `json:"commits"`
	CanCommit bool     `json:"canCommit"`
	// CanMerge: the change committed nodes it has not merged yet (Unmerged); CanClose: nothing holds its close (ADR 0166)
	CanMerge bool `json:"canMerge"`
	Unmerged int  `json:"unmerged,omitempty"`
	CanClose bool `json:"canClose"`
	// Candidates are what the next commit may write, node by node: the choices of a partial commit (CommitChangeNodes)
	Candidates []CommitCandidate `json:"candidates,omitempty"`
}

// A CommitCandidate is a node the next commit of a change may write.
type CommitCandidate struct {
	Node domain.NodeID `json:"nodeId"`
	Key  string        `json:"key"`
	Type string        `json:"type"`
	// Kind is created, modified or reverted (withdrawn since its commit)
	Kind   string                `json:"kind"`
	Impact domain.ChangeImpactID `json:"changeImpactId,omitempty"`
}

// The kinds of a commit candidate.
const (
	CandidateCreated  = "created"
	CandidateModified = "modified"
	CandidateReverted = "reverted"
)

// CommitState reads what a change can commit and merge.
func (g *Graph) CommitState(ctx context.Context, id domain.ChangeID) (st CommitState, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		if c.Status != domain.ChangeDraft && c.Status != domain.ChangeActive {
			return nil
		}
		draftOf, err := g.draftsOf(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, cn := range mainImpactsOf(c) {
			if cn.Review != domain.ReviewAccepted {
				continue
			}
			if ok, err := g.draftModifies(ctx, tx, draftOf(cn.ID, "")); err != nil {
				return err
			} else if ok {
				st.ToCommit = append(st.ToCommit, cn.Key)
				kind := CandidateModified
				if cn.Intent == domain.IntentCreated {
					kind = CandidateCreated
				}
				st.Candidates = append(st.Candidates, CommitCandidate{Node: nodeOf(cn), Key: cn.Key, Type: cn.Type, Kind: kind, Impact: cn.ID})
			}
		}
		w, _, err := g.withdrawnSince(ctx, tx, c)
		if err != nil {
			return err
		}
		if len(w) > 0 {
			own, _, err := ownBranch(ctx, tx, c)
			if err != nil {
				return err
			}
			for _, id := range w {
				n, err := tx.LatestOn(ctx, id, own.Name)
				if err != nil {
					return err
				}
				st.Candidates = append(st.Candidates, CommitCandidate{Node: id, Key: n.Key, Type: n.Type, Kind: CandidateReverted})
			}
		}
		if st.Unsettled, err = g.unsettled(ctx, tx, c); err != nil {
			return err
		}
		st.Reverts, st.Commits = len(w), hasCommits(c)
		if st.Commits {
			if st.Unmerged, err = g.unmerged(ctx, tx, c); err != nil {
				return err
			}
		}
		st.CanCommit = len(st.ToCommit) > 0 || st.Reverts > 0
		st.CanMerge = st.Unmerged > 0
		st.CanClose = len(st.Unsettled) == 0 && st.Commits
		return nil
	})
	return
}

// unmerged counts the nodes the branch of a change holds a version of that what it is forked from does not hold (ADR
// 0166): what its next merge moves.
func (g *Graph) unmerged(ctx context.Context, tx Tx, c domain.Change) (int, error) {
	own, ok, err := ownBranch(ctx, tx, c)
	if err != nil || !ok {
		return 0, err
	}
	to, err := ontoOf(ctx, tx, own)
	if err != nil {
		return 0, err
	}
	ids, err := tx.BranchNodeIDs(ctx, own.Name)
	if err != nil {
		return 0, err
	}
	from, err := branchVersions(ctx, tx, own, ids)
	if err != nil {
		return 0, err
	}
	into, err := to.versions(ctx, tx, ids)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		if v, ok := from[id]; ok && into[id] != v {
			n++
		}
	}
	return n, nil
}
