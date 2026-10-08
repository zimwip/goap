package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// This file is how a change edits nodes (ADR 0076, 0079). Every write names the change; a node has no version during
// the change: ImpactNodeCreate or ImpactNodeCheckout gives it a draft on the flow (once per change and flow), and the
// draft stays until the change lands, which writes the version from it:
//
//   - ImpactNodeUpdate and the link operations edit the draft (an `updated` event with the patch), writing no version;
//   - an accepted review runs the checks of a version on the draft and records the acceptance; editing it again sends
//     the review back to proposed;
//   - a lifecycle transition (ImpactNodeTransition) sets the state of the draft, which a node with none is checked out
//     for first; authorized and guarded when it is taken (the guard sees the change, the impact and the draft);
//   - ImpactNodeCancel drops the draft; a creation cancelled leaves no node.

// NodeCreate is a node ImpactNodeCreate creates.
type NodeCreate struct {
	Key, Type  string
	Properties map[string]any
	// Owner is the key of the organisational unit owning the node (ADR 0054); empty: the unit holding the change.
	Owner     string
	Rationale string
	// Links are outgoing links of the draft.
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

// NodeUpdate edits a draft.
type NodeUpdate struct {
	// Node and Key name the node when ImpactNodeUpdate is given no change impact (see resolve).
	Node domain.NodeID
	Key  string
	// Properties are merged over the ones of the draft.
	Properties map[string]any
	// Owner transfers the node to another organisational unit (its key, ADR 0054).
	Owner           string
	Flow, Execution string
}

// LinkWrite is an outgoing link of a draft: to an exact node version, or, with Version 0, to the draft of a node the
// change holds (resolved to the version landing writes for it).
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
	// inh are, for a sub-change, the drafts its parents hold, by node (ADR 0081).
	inh map[domain.NodeID]inherited
	// takes is set when the operation takes a node a parent creates (ADR 0081): the change impact (intent created) is not
	// recorded yet, the created event of the checkout adds it with the copy of the parent's draft.
	takes *inherited
}

