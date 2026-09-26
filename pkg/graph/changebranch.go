package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
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
	b, err := tx.Branch(ctx, c.Branch)
	if err != nil {
		return b, false, err
	}
	return b, b.Origin == domain.ChangeBranchOrigin(c.ID), nil
}

// integrate merges the branch of a change into its parent branch and marks the
// change applied. Unresolved conflicts leave the change merge_pending.
func (g *Graph) integrate(ctx context.Context, tx Tx, c domain.Change, own domain.Branch, resolutions map[domain.NodeID]Resolution) (domain.Change, error) {
	if ff, err := g.fastForward(ctx, tx, c, own); err != nil || ff {
		if err != nil {
			return c, err
		}
		c, err = tx.Change(ctx, c.ID)
		return c, err
	}
	plan, err := planMerge(ctx, tx, own.Name, own.Parent)
	if err != nil {
		return c, err
	}
	for _, cand := range plan.Conflicting() {
		if _, ok := resolutions[cand.Node]; !ok {
			c.Status = domain.ChangeMergePending
			return c, tx.PutChange(ctx, c)
		}
	}
	res, err := g.mergeBranchTx(ctx, tx, MergeRequest{From: own.Name, Into: own.Parent, Title: "merge " + c.Title,
		Namespace: c.Namespace, Resolutions: resolutions})
	if err != nil {
		return c, err
	}
	c.Status = domain.ChangeApplied
	c.ResultBaselineID = res.Baseline.ID
	if err := g.land(ctx, tx, c, own, res.Change.ID); err != nil {
		return c, err
	}
	return c, tx.PutChange(ctx, c)
}

// MergeChange completes a merge_pending change: its branch is merged into the
// branch it was forked from, with the given resolutions of the conflicts.
func (g *Graph) MergeChange(ctx context.Context, id domain.ChangeID, resolutions map[domain.NodeID]Resolution) (c domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		if c.Status != domain.ChangeMergePending {
			return fmt.Errorf("change %s is %s, not merge_pending: %w", id, c.Status, ErrConflict)
		}
		own, ok, err := ownBranch(ctx, tx, c)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("change %s has no branch of its own: %w", id, ErrInvalid)
		}
		if plan, err := planMerge(ctx, tx, own.Name, own.Parent); err != nil {
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
		for name, hops := domain.BranchOf(branch), 0; hops < 64; hops++ {
			if n, err = tx.LatestOn(ctx, id, name); err == nil {
				return nil
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
			if name == domain.MainBranch {
				break
			}
			b, err := tx.Branch(ctx, name)
			if err != nil {
				return err
			}
			name = domain.BranchOf(b.Parent)
		}
		return fmt.Errorf("node key %q on branch %s: %w", key, branch, ErrNotFound)
	})
	return
}

// fastForward lands a change whose target branch has not moved since its
// branch was forked: the versions of the branch become versions of the target,
// no merge version is made, and each change impact lands as the version it wrote.
func (g *Graph) fastForward(ctx context.Context, tx Tx, c domain.Change, own domain.Branch) (bool, error) {
	into, err := branchHead(ctx, tx, own.Parent)
	if err != nil || into.ID != own.ForkBaseline {
		return false, err
	}
	from, err := branchHead(ctx, tx, own.Name)
	if err != nil {
		return false, err
	}
	// only what the change lands moves: the versions of its head baseline (a rejected or replaced version stays behind)
	moves := map[domain.NodeID]domain.Version{}
	for id, v := range from.Nodes {
		moves[id] = v
	}
	for _, cn := range c.Nodes { // the retired nodes are not in the baseline any more: their tombstone lands too
		if cn.Post == nil || cn.Review != domain.ReviewAccepted || cn.Flow != "" || cn.Superseded {
			continue
		}
		if n, err := tx.LatestOn(ctx, cn.Post.ID, own.Name); err == nil && n.Deleted {
			moves[n.ID] = n.Version
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return false, err
		}
	}
	for id, v := range moves {
		n, err := tx.Node(ctx, domain.NodeRef{ID: id, Version: v})
		if err != nil {
			return false, err
		}
		if domain.BranchOf(n.Branch) != own.Name {
			continue
		}
		if err := tx.MoveVersion(ctx, n.Ref(), domain.BranchOf(own.Parent)); err != nil {
			return false, err
		}
	}
	res := domain.Baseline{ID: domain.BaselineID(g.newID()), Name: "merge " + c.Title, Branch: domain.BranchOf(own.Parent), ParentID: into.ID, ChangeID: c.ID,
		Nodes: from.Nodes, CreatedAt: g.now()}
	if err := tx.PutBaseline(ctx, res); err != nil {
		return false, err
	}
	if err := g.advanceBranch(ctx, tx, domain.BranchOf(own.Parent), res.ID); err != nil {
		return false, err
	}
	own.Status = domain.BranchMerged
	if err := tx.PutBranch(ctx, own); err != nil {
		return false, err
	}
	c.Status, c.ResultBaselineID = domain.ChangeApplied, res.ID
	if err := g.land(ctx, tx, c, own, c.ID); err != nil {
		return false, err
	}
	return true, tx.PutChange(ctx, c)
}

// ensureOwnBranch gives a change the branch of its own that the versions it writes live on
// (ADR 0024), forked from its reference baseline like the one opened with the change. The
// items of a change are only applied when it is, so nothing is on the branch it acted on yet.
func (g *Graph) ensureOwnBranch(ctx context.Context, tx Tx, c domain.Change) (domain.Change, domain.Branch, error) {
	if c.ParentID != "" {
		return c, domain.Branch{}, fmt.Errorf("change %s is a sub-change without a branch of its own: %w", c.ID, ErrInvalid)
	}
	parent, err := branchOf(ctx, tx, c.Branch)
	if err != nil {
		return c, parent, err
	}
	if parent.Status != domain.BranchOpen {
		return c, parent, fmt.Errorf("branch %s is %s: %w", parent.Name, parent.Status, ErrConflict)
	}
	fork, err := tx.Baseline(ctx, c.BaselineID)
	if err != nil {
		return c, parent, err
	}
	own := domain.Branch{Name: changeBranchName(c.ID), Parent: parent.Name, ForkBaseline: fork.ID, Head: fork.ID,
		Origin: domain.ChangeBranchOrigin(c.ID), Status: domain.BranchOpen, CreatedAt: g.now()}
	if err := tx.PutBranch(ctx, own); err != nil {
		return c, own, err
	}
	c.Branch = own.Name
	return c, own, tx.PutChange(ctx, c)
}
