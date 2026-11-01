package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
)

// Rebasing the drafts a commit writes (ADR 0165): before a commit writes its versions, each accepted modification it
// writes is compared with the head of the branch the change merges into. A node the head moved since its draft was
// checked out is merged three-way (base: the common ancestor of the two versions; ours: the draft; theirs: the head's
// version) and its draft checked out again from the head's version, the difference the merge makes recorded as an update
// (DraftDiff); a state both sides moved stays the change's only when its lifecycle allows the move from the head's state,
// the moves the change recorded are replayed over the head's state otherwise, so the commit still walks a chain of
// allowed moves (walkTransitions). A clean merge is reviewed by graph.merge and the commit goes on; a conflict sends its
// impact back to proposed and stops the commit (ErrConflict), the rebase kept for the person or the agent to settle
// (ImpactNodeResolve or an edit, then a review). The versions the commit writes then derive from the head's, and the
// merge that follows is a fast-forward (fastForward follows the lineage of each node).

// rebaseConflict refuses a commit whose drafts the rebase onto the head of target left with conflicts on left.
func rebaseConflict(id domain.ChangeID, target string, left []string) error {
	return fmt.Errorf("change %s: its drafts were rebased onto the head of %s, with conflicts on %s: settle them, review them and commit again: %w",
		id, target, strings.Join(left, ", "), ErrConflict)
}

// draftRebase is a draft the head moved under: its impact and draft, the version the head holds, the one both start
// from.
type draftRebase struct {
	cn       domain.ChangeImpact
	draft    domain.Draft
	head     domain.NodeRef
	ancestor domain.NodeRef
}

