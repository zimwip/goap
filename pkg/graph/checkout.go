package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// This file is how a change edits nodes (ADR 0076). Every write names the change; a node is edited through a working
// version, the version a ImpactNodeCreate or a ImpactNodeCheckout writes on the branch of the change (of the flow), checked out:
//
//   - ImpactNodeUpdate and the link operations edit the working version in place, writing no version;
//   - an accepted review authorizes ImpactNodeCheckin, which freezes it; changing it again is a new ImpactNodeCheckout;
//   - a lifecycle transition is a version of its own, from a checked-in version (ImpactNodeTransition): checkout, the move
//     and check-in in one operation, authorized and guarded when it is taken;
//   - ImpactNodeCancel drops the working version; a creation cancelled before its first check-in removes the node.

// NodeCreate is a node ImpactNodeCreate creates.
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

// NodeCheckout names the node ImpactNodeCheckout or ImpactNodeTransition works on: a change impact, or a node (then the impact the
// flow sees on it, declared when the change holds none).
type NodeCheckout struct {
	Impact domain.ChangeImpactID
	Node   domain.NodeID
	// Key names the node by its key, when neither Impact nor Node is given.
	Key string
	// Rationale says why, when the call declares the impact.
	Rationale       string
	Flow, Execution string
	ProducedBy      string
}

// NodeTransition moves a node along its lifecycle (ImpactNodeTransition): To is the state it goes to.
type NodeTransition struct {
	NodeCheckout
	To string
}

