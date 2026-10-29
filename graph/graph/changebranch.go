package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/operations"
)

// changeBranchName names the branch owned by a change: its whole id. A prefix of it is not unique: the 8 first hex
// digits of random ids collide once a graph holds some tens of thousands of changes, and the second change took over
// the branch of the first (the store keeps one branch per name), so neither committed nor merged again.
func changeBranchName(id domain.ChangeID) string {
	return "change-" + string(id)
}

// ownBranch returns the branch owned by the change, when it has one.
func ownBranch(ctx context.Context, tx Tx, c domain.Change) (domain.Branch, bool, error) {
	if domain.BranchOf(c.Branch) == domain.MainBranch {
		return domain.Branch{}, false, nil
	}
	b, err := tx.Branch(ctx, c.Namespace, c.Branch)
	if err != nil {
		return b, false, err
	}
	return b, b.Origin == domain.ChangeBranchOrigin(c.ID), nil
}

// integrateTx integrates a committed change into the branch it was forked from (its own branch is the one to merge).
// Unresolved conflicts leave a change committed.
func (g *Graph) integrateTx(ctx context.Context, tx Tx, c domain.Change, resolutions map[domain.NodeID]Resolution) (domain.Change, error) {
	own, ok, err := ownBranch(ctx, tx, c)
	if err != nil {
		return c, err
	}
	if !ok {
		return c, fmt.Errorf("change %s has no branch of its own: %w", c.ID, ErrInvalid)
	}
	return g.integrate(ctx, tx, c, own, resolutions, true)
}

// integrate merges the branch of a change into its parent branch and marks the change applied. Unresolved conflicts
// leave the change committed: its integration waits for IntegrateChange.
func (g *Graph) integrate(ctx context.Context, tx Tx, c domain.Change, own domain.Branch, resolutions map[domain.NodeID]Resolution, closing bool) (domain.Change, error) {
	if ff, err := g.fastForward(ctx, tx, c, own, closing); err != nil || ff {
		if err != nil {
			return c, err
		}
		c, err = tx.Change(ctx, c.ID)
		return c, err
	}
	if !closing {
		// the rebase brought the branch over the head under the target's lock: only a target moved meanwhile gets here
		return c, fmt.Errorf("the target of change %s moved meanwhile: merge it again: %w", c.ID, ErrConflict)
	}
	plan, err := planMerge(ctx, tx, c.Namespace, own.Name, own.Parent)
	if err != nil {
		return c, err
	}
	for _, cand := range plan.Conflicting() {
		if _, ok := resolutions[cand.Node]; !ok {
			// the merge waits for a resolution: the change is committed, frozen until it is integrated
			c.Status = domain.ChangeCommitted
			return c, tx.PutChange(ctx, c)
		}
	}
	res, err := g.mergeBranchTx(ctx, tx, MergeRequest{From: own.Name, Into: own.Parent, Title: "merge " + c.Title,
		Namespace: c.Namespace, Resolutions: resolutions, OwnerOrg: c.OwnerOrg.String(), ProjectID: c.ProjectID.String()})
	if err != nil {
		return c, err
	}
	c.Status = domain.ChangeApplied
	c.LandedCommit = res.Commit.ID
	if err := g.commit(ctx, tx, c, own, res.Change.ID, res.Commit.ID, nil, nil); err != nil {
		return c, err
	}
	g.raiseMerged(ctx, tx, c, own.Parent, &res.Commit, res.merged)
	g.raiseApplied(ctx, tx, c, res.Commit)
	return c, tx.PutChange(ctx, c)
}

