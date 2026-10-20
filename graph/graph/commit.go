package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// This file is the producer-facing way of making a change of node edits
// (ADR 0024): the producer says which nodes it creates or modifies and why; the
// change, its change impacts, the versions on the change branch, the reviews and
// the apply follow. Services that project their own definitions into the graph
// (registry, seeds) use it.

// Commit runs the edits as one change, through the operations every change uses (ADR 0076): it opens the change on a
// branch of its own; a created node is a ImpactNodeCreate, a modified one a ImpactNodeCheckout with its ImpactNodeUpdate and link edits;
// the state edits are transitions (ImpactNodeTransition, on the draft), each impact that modifies its node is accepted by
// the producer and one that modifies nothing withdrawn (ADR 0102), and the change is applied. Each step is the
// operation a change takes, in its own transaction, as a person or an agent would take it: a commit that commits nothing
// (ErrNothingToCommit) or fails leaves no change behind (purged); when the branch cannot be merged without conflict
// (another change moved a node meanwhile) the change is abandoned and ErrConflict returned: the producer reads again and
// rebuilds its edits.
// A created node of a structure or a User must name its required parent (checkRequiredParent, ADR 0040).
// The baseline returned is a header, without its state: reading it costs the size of the namespace (ADR 0137).
func (g *Graph) Commit(ctx context.Context, in Commit) (res CommitResult, err error) {
	if len(in.Edits) == 0 {
		return res, fmt.Errorf("a commit needs at least one edit: %w", ErrInvalid)
	}
	if err := checkCarried(in.Events); err != nil {
		return res, err
	}
	for _, e := range in.Edits {
		if err := g.checkRequiredParent(e); err != nil {
			return res, err
		}
	}
	c, err := g.CreateChange(ctx, NewChange{Namespace: in.Namespace, Title: in.Title, Intent: in.Intent, Methodology: in.Methodology,
		BaselineID: in.Baseline, Branch: in.Branch, Data: in.Data, OwnBranch: true, OwnerOrg: in.OwnerOrg, ProjectID: in.ProjectID})
	if err != nil {
		return res, err
	}
	res.Change = c.ID
	defer func() {
		if err == nil {
			return
		}
		// a commit is all or nothing: a change that committed nothing leaves no trace (purged with its drafts and its log,
		// no key is held, ADR 0079), else it is abandoned
		if _, perr := g.PurgeChange(ctx, c.ID); perr == nil {
			return
		}
		abandoned := domain.ChangeAbandoned
		if _, aerr := g.UpdateChange(ctx, c.ID, ChangePatch{Status: &abandoned}); aerr != nil {
			err = errors.Join(err, aerr)
		}
	}()
	order, err := g.commitOrder(ctx, in.Edits)
	if err != nil {
		return res, err
	}
	impacts := make([]domain.ChangeImpactID, len(in.Edits))
	posts := make([]*domain.NodeRef, len(in.Edits)) // the drafts
	written := map[string]int{}                     // key of a created node → its edit
	type late struct {
		from int
		l    LinkEdit
	}
	var later []late // links to a node of the commit not written yet (a cycle): added once every node is
	created := map[string]bool{}
	for _, e := range in.Edits {
		if e.Pre == nil {
			created[e.Key] = true
		}
	}
	why := func(e NodeEdit) string {
		if e.Rationale != "" {
			return e.Rationale
		}
		return in.Title
	}
	for _, i := range order {
		e := in.Edits[i]
		var links []LinkWrite
		for _, l := range e.Links {
			to := l.To
			if l.ToKey != "" {
				j, ok := written[l.ToKey]
				var ref domain.NodeRef
				switch {
				case ok:
					ref = *posts[j]
				case created[l.ToKey]:
					later = append(later, late{i, l})
					continue
				default:
					// not a node of this commit: a node the graph already holds, whose version stays
					n, err := g.NodeByKeyOn(ctx, in.Namespace, in.Branch, l.ToKey)
					if errors.Is(err, ErrNotFound) {
						return res, fmt.Errorf("link of %s to %q: no such node, neither stored nor created by this commit: %w", nodeName(e), l.ToKey, ErrInvalid)
					} else if err != nil {
						return res, err
					}
					ref = n.Ref()
				}
				to = &ref
			}
			if to == nil {
				return res, fmt.Errorf("link of %s needs a target: %w", nodeName(e), ErrInvalid)
			}
			if e.Pre != nil {
				// a link is part of its source version: the one the version already carries (same attributes, not removed
				// by the edit) is not added again
				if has, err := g.linksTo(ctx, *e.Pre, l.Type, to.ID, l.Props, e.RemoveLinks); err != nil {
					return res, err
				} else if has {
					continue
				}
			}
			links = append(links, LinkWrite{Type: l.Type, To: *to, Properties: l.Props})
		}
		if e.Pre != nil {
			// the impact names the version the edit starts from: a stale one conflicts (it is not in the reference baseline)
			added, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: e.Pre, Rationale: why(e), ProducedBy: in.By}})
			if err != nil {
				return res, fmt.Errorf("%s: %w", nodeName(e), err)
			}
			impacts[i] = added[0].ID
		}
		switch {
		case e.Pre == nil:
			cn, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: e.Key, Type: e.Type, Properties: e.Props, Owner: e.Owner, Rationale: why(e), Links: links, ProducedBy: in.By})
			if err != nil {
				return res, fmt.Errorf("%s: %w", nodeName(e), err)
			}
			impacts[i], posts[i], written[e.Key] = cn.ID, cn.Post, i
		case len(e.Props) > 0 || e.Owner != "" || len(links) > 0 || len(e.RemoveLinks) > 0:
			cn, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: impacts[i]})
			if err != nil {
				return res, fmt.Errorf("%s: %w", nodeName(e), err)
			}
			posts[i] = cn.Post
			if len(e.Props) > 0 || e.Owner != "" {
				if _, err := g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: e.Props, Owner: e.Owner}); err != nil {
					return res, fmt.Errorf("%s: %w", nodeName(e), err)
				}
			}
			for _, id := range e.RemoveLinks {
				// the draft holds a copy of the link of the version it was checked out from: ImpactLinkDelete finds it
				if err := g.ImpactLinkDelete(ctx, c.ID, id, "", ""); err != nil {
					return res, fmt.Errorf("%s: %w", nodeName(e), err)
				}
			}
			for _, l := range links {
				if _, err := g.ImpactLinkCreate(ctx, c.ID, cn.ID, l, "", ""); err != nil {
					return res, fmt.Errorf("%s: %w", nodeName(e), err)
				}
			}
		}
	}
	for _, x := range later {
		to := posts[written[x.l.ToKey]]
		if _, err := g.ImpactLinkCreate(ctx, c.ID, impacts[x.from], LinkWrite{Type: x.l.Type, To: *to, Properties: x.l.Props}, "", ""); err != nil {
			return res, fmt.Errorf("link %s from %s to %s: %w", x.l.Type, nodeName(in.Edits[x.from]), x.l.ToKey, err)
		}
	}
	// The producer's edits go through the rules of every change (ADR 0102): a state edit is a move of the node's draft (a
	// node with none is checked out by the move); then each impact that modifies its node is accepted by the producer
	// (the rationale is the comment) and one that modifies nothing (an edit restating what the node holds) is withdrawn.
	// A commit left with nothing to commit is ErrNothingToCommit.
	move := func(i int, e NodeEdit) error {
		var node domain.NodeID
		var cur string
		ref := e.Pre
		if posts[i] != nil {
			ref = posts[i]
		}
		n, err := g.ChangeNode(ctx, c.ID, "", *ref)
		if err != nil {
			return err
		}
		node, cur = n.ID, n.State
		if cur == "" && g.Types != nil {
			if lc := g.catalog().Lifecycle(in.Edits[i].Type); lc != nil && e.Pre == nil {
				cur = lc.Initial
			}
		}
		if cur == e.State {
			return nil
		}
		if _, err := g.ImpactNodeTransition(ctx, c.ID, NodeTransition{NodeCheckout: NodeCheckout{Impact: impacts[i], Node: node, Rationale: why(e), ProducedBy: in.By}, To: e.State}); err != nil {
			return fmt.Errorf("%s: %w", nodeName(e), err)
		}
		return nil
	}
	for i, e := range in.Edits {
		if e.State != "" {
			if err := move(i, e); err != nil {
				return res, err
			}
		}
	}
	// the producer's acceptances are one review, submitted at once (ADR 0105)
	accept := domain.ReviewBatch{By: in.By}
	for i, e := range in.Edits {
		if impacts[i] == "" {
			continue
		}
		if modifies, err := g.ImpactModifies(ctx, c.ID, impacts[i]); err != nil {
			return res, err
		} else if !modifies {
			if err := g.WithdrawImpact(ctx, c.ID, impacts[i], "", ""); err != nil {
				return res, fmt.Errorf("%s: %w", nodeName(e), err)
			}
			continue
		}
		accept.Verdicts = append(accept.Verdicts, domain.ImpactVerdict{Impact: impacts[i], Status: domain.ReviewAccepted, Comment: why(e)})
	}
	if len(accept.Verdicts) == 0 {
		return res, fmt.Errorf("commit %q: every edit restates what the graph holds: %w", in.Title, ErrNothingToCommit)
	}
	if _, err := g.ImpactNodeReviewBatch(ctx, c.ID, accept); err != nil {
		return res, fmt.Errorf("commit %q: %w", in.Title, err)
	}
	// the writer's events ride on the commit (ADR 0136): raised with change.applied, in its transaction
	if res.Baseline, err = g.apply(withCarried(ctx, in.Events), c.ID, in.BaselineName); err != nil {
		return res, err
	}
	if done, err := g.Change(ctx, c.ID); err != nil {
		return res, err
	} else if done.Status == domain.ChangeCommitted {
		return res, fmt.Errorf("commit %q conflicts with a concurrent change: %w", in.Title, ErrConflict)
	}
	return res, nil
}

