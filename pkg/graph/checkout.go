package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/zimwip/goap/pkg/domain"
)

// This file is how a change edits nodes (ADR 0076). Every write names the change; a node is edited through a working
// version, the version a CreateNode or a CheckoutNode writes on the branch of the change (of the flow), checked out:
//
//   - UpdateNode and the link operations edit the working version in place, writing no version;
//   - an accepted review authorizes CheckinNode, which freezes it; changing it again is a new CheckoutNode;
//   - a lifecycle transition is a version of its own, from a checked-in version (TransitionNode): checkout, the move
//     and check-in in one operation, authorized and guarded when it is taken;
//   - CancelCheckout drops the working version; a creation cancelled before its first check-in removes the node.

// NodeCreate is a node CreateNode creates.
type NodeCreate struct {
	Key, Type  string
	Properties map[string]any
	// Owner is the key of the organisational unit owning the node (ADR 0054); empty: the unit holding the change.
	Owner     string
	Rationale string
	// Links are outgoing links of the working version to exact node versions.
	Links []LinkWrite
	// Flow is the flow the call works on ("": the active option, else the main flow; domain.MainFlow names the main
	// flow) and Execution the action run that makes it (ADR 0025).
	Flow, Execution string
	ProducedBy      string
	DerivedFrom     []domain.ItemID
}

// NodeCheckout names the node CheckoutNode or TransitionNode works on: a change impact, or a node (then the impact the
// flow sees on it, declared when the change holds none).
type NodeCheckout struct {
	Impact domain.ChangeImpactID
	Node   domain.NodeID
	// Rationale says why, when the call declares the impact.
	Rationale       string
	Flow, Execution string
	ProducedBy      string
}

// NodeTransition moves a node along its lifecycle (TransitionNode): To is the state it goes to.
type NodeTransition struct {
	NodeCheckout
	To string
}

// NodeUpdate edits a working version in place.
type NodeUpdate struct {
	// Properties are merged over the ones of the working version.
	Properties map[string]any
	// Owner transfers the node to another organisational unit (its key, ADR 0054).
	Owner           string
	Flow, Execution string
}

// LinkWrite is an outgoing link of a working version, to an exact node version.
type LinkWrite struct {
	Type       string
	To         domain.NodeRef
	Properties map[string]any
}

// work is what an operation on a change impact needs: the change, the flow it works on and the branch versions are
// written on, and the change impact as the flow sees it.
type work struct {
	c      domain.Change
	flow   string
	branch string
	ns     string
	ix     *typeIndex
	seen   []domain.ChangeImpact
	cn     domain.ChangeImpact // the stored change impact
	vcn    domain.ChangeImpact // as the flow sees it
}