// NodeUpdate edits a working version in place.
type NodeUpdate struct {
	// Node and Key name the node when ImpactNodeUpdate is given no change impact (see resolve).
	Node domain.NodeID
	Key  string
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

// target names what an operation works on: a change impact, or a node (by id, or by key and type), which the
// resolver turns into a change impact (see resolve).
type target struct {
	Impact domain.ChangeImpactID
	Node   domain.NodeID
	Key    string
	Type   string
	// create: the operation creates the node, so the key must be new.
	create          bool
	Rationale       string
	Flow, Execution string
	ProducedBy      string
	DerivedFrom     []domain.ItemID
}

func (in NodeCheckout) target() target {
	return target{Impact: in.Impact, Node: in.Node, Key: in.Key, Rationale: in.Rationale, Flow: in.Flow, Execution: in.Execution, ProducedBy: in.ProducedBy}
}

// resolve is the one entry of every operation on a node of a change (ADR 0077, "Resolution"): it finds the change
// impact the operation names, declaring it in the same transaction when the change holds none, and selects it.
//
//   - an impact id is taken as it is;
//   - a node (id, or key) the flow already sees in the change reuses its impact, never doubling it;
//   - else a node of the reference baseline gets an impact of intent modified, its pre the baseline version;
//   - else, for a creation, the key is new: the impact is checked but not recorded, the created event of its first
//     version carries it (ADR 0077); for any other operation the node is in neither the baseline nor the change:
//     ErrNotFound.
//
// A creation whose key the baseline or the change already holds is an ErrConflict.
func (g *Graph) resolve(ctx context.Context, tx Tx, id domain.ChangeID, t target) (*work, error) {
	w, err := g.workOn(ctx, tx, id, t.Flow)
	if err != nil {
		return nil, err
	}
	if t.Impact != "" {
		return w, g.impact(ctx, tx, w, t.Impact)
	}
	if t.Node == "" && t.Key == "" {
		return nil, invalidf("name a change impact or a node")
	}
	if t.create && (t.Key == "" || t.Type == "") {
		return nil, invalidf("a node needs a key and a type")
	}
	var found *domain.ChangeImpact
	for i, cn := range w.seen {
		if (t.Node != "" && nodeOf(cn) == t.Node) || (t.Node == "" && cn.Key == t.Key && (t.Type == "" || cn.Type == t.Type)) {
			found = &w.seen[i]
		}
	}
	if found != nil {
		if t.create {
			return nil, fmt.Errorf("cannot create %s (%s): the change already holds it as change impact %s (%s): %w", t.Key, t.Type, found.ID, found.Intent, ErrConflict)
		}
		return w, g.impact(ctx, tx, w, found.ID)
	}
	b, err := tx.Baseline(ctx, w.c.BaselineID)
	if err != nil {
		return nil, err
	}
	nid := t.Node
	if nid == "" {
		if got, err := tx.NodeIDByKey(ctx, w.ns, t.Key); err == nil {
			nid = got
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	v, inBase := b.Nodes[nid]
	inBase = inBase && nid != ""
	var decl domain.ChangeImpact
	switch {
	case t.create && inBase:
		return nil, fmt.Errorf("cannot create %s (%s): the reference baseline of change %s already holds it: %w", t.Key, t.Type, id, ErrConflict)
	case t.create:
		decl = domain.ChangeImpact{Intent: domain.IntentCreated, Key: t.Key, Type: t.Type, DerivedFrom: t.DerivedFrom}
	case inBase:
		decl = domain.ChangeImpact{Intent: domain.IntentModified, Pre: &domain.NodeRef{ID: nid, Version: v}}
	default:
		name := string(t.Node)
		if name == "" {
			name = t.Key
		}
		return nil, fmt.Errorf("node %s is in neither the reference baseline nor the change %s: %w", name, id, ErrNotFound)
	}
	decl.Rationale, decl.Flow, decl.Execution, decl.ProducedBy = t.Rationale, t.Flow, t.Execution, t.ProducedBy
	out, err := g.declareTx(ctx, tx, id, []domain.ChangeImpact{decl}, !t.create)
	if err != nil {
		return nil, err
	}
	if w, err = g.workOn(ctx, tx, id, t.Flow); err != nil {
		return nil, err
	}
	if t.create {
		// not recorded yet: the created event of the first version adds it (createTx)
		w.cn, w.vcn = out[0], out[0]
		w.seen = append(w.seen, out[0])
		return w, nil
	}
	return w, g.impact(ctx, tx, w, out[0].ID)
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
// to the versions the flow sees for the nodes of the change), and records it as the impact's post (event op).
func (g *Graph) putVersion(ctx context.Context, tx Tx, w *work, n domain.Node, base *domain.Node, op domain.ImpactOp, patch map[string]any) (domain.NodeRef, error) {
	if err := tx.PutNode(ctx, n); err != nil {
		return domain.NodeRef{}, err
	}
	ref := n.Ref()
	ev := domain.ImpactEvent{Change: w.c.ID, Impact: w.cn.ID, Op: op, Flow: w.flow, Execution: n.Execution, Post: &ref, Patch: patch}
	if op == domain.ImpactCreated {
		st := w.cn // the creation adds its impact (ADR 0077)
		ev.State = &st
	}
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
	if base != nil {
		if err := g.followVersion(ctx, tx, w, base.Ref(), ref); err != nil {
			return ref, err
		}
	}
	return ref, g.emit(ctx, tx, ev)
}

// followVersion moves to the new version of a node the links the working versions of the change hold to the version it
// follows: a link to a node of the change targets the version the change sees (ADR 0076 §4). The links of a frozen
// version stay as they are: they become suspect (ADR 0003).
func (g *Graph) followVersion(ctx context.Context, tx Tx, w *work, from, to domain.NodeRef) error {
	in, err := tx.InLinks(ctx, from)
	if err != nil {
		return err
	}
	for _, l := range in {
		if l.From.ID == to.ID {
			continue
		}
		src, err := tx.Node(ctx, l.From)
		if err != nil {
			return err
		}
		if !src.CheckedOut || src.ChangeID != w.c.ID || domain.BranchOf(src.Branch) != w.branch {
			continue
		}
		if err := tx.DeleteLink(ctx, l.ID); err != nil {
			return err
		}
		if err := tx.PutLink(ctx, domain.Link{ID: domain.LinkID(g.newID()), Type: l.Type, From: l.From, To: to, Properties: l.Properties, ChangeID: w.c.ID}); err != nil {
			return err
		}
	}
	return nil
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
	if err := w.ix.checkLinkAttributes(l.Type, l.Properties); err != nil {
		return domain.Link{}, err
	}
	link := domain.Link{ID: domain.LinkID(g.newID()), Type: l.Type, From: from.Ref(), To: retarget(w.seen, l.To), Properties: l.Properties, ChangeID: w.c.ID}
	return link, tx.PutLink(ctx, link)
}

// ImpactNodeCreate creates a node in a change: one `created` event adds the change impact (intent created) and the
// first version of the node, checked out (ADR 0076, 0077). There is no proposal of a new node.
func (g *Graph) ImpactNodeCreate(ctx context.Context, id domain.ChangeID, in NodeCreate) (cn domain.ChangeImpact, err error) {
	if in.Key == "" || in.Type == "" {
		return cn, invalidf("a node needs a key and a type")
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		var err error
		cn, err = g.createTx(ctx, tx, id, in, nil)
		return err
	})
	return
}

// createTx is ImpactNodeCreate inside a transaction. origins are the nodes the new node derives from (a merge or a
// split, ADR 0077): recorded on its first version and in the patch of the created event.
func (g *Graph) createTx(ctx context.Context, tx Tx, id domain.ChangeID, in NodeCreate, origins []domain.NodeRef) (domain.ChangeImpact, error) {
	w, err := g.resolve(ctx, tx, id, target{Key: in.Key, Type: in.Type, create: true, Rationale: in.Rationale, Flow: in.Flow,
		Execution: in.Execution, ProducedBy: in.ProducedBy, DerivedFrom: in.DerivedFrom})
	if err != nil {
		return domain.ChangeImpact{}, err
	}
	n, err := g.newVersion(ctx, tx, w, nil, in.Execution)
	if err != nil {
		return domain.ChangeImpact{}, err
	}
	n.CheckedOut, n.Properties, n.Origins = true, maps.Clone(in.Properties), slices.Clone(origins)
	if err := w.ix.checkAttributes(n, n.Properties); err != nil {
		return domain.ChangeImpact{}, err
	}
	if in.Owner != "" {
		unit, err := g.structureNode(ctx, tx, domain.StructureOrganisation, in.Owner)
		if err != nil {
			return domain.ChangeImpact{}, err
		}
		n.Owner = unit.ID
	}
	var patch map[string]any
	if len(origins) > 0 {
		patch = map[string]any{"origins": originsPatch(ctx, tx, origins)}
	}
	ref, err := g.putVersion(ctx, tx, w, n, nil, domain.ImpactCreated, patch)
	if err != nil {
		return domain.ChangeImpact{}, err
	}
	for _, l := range in.Links {
		if _, err := g.putLink(ctx, tx, w, n, l); err != nil {
			return domain.ChangeImpact{}, err
		}
	}
	return w.seenAs(&ref), nil
}

// ImpactNodeCheckout puts a node in edit mode in a change: it writes the next version of the node, a copy of the version
// the change sees (properties, state, owner and outgoing links), checked out. The node must be in an editable state of
// its lifecycle (a transition reopens it), and not checked out already on the flow. A checkout after a check-in sends
// the review of the impact back to proposed: what is changed is reviewed again.
func (g *Graph) ImpactNodeCheckout(ctx context.Context, id domain.ChangeID, in NodeCheckout) (cn domain.ChangeImpact, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.resolve(ctx, tx, id, in.target())
		if err != nil {
			return err
		}
		ref, err := g.checkoutTx(ctx, tx, w, in.Execution)
		if err != nil {
			return err
		}
		cn = w.seenAs(&ref)
		return nil
	})
	return
}

// checkoutTx is the checkout of the impact w selects, inside a transaction (see ImpactNodeCheckout).
func (g *Graph) checkoutTx(ctx context.Context, tx Tx, w *work, execution string) (domain.NodeRef, error) {
	id := w.c.ID
	if w.vcn.Review == domain.ReviewRejected {
		return domain.NodeRef{}, fmt.Errorf("change impact %s is rejected: %w", w.cn.ID, ErrConflict)
	}
	base, err := w.base(ctx, tx)
	if err != nil {
		return domain.NodeRef{}, err
	}
	switch {
	case base == nil:
		return domain.NodeRef{}, invalidf("change impact %s has no version to check out", w.cn.ID)
	case base.CheckedOut:
		return domain.NodeRef{}, fmt.Errorf("change impact %s (%s) is already checked out: %w", w.cn.ID, w.cn.Key, ErrConflict)
	default:
		if lc := w.ix.lifecycleOf(base.Type); lc != nil && base.State != "" && !lc.Editable(base.State) {
			return domain.NodeRef{}, invalidf("cannot check out %s (%s): it is %s, not editable; reopen it first with a transition", base.Key, base.Type, base.State)
		}
	}
	n, err := g.newVersion(ctx, tx, w, base, execution)
	if err != nil {
		return domain.NodeRef{}, err
	}
	n.CheckedOut = true
	ref, err := g.putVersion(ctx, tx, w, n, base, domain.ImpactCheckedOut, nil)
	if err != nil {
		return ref, err
	}
	if w.vcn.Review != domain.ReviewProposed {
		r := domain.Review{Status: domain.ReviewProposed, By: g.caller(ctx), At: g.now(), Comment: "checked out again", Flow: w.flow, Execution: execution}
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: w.cn.ID, Op: domain.ImpactReviewed, Flow: w.flow, Execution: execution, By: r.By, Review: &r}); err != nil {
			return ref, err
		}
		w.vcn.Review = domain.ReviewProposed
	}
	return ref, nil
}