// linksTo reports whether a version already has an outgoing link of the type to the node with these attributes, other
// than the ones the edit removes.
func (g *Graph) linksTo(ctx context.Context, from domain.NodeRef, typ string, to domain.NodeID, props map[string]any, removed []domain.LinkID) (has bool, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		out, err := tx.OutLinks(ctx, from)
		for _, l := range out {
			has = has || l.Type == typ && l.To.ID == to && !slices.Contains(removed, l.ID) && sameLinkProps(l.Properties, props)
		}
		return err
	})
	return
}

// sameLinkProps compares the attributes of two links, no attribute and an empty set alike.
func sameLinkProps(a, b map[string]any) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func nodeName(e NodeEdit) string {
	if e.Pre != nil {
		return e.Pre.String()
	}
	return e.Key
}

// commitOrder orders the edits so that what a link points to is written before
// the node that links to it: an edit comes after the created node it links to by
// key, after the modified node it links to, and (for a modified node) after the
// modified nodes its current version links to. Cycles among modified nodes keep
// the given order; a cycle among created nodes is written in the given order, the links that close it added after the nodes.
func (g *Graph) commitOrder(ctx context.Context, edits []NodeEdit) ([]int, error) {
	byKey := map[string]int{}
	byNode := map[domain.NodeID]int{}
	for i, e := range edits {
		if e.Pre == nil {
			if _, dup := byKey[e.Key]; dup {
				return nil, fmt.Errorf("node %q is created twice: %w", e.Key, ErrInvalid)
			}
			byKey[e.Key] = i
		} else {
			byNode[e.Pre.ID] = i
		}
	}
	type dep struct {
		on int
	}
	deps := make([][]dep, len(edits))
	err := g.repo.InTx(ctx, func(tx Tx) error {
		for i, e := range edits {
			for _, l := range e.Links {
				if l.ToKey != "" {
					if j, ok := byKey[l.ToKey]; ok {
						deps[i] = append(deps[i], dep{j})
					}
				} else if l.To != nil {
					if j, ok := byNode[l.To.ID]; ok && j != i {
						deps[i] = append(deps[i], dep{j})
					}
				}
			}
			if e.Pre == nil {
				continue
			}
			out, err := tx.OutLinks(ctx, *e.Pre)
			if err != nil {
				return err
			}
			for _, l := range out {
				if j, ok := byNode[l.To.ID]; ok && j != i {
					deps[i] = append(deps[i], dep{j})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var order []int
	state := make([]int, len(edits)) // 0 new, 1 visiting, 2 done
	var visit func(i int) error
	visit = func(i int) error {
		switch state[i] {
		case 2:
			return nil
		case 1:
			return nil // a cycle: keep the given order, the link that closes it is added after the nodes
		}
		state[i] = 1
		for _, d := range deps[i] {
			if err := visit(d.on); err != nil {
				return err
			}
		}
		state[i] = 2
		order = append(order, i)
		return nil
	}
	for i := range edits {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return order, nil
}