// workOn opens an operation on a change, on a flow: the change gets its branch, the flow its own.
func (g *Graph) workOn(ctx context.Context, tx Tx, id domain.ChangeID, flow string) (*work, error) {
	c, err := changeOpen(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	own, ok, err := ownBranch(ctx, tx, c)
	if err != nil {
		return nil, err
	} else if !ok {
		if c, own, err = g.ensureOwnBranch(ctx, tx, c); err != nil {
			return nil, err
		}
	}
	w := &work{c: c, flow: c.ResolveFlow(flow), branch: domain.BranchOf(c.Branch), ns: domain.NamespaceOf(c.Namespace)}
	if w.flow != "" {
		if _, ok := c.Flow(w.flow); !ok {
			return nil, fmt.Errorf("flow %s: %w", w.flow, ErrNotFound)
		}
		if c.FlowStatusOf(w.flow) != domain.FlowOpen {
			return nil, fmt.Errorf("flow %s is not open: %w", w.flow, ErrConflict)
		}
		if w.branch, err = g.ensureFlowBranch(ctx, tx, c, own, w.flow); err != nil {
			return nil, err
		}
	}
	if w.ix, err = g.typesAt(ctx, tx, c.BaselineID); err != nil {
		return nil, err
	}
	if w.seen, err = g.newFlowNodes(tx, c, w.flow).nodes(ctx); err != nil {
		return nil, err
	}
	return w, nil
}

// impact selects the change impact the operation works on, to edit it: one written in a state of the lifecycle of the
// change that the change has left is frozen (ADR 0058).
func (g *Graph) impact(ctx context.Context, tx Tx, w *work, id domain.ChangeImpactID) error {
	if err := w.selectImpact(id); err != nil {
		return err
	}
	if frozen, in, err := g.frozen(ctx, tx, w.c, id); err != nil {
		return err
	} else if frozen {
		return fmt.Errorf("change impact %s was written in state %s, which the change has left (now %s): go back to it first: %w", id, in, w.c.State, ErrConflict)
	}
	return nil
}

// selectImpact selects the change impact the operation works on, as the flow sees it.
func (w *work) selectImpact(id domain.ChangeImpactID) error {
	i, err := findChangeImpact(w.c, id)
	if err != nil {
		return err
	}
	w.cn = w.c.Nodes[i]
	found := false
	for _, cn := range w.seen {
		if cn.ID == id {
			w.vcn, found = cn, true
		}
	}
	if !found {
		return fmt.Errorf("change impact %s is not on the flow %q of change %s: %w", id, w.flow, w.c.ID, ErrNotFound)
	}
	return nil
}

// resolve finds the change impact a checkout or a transition names, declaring it (intent modified, from the version of
// the reference baseline) when the change holds none on the node.
func (g *Graph) resolve(ctx context.Context, tx Tx, id domain.ChangeID, in NodeCheckout) (*work, error) {
	w, err := g.workOn(ctx, tx, id, in.Flow)
	if err != nil {
		return nil, err
	}
	impact := in.Impact
	if impact == "" {
		if in.Node == "" {
			return nil, invalidf("name a change impact or a node")
		}
		for _, cn := range w.seen {
			if nodeOf(cn) == in.Node {
				impact = cn.ID
			}
		}
	}
	if impact == "" {
		b, err := tx.Baseline(ctx, w.c.BaselineID)
		if err != nil {
			return nil, err
		}
		v, ok := b.Nodes[in.Node]
		if !ok {
			return nil, fmt.Errorf("node %s is not in the reference baseline of change %s: %w", in.Node, id, ErrNotFound)
		}
		decl, err := g.declareTx(ctx, tx, id, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &domain.NodeRef{ID: in.Node, Version: v},
			Rationale: in.Rationale, Flow: in.Flow, Execution: in.Execution, ProducedBy: in.ProducedBy}})
		if err != nil {
			return nil, err
		}
		impact = decl[0].ID
		if w, err = g.workOn(ctx, tx, id, in.Flow); err != nil {
			return nil, err
		}
	}
	return w, g.impact(ctx, tx, w, impact)
}

// head is the version the flow sees for the impact (on the main flow, the latest of its node on the change branch);
// nil when the impact has none.
func (w *work) head(ctx context.Context, tx Tx) (*domain.Node, error) {
	if w.vcn.Post == nil {
		return nil, nil
	}
	if w.flow != "" {
		n, err := tx.Node(ctx, *w.vcn.Post)
		return &n, err
	}
	n, err := tx.LatestOn(ctx, w.vcn.Post.ID, w.branch)
	return &n, err
}

