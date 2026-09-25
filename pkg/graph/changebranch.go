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
func ownBranch(ctx context.Context, tx Tx, c domain.ChangeSet) (domain.Branch, bool, error) {
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
func (g *Graph) integrate(ctx context.Context, tx Tx, c domain.ChangeSet, own domain.Branch, resolutions map[domain.NodeID]Resolution) (domain.ChangeSet, error) {
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
	return c, tx.PutChange(ctx, c)
}

// MergeChange completes a merge_pending change: its branch is merged into the
// branch it was forked from, with the given resolutions of the conflicts.
func (g *Graph) MergeChange(ctx context.Context, id domain.ChangeID, resolutions map[domain.NodeID]Resolution) (c domain.ChangeSet, err error) {
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
// same namespace are attached to as well.
func (g *Graph) SharedNodes(ctx context.Context, id domain.ChangeID) (out []SharedNode, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		refs, err := tx.Attachments(ctx, id)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			ids, err := tx.NodeAttachments(ctx, ref.ID)
			if err != nil {
				return err
			}
			var others []domain.ChangeID
			for _, o := range ids {
				if o == id {
					continue
				}
				oc, err := tx.Change(ctx, o)
				if err != nil {
					return err
				}
				if oc.Status == domain.ChangeApplied || oc.Status == domain.ChangeAbandoned || domain.NamespaceOf(oc.Namespace) != domain.NamespaceOf(c.Namespace) {
					continue
				}
				others = append(others, o)
			}
			if len(others) == 0 {
				continue
			}
			n, err := tx.Node(ctx, ref)
			if err != nil {
				return err
			}
			out = append(out, SharedNode{Node: ref, Key: n.Key, Changes: others})
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