// IntegrateChange merges what a change committed into what it was forked from (ADR 0102, merge): a root change that
// committed on its own branch into the branch it was forked from, a sub-change into its parent. The change stays open
// (ADR 0166): it goes on working, committing and merging until it is closed (CloseChange). A root change whose three-way
// merge waits for its resolutions (committed) is integrated with them, and closed.
//
//goap:operation change.merge doer
func (g *Graph) IntegrateChange(ctx context.Context, id domain.ChangeID, resolutions map[domain.NodeID]Resolution) (c domain.Change, err error) {
	if err := g.allow(ctx, id, operations.ChangeMerge); err != nil {
		return c, err
	}
	if c, err = g.Change(ctx, id); err != nil {
		return c, err
	}
	switch {
	case c.Status == domain.ChangeDraft || c.Status == domain.ChangeActive:
		return g.merge(ctx, id, resolutions, false)
	case c.Status != domain.ChangeCommitted || c.ParentID != "":
		return c, fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
	}
	err = g.changeTx(ctx, id, func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		if c.Status != domain.ChangeCommitted {
			return fmt.Errorf("change %s is %s, not committed: %w", id, c.Status, ErrConflict)
		}
		own, ok, err := ownBranch(ctx, tx, c)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("change %s has no branch of its own: %w", id, ErrInvalid)
		}
		if err := tx.LockBranch(ctx, c.Namespace, domain.BranchOf(own.Parent)); err != nil {
			return err
		}
		if plan, err := planMerge(ctx, tx, c.Namespace, own.Name, own.Parent); err != nil {
			return err
		} else if un := unresolved(plan, resolutions); len(un) > 0 {
			return fmt.Errorf("merge %s into %s: unresolved conflicts on %v: %w", own.Name, own.Parent, un, ErrConflict)
		}
		c, err = g.integrate(ctx, tx, c, own, resolutions, true)
		return err
	})
	return c, err
}

// CloseChange closes a change (ADR 0166): nothing of it left uncommitted or unsettled (every impact committed, none
// rejected or a candidate, no open flow, decision point or sub-change, no pending revert), its lifecycle in a final
// state, what it committed and has not merged yet merged into what it was forked from; the change is then applied and
// its branch merged.
//
//goap:operation change.close doer
func (g *Graph) CloseChange(ctx context.Context, id domain.ChangeID) (c domain.Change, err error) {
	if err := g.allow(ctx, id, operations.ChangeClose); err != nil {
		return c, err
	}
	return g.closeChange(ctx, id)
}

// closeChange is CloseChange once allowed.
func (g *Graph) closeChange(ctx context.Context, id domain.ChangeID) (c domain.Change, err error) {
	if c, err = g.Change(ctx, id); err != nil {
		return c, err
	}
	if c.Status != domain.ChangeDraft && c.Status != domain.ChangeActive {
		return c, fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
	}
	return g.merge(ctx, id, nil, true)
}

func unresolved(p MergePlan, resolutions map[domain.NodeID]Resolution) []string {
	var out []string
	for _, cand := range p.Conflicting() {
		if _, ok := resolutions[cand.Node]; !ok {
			out = append(out, cand.Key)
		}
	}
	return out
}

// SharedNode is a node several open changes act on: a merge is needed.
type SharedNode struct {
	Node    domain.NodeRef    `json:"node"`
	Key     string            `json:"key"`
	Changes []domain.ChangeID `json:"changes"`
}

// SharedNodes lists the nodes of a change that other unapplied changes of the
// same namespace act on as well.
func (g *Graph) SharedNodes(ctx context.Context, id domain.ChangeID) (out []SharedNode, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		open, err := g.openChangeHolders(ctx, tx)
		if err != nil {
			return err
		}
		seen := map[domain.NodeID]bool{}
		for _, cn := range c.Impacts().All() {
			if cn.Pre == nil || seen[cn.Pre.ID] {
				continue
			}
			seen[cn.Pre.ID] = true
			var others []domain.ChangeID
			for _, o := range open[cn.Pre.ID] {
				if o == id {
					continue
				}
				oc, err := tx.Change(ctx, o)
				if err != nil {
					return err
				}
				if domain.NamespaceOf(oc.Namespace) != domain.NamespaceOf(c.Namespace) {
					continue
				}
				others = append(others, o)
			}
			if len(others) == 0 {
				continue
			}
			n, err := tx.Node(ctx, *cn.Pre)
			if err != nil {
				return err
			}
			out = append(out, SharedNode{Node: *cn.Pre, Key: n.Key, Changes: others})
		}
		return nil
	})
	return
}