// base is what a new version of the impact starts from: its head, else its pre version (on the main flow, the latest
// of the node on the change branch, where a sub-change may have merged one).
func (w *work) base(ctx context.Context, tx Tx) (*domain.Node, error) {
	if h, err := w.head(ctx, tx); h != nil || err != nil {
		return h, err
	}
	if w.cn.Pre == nil {
		return nil, nil
	}
	n, err := tx.Node(ctx, *w.cn.Pre)
	if err != nil {
		return nil, err
	}
	if w.flow == "" {
		if latest, err := tx.LatestOn(ctx, n.ID, w.branch); err == nil {
			n = latest
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return &n, nil
}

// working is the working version of the impact, editable: checked out and not reviewed yet.
func (w *work) working(ctx context.Context, tx Tx) (domain.Node, error) {
	h, err := w.head(ctx, tx)
	if err != nil {
		return domain.Node{}, err
	}
	if h == nil || !h.CheckedOut {
		return domain.Node{}, fmt.Errorf("change impact %s (%s) is not checked out: check it out first: %w", w.cn.ID, w.cn.Key, ErrConflict)
	}
	switch w.vcn.Review {
	case domain.ReviewAccepted:
		return domain.Node{}, fmt.Errorf("change impact %s (%s) is accepted: check it in, then check it out again to change it: %w", w.cn.ID, w.cn.Key, ErrConflict)
	case domain.ReviewRejected:
		return domain.Node{}, fmt.Errorf("change impact %s is rejected: %w", w.cn.ID, ErrConflict)
	}
	return *h, nil
}

// newVersion is the next version of the impact's node on the branch the operation writes on, from base (nil: the
// creation of the node), with its properties and state.
func (g *Graph) newVersion(ctx context.Context, tx Tx, w *work, base *domain.Node, execution string) (domain.Node, error) {
	n := domain.Node{Branch: w.branch, Namespace: w.ns, Key: w.cn.Key, Type: w.cn.Type, ChangeID: w.c.ID, ChangeImpact: w.cn.ID,
		CreatedAt: g.now(), Comment: w.cn.Rationale, Execution: execution}
	if base == nil {
		n.ID, n.Version, n.Reason = domain.NodeID(g.newID()), 1, domain.ReasonCreate
		if lc := w.ix.lifecycleOf(n.Type); lc != nil {
			n.State = lc.Initial
		}
		return n, nil
	}
	if domain.NamespaceOf(base.Namespace) != w.ns {
		return n, invalidf("node %s is in namespace %s, the change acts on %s", base.Key, domain.NamespaceOf(base.Namespace), w.ns)
	}
	v, err := nextVersion(ctx, tx, base.ID)
	if err != nil {
		return n, err
	}
	n.ID, n.Version, n.Parents, n.Properties, n.State = base.ID, v, []domain.Version{base.Version}, maps.Clone(base.Properties), base.State
	n.Reason = domain.ReasonRevise
	if on, err := onBranch(ctx, tx, base.Ref(), w.branch); err != nil {
		return n, err
	} else if !on {
		n.Reason = domain.ReasonDerive
	}
	return n, nil
}

// putVersion stores a new version of the impact's node with the outgoing links of the version it follows (retargeted
// to the versions the flow sees for the nodes of the change), and records it as the impact's post.
func (g *Graph) putVersion(ctx context.Context, tx Tx, w *work, n domain.Node, base *domain.Node) (domain.NodeRef, error) {
	if err := tx.PutNode(ctx, n); err != nil {
		return domain.NodeRef{}, err
	}
	ref := n.Ref()
	if base != nil {
		out, err := tx.OutLinks(ctx, base.Ref())
		if err != nil {
			return ref, err
		}
		for _, l := range out {
			if err := tx.PutLink(ctx, domain.Link{ID: domain.LinkID(g.newID()), Type: l.Type, From: ref, To: retarget(w.seen, l.To), Properties: l.Properties, ChangeID: w.c.ID}); err != nil {
				return ref, err
			}
		}
	}
	return ref, g.emit(ctx, tx, domain.ImpactEvent{Change: w.c.ID, Impact: w.cn.ID, Op: domain.ImpactWritten, Flow: w.flow, Execution: n.Execution, Post: &ref})
}

// seenAs returns the change impact as the operation leaves it, with its post.
func (w *work) seenAs(post *domain.NodeRef) domain.ChangeImpact {
	cn := w.vcn
	if post != nil {
		p := *post
		cn.Post = &p
	}
	return cn
}

// putLink adds an outgoing link to a working version.
func (g *Graph) putLink(ctx context.Context, tx Tx, w *work, from domain.Node, l LinkWrite) (domain.Link, error) {
	if l.Type == "" || l.To.Version == 0 {
		return domain.Link{}, invalidf("a link needs a type and an exact target version")
	}
	to, err := tx.Node(ctx, l.To)
	if err != nil {
		return domain.Link{}, err
	}
	if err := w.ix.checkLink(l.Type, from.Type, to.Type); err != nil {
		return domain.Link{}, err
	}
	link := domain.Link{ID: domain.LinkID(g.newID()), Type: l.Type, From: from.Ref(), To: retarget(w.seen, l.To), Properties: l.Properties, ChangeID: w.c.ID}
	return link, tx.PutLink(ctx, link)
}

// CreateNode creates a node in a change: it declares the change impact (intent created) and writes the first version
// of the node, checked out (ADR 0076).
func (g *Graph) CreateNode(ctx context.Context, id domain.ChangeID, in NodeCreate) (cn domain.ChangeImpact, err error) {
	if in.Key == "" || in.Type == "" {
		return cn, invalidf("a node needs a key and a type")
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		decl, err := g.declareTx(ctx, tx, id, []domain.ChangeImpact{{Intent: domain.IntentCreated, Key: in.Key, Type: in.Type, Rationale: in.Rationale,
			Flow: in.Flow, Execution: in.Execution, ProducedBy: in.ProducedBy, DerivedFrom: in.DerivedFrom}})
		if err != nil {
			return err
		}
		w, err := g.workOn(ctx, tx, id, in.Flow)
		if err != nil {
			return err
		}
		if err := g.impact(ctx, tx, w, decl[0].ID); err != nil {
			return err
		}
		n, err := g.newVersion(ctx, tx, w, nil, in.Execution)
		if err != nil {
			return err
		}
		n.CheckedOut, n.Properties = true, maps.Clone(in.Properties)
		if err := g.validateProps(ctx, w.ix, n, n.Properties); err != nil {
			return err
		}
		if in.Owner != "" {
			unit, err := g.structureNode(ctx, tx, domain.StructureOrganisation, in.Owner)
			if err != nil {
				return err
			}
			n.Owner = unit.ID
		}
		ref, err := g.putVersion(ctx, tx, w, n, nil)
		if err != nil {
			return err
		}
		for _, l := range in.Links {
			if _, err := g.putLink(ctx, tx, w, n, l); err != nil {
				return err
			}
		}
		cn = w.seenAs(&ref)
		return nil
	})
	return
}

// CheckoutNode puts a node in edit mode in a change: it writes the next version of the node, a copy of the version
// the change sees (properties, state, owner and outgoing links), checked out; for a creation declared before
// (AddNodes), the first version of the node. The node must be in an editable state of
// its lifecycle (a transition reopens it), and not checked out already on the flow. A checkout after a check-in sends
// the review of the impact back to proposed: what is changed is reviewed again.
func (g *Graph) CheckoutNode(ctx context.Context, id domain.ChangeID, in NodeCheckout) (cn domain.ChangeImpact, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.resolve(ctx, tx, id, in)
		if err != nil {
			return err
		}
		if w.vcn.Review == domain.ReviewRejected {
			return fmt.Errorf("change impact %s is rejected: %w", w.cn.ID, ErrConflict)
		}
		base, err := w.base(ctx, tx)
		if err != nil {
			return err
		}
		switch {
		case base == nil && w.cn.Intent != domain.IntentCreated:
			return invalidf("change impact %s has no version to check out", w.cn.ID)
		case base == nil:
			// a creation declared before (AddNodes): its first version
			if err := w.ix.checkNode(w.ns, w.cn.Type); err != nil {
				return err
			}
		case base.CheckedOut:
			return fmt.Errorf("change impact %s (%s) is already checked out: %w", w.cn.ID, w.cn.Key, ErrConflict)
		default:
			if lc := w.ix.lifecycleOf(base.Type); lc != nil && base.State != "" && !lc.Editable(base.State) {
				return invalidf("cannot check out %s (%s): it is %s, not editable; reopen it first with a transition", base.Key, base.Type, base.State)
			}
		}
		n, err := g.newVersion(ctx, tx, w, base, in.Execution)
		if err != nil {
			return err
		}
		n.CheckedOut = true
		if base == nil {
			if err := g.validateProps(ctx, w.ix, n, n.Properties); err != nil {
				return err
			}
		}
		ref, err := g.putVersion(ctx, tx, w, n, base)
		if err != nil {
			return err
		}
		if w.vcn.Review != domain.ReviewProposed {
			r := domain.Review{Status: domain.ReviewProposed, By: g.caller(ctx), At: g.now(), Comment: "checked out again", Flow: w.flow, Execution: in.Execution}
			if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: w.cn.ID, Op: domain.ImpactReviewed, Flow: w.flow, Execution: in.Execution, By: r.By, Review: &r}); err != nil {
				return err
			}
			w.vcn.Review = domain.ReviewProposed
		}
		cn = w.seenAs(&ref)
		return nil
	})
	return
}

