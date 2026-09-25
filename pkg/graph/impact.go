package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
)

// checkImpact validates the pre/post sides of an impact item. Pre (Target) is a
// version of the reference baseline: when its type has a lifecycle it must be
// in a non-editable (released) state. Post names the proposal that produces the
// new version; that proposal must act on the pre node (or create the node
// when there is no pre).
func (g *Graph) checkImpact(ctx context.Context, tx Tx, base domain.Baseline, items map[domain.ItemID]domain.ChangeItem, it domain.ChangeItem) error {
	if it.Kind != domain.KindImpact {
		return nil
	}
	if it.Target != nil && !it.Target.IsZero() {
		n, err := tx.Node(ctx, *it.Target)
		if err != nil {
			return err
		}
		ix, err := g.typesAt(ctx, tx, base.ID)
		if err != nil {
			return err
		}
		if lc := ix.lifecycleOf(n.Type); lc != nil && n.State != "" && lc.Editable(n.State) {
			return fmt.Errorf("pre %s is %s, an editable state: an impact starts from a released version", n.Key, n.State)
		}
	}
	if it.Post == nil || it.Post.Item == "" {
		return nil
	}
	pi, ok := items[it.Post.Item]
	if !ok {
		return fmt.Errorf("post references unknown item %s", it.Post.Item)
	}
	p := pi.Proposal
	if pi.Kind != domain.KindProposal || p == nil || p.Node == nil {
		return fmt.Errorf("post %s is not a node proposal", it.Post.Item)
	}
	switch p.Op {
	case domain.OpCreateNode:
		if it.Target != nil && !it.Target.IsZero() {
			return fmt.Errorf("post %s creates a node, the impact cannot have a pre", it.Post.Item)
		}
	case domain.OpUpdateNode, domain.OpDeleteNode, domain.OpTransitionNode, domain.OpMergeNode:
		if it.Target == nil || it.Target.IsZero() {
			return fmt.Errorf("post %s modifies a node, the impact needs its pre", it.Post.Item)
		}
		if p.Node.Base == nil || p.Node.Base.ID != it.Target.ID {
			return fmt.Errorf("post %s acts on another node than the pre", it.Post.Item)
		}
	default:
		return fmt.Errorf("post %s is a %s, not a node version", it.Post.Item, p.Op)
	}
	return nil
}

// Impact is the pre/post view of an impact item: the released version the
// change starts from and the version it produces, with their maturity states.
type Impact struct {
	Item      domain.ItemID   `json:"item"`
	Key       string          `json:"key"`
	Type      string          `json:"type"`
	Pre       *domain.NodeRef `json:"pre,omitempty"`
	PreState  string          `json:"preState,omitempty"`
	Post      *domain.NodeRef `json:"post,omitempty"`
	PostState string          `json:"postState,omitempty"`
}

// Impacts returns the pre/post view of the impacts of a change. The post
// version is known once the change has applied its proposals (on its own
// branch, or on the branch it applies to).
func (g *Graph) Impacts(ctx context.Context, id domain.ChangeID) (out []Impact, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		items := map[domain.ItemID]domain.ChangeItem{}
		for _, it := range c.Items {
			items[it.ID] = it
		}
		own, hasOwn, err := ownBranch(ctx, tx, c)
		if err != nil {
			return err
		}
		applied := c.Status == domain.ChangeApplied || c.Status == domain.ChangeMergePending
		for _, it := range c.Items {
			if it.Kind != domain.KindImpact || !c.InEffect(it.ID) {
				continue
			}
			im := Impact{Item: it.ID}
			var nodeID domain.NodeID
			if it.Target != nil && !it.Target.IsZero() {
				pre, err := tx.Node(ctx, *it.Target)
				if err != nil {
					return err
				}
				ref := pre.Ref()
				im.Pre, im.PreState, im.Key, im.Type, nodeID = &ref, pre.State, pre.Key, pre.Type, pre.ID
			}
			switch {
			case it.Post != nil && it.Post.Node != nil:
				post, err := tx.Node(ctx, *it.Post.Node)
				if err != nil {
					return err
				}
				ref := post.Ref()
				im.Post, im.PostState, im.Key, im.Type = &ref, post.State, post.Key, post.Type
			case it.Post != nil && applied:
				if nodeID == "" { // created node: look it up by key
					pi := items[it.Post.Item]
					if pi.Proposal == nil || pi.Proposal.Node == nil {
						break
					}
					ns := firstNonEmpty(pi.Proposal.Node.Namespace, c.Namespace)
					if nodeID, err = tx.NodeIDByKey(ctx, ns, pi.Proposal.Node.Key); err != nil {
						if errors.Is(err, ErrNotFound) {
							break
						}
						return err
					}
					im.Key, im.Type = pi.Proposal.Node.Key, pi.Proposal.Node.Type
				}
				branch := domain.BranchOf(c.Branch)
				if hasOwn {
					branch = own.Name
				}
				post, err := tx.LatestOn(ctx, nodeID, branch)
				if errors.Is(err, ErrNotFound) {
					break
				}
				if err != nil {
					return err
				}
				if im.Pre != nil && post.Version <= im.Pre.Version {
					break
				}
				ref := post.Ref()
				im.Post, im.PostState = &ref, post.State
			}
			out = append(out, im)
		}
		return nil
	})
	return
}