// NodeByKeyOn returns the latest version of the node with the given key as seen
// from a branch: the branch's own version, else its parent's, up to main.
func (g *Graph) NodeByKeyOn(ctx context.Context, namespace, branch, key string) (n domain.Node, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		id, err := tx.NodeIDByKey(ctx, namespace, key)
		if err != nil {
			return err
		}
		n, err = latestOnTx(ctx, tx, namespace, branch, id)
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("node key %q on branch %s: %w", key, branch, ErrNotFound)
		}
		return err
	})
	return
}

// latestOnTx is the latest version of a node on a branch of a namespace: written or joined there, else on the branch
// it was forked from, up to main.
func latestOnTx(ctx context.Context, tx Tx, namespace, branch string, id domain.NodeID) (domain.Node, error) {
	for name, hops := domain.BranchOf(branch), 0; hops < 64; hops++ {
		if n, err := tx.LatestOn(ctx, id, name); err == nil {
			return n, nil
		} else if !errors.Is(err, ErrNotFound) {
			return n, err
		}
		if name == domain.MainBranch {
			break
		}
		b, err := tx.Branch(ctx, namespace, name)
		if err != nil {
			return domain.Node{}, err
		}
		name = domain.BranchOf(b.Parent)
	}
	return domain.Node{}, fmt.Errorf("node %s on branch %s: %w", id, branch, ErrNotFound)
}

// errTargetMoved tells a fast-forward its target moved between what it read and the lock: the caller rebases under the
// lock, then merges again.
var errTargetMoved = errors.New("the target moved since it was read")

// fastForward commits a change whose target has not moved, for the nodes its branch changed, since its branch was
// forked: the target takes the commits of the branch (ADR 0032, 0176), no merge version is made, and each change impact
// commits as the version it wrote. The target is a lasting branch, which records the commit in a commit, or the branch
// of the parent change of a sub-change (ADR 0103), which holds no commit (ADR 0140).
//
// Everything is read before the target is locked, against the head read then, read again while the head moves before
// the lock (prepareAttempts) with no write, so the fork is moved once, onto the head the plan stands on: the lock is
// held for the writes of the target only. A head moved still is read again under the lock (the fork moved again): a
// target moved on a node the branch changed is errTargetMoved, for the caller to rebase; a target that holds the lock
// already (the caller took it) cannot have moved. False with no error: not a fast-forward, the target locked.
func (g *Graph) fastForward(ctx context.Context, tx Tx, c domain.Change, own domain.Branch, closing bool) (bool, error) {
	ck, err := g.checkFastForward(ctx, tx, c, own)
	// a target moved while the plan was read is read again before anything is written, so the fork moves once, onto a
	// head that stands
	for i := 0; err == nil && ck.ok && i < prepareAttempts; i++ {
		now, ferr := freshOnto(ctx, tx, own)
		if ferr != nil {
			return false, ferr
		}
		if now.head == ck.to.head {
			break
		}
		ck, err = g.checkFastForward(ctx, tx, c, own)
	}
	if err != nil {
		return false, err
	}
	var ff fastForwardPlan
	if ck.ok {
		if ff, err = g.finishFastForward(ctx, tx, ck); err != nil {
			return false, err
		}
	}
	// the head of the target is read again under its lock (ADR 0127): a merge meanwhile is seen, and the plan read again
	// with the target still; a node the target moved on meanwhile is the caller's to rebase, under the lock
	if err := tx.LockBranch(ctx, c.Namespace, domain.BranchOf(own.Parent)); err != nil {
		return false, err
	}
	if now, err := ontoOf(ctx, tx, own); err != nil {
		return false, err
	} else if now.head != ck.to.head {
		if ff.ok {
			c, own = ff.c, ff.own
		}
		if ck, err = g.checkFastForward(ctx, tx, c, own); err != nil {
			return false, err
		} else if !ck.ok {
			return false, errTargetMoved
		}
		if ff, err = g.finishFastForward(ctx, tx, ck); err != nil {
			return false, err
		}
	}
	if !ff.ok {
		return false, nil
	}
	return g.applyFastForward(ctx, tx, ff, closing)
}