// workOn opens an operation on a change, on a flow: the change gets its branch (where its versions are written when it
// lands, ADR 0079).
func (g *Graph) workOn(ctx context.Context, tx Tx, id domain.ChangeID, flow string) (*work, error) {
	c, err := changeOpen(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if _, ok, err := ownBranch(ctx, tx, c); err != nil {
		return nil, err
	} else if !ok {
		if c, _, err = g.ensureOwnBranch(ctx, tx, c); err != nil {
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
		w.branch = flowBranchName(w.flow)
	}
	if w.ix, err = g.typesAt(ctx, tx, c.BaselineID); err != nil {
		return nil, err
	}
	if w.seen, err = g.newFlowNodes(tx, c, w.flow).nodes(ctx); err != nil {
		return nil, err
	}
	if w.inh, err = g.inheritedDrafts(ctx, tx, c); err != nil {
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
	// a sub-change takes a node its parent holds a draft of (ADR 0081): one the parent creates is in no baseline
	for node, x := range w.inh {
		if !((t.Node != "" && node == t.Node) || (t.Node == "" && x.draft.Key == t.Key && (t.Type == "" || x.draft.Type == t.Type))) {
			continue
		}
		if t.create {
			return nil, fmt.Errorf("cannot create %s (%s): parent change %s holds it as change impact %s (%s): %w", t.Key, t.Type, x.from.ID, x.impact.ID, x.impact.Intent, ErrConflict)
		}
		if x.draft.Base != nil {
			break // an existing node: declared from the baseline below, its checkout copies the parent's draft
		}
		decl := domain.ChangeImpact{Intent: domain.IntentCreated, Key: x.draft.Key, Type: x.draft.Type, Rationale: firstNonEmpty(t.Rationale, x.impact.Rationale),
			Flow: t.Flow, Execution: t.Execution, ProducedBy: t.ProducedBy, DerivedFrom: []domain.ItemID{domain.ItemID(x.impact.ID)}}
		out, err := g.declareTx(ctx, tx, id, []domain.ChangeImpact{decl}, false)
		if err != nil {
			return nil, err
		}
		x := x
		w.cn, w.vcn, w.takes = out[0], out[0], &x
		w.seen = append(w.seen, out[0])
		return w, nil
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

// base is the stored version a draft of the impact starts from: its pre version (on the main flow, the latest of the
// node on the change branch); nil for a creation. A sub-change copies the draft its parent holds first (ADR 0081).
func (w *work) base(ctx context.Context, tx Tx) (*domain.Node, error) {
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

// working is the draft of the impact on the flow itself, editable: the flow checked the node out, and the impact is not
// rejected. An accepted one is edited all the same: editedAgain then sends its review back to proposed.
func (g *Graph) working(ctx context.Context, tx Tx, w *work) (domain.Draft, error) {
	rows, err := g.drafts(ctx, tx, w.c.ID)
	if err != nil {
		return domain.Draft{}, err
	}
	d := ownDraft(rows, w.cn.ID, w.flow)
	if d == nil {
		if seen, err := g.seenDraft(ctx, tx, w.c, w.flow, w.cn.ID); err != nil {
			return domain.Draft{}, err
		} else if seen != nil {
			// the draft of a parent flow is that flow's to edit: this flow checks the node out to write its own
			return domain.Draft{}, fmt.Errorf("%s holds a draft of another flow: check it out on this one first: %w", draftOf(w.cn), ErrConflict)
		}
		return domain.Draft{}, fmt.Errorf("%s is not checked out: check it out first: %w", draftOf(w.cn), ErrConflict)
	}
	if w.vcn.Review == domain.ReviewRejected {
		return domain.Draft{}, fmt.Errorf("change impact %s is rejected: %w", w.cn.ID, ErrConflict)
	}
	return *d, nil
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

// checkDraftAttributes checks the type and enum of the values of a draft: what every edit of a draft is held to.
func (w *work) checkDraftAttributes(d domain.Draft, props map[string]any) error {
	return w.ix.checkAttributes(draftNode(w.c, d), props)
}

// ImpactNodeCreate creates a node in a change: one `created` event adds the change impact (intent created) and the
// draft of the node (ADR 0076, 0077, 0079). There is no proposal of a new node, and no version until the change lands.
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
// split, ADR 0077): recorded on its draft and in the patch of the created event.
func (g *Graph) createTx(ctx context.Context, tx Tx, id domain.ChangeID, in NodeCreate, origins []domain.NodeRef) (domain.ChangeImpact, error) {
	w, err := g.resolve(ctx, tx, id, target{Key: in.Key, Type: in.Type, create: true, Rationale: in.Rationale, Flow: in.Flow,
		Execution: in.Execution, ProducedBy: in.ProducedBy, DerivedFrom: in.DerivedFrom})
	if err != nil {
		return domain.ChangeImpact{}, err
	}
	d := domain.Draft{Change: id, Impact: w.cn.ID, Flow: w.flow, Node: domain.NodeID(g.newID()), Key: in.Key, Type: in.Type,
		Properties: cloneMap(in.Properties), Origins: slices.Clone(origins), Execution: in.Execution}
	if lc := w.ix.lifecycleOf(d.Type); lc != nil {
		d.State = lc.Initial
	}
	if err := w.checkDraftAttributes(d, d.Properties); err != nil {
		return domain.ChangeImpact{}, err
	}
	if in.Owner != "" {
		unit, err := g.structureNode(ctx, tx, domain.StructureOrganisation, in.Owner)
		if err != nil {
			return domain.ChangeImpact{}, err
		}
		d.Owner = unit.ID
	}
	for _, l := range in.Links {
		dl, err := g.newDraftLink(ctx, tx, w, d, l)
		if err != nil {
			return domain.ChangeImpact{}, err
		}
		d.Links = append(d.Links, dl)
	}
	var patch map[string]any
	if len(origins) > 0 {
		patch = map[string]any{"origins": originsPatch(ctx, tx, origins)}
	}
	ref := d.Ref()
	st := w.cn // the creation adds its impact (ADR 0077)
	if err := g.emit(ctx, tx, domain.ImpactEvent{Change: w.c.ID, Impact: w.cn.ID, Op: domain.ImpactCreated, Flow: w.flow, Execution: in.Execution, State: &st, Post: &ref, Draft: &d, Patch: patch}); err != nil {
		return domain.ChangeImpact{}, err
	}
	return w.seenAs(&ref), nil
}

// ImpactNodeCheckout puts a node in edit mode in a change: it gives the node a draft on the flow, a copy of what the
// flow sees of it (properties, state, owner and outgoing links), whatever its state (ADR 0078). The node is checked
// out once per change and flow: it is refused while the flow holds a draft of it (update it, ADR 0079). A flow whose
// parent flow holds a draft copies that one. A checkout of an impact that was decided sends its review back to
// proposed: what is changed is reviewed again (the checkout a transition makes for itself keeps it: the move is not an edit).
func (g *Graph) ImpactNodeCheckout(ctx context.Context, id domain.ChangeID, in NodeCheckout) (cn domain.ChangeImpact, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.resolve(ctx, tx, id, in.target())
		if err != nil {
			return err
		}
		ref, err := g.checkoutTx(ctx, tx, w, in.Execution, false)
		if err != nil {
			return err
		}
		cn = w.seenAs(&ref)
		return nil
	})
	return
}

// checkoutTx is the checkout of the impact w selects, inside a transaction (see ImpactNodeCheckout).
func (g *Graph) checkoutTx(ctx context.Context, tx Tx, w *work, execution string, keepReview bool) (domain.NodeRef, error) {
	id := w.c.ID
	if w.vcn.Review == domain.ReviewRejected {
		return domain.NodeRef{}, fmt.Errorf("change impact %s is rejected: %w", w.cn.ID, ErrConflict)
	}
	rows, err := g.drafts(ctx, tx, id)
	if err != nil {
		return domain.NodeRef{}, err
	}
	if ownDraft(rows, w.cn.ID, w.flow) != nil {
		return domain.NodeRef{}, fmt.Errorf("%s is already checked out: %w", draftOf(w.cn), ErrConflict)
	}
	var d domain.Draft
	if w.takes != nil {
		// a node a parent creates: one created event adds the impact with the copy of the parent's draft (ADR 0081)
		d, err := g.inheritDraft(ctx, tx, w, *w.takes, execution)
		if err != nil {
			return domain.NodeRef{}, err
		}
		ref, st := d.Ref(), w.cn
		return ref, g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: w.cn.ID, Op: domain.ImpactCreated, Flow: w.flow, Execution: execution, State: &st, Post: &ref, Draft: &d})
	}
	if parent, err := g.seenDraft(ctx, tx, w.c, w.flow, w.cn.ID); err != nil {
		return domain.NodeRef{}, err
	} else if parent != nil {
		d = g.forkDraft(w, *parent, execution)
	} else if x, ok := w.inh[nodeOf(w.cn)]; ok {
		// the draft a parent change holds of the node (ADR 0081)
		if d, err = g.inheritDraft(ctx, tx, w, x, execution); err != nil {
			return domain.NodeRef{}, err
		}
	} else {
		base, err := w.base(ctx, tx)
		if err != nil {
			return domain.NodeRef{}, err
		}
		if base == nil {
			return domain.NodeRef{}, invalidf("change impact %s has no version to check out", w.cn.ID)
		}
		if domain.NamespaceOf(base.Namespace) != w.ns {
			return domain.NodeRef{}, invalidf("node %s is in namespace %s, the change acts on %s", base.Key, domain.NamespaceOf(base.Namespace), w.ns)
		}
		links, err := tx.OutLinks(ctx, base.Ref())
		if err != nil {
			return domain.NodeRef{}, err
		}
		d = g.draftFrom(w, *base, links, execution)
	}
	ref := d.Ref()
	if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: w.cn.ID, Op: domain.ImpactCheckedOut, Flow: w.flow, Execution: execution, Post: &ref, Draft: &d}); err != nil {
		return ref, err
	}
	if w.vcn.Review != domain.ReviewProposed && !keepReview {
		r := domain.Review{Status: domain.ReviewProposed, By: g.caller(ctx), At: g.now(), Comment: "checked out again", Flow: w.flow, Execution: execution}
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: w.cn.ID, Op: domain.ImpactReviewed, Flow: w.flow, Execution: execution, By: r.By, Review: &r}); err != nil {
			return ref, err
		}
		w.vcn.Review = domain.ReviewProposed
	}
	return ref, nil
}