// rebaseDraftsTx is rebaseDrafts inside a transaction: the branch the drafts are rebased onto and the keys of the
// impacts left with conflicts.
func (g *Graph) rebaseDraftsTx(ctx context.Context, tx Tx, c domain.Change, nodes []domain.NodeID) (string, []string, error) {
	switch c.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned, domain.ChangeCommitted:
		return "", nil, nil // the commit refuses it
	}
	own, isOwn, err := ownBranch(ctx, tx, c)
	if err != nil {
		return "", nil, err
	}
	sel := &applier{}
	if len(nodes) > 0 {
		sel.only, sel.taken = map[domain.NodeID]bool{}, map[domain.NodeID]bool{}
		for _, n := range nodes {
			sel.only[n] = true
		}
	}
	draftOf, err := g.draftsOf(ctx, tx, c.ID)
	if err != nil {
		return "", nil, err
	}
	impacts := mainImpactsOf(c)
	// the nodes the change holds a draft of: a link to one is a link to its draft
	drafted := map[domain.NodeID]bool{}
	var todo []draftRebase
	var ids []domain.NodeID
	for _, cn := range impacts.All() {
		d := draftOf(cn.ID, "")
		if d == nil {
			continue
		}
		drafted[d.Node] = true
		if d.Base == nil || cn.Review != domain.ReviewAccepted || !sel.takesImpact(cn, impacts) {
			continue
		}
		todo = append(todo, draftRebase{cn: cn, draft: *d})
		ids = append(ids, d.Node)
	}
	if len(todo) == 0 {
		return "", nil, nil
	}
	// the head of the branch the change merges into, read for those nodes only (ADR 0137)
	var target string
	var head map[domain.NodeID]domain.Version
	// a change that committed nothing yet starts from the head now: its branch takes the commits of its target up to
	// there (ADR 0177); one with no branch of its own yet gets it, forked from its reference commit, which never moves
	follow := !hasCommits(c)
	if !isOwn && follow && c.ParentID == "" {
		h, err := branchHeadHeader(ctx, tx, c.Namespace, domain.BranchOf(c.Branch))
		if err != nil {
			return "", nil, err
		}
		if h.ID != c.ForkCommit {
			if c, own, err = g.ensureOwnBranch(ctx, tx, c); err != nil {
				return "", nil, err
			}
			isOwn = true
		}
	}
	if isOwn {
		to, err := ontoOf(ctx, tx, own)
		if err != nil {
			return "", nil, err
		}
		target = to.parent.Name
		if head, err = to.versions(ctx, tx, ids); err != nil {
			return "", nil, err
		}
		if follow {
			if moved, err := to.moved(ctx, tx, own); err != nil {
				return "", nil, err
			} else if moved {
				if err := g.takeOnto(ctx, tx, c.ID, &own, &to); err != nil {
					return "", nil, err
				}
				g.raiseRebased(ctx, tx, c.ID, own.Name, nil)
			}
		}
	} else {
		target = domain.BranchOf(c.Branch)
		h, err := branchHeadHeader(ctx, tx, c.Namespace, target)
		if err != nil {
			return "", nil, err
		}
		if head, err = versionsAt(ctx, tx, h.ID, ids); err != nil {
			return "", nil, err
		}
	}
	var moved []draftRebase
	for _, r := range todo {
		base := *r.draft.Base
		tv, ok := head[base.ID]
		if !ok || tv == base.Version {
			continue
		}
		if isOwn {
			// a version the change committed, checked out again: the rebase of what it committed brings it up (rebaseOnto)
			if n, err := tx.Node(ctx, base); err != nil {
				return "", nil, err
			} else if domain.BranchOf(n.Branch) == own.Name {
				continue
			}
		}
		anc, err := CommonAncestor(ctx, tx, base.ID, base.Version, tv)
		if err != nil {
			return "", nil, err
		}
		if anc.Version == tv {
			continue // the draft starts from a version over the head's already
		}
		r.head, r.ancestor = domain.NodeRef{ID: base.ID, Version: tv}, anc
		moved = append(moved, r)
	}
	if len(moved) == 0 {
		return target, nil, nil
	}
	ix, err := g.typesAt(ctx, tx, c.ForkCommit)
	if err != nil {
		return "", nil, err
	}
	events, err := g.foldedEvents(ctx, tx, c.ID)
	if err != nil {
		return "", nil, err
	}
	toDraft := func(links []domain.DraftLink) {
		for i, l := range links {
			if !l.External() && drafted[l.To.ID] {
				links[i].To = domain.DraftRef(l.To.ID)
			}
		}
	}
	rebased := map[string]any{"branch": target, "drafts": true}
	why := "rebased onto " + target
	var nodesDone []rebasedNode
	var left []string
	for _, r := range moved {
		d := r.draft
		theirsNode, err := tx.Node(ctx, r.head)
		if err != nil {
			return "", nil, err
		}
		theirsLinks, err := heldOutLinks(ctx, tx, r.head)
		if err != nil {
			return "", nil, err
		}
		// the draft checked out again from the head's version: its copy, the links to a node the change drafts turned
		// into links to its draft
		event := g.newID()
		cp := &domain.BaseCopy{}
		for _, l := range theirsLinks {
			if !l.External() && drafted[l.To.ID] && !slices.Contains(cp.Drafted, l.To) {
				cp.Drafted = append(cp.Drafted, l.To)
			}
		}
		copied := domain.CopyDraft(domain.ImpactEvent{ID: event, Change: c.ID, Impact: r.cn.ID, Execution: d.Execution, FromBase: cp,
			Draft: &domain.Draft{Execution: d.Execution}}, theirsNode, theirsLinks)
		anc, err := g.storedDraft(ctx, tx, r.ancestor)
		if err != nil {
			return "", nil, err
		}
		toDraft(anc.Links)
		merged, conflicts := domain.MergeDrafts(anc, d, copied)
		g.numberLinks(&merged)
		var moves []stateMove
		if lc := ix.lifecycleOf(d.Type); lc != nil {
			conflicts = slices.DeleteFunc(conflicts, func(s string) bool { return s == domain.ConflictState })
			var ok bool
			if moves, ok = rebasedMoves(lc, events, r.cn.ID, anc.State, d.State, copied.State); !ok {
				conflicts = append(conflicts, domain.ConflictState)
				slices.Sort(conflicts)
			}
			merged.State = copied.State // the state moves by the moves replayed, never by the update
		}
		if len(conflicts) == 0 {
			conflicts = nil
		}
		merged.Change, merged.Impact, merged.Flow, merged.Base, merged.MergedWith = c.ID, r.cn.ID, "", &r.head, nil
		draftRef := domain.DraftRef(r.head.ID)
		checkout := domain.ImpactEvent{ID: event, Change: c.ID, Impact: r.cn.ID, Op: domain.ImpactCheckedOut, Execution: d.Execution, Post: &draftRef,
			Draft: &copied, FromBase: cp}
		if r.cn.Pre != nil {
			pre := r.head // the impact now starts from the head's version
			checkout.Pre = &pre
		}
		patch := domain.DraftDiff(copied, merged)
		if patch == nil {
			patch = map[string]any{}
		}
		patch["rebased"], patch["conflicts"] = rebased, conflicts
		evs := []domain.ImpactEvent{checkout, {Change: c.ID, Impact: r.cn.ID, Op: domain.ImpactUpdated, Execution: d.Execution, Post: &draftRef, Patch: patch}}
		for _, m := range moves {
			evs = append(evs, domain.ImpactEvent{Change: c.ID, Impact: r.cn.ID, Op: domain.ImpactTransitioned, Execution: d.Execution, Post: &draftRef,
				Patch: map[string]any{"state": map[string]any{"from": m.from, "to": m.to}}})
		}
		if err := g.emit(ctx, tx, evs...); err != nil {
			return "", nil, err
		}
		nodesDone = append(nodesDone, rebasedNode{cn: r.cn, conflicts: conflicts})
		if len(conflicts) > 0 {
			left = append(left, fmt.Sprintf("%s (%s)", r.cn.Key, strings.Join(conflicts, ", ")))
		}
	}
	if err := g.reviewRebased(ctx, tx, c, nodesDone, why); err != nil {
		return "", nil, err
	}
	return target, left, nil
}