// prepareAttempts is how many times a fast-forward reads its plan again when the target moved before it locks it.
const prepareAttempts = 3

// freshOnto is ontoOf reading the branch of the target again, not as the transaction read it before.
func freshOnto(ctx context.Context, tx Tx, own domain.Branch) (onto, error) {
	if gt, ok := unwrapGuard(tx); ok {
		gt.forgetBranch(own.Namespace, domain.BranchOf(own.Parent))
	}
	return ontoOf(ctx, tx, own)
}

// fastForwardPlan is what a fast-forward reads before it locks its target: the change and its branch (its fork moved
// onto the head read), the target and its head, the versions the target takes (moves), the nodes the fast-forward
// changes (delta, 0: gone) and those nodes as the target holds them once it took them (heads).
type fastForwardPlan struct {
	ok    bool
	c     domain.Change
	own   domain.Branch
	to    onto
	moves []domain.NodeRef
	delta map[domain.NodeID]domain.Version
	heads map[domain.NodeID]domain.Node
}

// fastForwardCheck is what a fast-forward reads, writing nothing, against the head of its target as it reads it: ok
// when the target moved on no node the branch changed, the target moved since the fork (moved) and the versions of
// the nodes the branch holds on its fork, on its branch and on the target.
type fastForwardCheck struct {
	ok, moved            bool
	c                    domain.Change
	own                  domain.Branch
	to                   onto
	ids                  []domain.NodeID
	intoNodes, fromNodes map[domain.NodeID]domain.Version
	raw                  map[domain.NodeID]CommittedVersion
}

// checkFastForward reads whether a change fast-forwards onto the head of its target as it reads it (a target moved on
// a node the branch changed is no fast-forward, ok false), writing nothing.
func (g *Graph) checkFastForward(ctx context.Context, tx Tx, c domain.Change, own domain.Branch) (fastForwardCheck, error) {
	ck := fastForwardCheck{c: c, own: own}
	to, err := freshOnto(ctx, tx, own)
	if err != nil {
		return ck, err
	}
	ck.to = to
	// what the branch changed since its fork joins the target: the versions the branch holds now (a rejected or
	// replaced version stays behind, a revert gives the fork's), and the tombstones of the nodes it retired; only a node
	// the branch holds a version of can differ, the states are read for those (ADR 0137)
	ids, err := tx.BranchNodeIDs(ctx, own.Name)
	if err != nil {
		return ck, err
	}
	slices.Sort(ids)
	forkNodes, err := forkVersions(ctx, tx, own, ids)
	if err != nil {
		return ck, err
	}
	intoNodes, err := to.versions(ctx, tx, ids)
	if err != nil {
		return ck, err
	}
	// the commits the branch holds, read once: its state over the fork, and the reverts the target takes
	raw, err := tx.BranchCommits(ctx, own.Namespace, own.Name, math.MaxInt64, ids)
	if err != nil {
		return ck, err
	}
	fromNodes := overFork(forkNodes, raw)
	// a node the branch holds as its fork does (a revert, ADR 0140) is no work of the branch: the target's version stands
	// over it (its revert is written again over the new fork, refreshReverts)
	worked := slices.DeleteFunc(slices.Clone(ids), func(id domain.NodeID) bool {
		fv, inFork := forkNodes[id]
		hv, inFrom := fromNodes[id]
		return inFork == inFrom && fv == hv
	})
	if !maps.Equal(subMap(forkNodes, worked), subMap(intoNodes, worked)) {
		// the target moved on a node the branch changed: a fast-forward still when the branch's version derives from the
		// target's (a draft rebased at its commit, ADR 0165), a merge otherwise
		if ok, err := overTarget(ctx, tx, worked, forkNodes, intoNodes, fromNodes); err != nil || !ok {
			return ck, err
		}
	}
	if ck.moved, err = to.moved(ctx, tx, own); err != nil {
		return ck, err
	}
	ck.ok, ck.ids, ck.intoNodes, ck.fromNodes, ck.raw = true, ids, intoNodes, fromNodes, raw
	return ck, nil
}