// ImpactNodeUpdate edits the draft of a change impact: its properties (merged and validated) and its owner. No version
// is written; the edit is an updated event of the impact log.
func (g *Graph) ImpactNodeUpdate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, in NodeUpdate) (cn domain.ChangeImpact, err error) {
	if len(in.Properties) == 0 && in.Owner == "" {
		return cn, invalidf("nothing to update")
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		cn, err = g.updateTx(ctx, tx, id, impact, in)
		return err
	})
	return
}

// updateTx is ImpactNodeUpdate inside a transaction.
func (g *Graph) updateTx(ctx context.Context, tx Tx, id domain.ChangeID, impact domain.ChangeImpactID, in NodeUpdate) (domain.ChangeImpact, error) {
	w, err := g.resolve(ctx, tx, id, target{Impact: impact, Node: in.Node, Key: in.Key, Flow: in.Flow, Execution: in.Execution})
	if err != nil {
		return domain.ChangeImpact{}, err
	}
	d, err := g.working(ctx, tx, w)
	if err != nil {
		return domain.ChangeImpact{}, err
	}
	patch := map[string]any{}
	if len(in.Properties) > 0 {
		props := cloneMap(d.Properties)
		if props == nil {
			props = map[string]any{}
		}
		maps.Copy(props, in.Properties)
		if err := w.checkDraftAttributes(d, props); err != nil {
			return domain.ChangeImpact{}, err
		}
		patch["props"] = in.Properties
	}
	if in.Owner != "" {
		unit, err := g.structureNode(ctx, tx, domain.StructureOrganisation, in.Owner)
		if err != nil {
			return domain.ChangeImpact{}, err
		}
		patch["owner"], patch["ownerId"] = in.Owner, string(unit.ID)
	}
	ref := d.Ref()
	if err := g.emitUpdated(ctx, tx, w, ref, in.Execution, patch); err != nil {
		return domain.ChangeImpact{}, err
	}
	return w.seenAs(&ref), nil
}

