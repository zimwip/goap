package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/zimwip/goap/pkg/domain"
)

// Apply materializes the non-rejected proposals of a change into new node
// versions and a new baseline (the target graph).
//
// Versioning rules:
//   - outgoing links are part of the source node version: adding or removing
//     an outgoing link on an existing node creates a new version of that node;
//   - a node that gets a new version carries its outgoing links forward,
//     re-targeted to the new versions of nodes also changed by this change;
//   - incoming links from nodes not touched by the change are NOT carried: they
//     keep pointing to the previous version and become suspect links.
func (g *Graph) Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	var result domain.Baseline
	err := g.repo.InTx(ctx, func(tx Tx) (err error) {
		result, err = g.applyTx(ctx, tx, id, baselineName)
		return err
	})
	return result, err
}

func (g *Graph) applyTx(ctx context.Context, tx Tx, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return domain.Baseline{}, err
	}
	if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned {
		return domain.Baseline{}, fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
	}
	base, err := tx.Baseline(ctx, c.BaselineID)
	if err != nil {
		return domain.Baseline{}, err
	}
	a := &applier{g: g, tx: tx, ctx: ctx, change: c, branch: domain.BranchOf(c.Branch), target: maps.Clone(base.Nodes),
		bumped: map[domain.NodeID]*bump{}, created: map[domain.ItemID]domain.NodeRef{}, removed: map[domain.LinkID]bool{}}
	if err := a.run(); err != nil {
		return domain.Baseline{}, err
	}
	if baselineName == "" {
		baselineName = c.Title
	}
	result := domain.Baseline{ID: domain.BaselineID(g.newID()), Name: baselineName, Branch: a.branch, ParentID: base.ID, ChangeID: c.ID, Nodes: a.target, CreatedAt: g.now()}
	if err := tx.PutBaseline(ctx, result); err != nil {
		return domain.Baseline{}, err
	}
	if err := g.advanceBranch(ctx, tx, a.branch, result.ID); err != nil {
		return domain.Baseline{}, err
	}
	c.Status = domain.ChangeApplied
	c.ResultBaselineID = result.ID
	return result, tx.PutChange(ctx, c)
}

// advanceBranch moves the head of a branch (main is created on first use).
func (g *Graph) advanceBranch(ctx context.Context, tx Tx, name string, head domain.BaselineID) error {
	b, err := tx.Branch(ctx, name)
	switch {
	case errors.Is(err, ErrNotFound) && name == domain.MainBranch:
		b = domain.Branch{Name: name, Status: domain.BranchOpen, CreatedAt: g.now()}
	case err != nil:
		return err
	}
	b.Head = head
	return tx.PutBranch(ctx, b)
}

// nextVersion returns the next version number of a node (numbered across branches).
func nextVersion(ctx context.Context, tx Tx, id domain.NodeID) (domain.Version, error) {
	vs, err := tx.Versions(ctx, id)
	if err != nil {
		return 0, err
	}
	return domain.Version(len(vs) + 1), nil
}

type bump struct {
	base    domain.Node
	props   map[string]any
	deleted bool
	added   []domain.LinkDraft
	next    domain.NodeRef
	// merge_node: base is the version on the change branch (zero when the
	// node is new there), from the merged version and ancestor their common ancestor.
	merge    bool
	from     domain.Node
	ancestor *domain.Node
}

type applier struct {
	g        *Graph
	tx       Tx
	ctx      context.Context
	change   domain.ChangeSet
	branch   string
	target   map[domain.NodeID]domain.Version
	bumped   map[domain.NodeID]*bump
	order    []domain.NodeID
	created  map[domain.ItemID]domain.NodeRef
	removed  map[domain.LinkID]bool
	newLinks []domain.LinkDraft // links whose source is a created node
}