// finishFastForward prepares what a fast-forward checked writes: the fork of the branch moved onto the head checked
// when the target moved since the fork, on other nodes (the branch is the change's, under the lock of its tree), the
// versions the target takes and those nodes as it holds them once it took them.
func (g *Graph) finishFastForward(ctx context.Context, tx Tx, ck fastForwardCheck) (fastForwardPlan, error) {
	c, own, to, ids, intoNodes, fromNodes, raw := ck.c, ck.own, ck.to, ck.ids, ck.intoNodes, ck.fromNodes, ck.raw
	ff := fastForwardPlan{c: c, own: own, to: to}
	if ck.moved {
		// the work of the branch stands over the target as it is, the rebase that would only move the fork left out
		// (ADR 0140)
		if err := g.moveFork(ctx, tx, c.ID, &own, to); err != nil {
			return ff, err
		}
		g.raiseRebased(ctx, tx, c.ID, own.Name, nil)
		var err error
		if c, err = tx.Change(ctx, c.ID); err != nil {
			return ff, err
		}
		// the reverts written again over the new fork (refreshReverts) are the ones the target takes
		if raw, err = tx.BranchCommits(ctx, own.Namespace, own.Name, math.MaxInt64, ids); err != nil {
			return ff, err
		}
	}
	var moves []domain.NodeRef
	delta := map[domain.NodeID]domain.Version{}
	for _, id := range ids {
		if v, ok := fromNodes[id]; ok && intoNodes[id] != v {
			moves = append(moves, domain.NodeRef{ID: id, Version: v})
			delta[id] = v
		}
	}
	for _, id := range ids {
		if _, ok := intoNodes[id]; !ok {
			continue
		}
		if _, ok := fromNodes[id]; ok {
			continue
		}
		delta[id] = 0
		if n, err := tx.LatestOn(ctx, id, own.Name); err == nil && n.Deleted {
			moves = append(moves, n.Ref())
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return ff, err
		}
	}
	// the reverts the branch holds are versions the target takes as they are (ADR 0176): copies of what it holds
	for _, id := range ids {
		if l, ok := raw[id]; ok && l.Revert && !l.Deleted && intoNodes[id] != l.Version {
			moves = append(moves, domain.NodeRef{ID: id, Version: l.Version})
			delta[id] = l.Version
		}
	}
	// the nodes the fast-forward changes, as the target holds them once it took the commits: read before the lock
	heads, err := targetHeads(ctx, tx, delta, moves)
	if err != nil {
		return ff, err
	}
	ff.ok, ff.c, ff.own, ff.moves, ff.delta, ff.heads = true, c, own, moves, delta, heads
	return ff, nil
}

// targetHeads reads the nodes a fast-forward changes at the version the target holds of each once it took the commits:
// the version it moves the node to, the tombstone of a node it deletes; nil when a node has none of them (read then).
func targetHeads(ctx context.Context, tx Tx, delta map[domain.NodeID]domain.Version, moves []domain.NodeRef) (map[domain.NodeID]domain.Node, error) {
	at := make(map[domain.NodeID]domain.Version, len(delta))
	for _, r := range moves {
		at[r.ID] = r.Version
	}
	refs := make([]domain.NodeRef, 0, len(delta))
	for id := range delta {
		v, ok := at[id]
		if !ok {
			return nil, nil
		}
		refs = append(refs, domain.NodeRef{ID: id, Version: v})
	}
	ns, err := tx.NodesAt(ctx, refs, "")
	if err != nil {
		return nil, err
	}
	heads := make(map[domain.NodeID]domain.Node, len(ns))
	for _, n := range ns {
		heads[n.ID] = n
	}
	return heads, nil
}