// linkPatch describes a link added to a draft, for the updated event: the audit trail reads to, the draft fold the rest.
func linkPatch(l domain.DraftLink) map[string]any {
	return map[string]any{"id": string(l.ID), "type": l.Type, "to": l.To.String(), "toId": string(l.To.ID), "toVersion": int(l.To.Version), "props": l.Properties}
}

// ImpactLinkCreate adds an outgoing link to the draft of a change impact.
func (g *Graph) ImpactLinkCreate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, l LinkWrite, flow, execution string) (link domain.Link, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		link, err = g.linkCreateTx(ctx, tx, id, impact, l, flow, execution)
		return err
	})
	return
}

// linkCreateTx is ImpactLinkCreate inside a transaction.
func (g *Graph) linkCreateTx(ctx context.Context, tx Tx, id domain.ChangeID, impact domain.ChangeImpactID, l LinkWrite, flow, execution string) (domain.Link, error) {
	w, err := g.workOn(ctx, tx, id, flow)
	if err != nil {
		return domain.Link{}, err
	}
	if err := g.impact(ctx, tx, w, impact); err != nil {
		return domain.Link{}, err
	}
	d, err := g.working(ctx, tx, w)
	if err != nil {
		return domain.Link{}, err
	}
	dl, err := g.newDraftLink(ctx, tx, w, d, l)
	if err != nil {
		return domain.Link{}, err
	}
	link := domain.Link{ID: dl.ID, Type: dl.Type, From: d.Ref(), To: dl.To, Properties: dl.Properties, ChangeID: id}
	return link, g.emitUpdated(ctx, tx, w, d.Ref(), execution, map[string]any{"addLink": linkPatch(dl)})
}

// emitUpdated records an edit of the draft ref (the updated event with its patch), then sends an accepted review back
// to proposed (ADR 0077).
func (g *Graph) emitUpdated(ctx context.Context, tx Tx, w *work, ref domain.NodeRef, execution string, patch map[string]any) error {
	if err := g.emit(ctx, tx, domain.ImpactEvent{Change: w.c.ID, Impact: w.cn.ID, Op: domain.ImpactUpdated, Flow: w.flow, Execution: execution, Post: &ref, Patch: patch}); err != nil {
		return err
	}
	return g.editedAgain(ctx, tx, w, execution)
}

// emitAddLink and emitRemoveLink record the edit of the links of a draft.
func (g *Graph) emitAddLink(ctx context.Context, tx Tx, w *work, d domain.Draft, link domain.DraftLink, execution string) error {
	return g.emitUpdated(ctx, tx, w, d.Ref(), execution, map[string]any{"addLink": linkPatch(link)})
}