// UpdateNode edits the working version of a change impact in place: its properties (merged and validated) and its
// owner. No version is written; the edit is an updated event of the impact log.
func (g *Graph) UpdateNode(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, in NodeUpdate) (cn domain.ChangeImpact, err error) {
	if len(in.Properties) == 0 && in.Owner == "" {
		return cn, invalidf("nothing to update")
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.workOn(ctx, tx, id, in.Flow)
		if err != nil {
			return err
		}
		if err := g.impact(ctx, tx, w, impact); err != nil {
			return err
		}
		n, err := w.working(ctx, tx)
		if err != nil {
			return err
		}
		patch := map[string]any{}
		if len(in.Properties) > 0 {
			props := maps.Clone(n.Properties)
			if props == nil {
				props = map[string]any{}
			}
			maps.Copy(props, in.Properties)
			if err := g.validateProps(ctx, w.ix, n, props); err != nil {
				return err
			}
			if err := tx.SetNodeProps(ctx, n.Ref(), props); err != nil {
				return err
			}
			patch["props"] = in.Properties
		}
		if in.Owner != "" {
			unit, err := g.structureNode(ctx, tx, domain.StructureOrganisation, in.Owner)
			if err != nil {
				return err
			}
			if err := tx.SetNodeOwner(ctx, n.Ref(), unit.ID); err != nil {
				return err
			}
			patch["owner"] = in.Owner
		}
		ref := n.Ref()
		cn = w.seenAs(&ref)
		return g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: impact, Op: domain.ImpactUpdated, Flow: w.flow, Execution: in.Execution, Post: &ref, Patch: patch})
	})
	return
}