// applyFastForward writes a fast-forward prepared, its target locked and where the plan read it.
func (g *Graph) applyFastForward(ctx context.Context, tx Tx, ff fastForwardPlan, closing bool) (bool, error) {
	c, own, to := ff.c, ff.own, ff.to
	target := to.parent.Name
	moves, delta := ff.moves, ff.delta
	if len(moves) == 0 && len(delta) == 0 {
		if !closing {
			return false, fmt.Errorf("change %s: what it committed is merged already, nothing to merge: %w", c.ID, ErrNothingToCommit)
		}
		if c.LandedCommit != "" || to.parent.Ephemeral {
			// merged before (ADR 0166): the close only closes
			return true, g.closeMerged(ctx, tx, c, own, target)
		}
	}
	// the target takes the commits of the branch (ADR 0176): one row per commit, the versions they wrote as they are
	if err := takeCommits(ctx, tx, own.Namespace, target, own.Name, c.ID, moves); err != nil {
		return false, err
	}
	// the impacts whose node this merge moves record their version on the target, the ones merged before kept
	only := make(map[domain.NodeID]bool, len(delta))
	for id := range delta {
		only[id] = true
	}
	if closing {
		own.Status = domain.BranchMerged
		if err := tx.PutBranch(ctx, own); err != nil {
			return false, err
		}
		c.Status = domain.ChangeApplied
	}
	if to.parent.Ephemeral {
		// the branch of the parent holds what the sub-change committed, its impacts committed there (recordInParent)
		if err := g.commit(ctx, tx, c, own, c.ID, "", only, nil); err != nil {
			return false, err
		}
		// the parent's branch records the merge as a commit of its own (ADR 0175)
		pb := to.parent
		res, err := g.commitOwn(ctx, tx, &pb, domain.Commit{Name: c.Title, ChangeID: c.ID, Kind: domain.CommitKindFastForward, MergedFrom: own.HeadOrFork()})
		if err != nil {
			return false, err
		}
		if err := g.raiseMergedRefs(ctx, tx, c, target, &res, moves); err != nil {
			return false, err
		}
		if err := tx.PutChange(ctx, c); err != nil {
			return false, err
		}
		if closing {
			return true, nil
		}
		return true, g.forkAfterMerge(ctx, tx, c.ID, own, onto{parent: pb, head: res.ID})
	}
	// a fast-forward, not a merge (the target did not move since the fork): name it after the change, not "merge …"
	res := domain.Commit{ID: domain.CommitID(g.newID()), Name: c.Title, Namespace: c.Namespace, Branch: target, ParentID: to.head, ChangeID: c.ID,
		Kind: domain.CommitKindFastForward, CreatedAt: g.now()}
	if err := putCommitOver(ctx, tx, res, to.head, delta); err != nil {
		return false, err
	}
	if err := g.advanceBranch(ctx, tx, c.Namespace, target, res); err != nil {
		return false, err
	}
	c.LandedCommit = res.ID
	if err := g.commit(ctx, tx, c, own, c.ID, res.ID, only, ff.heads); err != nil {
		return false, err
	}
	if err := g.raiseMergedRefs(ctx, tx, c, target, &res, moves); err != nil {
		return false, err
	}
	if closing {
		g.raiseApplied(ctx, tx, c, res)
		return true, tx.PutChange(ctx, c)
	}
	if err := tx.PutChange(ctx, c); err != nil {
		return false, err
	}
	// the change stays open (ADR 0166): its branch now starts from what it merged
	return true, g.forkAfterMerge(ctx, tx, c.ID, own, onto{parent: to.parent, head: res.ID})
}