func (g *Graph) emitRemoveLink(ctx context.Context, tx Tx, w *work, d domain.Draft, l domain.DraftLink, execution string) error {
	return g.emitUpdated(ctx, tx, w, d.Ref(), execution, map[string]any{"removeLink": map[string]any{"id": string(l.ID), "type": l.Type, "to": l.To.String(), "toId": string(l.To.ID)}})
}

// linkWork finds the draft of the flow holding a link, and the link: by its id, or by the id of the stored link of the
// version the draft was checked out from, which the checkout copied.
func (g *Graph) linkWork(ctx context.Context, tx Tx, id domain.ChangeID, link domain.LinkID, flow string) (*work, domain.Draft, domain.DraftLink, error) {
	w, err := g.workOn(ctx, tx, id, flow)
	if err != nil {
		return nil, domain.Draft{}, domain.DraftLink{}, err
	}
	rows, err := g.drafts(ctx, tx, id)
	if err != nil {
		return nil, domain.Draft{}, domain.DraftLink{}, err
	}
	var stored *domain.Link
	if l, err := tx.Link(ctx, link); err == nil {
		stored = &l
	} else if !errors.Is(err, ErrNotFound) {
		return nil, domain.Draft{}, domain.DraftLink{}, err
	}
	other := false
	for _, d := range rows {
		var found *domain.DraftLink
		if l, ok := d.Link(link); ok {
			found = &l
		} else if stored != nil && d.Base != nil && *d.Base == stored.From {
			for _, c := range d.Links {
				if c.Type == stored.Type && c.To.ID == stored.To.ID {
					c := c
					found = &c
				}
			}
		}
		if found == nil {
			continue
		}
		if d.Flow != w.flow {
			other = true
			continue
		}
		if err := g.impact(ctx, tx, w, d.Impact); err != nil {
			return nil, domain.Draft{}, domain.DraftLink{}, err
		}
		d, err := g.working(ctx, tx, w)
		if err != nil {
			return nil, d, *found, err
		}
		return w, d, *found, nil
	}
	if other {
		return nil, domain.Draft{}, domain.DraftLink{}, fmt.Errorf("link %s is a link of a draft of another flow of change %s: check the node out on this one first: %w", link, id, ErrConflict)
	}
	return nil, domain.Draft{}, domain.DraftLink{}, fmt.Errorf("link %s is not a link of a draft of change %s: %w", link, id, ErrConflict)
}

// ImpactLinkUpdate replaces the properties of an outgoing link of a draft.
func (g *Graph) ImpactLinkUpdate(ctx context.Context, id domain.ChangeID, link domain.LinkID, props map[string]any, flow, execution string) (l domain.Link, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, d, cur, err := g.linkWork(ctx, tx, id, link, flow)
		if err != nil {
			return err
		}
		if err := w.ix.checkLinkAttributes(cur.Type, props); err != nil {
			return err
		}
		l = domain.Link{ID: cur.ID, Type: cur.Type, From: d.Ref(), To: cur.To, Properties: props, ChangeID: id}
		return g.emitUpdated(ctx, tx, w, d.Ref(), execution, map[string]any{"updateLink": map[string]any{"id": string(cur.ID), "type": cur.Type, "toId": string(cur.To.ID), "props": props}})
	})
	return
}

// ImpactLinkDelete removes an outgoing link of a draft: removing a child is a modification of its parent (ADR 0024 §4).
func (g *Graph) ImpactLinkDelete(ctx context.Context, id domain.ChangeID, link domain.LinkID, flow, execution string) error {
	return g.repo.InTx(ctx, func(tx Tx) error {
		w, d, l, err := g.linkWork(ctx, tx, id, link, flow)
		if err != nil {
			return err
		}
		return g.emitRemoveLink(ctx, tx, w, d, l, execution)
	})
}

// checkAccepted is the gate of an accepted review (ADR 0079): the draft of the impact the flow sees must satisfy what
// a version does (checkDraft: validators, required links, link attributes) and the review gate of the merge and split
// origins. Nothing is written: the version is written when the change lands. It does nothing when the impact has no
// draft.
func (g *Graph) checkAccepted(ctx context.Context, tx Tx, id domain.ChangeID, flow string, impact domain.ChangeImpactID) error {
	w, err := g.workOn(ctx, tx, id, flow)
	if err != nil {
		return err
	}
	if err := w.selectImpact(impact); err != nil {
		return err
	}
	d, err := g.seenDraft(ctx, tx, w.c, w.flow, impact)
	if err != nil || d == nil {
		return err
	}
	if err := g.checkOrigins(w.seen, w.vcn, d); err != nil {
		return err
	}
	return g.checkDraft(ctx, w.ix, w.c, *d)
}

