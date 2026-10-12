package graph

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// Iterating on the branch of a change and merging it (ADR 0102). Committing a root change writes its versions on its own
// branch and leaves it open: it is edited and committed again as often as needed. Merging it takes the branch into the
// one it was forked from, once every impact of it is settled, and closes it. When the target moved since the fork, the
// merge first rebases the branch onto its head: the fork moves there, the nodes changed on both sides are merged
// three-way into drafts (the target's version as their second parent), committed when the rebase met no conflict, left to
// settle otherwise; the merge is then a fast-forward. A sub-change, which has no branch of its own (ADR 0081), is merged
// into its parent the same way: settled, rebased onto its parent, asked to the merge guards, then integrated.

// hasCommits reports whether a root change committed versions on its own branch it has not merged yet.
func hasCommits(c domain.Change) bool {
	return c.ResultBaselineID != "" && (c.Status == domain.ChangeDraft || c.Status == domain.ChangeActive)
}

// uncommitted lists the change impacts whose draft holds a modification not committed yet (ADR 0102).
func (g *Graph) uncommitted(ctx context.Context, tx Tx, c domain.Change) ([]string, error) {
	rows, err := g.drafts(ctx, tx, c.ID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, cn := range mainImpactsOf(c) {
		if ok, err := g.draftModifies(ctx, tx, ownDraft(rows, cn.ID, "")); err != nil {
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
	fork, err := tx.Baseline(ctx, own.ForkBaseline)
	if err != nil {
		return nil, nil, err
	}
	head, err := branchHead(ctx, tx, c.Namespace, own.Name)
	if err != nil {
		return nil, nil, err
	}
	held := map[domain.NodeID]bool{}
	for _, cn := range mainImpactsOf(c) {
		held[nodeOf(cn)] = true
	}
	var out []domain.NodeID
	for _, id := range slices.Sorted(maps.Keys(head.Nodes)) {
		if !held[id] && fork.Nodes[id] != head.Nodes[id] {
			out = append(out, id)
		}
	}
	return out, fork.Nodes, nil
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
	rows, err := g.drafts(ctx, tx, c.ID)
	if err != nil {
		return nil, err
	}
	for _, cn := range mainImpactsOf(c) {
		if cn.Via != "" {
			continue
		}
		modifies, err := g.draftModifies(ctx, tx, ownDraft(rows, cn.ID, ""))
		if err != nil {
			return nil, err
		}
		switch {
		case cn.Review == domain.ReviewRejected:
			out = append(out, cn.Key+" is rejected (rework or withdraw it)")
		case !modifies && cn.Landed == nil:
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
func (g *Graph) merge(ctx context.Context, id domain.ChangeID, resolutions map[domain.NodeID]Resolution) (c domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		if left, err := g.unsettled(ctx, tx, c); err != nil {
			return err
		} else if len(left) > 0 {
			return fmt.Errorf("change %s cannot be merged yet: %s: %w", id, strings.Join(left, "; "), ErrConflict)
		}
		if !hasCommits(c) {
			return fmt.Errorf("change %s committed nothing on its branch: %w", id, ErrNothingToCommit)
		}
		return g.checkParentDrafts(ctx, tx, c)
	})
	if err != nil {
		return c, err
	}
	var res RebaseResult
	err = g.repo.InTx(ctx, func(tx Tx) error {
		res, err = g.rebaseTx(ctx, tx, c)
		return err
	})
	if err != nil {
		return c, err
	}
	if res.Changed() {
		return c, fmt.Errorf("change %s %s: %w", id, res.summary(), ErrConflict)
	}
	if err := g.checkMerge(ctx, id); err != nil {
		return c, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		own, ok, err := ownBranch(ctx, tx, c)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("change %s has no branch of its own: %w", id, ErrInvalid)
		}
		if c.ParentID == "" {
			c, err = g.integrate(ctx, tx, c, own, resolutions)
			return err
		}
		// a sub-change fast-forwards the branch of its parent, which takes its work as its own (ADR 0103)
		if ff, err := g.fastForward(ctx, tx, c, own); err != nil {
			return err
		} else if !ff {
			return fmt.Errorf("the branch of parent change %s moved meanwhile: merge %s again: %w", c.ParentID, id, ErrConflict)
		}
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		return g.recordInParent(ctx, tx, c)
	})
	return c, err
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
	rows, err := g.drafts(ctx, tx, parent.ID)
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
		if ok, err := g.draftModifies(ctx, tx, ownDraft(rows, cn.ID, "")); err != nil {
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

// recordInParent gives the parent of a merged sub-change an impact for each node the sub-change landed on the parent's
// branch (ADR 0103): its own impact of the node, else a new one, accepted with the sub-change's review and landed as the
// version the sub-change committed. The parent then holds the work as its own, committed.
func (g *Graph) recordInParent(ctx context.Context, tx Tx, c domain.Change) error {
	parent, err := tx.Change(ctx, c.ParentID)
	if err != nil {
		return err
	}
	head, err := branchHead(ctx, tx, parent.Namespace, parent.Branch)
	if err != nil {
		return err
	}
	for _, cn := range mainImpactsOf(c) {
		if cn.Landed == nil {
			continue
		}
		ref := *cn.Landed
		var events []domain.ImpactEvent
		id := domain.ChangeImpactID("")
		for _, p := range mainImpactsOf(parent) {
			if nodeOf(p) == ref.ID {
				id = p.ID
			}
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
		r := domain.Review{Status: domain.ReviewAccepted, By: last.By, At: g.now(), ReviewID: last.ReviewID,
			Comment: strings.TrimSpace(fmt.Sprintf("merged from sub-change %s (%s): %s", c.Title, c.ID, last.Comment))}
		events = append(events,
			domain.ImpactEvent{Change: parent.ID, Impact: id, Op: domain.ImpactReviewed, By: r.By, Review: &r},
			domain.ImpactEvent{Change: parent.ID, Impact: id, Op: domain.ImpactLanded, Landed: &ref, Baseline: head.ID, Branch: domain.BranchOf(parent.Branch)})
		if err := g.emit(ctx, tx, events...); err != nil {
			return err
		}
	}
	// the parent's branch now holds the sub-change's commits: the parent has committed work to merge
	if parent, err = tx.Change(ctx, parent.ID); err != nil {
		return err
	}
	parent.ResultBaselineID = head.ID
	return tx.PutChange(ctx, parent)
}

// rebaseCommittedTx rebases a root change that committed on its own branch onto the head of its target branch: a
// baseline of kind rebase on its branch (the head of the target with the versions the change committed; its parent the
// previous head of the branch, MergedFrom the head it was rebased onto, RebasedFrom the fork it left), the fork of the
// branch and the change's reference baseline moved to that head, and, for each node changed on both sides since the
// fork, a draft merging the two (base: the version at the fork; ours: the version the change committed; theirs: the
// version on the target), its Base the committed version and MergedWith the target's. A rebase with no conflict keeps the
// reviews and commits the merged drafts; a conflict sends its impact back to proposed, named as ADR 0101 does, to be
// settled, accepted and committed.
func (g *Graph) rebaseCommittedTx(ctx context.Context, tx Tx, c domain.Change, own domain.Branch) (RebaseResult, error) {
	target := domain.BranchOf(own.Parent)
	res := RebaseResult{Branch: target, From: own.ForkBaseline}
	head, err := branchHead(ctx, tx, c.Namespace, target)
	if err != nil {
		return res, err
	}
	res.Onto = head.ID
	if head.ID == own.ForkBaseline {
		return res, nil
	}
	if keys, err := g.uncommitted(ctx, tx, c); err != nil {
		return res, err
	} else if len(keys) > 0 {
		return res, fmt.Errorf("change %s holds uncommitted modifications (%s): commit or cancel them before it is rebased: %w", c.ID, strings.Join(keys, ", "), ErrConflict)
	}
	fork, err := tx.Baseline(ctx, own.ForkBaseline)
	if err != nil {
		return res, err
	}
	mine, err := branchHead(ctx, tx, c.Namespace, own.Name)
	if err != nil {
		return res, err
	}
	impactOf := map[domain.NodeID]domain.ChangeImpact{}
	for _, cn := range mainImpactsOf(c) {
		impactOf[nodeOf(cn)] = cn
	}
	nodes := maps.Clone(head.Nodes)
	if nodes == nil {
		nodes = map[domain.NodeID]domain.Version{}
	}
	for _, id := range slices.Sorted(maps.Keys(mine.Nodes)) {
		hv := mine.Nodes[id]
		fv, inFork := fork.Nodes[id]
		if inFork && fv == hv {
			continue // the change did not change it: the target's version stands
		}
		nodes[id] = hv
		tv, inHead := head.Nodes[id]
		if inHead == inFork && tv == fv {
			continue // changed by the change only
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
			base, err := g.storedDraft(ctx, tx, domain.NodeRef{ID: id, Version: fv})
			if err != nil {
				return res, err
			}
			theirs, err := g.storedDraft(ctx, tx, theirsRef)
			if err != nil {
				return res, err
			}
			merged, conflicts = domain.MergeDrafts(base, ours, theirs)
			g.numberLinks(&merged)
		}
		if len(conflicts) == 0 && !domain.DraftChanged(&ours, merged) {
			continue // what the target changed, the change holds already
		}
		for i := range merged.Links {
			merged.Links[i].ID = domain.LinkID(g.newID()) // the links of the version it lands as are its own
		}
		merged.Change, merged.Impact, merged.Flow = c.ID, cn.ID, ""
		merged.Base, merged.MergedWith = &oursRef, &theirsRef
		rebased := map[string]any{"branch": target, "baseline": string(head.ID)}
		var pre *domain.NodeRef
		if cn.Pre != nil && inHead {
			pre = &theirsRef // the change now starts from the target's version
		}
		if err := g.installRebased(ctx, tx, c, cn, ours, merged, pre, rebased, conflicts, len(conflicts) > 0, "rebased onto "+target); err != nil {
			return res, err
		}
		res.Impacts = append(res.Impacts, RebasedImpact{Impact: cn.ID, Key: cn.Key, Changed: len(conflicts) > 0 && cn.Review != domain.ReviewProposed,
			Merged: true, Conflicts: conflicts})
	}
	// the impacts that committed nothing start from the target's version of their node too (to check again, ADR 0101)
	for _, cn := range mainImpactsOf(c) {
		if cn.Landed != nil || cn.Pre == nil {
			continue
		}
		if v, ok := head.Nodes[cn.Pre.ID]; ok && v != cn.Pre.Version {
			ref := domain.NodeRef{ID: cn.Pre.ID, Version: v}
			if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactRebased, Pre: &ref}); err != nil {
				return res, err
			}
		}
	}
	rb := domain.Baseline{ID: domain.BaselineID(g.newID()), Name: c.Title, Namespace: c.Namespace, Branch: own.Name, ParentID: mine.ID, ChangeID: c.ID,
		Kind: domain.BaselineRebase, Nodes: nodes, MergedFrom: head.ID, RebasedFrom: fork.ID, CreatedAt: g.now()}
	if err := tx.PutBaseline(ctx, rb); err != nil {
		return res, err
	}
	e, err := domain.HeaderEntry(g.newID(), c.ID, g.caller(ctx), g.now(),
		domain.HeaderEdit{Fields: map[string]domain.HeaderValue{HeaderBaselineID: {From: string(c.BaselineID), To: string(head.ID)}}})
	if err != nil {
		return res, err
	}
	if _, err := tx.AppendLog(ctx, e); err != nil {
		return res, err
	}
	if own.RebasedFrom == "" {
		own.RebasedFrom = own.ForkBaseline
	}
	own.ForkBaseline, own.Head = head.ID, rb.ID
	if err := tx.PutBranch(ctx, own); err != nil {
		return res, err
	}
	if c, err = tx.Change(ctx, c.ID); err != nil {
		return res, err
	}
	c.BaselineID = head.ID
	if err := tx.PutChange(ctx, c); err != nil {
		return res, err
	}
	// a rebase that met no conflict commits its merges: the branch holds the work brought up to date
	if len(res.Impacts) > 0 && !res.Changed() {
		if _, err := g.commitOnBranch(ctx, tx, c.ID, ""); err != nil {
			return res, err
		}
	}
	return res, nil
}

// committedBehind fills the rebase state of a root change that committed on its branch: the head of its target when it
// moved since the fork, and the impacts whose node changed on both sides.
func (g *Graph) committedBehind(ctx context.Context, tx Tx, c domain.Change, own domain.Branch, st *RebaseState) error {
	target := domain.BranchOf(own.Parent)
	head, err := branchHead(ctx, tx, c.Namespace, target)
	if err != nil || head.ID == own.ForkBaseline {
		return err
	}
	st.Branch, st.Onto = target, head.ID
	fork, err := tx.Baseline(ctx, own.ForkBaseline)
	if err != nil {
		return err
	}
	mine, err := branchHead(ctx, tx, c.Namespace, own.Name)
	if err != nil {
		return err
	}
	for _, cn := range mainImpactsOf(c) {
		id := nodeOf(cn)
		hv, ok := mine.Nodes[id]
		if !ok || fork.Nodes[id] == hv {
			continue
		}
		if tv, ok := head.Nodes[id]; ok && tv != fork.Nodes[id] {
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
	CanMerge  bool     `json:"canMerge"`
}

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
		rows, err := g.drafts(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, cn := range mainImpactsOf(c) {
			if cn.Review != domain.ReviewAccepted {
				continue
			}
			if ok, err := g.draftModifies(ctx, tx, ownDraft(rows, cn.ID, "")); err != nil {
				return err
			} else if ok {
				st.ToCommit = append(st.ToCommit, cn.Key)
			}
		}
		w, _, err := g.withdrawnSince(ctx, tx, c)
		if err != nil {
			return err
		}
		if st.Unsettled, err = g.unsettled(ctx, tx, c); err != nil {
			return err
		}
		st.Reverts, st.Commits = len(w), hasCommits(c)
		st.CanCommit = len(st.ToCommit) > 0 || st.Reverts > 0
		st.CanMerge = len(st.Unsettled) == 0 && st.Commits
		return nil
	})
	return
}
