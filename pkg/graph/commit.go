package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
)

// This file is the producer-facing way of making a change of node edits
// (ADR 0024): the producer says which nodes it creates or modifies and why; the
// change, its change impacts, the versions on the change branch, the reviews and
// the apply follow. Services that project their own definitions into the graph
// (registry, seeds) use it.

// NodeEdit is one node created or modified by a Commit.
type NodeEdit struct {
	// Key and Type name a created node (Pre is nil).
	Key, Type string
	// Pre is the version a modified node starts from (in the reference baseline).
	Pre *domain.NodeRef
	// Props are merged over the current properties (a nil value clears one).
	Props map[string]any
	// State moves the node to a lifecycle state, after its other edits, checked in: a transition of its own
	// (ImpactNodeTransition, ADR 0076); before them when the node rests in a state that is not editable and State is
	// (a retired entry restored).
	State string
	// Owner transfers the node to another organisational unit (its key, ADR 0054); empty: unchanged, or the unit
	// holding the commit for a created node.
	Owner string
	// Rationale says why; the title of the commit when empty.
	Rationale string
	Links     []LinkEdit
	// RemoveLinks are outgoing links of the Pre version the new version leaves out (removing a child is a
	// modification of its parent, ADR 0024 §4).
	RemoveLinks []domain.LinkID
}

// LinkEdit is an outgoing link added by an edit: to an existing version (To) or
// to a node created by another edit of the commit (ToKey). Nodes created by the commit may link each other in
// any direction, cycles included: a link to a node not written yet is added, in the same change, once it is.
type LinkEdit struct {
	Type  string
	To    *domain.NodeRef
	ToKey string
	Props map[string]any
}

// Commit describes a change made of node edits, applied as one unit.
type Commit struct {
	Namespace, Title, Intent, Methodology string
	Data                                  map[string]any
	Baseline                              domain.BaselineID
	// Branch the commit lands on (default main).
	Branch string
	// By is who accepts the edits: the producer.
	By           string
	BaselineName string
	Edits        []NodeEdit
	// OwnerOrg is the unit holding the commit and ProjectID the project it acts in (ADR 0054); empty: the root unit,
	// the default project.
	OwnerOrg, ProjectID string
}

// CommitResult is the outcome of a Commit.
type CommitResult struct {
	Change   domain.ChangeID
	Baseline domain.Baseline
}