// editedAgain sends an accepted review back to proposed when its draft is edited again (ADR 0077): what was accepted
// is no longer what is there. It follows the updated event.
func (g *Graph) editedAgain(ctx context.Context, tx Tx, w *work, execution string) error {
	if w.vcn.Review != domain.ReviewAccepted {
		return nil
	}
	r := domain.Review{Status: domain.ReviewProposed, By: g.caller(ctx), At: g.now(), Comment: "edited again", Flow: w.flow, Execution: execution}
	if err := g.emit(ctx, tx, domain.ImpactEvent{Change: w.c.ID, Impact: w.cn.ID, Op: domain.ImpactReviewed, Flow: w.flow, Execution: execution, By: r.By, Review: &r}); err != nil {
		return err
	}
	w.vcn.Review = domain.ReviewProposed
	return nil
}

// ImpactNodeCancel drops the draft of a change impact on the flow: the impact goes back to what the flow saw before (the
// draft of a parent flow), or to none. A creation cancelled leaves no node: the change impact is removed.
func (g *Graph) ImpactNodeCancel(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow, execution string) (cn domain.ChangeImpact, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.workOn(ctx, tx, id, flow)
		if err != nil {
			return err
		}
		if err := g.impact(ctx, tx, w, impact); err != nil {
			return err
		}
		rows, err := g.drafts(ctx, tx, id)
		if err != nil {
			return err
		}
		d := ownDraft(rows, impact, w.flow)
		if d == nil {
			return fmt.Errorf("change impact %s (%s) is not checked out: %w", impact, w.cn.Key, ErrConflict)
		}
		// what the flow falls back to: the draft of its parent flows
		var back *domain.NodeRef
		if ids := flowIDs(flowChain(w.c, w.flow)); w.flow != "" {
			for _, f := range append(ids[1:], "") {
				if ownDraft(rows, impact, f) != nil {
					r := d.Ref()
					back = &r
					break
				}
			}
		}
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: impact, Op: domain.ImpactCancelled, Flow: w.flow, Execution: execution, Post: back}); err != nil {
			return err
		}
		cn = w.vcn
		cn.Post = back
		return nil
	})
	return
}

// ImpactNodeTransition moves a node along its lifecycle in a change (ADR 0076, 0079): it sets the state of the draft of
// the node, which a node with none is checked out for first; the transitioned event carries the patch {"state": {"from",
// "to"}} and the properties its actions set. The transition is authorized before the transaction (Graph.Authorizer reads
// the access graph), then its requirements, its guard (which sees the change, the impact of the node and its draft: a
// transition may require a review) and its actions run on the draft.
func (g *Graph) ImpactNodeTransition(ctx context.Context, id domain.ChangeID, in NodeTransition) (cn domain.ChangeImpact, err error) {
	if in.To == "" {
		return cn, invalidf("a transition names the state it goes to")
	}
	var authorized *pendingMove
	if g.Authorizer != nil {
		var m pendingMove
		err := g.repo.InTx(ctx, func(tx Tx) error {
			_, cur, t, err := g.transitionOf(ctx, tx, id, in)
			if err != nil {
				return err
			}
			m = pendingMove{node: cur, t: t}
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
		w, cur, t, err := g.transitionOf(ctx, tx, id, in)
		if err != nil {
			return err
		}
		if authorized != nil && (pendingMove{node: cur, t: t}).key() != authorized.key() {
			return fmt.Errorf("%s moved while the transition was authorized: take it again: %w", cur.Key, ErrConflict)
		}
		rows, err := g.drafts(ctx, tx, id)
		if err != nil {
			return err
		}
		if ownDraft(rows, w.cn.ID, w.flow) == nil {
			// the node is checked out first, the draft the move acts on
			if _, err := g.checkoutTx(ctx, tx, w, in.Execution, true); err != nil {
				return err
			}
			if w, err = g.resolve(ctx, tx, id, target{Impact: w.cn.ID, Flow: in.Flow, Execution: in.Execution}); err != nil {
				return err
			}
		}
		d, err := g.working(ctx, tx, w)
		if err != nil {
			return err
		}
		moved := d.Clone()
		from := d.State
		moved.State = t.To
		n := draftNode(w.c, moved)
		reader, err := g.newDraftReader(ctx, tx, w.c, w.flow, w.seen)
		if err != nil {
			return err
		}
		reader.byNode[moved.Node] = moved
		out, err := reader.outLinks(ctx, tx, moved.Ref())
		if err != nil {
			return err
		}
		viewTarget, err := g.viewTarget(ctx, tx, w)
		if err != nil {
			return err
		}
		a := &applier{g: g, tx: tx, ctx: ctx, change: w.c, ix: w.ix, branch: w.branch, target: viewTarget, impact: &w.vcn, draft: &moved, drafts: reader}
		children, err := a.checkTransition(n, out, t)
		if err != nil {
			return err
		}
		set, unset, err := a.runActions(n, t, children)
		if err != nil {
			return err
		}
		patch := map[string]any{"state": map[string]any{"from": from, "to": t.To}}
		if len(set) > 0 {
			patch["props"] = set
		}
		if len(unset) > 0 {
			patch["unset"] = unset
		}
		ref := d.Ref()
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: w.c.ID, Impact: w.cn.ID, Op: domain.ImpactTransitioned, Flow: w.flow, Execution: in.Execution, Post: &ref, Patch: patch}); err != nil {
			return err
		}
		cn = w.seenAs(&ref)
		return nil
	})
	return
}