// CreateLink adds an outgoing link to the working version of a change impact, in place.
func (g *Graph) CreateLink(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, l LinkWrite, flow, execution string) (link domain.Link, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.workOn(ctx, tx, id, flow)
		if err != nil {
			return err
		}
		if err := g.impact(ctx, tx, w, impact); err != nil {
			return err
		}
		n, err := w.working(ctx, tx)
		if err != nil {
			return err
		}
		if link, err = g.putLink(ctx, tx, w, n, l); err != nil {
			return err
		}
		ref := n.Ref()
		return g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: impact, Op: domain.ImpactUpdated, Flow: w.flow, Execution: execution, Post: &ref,
			Patch: map[string]any{"addLink": map[string]any{"id": string(link.ID), "type": link.Type, "to": link.To.String()}}})
	})
	return
}

// linkWork finds the change impact whose working version is the source of a link.
func (g *Graph) linkWork(ctx context.Context, tx Tx, id domain.ChangeID, link domain.LinkID, flow string) (*work, domain.Link, domain.Node, error) {
	l, err := tx.Link(ctx, link)
	if err != nil {
		return nil, l, domain.Node{}, err
	}
	w, err := g.workOn(ctx, tx, id, flow)
	if err != nil {
		return nil, l, domain.Node{}, err
	}
	for _, cn := range w.seen {
		if cn.Post == nil || cn.Post.ID != l.From.ID {
			continue
		}
		if err := g.impact(ctx, tx, w, cn.ID); err != nil {
			return nil, l, domain.Node{}, err
		}
		n, err := w.working(ctx, tx)
		if err != nil {
			return nil, l, n, err
		}
		if n.Ref() != l.From {
			break
		}
		return w, l, n, nil
	}
	return nil, l, domain.Node{}, fmt.Errorf("link %s is not a link of a working version of change %s: %w", link, id, ErrConflict)
}

// UpdateLink replaces the properties of an outgoing link of a working version, in place.
func (g *Graph) UpdateLink(ctx context.Context, id domain.ChangeID, link domain.LinkID, props map[string]any, flow, execution string) (l domain.Link, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, cur, n, err := g.linkWork(ctx, tx, id, link, flow)
		if err != nil {
			return err
		}
		if err := tx.SetLinkProps(ctx, link, props); err != nil {
			return err
		}
		l = cur
		l.Properties = props
		ref := n.Ref()
		return g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: w.cn.ID, Op: domain.ImpactUpdated, Flow: w.flow, Execution: execution, Post: &ref,
			Patch: map[string]any{"updateLink": map[string]any{"id": string(link), "props": props}}})
	})
	return
}

