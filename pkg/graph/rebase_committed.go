package graph

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// Iterating on the branch of a change (ADR 0102). Committing a root change writes its versions on its own branch and
// leaves it open: it is checked out and edited again, and committed again, as often as needed. Merging it into the
// branch it was forked from is a separate act, possible once it holds a commit and no uncommitted modification, that
// closes it (applied). The merge first rebases the branch onto the head of its target when the target moved: the
// branch's fork moves to that head, the nodes changed on both sides are merged three-way into drafts (the version on
// the target as their second parent), committed when clean, left to settle otherwise; the merge is then a fast-forward.
// The change's methodology can hold the merge: the lifecycle of the change must be in a final state and the landing
// gate must accept what the branch holds.

// hasCommits reports whether a root change committed versions on its own branch it has not merged yet.
func hasCommits(c domain.Change) bool {
	return c.ParentID == "" && c.ResultBaselineID != "" && (c.Status == domain.ChangeDraft || c.Status == domain.ChangeActive)
}

// uncommitted lists the change impacts whose draft holds a modification not committed yet (ADR 0102).
func (g *Graph) uncommitted(ctx context.Context, tx Tx, c domain.Change) ([]string, error) {
	rows, err := g.drafts(ctx, tx, c.ID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, cn := range c.Nodes {
		if len(cn.Items) > 0 || cn.Flow != "" || cn.Superseded {
			continue
		}
		if ok, err := g.draftModifies(ctx, tx, ownDraft(rows, cn.ID, "")); err != nil {
			return nil, err
		} else if ok {
			out = append(out, cn.Key)
		}
	}
	return out, nil
}

// rebaseCommittedTx rebases a root change that committed on its own branch onto the head of its target branch: a
// baseline of kind rebase on its branch (the head of the target with the versions the change committed; its parent the
// previous head of the branch, MergedFrom the head it was rebased onto, RebasedFrom the fork it left), the fork of the
// branch and the change's reference baseline moved to that head, and, for each node changed on both sides since the
// fork, a draft merging the two (base: the version at the fork; ours: the version the change committed; theirs: the
// version on the target), its Base the committed version and MergedWith the target's. A clean merge keeps the review of
// the impact (the merge commits it); a conflict sends it back to proposed, named as ADR 0101 does.
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
	for _, cn := range c.Nodes {
		if len(cn.Items) == 0 && cn.Flow == "" && !cn.Superseded {
			impactOf[nodeOf(cn)] = cn
		}
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
		if err := g.installRebased(ctx, tx, c, cn, ours, merged, nil, rebased, conflicts, len(conflicts) > 0, "rebased onto "+target); err != nil {
			return res, err
		}
		res.Impacts = append(res.Impacts, RebasedImpact{Impact: cn.ID, Key: cn.Key, Changed: len(conflicts) > 0 && cn.Review != domain.ReviewProposed,
			Merged: true, Conflicts: conflicts})
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
	return res, tx.PutChange(ctx, c)
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
	for _, cn := range c.Nodes {
		if len(cn.Items) > 0 || cn.Flow != "" || cn.Superseded {
			continue
		}
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

// mergeRoot merges a root change that committed on its own branch into the branch it was forked from (ADR 0102): it
// refuses while a modification is not committed, rebases the branch first when the target moved (conflicts stop it,
// ErrConflict; clean merges are committed), asks the change's methodology (a final state of its lifecycle, the landing
// gate on what the branch holds, else no node in a state that cannot land), then fast-forwards the target. A target that
// moves in between falls back to the three-way merge (resolutions), which leaves the change committed while it waits.
func (g *Graph) mergeRoot(ctx context.Context, id domain.ChangeID, resolutions map[domain.NodeID]Resolution) (c domain.Change, err error) {
	if c, err = g.Change(ctx, id); err != nil {
		return c, err
	}
	if !hasCommits(c) {
		return c, fmt.Errorf("change %s has nothing committed on its branch: commit it first: %w", id, ErrConflict)
	}
	var res RebaseResult
	err = g.repo.InTx(ctx, func(tx Tx) error {
		own, ok, err := ownBranch(ctx, tx, c)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("change %s has no branch of its own: %w", id, ErrInvalid)
		}
		if keys, err := g.uncommitted(ctx, tx, c); err != nil {
			return err
		} else if len(keys) > 0 {
			return fmt.Errorf("change %s holds uncommitted modifications (%s): commit or cancel them before it is merged: %w", id, strings.Join(keys, ", "), ErrConflict)
		}
		res, err = g.rebaseCommittedTx(ctx, tx, c, own)
		return err
	})
	if err != nil {
		return c, err
	}
	var pending []string
	for _, i := range res.Impacts {
		if len(i.Conflicts) > 0 {
			pending = append(pending, i.Key+" "+strings.Join(i.Conflicts, ", "))
		}
	}
	if len(pending) > 0 {
		return c, fmt.Errorf("change %s rebased onto branch %s: settle the conflicts %s, review and commit them, then merge again: %w", id, res.Branch, strings.Join(pending, "; "), ErrConflict)
	}
	if len(res.Impacts) > 0 {
		// the clean merges of the rebase are versions to write: committed as the change's own
		if err := g.repo.InTx(ctx, func(tx Tx) error {
			_, err := g.commitOnBranch(ctx, tx, id, "", nil, false)
			return err
		}); err != nil {
			return c, err
		}
	}
	if err := g.checkFinalState(ctx, id); err != nil {
		return c, err
	}
	landing, err := g.askMergeGate(ctx, id)
	if err != nil {
		return c, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		if !hasCommits(c) {
			return fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
		}
		own, _, err := ownBranch(ctx, tx, c)
		if err != nil {
			return err
		}
		if landing == nil || !landing.decided {
			if stuck, err := g.stuckOnBranch(ctx, tx, c, own); err != nil {
				return err
			} else if len(stuck) > 0 {
				return invalidf("the change leaves nodes in a state that cannot land, move them to a landable state first: %s", joinSorted(stuck))
			}
		} else if !landing.ok {
			return invalidf("the change does not satisfy the goal of its landing gate")
		}
		c, err = g.integrate(ctx, tx, c, own, resolutions)
		return err
	})
	return c, err
}

// mergeView is what the branch of a change holds of its impacts: the version each committed (on the head of the branch)
// with the version it started from.
func (g *Graph) mergeView(ctx context.Context, tx Tx, c domain.Change, own domain.Branch) (posts, pres []domain.Node, err error) {
	mine, err := branchHead(ctx, tx, c.Namespace, own.Name)
	if err != nil {
		return nil, nil, err
	}
	for _, cn := range c.Nodes {
		if len(cn.Items) > 0 || cn.Flow != "" || cn.Superseded || cn.Landed == nil {
			continue
		}
		v, ok := mine.Nodes[nodeOf(cn)]
		if !ok {
			continue
		}
		n, err := tx.Node(ctx, domain.NodeRef{ID: nodeOf(cn), Version: v})
		if err != nil {
			return nil, nil, err
		}
		posts = append(posts, n)
		if cn.Pre != nil {
			p, err := tx.Node(ctx, *cn.Pre)
			if err != nil {
				return nil, nil, err
			}
			pres = append(pres, p)
		}
	}
	return posts, pres, nil
}

// stuckOnBranch lists the nodes the change committed that are in a state that cannot land (ADR 0078).
func (g *Graph) stuckOnBranch(ctx context.Context, tx Tx, c domain.Change, own domain.Branch) ([]string, error) {
	posts, _, err := g.mergeView(ctx, tx, c, own)
	if err != nil {
		return nil, err
	}
	ix, err := g.typesAt(ctx, tx, c.BaselineID)
	if err != nil {
		return nil, err
	}
	var stuck []string
	for _, n := range posts {
		if lc := ix.lifecycleOf(n.Type); lc != nil && n.State != "" && !lc.Landable(n.State) {
			stuck = append(stuck, fmt.Sprintf("%s (%s) in %s", n.Key, n.Type, n.State))
		}
	}
	return stuck, nil
}

// askMergeGate asks LandingGate about the merge of a change, outside any transaction (the gate may read the graph), on
// the blackboard of what its branch holds.
func (g *Graph) askMergeGate(ctx context.Context, id domain.ChangeID) (*landingDecision, error) {
	if g.LandingGate == nil {
		return nil, nil
	}
	var change domain.Change
	bb := domain.Blackboard{Nodes: map[domain.NodeRef]domain.NodeView{}}
	err := g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		own, ok, err := ownBranch(ctx, tx, c)
		if err != nil || !ok {
			return err
		}
		posts, pres, err := g.mergeView(ctx, tx, c, own)
		if err != nil {
			return err
		}
		ix, err := g.typesAt(ctx, tx, c.BaselineID)
		if err != nil {
			return err
		}
		for _, n := range append(pres, posts...) {
			v := domain.NodeView{Node: n}
			if lc := ix.lifecycleOf(n.Type); lc != nil && n.State != "" {
				v.NotLandable = !lc.Landable(n.State)
			}
			bb.Nodes[n.Ref()] = v
		}
		change = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	bb.Change = change
	decided, ok, err := g.LandingGate(ctx, change, bb)
	if err != nil {
		return nil, err
	}
	return &landingDecision{decided: decided, ok: ok}, nil
}