func (a *applier) bumpOf(ref domain.NodeRef) (*bump, error) {
	if b, ok := a.bumped[ref.ID]; ok {
		if b.merge {
			return nil, fmt.Errorf("node %s is both merged and modified: %w", ref.ID, ErrConflict)
		}
		if b.base.Version != ref.Version {
			return nil, fmt.Errorf("node %s referenced at two versions (v%d, v%d): %w", ref.ID, b.base.Version, ref.Version, ErrConflict)
		}
		return b, nil
	}
	if v, ok := a.target[ref.ID]; !ok || v != ref.Version {
		return nil, fmt.Errorf("node %s is not in the reference baseline: %w", ref, ErrConflict)
	}
	n, err := a.tx.Node(a.ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := a.checkHead(ref); err != nil {
		return nil, err
	}
	b := &bump{base: n, props: n.Properties}
	a.bumped[ref.ID] = b
	a.order = append(a.order, ref.ID)
	return b, nil
}

// checkHead fails when the node moved on the change branch since ref.
func (a *applier) checkHead(ref domain.NodeRef) error {
	latest, err := a.tx.LatestOn(a.ctx, ref.ID, a.branch)
	if errors.Is(err, ErrNotFound) {
		return nil // first version of the node on this branch (derive)
	}
	if err != nil {
		return err
	}
	if latest.Version != ref.Version {
		return fmt.Errorf("node %s was modified on %s since the reference baseline (now v%d): %w", ref, a.branch, latest.Version, ErrConflict)
	}
	return nil
}

// mergeOf registers a merge_node proposal.
func (a *applier) mergeOf(d *domain.NodeDraft) error {
	from, err := a.tx.Node(a.ctx, *d.From)
	if err != nil {
		return err
	}
	if _, ok := a.bumped[from.ID]; ok {
		return fmt.Errorf("node %s is both merged and modified: %w", from.ID, ErrConflict)
	}
	b := &bump{merge: true, from: from, props: d.Properties, deleted: from.Deleted}
	if d.Base != nil {
		if v, ok := a.target[d.Base.ID]; !ok || v != d.Base.Version {
			return fmt.Errorf("node %s is not in the reference baseline: %w", d.Base, ErrConflict)
		}
		if err := a.checkHead(*d.Base); err != nil {
			return err
		}
		if b.base, err = a.tx.Node(a.ctx, *d.Base); err != nil {
			return err
		}
		anc := d.Ancestor
		if anc == nil {
			r, err := CommonAncestor(a.ctx, a.tx, from.ID, d.Base.Version, from.Version)
			if err != nil {
				return err
			}
			anc = &r
		}
		an, err := a.tx.Node(a.ctx, *anc)
		if err != nil {
			return err
		}
		b.ancestor = &an
		if b.props == nil {
			b.props, _ = MergeProps(an.Properties, b.base.Properties, from.Properties)
		}
		b.deleted = b.base.Deleted || from.Deleted
	} else if _, ok := a.target[from.ID]; ok {
		return fmt.Errorf("node %s is already in the reference baseline, merge_node requires node.base: %w", from.ID, ErrConflict)
	} else if b.props == nil {
		b.props = from.Properties
	}
	a.bumped[from.ID] = b
	a.order = append(a.order, from.ID)
	return nil
}

func (a *applier) run() error {
	var proposals []domain.ChangeItem
	for _, it := range a.change.Items {
		if st := a.change.EffectiveStatus(it.ID); it.Kind == domain.KindProposal && st != domain.ItemRejected && st != domain.ItemSuperseded {
			proposals = append(proposals, it)
		}
	}
	// 1. node operations
	for _, it := range proposals {
		p := it.Proposal
		switch p.Op {
		case domain.OpCreateNode:
			n := domain.Node{ID: domain.NodeID(a.g.newID()), Version: 1, Branch: a.branch, Reason: domain.ReasonCreate,
				Key: p.Node.Key, Type: p.Node.Type, Properties: p.Node.Properties, ChangeID: a.change.ID, CreatedAt: a.g.now()}
			if n.Key == "" {
				n.Key = string(n.ID)
			}
			if err := a.tx.PutNode(a.ctx, n); err != nil {
				return err
			}
			a.created[it.ID] = n.Ref()
			a.target[n.ID] = 1
		case domain.OpUpdateNode:
			b, err := a.bumpOf(*p.Node.Base)
			if err != nil {
				return err
			}
			merged := maps.Clone(b.props)
			if merged == nil {
				merged = map[string]any{}
			}
			maps.Copy(merged, p.Node.Properties)
			b.props = merged
		case domain.OpDeleteNode:
			b, err := a.bumpOf(*p.Node.Base)
			if err != nil {
				return err
			}
			b.deleted = true
		case domain.OpMergeNode:
			if err := a.mergeOf(p.Node); err != nil {
				return err
			}
		}
	}
	// 2. link operations
	for _, it := range proposals {
		p := it.Proposal
		switch p.Op {
		case domain.OpAddLink:
			if p.Link.From.Item != "" {
				a.newLinks = append(a.newLinks, *p.Link)
				continue
			}
			b, err := a.bumpOf(*p.Link.From.Node)
			if err != nil {
				return err
			}
			b.added = append(b.added, *p.Link)
		case domain.OpRemoveLink:
			l, err := a.findLink(p.Link.LinkID)
			if err != nil {
				return err
			}
			if _, err := a.bumpOf(l.From); err != nil {
				return err
			}
			a.removed[l.ID] = true
		}
	}
	// 3. new versions of bumped nodes
	for _, id := range a.order {
		b := a.bumped[id]
		n := b.base
		switch {
		case b.merge:
			n = b.from
			n.Reason, n.Parents = domain.ReasonMerge, []domain.Version{b.from.Version}
			if b.ancestor != nil {
				n.Parents = []domain.Version{b.base.Version, b.from.Version}
			}
		case domain.BranchOf(b.base.Branch) == a.branch:
			n.Reason, n.Parents = domain.ReasonRevise, []domain.Version{b.base.Version}
		default:
			n.Reason, n.Parents = domain.ReasonDerive, []domain.Version{b.base.Version}
		}
		v, err := nextVersion(a.ctx, a.tx, id)
		if err != nil {
			return err
		}
		n.Version, n.Branch = v, a.branch
		n.Properties = b.props
		n.Deleted = b.deleted
		n.ChangeID = a.change.ID
		n.CreatedAt = a.g.now()
		if err := a.tx.PutNode(a.ctx, n); err != nil {
			return err
		}
		b.next = n.Ref()
		if b.deleted {
			delete(a.target, id)
		} else {
			a.target[id] = n.Version
		}
	}
	// 4. links of bumped nodes: carry forward + added
	for _, id := range a.order {
		b := a.bumped[id]
		if b.deleted {
			continue
		}
		if b.merge {
			if err := a.mergeLinks(b); err != nil {
				return err
			}
			continue
		}
		out, err := a.tx.OutLinks(a.ctx, b.base.Ref())
		if err != nil {
			return err
		}
		for _, l := range out {
			if a.removed[l.ID] {
				continue
			}
			to, ok := a.remap(l.To)
			if !ok {
				continue // target deleted or outside the target graph
			}
			if err := a.putLink(l.Type, b.next, to, l.Properties); err != nil {
				return err
			}
		}
		for _, d := range b.added {
			if err := a.addDraft(b.next, d); err != nil {
				return err
			}
		}
	}
	// 5. links from created nodes
	for _, d := range a.newLinks {
		from, ok := a.created[d.From.Item]
		if !ok {
			return fmt.Errorf("link source item %s is not a created node: %w", d.From.Item, ErrInvalid)
		}
		if err := a.addDraft(from, d); err != nil {
			return err
		}
	}
	return nil
}

func (a *applier) addDraft(from domain.NodeRef, d domain.LinkDraft) error {
	var to domain.NodeRef
	if d.To.Item != "" {
		r, ok := a.created[d.To.Item]
		if !ok {
			return fmt.Errorf("link target item %s is not a created node: %w", d.To.Item, ErrInvalid)
		}
		to = r
	} else {
		r, ok := a.remap(*d.To.Node)
		if !ok {
			return fmt.Errorf("link target %s is not in the target graph: %w", d.To.Node, ErrConflict)
		}
		to = r
	}
	return a.putLink(d.Type, from, to, d.Properties)
}

// remap returns the version of ref in the target graph when ref is the
// reference version of a node changed by this change.
func (a *applier) remap(ref domain.NodeRef) (domain.NodeRef, bool) {
	if b, ok := a.bumped[ref.ID]; ok && b.base.Version == ref.Version {
		if b.deleted {
			return domain.NodeRef{}, false
		}
		return b.next, true
	}
	// keep the link on the exact version it was asserted on (possibly suspect)
	_, ok := a.target[ref.ID]
	return ref, ok
}

// mergeLinks carries the 3-way merge of the outgoing links of a merged node:
// links are keyed by (type, target node); a link is kept when present on both
// sides or added on one side since the ancestor, and dropped when removed on
// one side.
func (a *applier) mergeLinks(b *bump) error {
	theirs, err := a.tx.OutLinks(a.ctx, b.from.Ref())
	if err != nil {
		return err
	}
	var ours, anc []domain.Link
	if b.ancestor != nil {
		if ours, err = a.tx.OutLinks(a.ctx, b.base.Ref()); err != nil {
			return err
		}
		if anc, err = a.tx.OutLinks(a.ctx, b.ancestor.Ref()); err != nil {
			return err
		}
	} else {
		ours, anc = theirs, theirs // new node: take the source links
	}
	for _, l := range MergeLinks(anc, ours, theirs) {
		to, ok := a.remapID(l.To)
		if !ok {
			continue
		}
		if err := a.putLink(l.Type, b.next, to, l.Properties); err != nil {
			return err
		}
	}
	return nil
}

// remapID resolves a link target by node id: the new version when the node
// changed in this apply, else the given version when the node is in the target graph.
func (a *applier) remapID(ref domain.NodeRef) (domain.NodeRef, bool) {
	if b, ok := a.bumped[ref.ID]; ok {
		if b.deleted {
			return domain.NodeRef{}, false
		}
		return b.next, true
	}
	_, ok := a.target[ref.ID]
	return ref, ok
}

func (a *applier) putLink(typ string, from, to domain.NodeRef, props map[string]any) error {
	return a.tx.PutLink(a.ctx, domain.Link{ID: domain.LinkID(a.g.newID()), Type: typ, From: from, To: to, Properties: props, ChangeID: a.change.ID})
}

func (a *applier) findLink(id domain.LinkID) (domain.Link, error) {
	for nid, v := range a.target {
		out, err := a.tx.OutLinks(a.ctx, domain.NodeRef{ID: nid, Version: v})
		if err != nil {
			return domain.Link{}, err
		}
		for _, l := range out {
			if l.ID == id {
				return l, nil
			}
		}
	}
	return domain.Link{}, fmt.Errorf("link %s not in reference baseline: %w", id, ErrNotFound)
}