// ImpactNodeUpdate edits the working version of a change impact in place: its properties (merged and validated) and its
// owner. No version is written; the edit is an updated event of the impact log.
func (g *Graph) ImpactNodeUpdate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, in NodeUpdate) (cn domain.ChangeImpact, err error) {
	if len(in.Properties) == 0 && in.Owner == "" {
		return cn, invalidf("nothing to update")
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.resolve(ctx, tx, id, target{Impact: impact, Node: in.Node, Key: in.Key, Flow: in.Flow, Execution: in.Execution})
		if err != nil {
			return err
		}
		impact = w.cn.ID
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
			if err := w.ix.checkAttributes(n, props); err != nil {
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

// ImpactLinkCreate adds an outgoing link to the working version of a change impact, in place.
func (g *Graph) ImpactLinkCreate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, l LinkWrite, flow, execution string) (link domain.Link, err error) {
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

// emitAddLink and emitRemoveLink record the in-place edit of the links of a working version.
func (g *Graph) emitAddLink(ctx context.Context, tx Tx, w *work, n domain.Node, link domain.Link, execution string) error {
	ref := n.Ref()
	return g.emit(ctx, tx, domain.ImpactEvent{Change: w.c.ID, Impact: w.cn.ID, Op: domain.ImpactUpdated, Flow: w.flow, Execution: execution, Post: &ref,
		Patch: map[string]any{"addLink": map[string]any{"id": string(link.ID), "type": link.Type, "to": link.To.String()}}})
}

func (g *Graph) emitRemoveLink(ctx context.Context, tx Tx, w *work, n domain.Node, l domain.Link, execution string) error {
	ref := n.Ref()
	return g.emit(ctx, tx, domain.ImpactEvent{Change: w.c.ID, Impact: w.cn.ID, Op: domain.ImpactUpdated, Flow: w.flow, Execution: execution, Post: &ref,
		Patch: map[string]any{"removeLink": map[string]any{"id": string(l.ID), "type": l.Type, "to": l.To.String()}}})
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

// ImpactLinkUpdate replaces the properties of an outgoing link of a working version, in place.
func (g *Graph) ImpactLinkUpdate(ctx context.Context, id domain.ChangeID, link domain.LinkID, props map[string]any, flow, execution string) (l domain.Link, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, cur, n, err := g.linkWork(ctx, tx, id, link, flow)
		if err != nil {
			return err
		}
		if err := w.ix.checkLinkAttributes(cur.Type, props); err != nil {
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

// ImpactLinkDelete removes an outgoing link of a working version, in place: removing a child is a modification of its
// parent (ADR 0024 §4).
func (g *Graph) ImpactLinkDelete(ctx context.Context, id domain.ChangeID, link domain.LinkID, flow, execution string) error {
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

// ImpactNodeCheckin freezes the working version of a change impact: an accepted review on the flow authorizes it. It changes
// no content, so an impact frozen by the lifecycle of the change (ADR 0058) is checked in all the same.
func (g *Graph) ImpactNodeCheckin(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow, execution string) (cn domain.ChangeImpact, err error) {
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
		if err := g.checkOrigins(ctx, tx, id, w.seen, w.vcn); err != nil {
			return err
		}
		if err := g.checkFrozen(ctx, tx, w.ix, *h); err != nil {
			return err
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

// ImpactNodeCancel drops the working version of a change impact: the impact goes back to the version it had before (on
// the flow), or to none. A creation cancelled before its first check-in leaves no version: the node and the change
// impact are removed. The working version must be the latest of its node (another change did not write one since).
func (g *Graph) ImpactNodeCancel(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow, execution string) (cn domain.ChangeImpact, err error) {
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

// ImpactNodeTransition moves a node along its lifecycle in a change (ADR 0076): from its checked-in version (refused while
// it is checked out on the flow), it writes the next version, identical but for the state, checked in at once. The
// transition is authorized before the transaction (Graph.Authorizer reads the access graph), then its requirements,
// its guard (which sees the impact of the node: a transition may require a review) and its actions run.
func (g *Graph) ImpactNodeTransition(ctx context.Context, id domain.ChangeID, in NodeTransition) (cn domain.ChangeImpact, err error) {
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
		ref, err := g.putVersion(ctx, tx, w, n, &b, domain.ImpactTransitioned, nil)
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
	w, err := g.resolve(ctx, tx, id, in.target())
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

// WithdrawImpact takes a change impact out of the change, explicitly (ADR 0076 §5b): its working version, if any,
// is dropped (a creation never checked in leaves no node behind) and the impact leaves the list. A rejected impact
// keeps its working version until then, to be reworked once reopened. Refused when the change checked a version of
// the impact in (frozen: reject the impact to leave it out of the landing), for an impact declared on another flow,
// one derived from items (their proposals decide it) and one another impact realizes a removal through (Via).
func (g *Graph) WithdrawImpact(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow, execution string) error {
	return g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.workOn(ctx, tx, id, flow)
		if err != nil {
			return err
		}
		if err := g.impact(ctx, tx, w, impact); err != nil {
			return err
		}
		switch {
		case w.cn.Flow != w.flow:
			return fmt.Errorf("change impact %s was declared on another flow: %w", impact, ErrConflict)
		case len(w.cn.Items) > 0:
			return fmt.Errorf("change impact %s is derived from items: decide them instead: %w", impact, ErrConflict)
		}
		for _, o := range w.c.Nodes {
			if o.Via == impact {
				return fmt.Errorf("change impact %s realizes the removal of %s: %w", impact, o.Key, ErrConflict)
			}
		}
		h, err := w.head(ctx, tx)
		if err != nil {
			return err
		}
		if h != nil {
			vs, err := tx.Versions(ctx, h.ID)
			if err != nil {
				return err
			}
			for _, v := range vs {
				if v.ChangeImpact == impact && !v.CheckedOut {
					return fmt.Errorf("change impact %s (%s) has a checked-in version (v%d): reject it instead: %w", impact, w.cn.Key, v.Version, ErrConflict)
				}
			}
		}
		// the event first: the impact leaves the projection before its working version goes
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: impact, Op: domain.ImpactWithdrawn, Flow: w.flow, Execution: execution}); err != nil {
			return err
		}
		if h != nil && h.CheckedOut {
			return tx.DropWorkingVersion(ctx, h.Ref())
		}
		return nil
	})
}