// DeleteLink removes an outgoing link of a working version, in place: removing a child is a modification of its
// parent (ADR 0024 §4).
func (g *Graph) DeleteLink(ctx context.Context, id domain.ChangeID, link domain.LinkID, flow, execution string) error {
	return g.repo.InTx(ctx, func(tx Tx) error {
		w, l, n, err := g.linkWork(ctx, tx, id, link, flow)
		if err != nil {
			return err
		}
		if err := tx.DeleteLink(ctx, link); err != nil {
			return err
		}
		ref := n.Ref()
		return g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: w.cn.ID, Op: domain.ImpactUpdated, Flow: w.flow, Execution: execution, Post: &ref,
			Patch: map[string]any{"removeLink": map[string]any{"id": string(link), "type": l.Type, "to": l.To.String()}}})
	})
}

// CheckinNode freezes the working version of a change impact: an accepted review on the flow authorizes it. It changes
// no content, so an impact frozen by the lifecycle of the change (ADR 0058) is checked in all the same.
func (g *Graph) CheckinNode(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow, execution string) (cn domain.ChangeImpact, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.workOn(ctx, tx, id, flow)
		if err != nil {
			return err
		}
		if err := w.selectImpact(impact); err != nil {
			return err
		}
		h, err := w.head(ctx, tx)
		if err != nil {
			return err
		}
		if h == nil || !h.CheckedOut {
			return fmt.Errorf("change impact %s (%s) is not checked out: %w", impact, w.cn.Key, ErrConflict)
		}
		if w.vcn.Review != domain.ReviewAccepted {
			return fmt.Errorf("change impact %s (%s) is %s: an accepted review authorizes its check-in: %w", impact, w.cn.Key, w.vcn.Review, ErrConflict)
		}
		if err := tx.CheckinVersion(ctx, h.Ref()); err != nil {
			return err
		}
		ref := h.Ref()
		cn = w.seenAs(&ref)
		return g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: impact, Op: domain.ImpactCheckedIn, Flow: w.flow, Execution: execution, Post: &ref})
	})
	return
}

// CancelCheckout drops the working version of a change impact: the impact goes back to the version it had before (on
// the flow), or to none. A creation cancelled before its first check-in leaves no version: the node and the change
// impact are removed. The working version must be the latest of its node (another change did not write one since).
func (g *Graph) CancelCheckout(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow, execution string) (cn domain.ChangeImpact, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.workOn(ctx, tx, id, flow)
		if err != nil {
			return err
		}
		if err := g.impact(ctx, tx, w, impact); err != nil {
			return err
		}
		h, err := w.head(ctx, tx)
		if err != nil {
			return err
		}
		if h == nil || !h.CheckedOut {
			return fmt.Errorf("change impact %s (%s) is not checked out: %w", impact, w.cn.Key, ErrConflict)
		}
		var back *domain.NodeRef
		if len(h.Parents) > 0 {
			p, err := tx.Node(ctx, domain.NodeRef{ID: h.ID, Version: h.Parents[0]})
			if err != nil {
				return err
			}
			// the version the flow saw before the checkout: one this change wrote on the branch of the operation
			if p.ChangeID == id && domain.BranchOf(p.Branch) == w.branch {
				ref := p.Ref()
				back = &ref
			}
		}
		// the event first: a cancelled creation leaves the projection before its node goes
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: impact, Op: domain.ImpactCancelled, Flow: w.flow, Execution: execution, Post: back}); err != nil {
			return err
		}
		cn = w.vcn
		cn.Post = back
		return tx.DropWorkingVersion(ctx, h.Ref())
	})
	return
}