// stateMove is a move of a lifecycle a rebase replays.
type stateMove struct{ from, to string }

// rebasedMoves are the moves that bring a rebased draft from the head's state (theirs) to the state it ends in: none
// when the change left the state alone or both sides reached one state; the moves the change recorded for the draft
// when only the change moved it, each allowed by the lifecycle from the head's state on; when both moved it, the one move
// from the head's state to the change's, if the lifecycle allows it. Not ok: a conflict of states.
func rebasedMoves(lc *domain.Lifecycle, events []domain.ImpactEvent, impact domain.ChangeImpactID, base, ours, theirs string) ([]stateMove, bool) {
	norm := func(s string) string {
		if s == "" {
			return lc.Initial
		}
		return s
	}
	base, ours, theirs = norm(base), norm(ours), norm(theirs)
	if ours == base || ours == theirs {
		return nil, true
	}
	if theirs == base {
		// the moves the change recorded since the draft was checked out, when they start from that state
		var recorded []stateMove
		for _, e := range events {
			if e.Impact != impact || e.Flow != "" {
				continue
			}
			switch {
			case e.StartsDraft():
				recorded = nil
			case e.Op == domain.ImpactTransitioned && e.Patch["state"] != nil:
				st, _ := e.Patch["state"].(map[string]any)
				from, _ := st["from"].(string)
				to, _ := st["to"].(string)
				recorded = append(recorded, stateMove{norm(from), to})
			}
		}
		if len(recorded) > 0 && recorded[0].from == theirs && recorded[len(recorded)-1].to == ours {
			at := theirs
			for _, m := range recorded {
				if _, ok := lc.Move(at, m.to); !ok || m.from != at {
					return nil, false
				}
				at = m.to
			}
			return recorded, true
		}
	}
	if _, ok := lc.Move(theirs, ours); ok {
		return []stateMove{{theirs, ours}}, true
	}
	return nil, false
}
