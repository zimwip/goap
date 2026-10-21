package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/operations"
)

// changeBranchName names the branch owned by a change.
func changeBranchName(id domain.ChangeID) string {
	s := string(id)
	if len(s) > 8 {
		s = s[:8]
	}
	return "change-" + s
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
	return g.integrate(ctx, tx, c, own, resolutions)
}

// integrate merges the branch of a change into its parent branch and marks the change applied. Unresolved conflicts
// leave the change committed: its integration waits for IntegrateChange.
func (g *Graph) integrate(ctx context.Context, tx Tx, c domain.Change, own domain.Branch, resolutions map[domain.NodeID]Resolution) (domain.Change, error) {
	if ff, err := g.fastForward(ctx, tx, c, own); err != nil || ff {
		if err != nil {
			return c, err
		}
		c, err = tx.Change(ctx, c.ID)
		return c, err
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
	c.ResultBaselineID = res.Baseline.ID
	if err := g.commit(ctx, tx, c, own, res.Change.ID, res.Baseline.ID); err != nil {
		return c, err
	}
	g.raiseApplied(ctx, tx, c, res.Baseline)
	return c, tx.PutChange(ctx, c)
}

// IntegrateChange merges a change into what it was forked from (ADR 0102, merge): a root change that committed on its
// own branch into the branch it was forked from, a sub-change into its parent. A root change whose three-way merge waits
// for its resolutions (committed) is integrated with them.
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
		return g.merge(ctx, id, resolutions)
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
		c, err = g.integrate(ctx, tx, c, own, resolutions)
		return err
	})
	return c, err
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
		for _, cn := range c.Nodes {
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

// fastForward commits a change whose target has not moved, for the nodes its branch changed, since its branch was
// forked: the versions of the branch join the target (ADR 0032), no merge version is made, and each change impact
// commits as the version it wrote. The target is a lasting branch, which records the commit in a baseline, or the branch
// of the parent change of a sub-change (ADR 0103), which holds no baseline (ADR 0140).
func (g *Graph) fastForward(ctx context.Context, tx Tx, c domain.Change, own domain.Branch) (bool, error) {
	to, err := ontoOf(ctx, tx, own)
	if err != nil {
		return false, err
	}
	// what the branch changed since its fork joins the target: the versions the branch holds now (a rejected or
	// replaced version stays behind, a revert gives the fork's), and the tombstones of the nodes it retired; only a node
	// the branch holds a version of can differ, the states are read for those (ADR 0137)
	target := to.parent.Name
	ids, err := tx.BranchNodeIDs(ctx, own.Name)
	if err != nil {
		return false, err
	}
	slices.Sort(ids)
	forkNodes, err := forkVersions(ctx, tx, own, ids)
	if err != nil {
		return false, err
	}
	intoNodes, err := to.versions(ctx, tx, ids)
	if err != nil {
		return false, err
	}
	if !maps.Equal(forkNodes, intoNodes) {
		return false, nil // the target moved on a node the branch changed: a merge, not a fast-forward
	}
	if moved, err := to.moved(ctx, tx, own); err != nil {
		return false, err
	} else if moved {
		// the target moved since the fork, on other nodes: the work of the branch stands over it as it is, the rebase
		// that would only move the fork left out (ADR 0140)
		if err := g.moveFork(ctx, tx, c.ID, &own, to); err != nil {
			return false, err
		}
		if c, err = tx.Change(ctx, c.ID); err != nil {
			return false, err
		}
	}
	fromNodes, err := branchVersions(ctx, tx, own, ids)
	if err != nil {
		return false, err
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
			return false, err
		}
	}
	for _, r := range moves {
		if err := tx.JoinBranch(ctx, r, target, c.ID, 0); err != nil {
			return false, err
		}
	}
	own.Status = domain.BranchMerged
	if err := tx.PutBranch(ctx, own); err != nil {
		return false, err
	}
	c.Status = domain.ChangeApplied
	if to.parent.Ephemeral {
		// the branch of the parent holds what the sub-change committed, its impacts committed there (recordInParent)
		endCommit(tx, target)
		if err := g.commit(ctx, tx, c, own, c.ID, ""); err != nil {
			return false, err
		}
		return true, tx.PutChange(ctx, c)
	}
	// a fast-forward, not a merge (the target did not move since the fork): name it after the change, not "merge …"
	res := domain.Baseline{ID: domain.BaselineID(g.newID()), Name: c.Title, Namespace: c.Namespace, Branch: target, ParentID: to.head, ChangeID: c.ID,
		Kind: domain.BaselineFastForward, CreatedAt: g.now()}
	if err := putBaselineOver(ctx, tx, res, to.head, delta); err != nil {
		return false, err
	}
	if err := g.advanceBranch(ctx, tx, c.Namespace, target, res); err != nil {
		return false, err
	}
	c.ResultBaselineID = res.ID
	if err := g.commit(ctx, tx, c, own, c.ID, res.ID); err != nil {
		return false, err
	}
	g.raiseApplied(ctx, tx, c, res)
	return true, tx.PutChange(ctx, c)
}

// ensureOwnBranch gives a change the branch of its own that the versions it writes live on
// (ADR 0024), forked from its reference baseline like the one opened with the change. The
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
	fork, err := headerOf(ctx, tx, c.BaselineID)
	if err != nil {
		return c, parent, err
	}
	own := domain.Branch{Ephemeral: true, Name: changeBranchName(c.ID), Namespace: c.Namespace, Parent: parent.Name, ForkBaseline: fork.ID,
		Origin: domain.ChangeBranchOrigin(c.ID), Intent: domain.IntentDerive, Status: domain.BranchOpen, CreatedAt: g.now()}
	if err := tx.PutBranch(ctx, own); err != nil {
		return c, own, err
	}
	c.Branch = own.Name
	return c, own, tx.PutChange(ctx, c)
}