// forkAfterMerge forks the branch of a change that stays open after its merge (ADR 0166) from what it merged into: the
// fork and the change's reference commit move there, journaled as a rebase is (HeaderForkCommit); the branch keeps
// what it committed, now on the target too.
func (g *Graph) forkAfterMerge(ctx context.Context, tx Tx, id domain.ChangeID, own domain.Branch, to onto) error {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return err
	}
	from := own.ForkCommit
	if err := to.forkOnHead(ctx, tx, &own); err != nil {
		return err
	}
	e, err := domain.HeaderEntry(g.newID(), c.ID, g.caller(ctx), g.now(),
		domain.HeaderEdit{Fields: map[string]domain.HeaderValue{HeaderForkCommit: {From: string(c.ForkCommit), To: string(own.ForkCommit)}}})
	if err != nil {
		return err
	}
	if _, err := tx.AppendLog(ctx, e); err != nil {
		return err
	}
	if err := g.forkMoved(ctx, tx, c, &own, from); err != nil {
		return err
	}
	c.ForkCommit = own.ForkCommit
	return tx.PutChange(ctx, c)
}

// closeMerged closes a change whose work is all merged already (ADR 0166): its branch merged, the change applied at the
// commit its last merge left.
func (g *Graph) closeMerged(ctx context.Context, tx Tx, c domain.Change, own domain.Branch, target string) error {
	own.Status = domain.BranchMerged
	if err := tx.PutBranch(ctx, own); err != nil {
		return err
	}
	c.Status = domain.ChangeApplied
	if c.LandedCommit != "" {
		res, err := tx.CommitHeader(ctx, c.LandedCommit)
		if err != nil {
			return err
		}
		g.raiseApplied(ctx, tx, c, res)
	}
	return tx.PutChange(ctx, c)
}

// overTarget reports whether every node the target moved since the fork is one the branch holds a version of deriving
// from the target's: the branch then stands over the target as it is.
func overTarget(ctx context.Context, tx Tx, ids []domain.NodeID, fork, into, from map[domain.NodeID]domain.Version) (bool, error) {
	for _, id := range ids {
		fv, inFork := fork[id]
		tv, inInto := into[id]
		if inFork == inInto && fv == tv {
			continue
		}
		v, ok := from[id]
		if !ok || !inInto {
			return false, nil
		}
		if v == tv {
			continue
		}
		anc, err := CommonAncestor(ctx, tx, id, v, tv)
		if errors.Is(err, ErrNotFound) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		if anc.Version != tv {
			return false, nil
		}
	}
	return true, nil
}

// ensureOwnBranch gives a change the branch of its own that the versions it writes live on
// (ADR 0024), forked from its reference commit like the one opened with the change. The
// items of a change are only applied when it is, so nothing is on the branch it acted on yet.
func (g *Graph) ensureOwnBranch(ctx context.Context, tx Tx, c domain.Change) (domain.Change, domain.Branch, error) {
	if c.ParentID != "" {
		return c, domain.Branch{}, fmt.Errorf("change %s is a sub-change without a branch of its own: %w", c.ID, ErrInvalid)
	}
	parent, err := branchOf(ctx, tx, c.Namespace, c.Branch)
	if err != nil {
		return c, parent, err
	}
	if parent.Status != domain.BranchOpen {
		return c, parent, fmt.Errorf("branch %s is %s: %w", parent.Name, parent.Status, ErrConflict)
	}
	fork, err := headerOf(ctx, tx, c.ForkCommit)
	if err != nil {
		return c, parent, err
	}
	own := domain.Branch{Ephemeral: true, Name: changeBranchName(c.ID), Namespace: c.Namespace, Parent: parent.Name, ForkCommit: fork.ID,
		Origin: domain.ChangeBranchOrigin(c.ID), Intent: domain.IntentDerive, Status: domain.BranchOpen, CreatedAt: g.now()}
	if err := tx.PutBranch(ctx, own); err != nil {
		return c, own, err
	}
	c.Branch = own.Name
	return c, own, tx.PutChange(ctx, c)
}

// subMap is the part of a state holding some nodes.
func subMap(m map[domain.NodeID]domain.Version, ids []domain.NodeID) map[domain.NodeID]domain.Version {
	out := make(map[domain.NodeID]domain.Version, len(ids))
	for _, id := range ids {
		if v, ok := m[id]; ok {
			out[id] = v
		}
	}
	return out
}