// Commit runs the edits as one change, through the operations every change uses (ADR 0076): it opens the change on a
// branch of its own; a created node is a ImpactNodeCreate, a modified one a ImpactNodeCheckout with its ImpactNodeUpdate and link edits;
// each impact is accepted (the rationale is the comment) and checked in, then the state edits are transitions
// (ImpactNodeTransition), and the change is applied. When the branch cannot be merged without conflict (another change moved
// a node meanwhile) the change is abandoned and ErrConflict returned: the producer reads again and rebuilds its edits.
// A created node of a structure or a User must name its required parent (checkRequiredParent, ADR 0040).
func (g *Graph) Commit(ctx context.Context, in Commit) (res CommitResult, err error) {
	if len(in.Edits) == 0 {
		return res, fmt.Errorf("a commit needs at least one edit: %w", ErrInvalid)
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
	var impacts []domain.ChangeImpactID
	defer func() {
		if err == nil {
			return
		}
		// a creation that was not checked in leaves no node behind: its key stays free (ADR 0076 §5b)
		for i, e := range in.Edits {
			if e.Pre == nil && i < len(impacts) && impacts[i] != "" {
				if out, _ := g.isCheckedOut(ctx, c.ID, impacts[i]); out {
					if _, cerr := g.ImpactNodeCancel(ctx, c.ID, impacts[i], "", ""); cerr != nil {
						err = errors.Join(err, cerr)
					}
				}
			}
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
	impacts = make([]domain.ChangeImpactID, len(in.Edits))
	posts := make([]*domain.NodeRef, len(in.Edits)) // the working versions
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
				// a link is part of its source version: the one the version already carries is not added again
				if has, err := g.linksTo(ctx, *e.Pre, l.Type, to.ID); err != nil {
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
			// a node at rest out of the editable states (a retired entry restored) is moved first, then edited
			if reopen, err := g.reopens(ctx, *e.Pre, e.State); err != nil {
				return res, err
			} else if reopen {
				if _, err := g.ImpactNodeTransition(ctx, c.ID, NodeTransition{NodeCheckout: NodeCheckout{Impact: impacts[i], Rationale: why(e), ProducedBy: in.By}, To: e.State}); err != nil {
					return res, fmt.Errorf("%s: %w", nodeName(e), err)
				}
			}
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
			for _, l := range links {
				if _, err := g.ImpactLinkCreate(ctx, c.ID, cn.ID, l, "", ""); err != nil {
					return res, fmt.Errorf("%s: %w", nodeName(e), err)
				}
			}
			for _, id := range e.RemoveLinks {
				if err := g.deleteCopiedLink(ctx, c.ID, *e.Pre, *cn.Post, id); err != nil {
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
	// The nodes moved to a state are checked in and moved first: a transition is a version of its own, and the links
	// the working versions of the change hold to a node follow its new versions until they are checked in (ADR 0076).
	freeze := func(i int, e NodeEdit) error {
		if impacts[i] == "" {
			return nil
		}
		if _, err := g.ImpactNodeReview(ctx, c.ID, impacts[i], domain.ReviewAccepted, in.By, why(e)); err != nil {
			return err
		}
		if posts[i] != nil {
			if _, err := g.ImpactNodeCheckin(ctx, c.ID, impacts[i], "", ""); err != nil {
				return fmt.Errorf("%s: %w", nodeName(e), err)
			}
		}
		return nil
	}
	move := func(i int, e NodeEdit) error {
		var node domain.NodeID
		var cur string
		ref := e.Pre
		if posts[i] != nil {
			ref = posts[i]
		}
		n, err := g.Node(ctx, *ref)
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
		if e.State == "" {
			continue
		}
		if err := freeze(i, e); err != nil {
			return res, err
		}
		if err := move(i, e); err != nil {
			return res, err
		}
	}
	for i, e := range in.Edits {
		if e.State != "" {
			continue
		}
		if err := freeze(i, e); err != nil {
			return res, err
		}
	}
	if res.Baseline, err = g.Apply(ctx, c.ID, in.BaselineName); err != nil {
		return res, err
	}
	if done, err := g.Change(ctx, c.ID); err != nil {
		return res, err
	} else if done.Status == domain.ChangeCommitted {
		return res, fmt.Errorf("commit %q conflicts with a concurrent change: %w", in.Title, ErrConflict)
	}
	return res, nil
}

// deleteCopiedLink removes from a working version the copy of an outgoing link of the version it follows: the
// checkout copied the links (new ids), the edit names the one it read.
func (g *Graph) deleteCopiedLink(ctx context.Context, id domain.ChangeID, pre, work domain.NodeRef, link domain.LinkID) error {
	var target domain.LinkID
	err := g.repo.InTx(ctx, func(tx Tx) error {
		l, err := tx.Link(ctx, link)
		if err != nil {
			return err
		}
		if l.From != pre {
			return invalidf("link %s is not an outgoing link of %s", link, pre)
		}
		out, err := tx.OutLinks(ctx, work)
		if err != nil {
			return err
		}
		for _, c := range out {
			if c.Type == l.Type && c.To.ID == l.To.ID {
				target = c.ID
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if target == "" {
		return invalidf("link %s is not an outgoing link of %s", link, pre)
	}
	return g.ImpactLinkDelete(ctx, id, target, "", "")
}

// linksTo reports whether a version already has an outgoing link of the type to the node.
func (g *Graph) linksTo(ctx context.Context, from domain.NodeRef, typ string, to domain.NodeID) (has bool, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		out, err := tx.OutLinks(ctx, from)
		for _, l := range out {
			has = has || l.Type == typ && l.To.ID == to
		}
		return err
	})
	return
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

// reopens tells whether an edit moving a node to state must take the transition before its other edits: the node
// rests in a state that is not editable and state is.
func (g *Graph) reopens(ctx context.Context, pre domain.NodeRef, state string) (bool, error) {
	if state == "" || g.Types == nil {
		return false, nil
	}
	n, err := g.Node(ctx, pre)
	if err != nil {
		return false, err
	}
	lc := g.catalog().Lifecycle(n.Type)
	return lc != nil && n.State != "" && !lc.Editable(n.State) && lc.Editable(state), nil
}

// isCheckedOut reports a change impact whose version on the main flow of the change is a working version.
func (g *Graph) isCheckedOut(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID) (out bool, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.workOn(ctx, tx, id, domain.MainFlow)
		if err != nil {
			return err
		}
		if err := w.selectImpact(impact); err != nil {
			return err
		}
		h, err := w.head(ctx, tx)
		out = err == nil && h != nil && h.CheckedOut
		return err
	})
	return
}