// transitionOf resolves what a transition starts from: the change impact (declared when the change holds none on the
// node), the node as the flow sees it (its draft, else the stored version it starts from) with the state it leaves, and
// the transition of the lifecycle.
func (g *Graph) transitionOf(ctx context.Context, tx Tx, id domain.ChangeID, in NodeTransition) (*work, domain.Node, domain.Transition, error) {
	w, err := g.resolve(ctx, tx, id, in.target())
	if err != nil {
		return nil, domain.Node{}, domain.Transition{}, err
	}
	if w.vcn.Review == domain.ReviewRejected {
		return nil, domain.Node{}, domain.Transition{}, fmt.Errorf("change impact %s is rejected: %w", w.cn.ID, ErrConflict)
	}
	var cur *domain.Node
	if d, err := g.seenDraft(ctx, tx, w.c, w.flow, w.cn.ID); err != nil {
		return nil, domain.Node{}, domain.Transition{}, err
	} else if d != nil {
		n := draftNode(w.c, *d)
		cur = &n
	} else if cur, err = w.base(ctx, tx); err != nil {
		return nil, domain.Node{}, domain.Transition{}, err
	}
	if cur == nil {
		return nil, domain.Node{}, domain.Transition{}, invalidf("change impact %s has no version to move", w.cn.ID)
	}
	lc := w.ix.lifecycleOf(cur.Type)
	if lc == nil {
		return nil, domain.Node{}, domain.Transition{}, invalidf("node type %s has no lifecycle: state %q", cur.Type, in.To)
	}
	if _, ok := lc.State(in.To); !ok {
		return nil, domain.Node{}, domain.Transition{}, invalidf("%s has no state %q", cur.Type, in.To)
	}
	from := cur.State
	if from == "" {
		from = lc.Initial
	}
	if from == in.To {
		return nil, domain.Node{}, domain.Transition{}, invalidf("%s (%s) is already %s", cur.Key, cur.Type, in.To)
	}
	t, ok := lc.Move(from, in.To)
	if !ok {
		return nil, domain.Node{}, domain.Transition{}, invalidf("%s (%s) cannot go from %s to %s", cur.Key, cur.Type, from, in.To)
	}
	at := *cur
	at.State = from
	return w, at, t, nil
}

// viewTarget is the state of the namespace as an operation on a flow sees it: the head of the change branch (else the
// reference baseline). The nodes the change holds a draft of are read through their drafts (draftReader).
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
	return target, nil
}

// WithdrawImpact takes a change impact out of the change, explicitly (ADR 0076 §5b): its draft, if any, is dropped (a
// creation leaves no node behind) and the impact leaves the list. A rejected impact keeps its draft until then, to be
// reworked once reopened. Refused for an impact declared on another flow, one derived from items (their proposals
// decide it) and one another impact realizes a removal through (Via).
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
		return g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: impact, Op: domain.ImpactWithdrawn, Flow: w.flow, Execution: execution})
	})
}