// TransitionNode moves a node along its lifecycle in a change (ADR 0076): from its checked-in version (refused while
// it is checked out on the flow), it writes the next version, identical but for the state, checked in at once. The
// transition is authorized before the transaction (Graph.Authorizer reads the access graph), then its requirements,
// its guard (which sees the impact of the node: a transition may require a review) and its actions run.
func (g *Graph) TransitionNode(ctx context.Context, id domain.ChangeID, in NodeTransition) (cn domain.ChangeImpact, err error) {
	if in.To == "" {
		return cn, invalidf("a transition names the state it goes to")
	}
	var authorized *pendingMove
	if g.Authorizer != nil {
		var m pendingMove
		err := g.repo.InTx(ctx, func(tx Tx) error {
			_, base, t, err := g.transitionOf(ctx, tx, id, in)
			if err != nil {
				return err
			}
			m = pendingMove{node: base, t: t}
			return errCollected
		})
		if !errors.Is(err, errCollected) {
			return cn, err
		}
		if err := g.Authorizer(ctx, m.node, m.t); err != nil {
			return cn, err
		}
		authorized = &m
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, base, t, err := g.transitionOf(ctx, tx, id, in)
		if err != nil {
			return err
		}
		if authorized != nil && (pendingMove{node: base, t: t}).key() != authorized.key() {
			return fmt.Errorf("%s moved while the transition was authorized: take it again: %w", base.Key, ErrConflict)
		}
		b := base
		n, err := g.newVersion(ctx, tx, w, &b, in.Execution)
		if err != nil {
			return err
		}
		n.State = t.To
		ref, err := g.putVersion(ctx, tx, w, n, &b)
		if err != nil {
			return err
		}
		target, err := g.viewTarget(ctx, tx, w)
		if err != nil {
			return err
		}
		a := &applier{g: g, tx: tx, ctx: ctx, change: w.c, ix: w.ix, branch: w.branch, target: target, impact: &w.vcn}
		children, err := a.checkTransition(n, t)
		if err != nil {
			return err
		}
		if err := a.runActions(n, t, children); err != nil {
			return err
		}
		cn = w.seenAs(&ref)
		return nil
	})
	return
}

// transitionOf resolves what a transition starts from: the change impact (declared when the change holds none on the
// node), its checked-in version with the state it leaves, and the transition of the lifecycle.
func (g *Graph) transitionOf(ctx context.Context, tx Tx, id domain.ChangeID, in NodeTransition) (*work, domain.Node, domain.Transition, error) {
	w, err := g.resolve(ctx, tx, id, in.NodeCheckout)
	if err != nil {
		return nil, domain.Node{}, domain.Transition{}, err
	}
	if w.vcn.Review == domain.ReviewRejected {
		return nil, domain.Node{}, domain.Transition{}, fmt.Errorf("change impact %s is rejected: %w", w.cn.ID, ErrConflict)
	}
	base, err := w.base(ctx, tx)
	if err != nil {
		return nil, domain.Node{}, domain.Transition{}, err
	}
	if base == nil {
		return nil, domain.Node{}, domain.Transition{}, invalidf("change impact %s has no version to move", w.cn.ID)
	}
	if base.CheckedOut {
		return nil, domain.Node{}, domain.Transition{}, fmt.Errorf("change impact %s (%s) is checked out: check it in before a transition: %w", w.cn.ID, w.cn.Key, ErrConflict)
	}
	lc := w.ix.lifecycleOf(base.Type)
	if lc == nil {
		return nil, domain.Node{}, domain.Transition{}, invalidf("node type %s has no lifecycle: state %q", base.Type, in.To)
	}
	if _, ok := lc.State(in.To); !ok {
		return nil, domain.Node{}, domain.Transition{}, invalidf("%s has no state %q", base.Type, in.To)
	}
	from := base.State
	if from == "" {
		from = lc.Initial
	}
	if from == in.To {
		return nil, domain.Node{}, domain.Transition{}, invalidf("%s (%s) is already %s", base.Key, base.Type, in.To)
	}
	t, ok := lc.Move(from, in.To)
	if !ok {
		return nil, domain.Node{}, domain.Transition{}, invalidf("%s (%s) cannot go from %s to %s", base.Key, base.Type, from, in.To)
	}
	at := *base
	at.State = from
	return w, at, t, nil
}

// viewTarget is the state of the namespace as an operation on a flow sees it: the head of the change branch (else the
// reference baseline) with the versions the flow sees for the nodes of the change.
func (g *Graph) viewTarget(ctx context.Context, tx Tx, w *work) (map[domain.NodeID]domain.Version, error) {
	b, err := branchHead(ctx, tx, w.c.Namespace, domain.BranchOf(w.c.Branch))
	if errors.Is(err, ErrNotFound) || (err == nil && b.ID == "") {
		b, err = tx.Baseline(ctx, w.c.BaselineID)
	}
	if err != nil {
		return nil, err
	}
	target := maps.Clone(b.Nodes)
	if target == nil {
		target = map[domain.NodeID]domain.Version{}
	}
	for _, cn := range w.seen {
		if cn.Post != nil {
			target[cn.Post.ID] = cn.Post.Version
		}
	}
	return target, nil
}
