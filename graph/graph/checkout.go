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

// This file is how a change edits nodes (ADR 0076, 0079). Every write names the change; a node has no version during
// the change: ImpactNodeCreate or ImpactNodeCheckout gives it a draft on the flow (once per change and flow), and the
// draft stays until the change commits, which writes the version from it:
//
//   - ImpactNodeUpdate and the link operations edit the draft (an `updated` event with the patch), writing no version;
//   - an accepted review runs the checks of a version on the draft and records the acceptance; editing it again sends
//     the review back to proposed;
//   - a lifecycle transition (ImpactNodeTransition) sets the state of the draft, which a node with none is checked out
//     for first; authorized and guarded when it is taken (the guard sees the change, the impact and the draft);
//   - ImpactNodeCancel drops the draft; a creation cancelled leaves no node.
//
// A creation may be planned before it is written (ProposeImpact with intent created: a key, a type, a rationale, no
// draft): the impact analysis names what the change will create, the specification writes it. Creating or checking
// out the key of a planned creation gives it its draft (a checkedOut event, so a flow fulfils it as it edits any
// impact); it commits like any creation.

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
	// planned is set when a creation names the key of a planned creation (resolve): its draft fulfils that impact
	planned bool
	// reuse is the node a creation takes over: the node of its key, which only abandoned changes wrote (takeKey)
	reuse domain.NodeID
	// transition is set for a move of the lifecycle of the node (ImpactNodeTransition): a freeze of its type may let
	// it through (domain.Freezes.Transitions, ADR 0132 §4)
	transition bool
	// withdraw is set for a withdrawal: a candidate the change never wrote is taken out whatever is frozen
	withdraw bool
}

// workOn opens an operation on a change, on a flow: the change gets its branch (where its versions are written when it
// commits, ADR 0079).
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
	return w, nil
}

// reread takes the change and what the flow sees again from the view of the change, after a write of the operation
// folded into it (ADR 0153): the branch, the flow and the types the work was opened on stand.
func (g *Graph) reread(ctx context.Context, tx Tx, w *work) (err error) {
	if w.c, err = changeOpen(ctx, tx, w.c.ID); err != nil {
		return err
	}
	w.seen, err = g.newFlowNodes(tx, w.c, w.flow).nodes(ctx)
	return err
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
	typ := w.cn.Type
	if typ == "" {
		if n, err := w.base(ctx, tx); err != nil {
			return err
		} else if n != nil {
			typ = n.Type
		}
	}
	return w.checkTypeFreeze(typ)
}

// checkTypeFreeze refuses to touch a node of a type a move of the lifecycle of the change froze (ADR 0132 §4): no
// creation, checkout or edit of it until the change goes back; a move of its own lifecycle when the freeze lets it.
func (w *work) checkTypeFreeze(typ string) error {
	if typ == "" {
		return nil
	}
	if w.withdraw && w.cn.Post == nil {
		return nil
	}
	for _, f := range w.c.InForceFreezes() {
		if w.transition && f.Transitions {
			continue
		}
		for _, frozen := range f.Types {
			if typ == frozen || (w.ix != nil && w.ix.cat != nil && w.ix.cat.IsA(typ, frozen)) {
				return fmt.Errorf("the nodes of type %s are frozen in change %s since it left a state of its lifecycle (now %s): go back first: %w", frozen, w.c.ID, w.c.State, ErrConflict)
			}
		}
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
	create bool
	// transition: the operation moves the lifecycle of the node (a type freeze may let it, ADR 0132 §4).
	transition      bool
	Rationale       string
	Flow, Execution string
	ProducedBy      string
	DerivedFrom     []domain.ItemID
}

// checkoutTarget is the node a checkout or a transition names.
func checkoutTarget(in NodeCheckout) target {
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
	w.transition = t.transition
	if t.Impact != "" {
		return w, g.impact(ctx, tx, w, t.Impact)
	}
	if t.create {
		if err := w.checkTypeFreeze(t.Type); err != nil {
			return nil, err
		}
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
		if t.create && plannedCreation(*found) && (t.Type == "" || found.Type == t.Type) {
			w.planned = true
			return w, g.impact(ctx, tx, w, found.ID)
		}
		if t.create {
			return nil, fmt.Errorf("cannot create %s (%s): the change already holds it as change impact %s (%s): %w", t.Key, t.Type, found.ID, found.Intent, ErrConflict)
		}
		return w, g.impact(ctx, tx, w, found.ID)
	}
	nid := t.Node
	if nid == "" {
		if got, err := tx.NodeIDByKey(ctx, w.ns, t.Key); err == nil {
			nid = got
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	// the reference baseline is read for that node only, never whole (ADR 0137)
	var v domain.Version
	inBase := false
	if nid != "" {
		at, err := versionsAt(ctx, tx, w.c.BaselineID, []domain.NodeID{nid})
		if err != nil {
			return nil, err
		}
		v, inBase = at[nid]
	}
	var decl domain.ChangeImpact
	var reuse domain.NodeID
	switch {
	case t.create && inBase:
		return nil, fmt.Errorf("cannot create %s (%s): the reference baseline of change %s already holds it: %w", t.Key, t.Type, id, ErrConflict)
	case t.create && nid != "":
		if err := g.takeKey(ctx, tx, w.c, nid, t.Key, t.Type); err != nil {
			return nil, err
		}
		reuse = nid
		decl = domain.ChangeImpact{Intent: domain.IntentCreated, Key: t.Key, Type: t.Type, DerivedFrom: t.DerivedFrom}
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
	// a creation is not recorded yet: the change is as it was read, unless the declaration activated a draft change
	if !t.create || w.c.Status == domain.ChangeDraft {
		if err := g.reread(ctx, tx, w); err != nil {
			return nil, err
		}
	}
	w.transition, w.reuse = t.transition, reuse
	if t.create {
		// not recorded yet: the created event of the first version adds it (createTx)
		w.cn, w.vcn = out[0], out[0]
		w.seen = append(w.seen, out[0])
		return w, nil
	}
	return w, g.impact(ctx, tx, w, out[0].ID)
}

// takeKey decides a creation whose key is the key of a node out of the reference baseline of change c (a key is one
// node's): a node another change wrote on a branch that is not abandoned holds it (ErrConflict, the creation is refused
// now rather than at the commit); a node only abandoned changes wrote never reached the graph, and the creation takes
// it over, so that the work of an abandoned change (an import stopped between its commit and its merge) does not hold
// its keys for ever. A node that lives only on other lasting lines of work (a named branch, main seen from a branch
// forked from it before the node was created) is taken over too: the same key on two lines of work is the same node,
// each line writing its own versions (a file a git branch adds, then the main branch when the branch is merged, ADR
// 0151); the lasting branch the change lands on holding it is the race of two changes, refused.
func (g *Graph) takeKey(ctx context.Context, tx Tx, c domain.Change, nid domain.NodeID, key, typ string) error {
	ns := domain.NamespaceOf(c.Namespace)
	vs, err := tx.Versions(ctx, nid)
	if err != nil {
		return err
	}
	line := ""
	for _, v := range vs {
		if v.Type != typ {
			return fmt.Errorf("cannot create %s (%s): the key is taken by a node of type %s: %w", key, typ, v.Type, ErrConflict)
		}
		for _, name := range append([]string{domain.BranchOf(v.Branch)}, v.Joined...) {
			b, err := tx.Branch(ctx, ns, name)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			// a version of a merged branch lives on in the branches it joined, checked with it (a sub-change merged
			// into a change abandoned since holds nothing)
			if err == nil && (b.Status == domain.BranchAbandoned || b.Status == domain.BranchMerged) {
				continue
			}
			if err == nil && !b.Ephemeral && b.Status == domain.BranchOpen {
				if line == "" {
					if line, err = lastingLine(ctx, tx, c); err != nil {
						return err
					}
				}
				if name != line {
					continue
				}
			}
			return fmt.Errorf("cannot create %s (%s): the key is taken by a node change %s created on branch %s: rename it, or merge or abandon that change: %w", key, typ, v.ChangeID, name, ErrConflict)
		}
	}
	return nil
}

// lastingLine is the lasting branch a change lands on in the end: its branch, else the branch its branch is forked
// from, up the branches of its parent changes (ADR 0103).
func lastingLine(ctx context.Context, tx Tx, c domain.Change) (string, error) {
	b, err := branchOf(ctx, tx, c.Namespace, c.Branch)
	for err == nil && b.Ephemeral {
		b, err = branchOf(ctx, tx, c.Namespace, b.Parent)
	}
	return b.Name, err
}

// plannedCreation reports whether the change impact is a creation planned by ProposeImpact and not drafted yet: no
// draft, no version.
func plannedCreation(cn domain.ChangeImpact) bool {
	return cn.Intent == domain.IntentCreated && cn.Post == nil && cn.Committed == nil
}

// base is the stored version a draft of the impact starts from: its pre version (on the main flow, the latest of the
// node on the change branch); nil for a creation.
func (w *work) base(ctx context.Context, tx Tx) (*domain.Node, error) {
	if w.cn.Pre == nil {
		if w.cn.Committed == nil {
			return nil, nil
		}
		// a node the change created and committed: checked out again from the version it wrote (ADR 0102)
		n, err := tx.LatestOn(ctx, nodeOf(w.cn), w.branch)
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return &n, err
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
	st, err := g.draftState(ctx, tx, w.c.ID)
	if err != nil {
		return domain.Draft{}, err
	}
	d, ok := st.draft(w.cn.ID, w.flow)
	if !ok {
		if seen, err := g.seenDraft(ctx, tx, w.c, w.flow, w.cn.ID); err != nil {
			return domain.Draft{}, err
		} else if seen != nil {
			// the draft of a parent flow is that flow's to edit: this flow checks the node out to write its own
			return domain.Draft{}, fmt.Errorf("%s holds a draft of another flow: check it out on this one first: %w", draftOf(w.cn), ErrConflict)
		}
		return domain.Draft{}, fmt.Errorf("%s is not checked out: check it out first: %w", draftOf(w.cn), ErrConflict)
	}
	return d.Clone(), nil
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
// draft of the node (ADR 0076, 0077, 0079). There is no proposal of a new node, and no version until the change commits.
//
//goap:operation impact.create doer
func (g *Graph) ImpactNodeCreate(ctx context.Context, id domain.ChangeID, in NodeCreate) (cn domain.ChangeImpact, err error) {
	if err := g.allow(ctx, id, operations.ImpactCreate); err != nil {
		return cn, err
	}
	if in.Key == "" || in.Type == "" {
		return cn, invalidf("a node needs a key and a type")
	}
	if ctx, err = g.withLinkSources(ctx, id, in.Links...); err != nil {
		return cn, err
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
	d, err := g.newDraft(ctx, tx, w, in, origins)
	if err != nil {
		return domain.ChangeImpact{}, err
	}
	ref := d.Ref()
	var patch map[string]any
	if len(origins) > 0 {
		patch = map[string]any{"origins": originsPatch(ctx, tx, origins)}
	}
	if w.planned {
		// the creation was planned: its impact is recorded, the draft fulfils it
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: w.c.ID, Impact: w.cn.ID, Op: domain.ImpactCheckedOut, Flow: w.flow, Execution: in.Execution, Post: &ref, Draft: &d, Patch: patch}); err != nil {
			return domain.ChangeImpact{}, err
		}
		return w.seenAs(&ref), nil
	}
	st := w.cn // the creation adds its impact (ADR 0077)
	if err := g.emit(ctx, tx, domain.ImpactEvent{Change: w.c.ID, Impact: w.cn.ID, Op: domain.ImpactCreated, Flow: w.flow, Execution: in.Execution, State: &st, Post: &ref, Draft: &d, Patch: patch}); err != nil {
		return domain.ChangeImpact{}, err
	}
	return w.seenAs(&ref), nil
}

// newDraft is the draft of a new node: its id, key, type, initial state, owner, properties and links, checked.
func (g *Graph) newDraft(ctx context.Context, tx Tx, w *work, in NodeCreate, origins []domain.NodeRef) (domain.Draft, error) {
	d := domain.Draft{Change: w.c.ID, Impact: w.cn.ID, Flow: w.flow, Node: domain.NodeID(g.newID()), Key: in.Key, Type: in.Type,
		Properties: cloneMap(in.Properties), Origins: slices.Clone(origins), Execution: in.Execution}
	if w.reuse != "" {
		d.Node = w.reuse
	}
	if lc := w.ix.lifecycleOf(d.Type); lc != nil {
		d.State = lc.Initial
	}
	if err := w.checkDraftAttributes(d, d.Properties); err != nil {
		return d, err
	}
	if in.Owner != "" {
		unit, _, err := g.resolveStructure(ctx, tx, domain.StructureOrganisation, in.Owner)
		if err != nil {
			return d, err
		}
		d.Owner = unit
	}
	for _, l := range in.Links {
		dl, err := g.newDraftLink(ctx, tx, w, d, l)
		if err != nil {
			return d, err
		}
		d.Links = append(d.Links, dl)
	}
	return d, nil
}

// ImpactNodeCheckout puts a node in edit mode in a change: it gives the node a draft on the flow, a copy of what the
// flow sees of it (properties, state, owner and outgoing links), whatever its state (ADR 0078). The node is checked
// out once per change and flow: it is refused while the flow holds a draft of it (update it, ADR 0079). A flow whose
// parent flow holds a draft copies that one. A checkout modifies nothing: the review stands until the draft is edited
// (editedAgain, ADR 0102).
//
//goap:operation impact.checkout doer
func (g *Graph) ImpactNodeCheckout(ctx context.Context, id domain.ChangeID, in NodeCheckout) (cn domain.ChangeImpact, err error) {
	if err := g.allow(ctx, id, operations.ImpactCheckout); err != nil {
		return cn, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.resolve(ctx, tx, id, checkoutTarget(in))
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
	if own, err := g.ownDraftOf(ctx, tx, id, w.cn.ID, w.flow); err != nil {
		return domain.NodeRef{}, err
	} else if own != nil {
		return domain.NodeRef{}, fmt.Errorf("%s is already checked out: %w", draftOf(w.cn), ErrConflict)
	}
	var d domain.Draft
	var event string
	var copied *domain.BaseCopy
	if parent, err := g.seenDraft(ctx, tx, w.c, w.flow, w.cn.ID); err != nil {
		return domain.NodeRef{}, err
	} else if parent != nil {
		d = g.forkDraft(w, *parent, execution)
	} else {
		base, err := w.base(ctx, tx)
		if err != nil {
			return domain.NodeRef{}, err
		}
		if base == nil && plannedCreation(w.cn) {
			// a planned creation checked out: its first draft, bare (ADR 0076: the creation fulfils the plan)
			if d, err = g.newDraft(ctx, tx, w, NodeCreate{Key: w.cn.Key, Type: w.cn.Type, Execution: execution}, nil); err != nil {
				return domain.NodeRef{}, err
			}
			ref := d.Ref()
			return ref, g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: w.cn.ID, Op: domain.ImpactCheckedOut, Flow: w.flow, Execution: execution, Post: &ref, Draft: &d})
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
		event = g.newID()
		d, copied = g.draftFrom(w, event, *base, links, execution)
	}
	ref := d.Ref()
	if err := g.emit(ctx, tx, domain.ImpactEvent{ID: event, Change: id, Impact: w.cn.ID, Op: domain.ImpactCheckedOut, Flow: w.flow, Execution: execution, Post: &ref, Draft: &d, FromBase: copied}); err != nil {
		return ref, err
	}
	return ref, nil
}

// ImpactNodeUpdate edits the draft of a change impact: its properties (merged and validated) and its owner. No version
// is written; the edit is an updated event of the impact log.
//
//goap:operation impact.update doer
func (g *Graph) ImpactNodeUpdate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, in NodeUpdate) (cn domain.ChangeImpact, err error) {
	if err := g.allow(ctx, id, operations.ImpactUpdate); err != nil {
		return cn, err
	}
	if len(in.Properties) == 0 && in.Owner == "" {
		return cn, invalidf("nothing to update")
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.resolve(ctx, tx, id, target{Impact: impact, Node: in.Node, Key: in.Key, Flow: in.Flow, Execution: in.Execution})
		if err != nil {
			return err
		}
		d, err := g.working(ctx, tx, w)
		if err != nil {
			return err
		}
		patch := map[string]any{}
		if len(in.Properties) > 0 {
			props := cloneMap(d.Properties)
			if props == nil {
				props = map[string]any{}
			}
			maps.Copy(props, in.Properties)
			if err := w.checkDraftAttributes(d, props); err != nil {
				return err
			}
			patch["props"] = in.Properties
		}
		if in.Owner != "" {
			unit, _, err := g.resolveStructure(ctx, tx, domain.StructureOrganisation, in.Owner)
			if err != nil {
				return err
			}
			patch["owner"] = unit.String()
		}
		ref := d.Ref()
		if err := g.emitUpdated(ctx, tx, w, ref, in.Execution, patch); err != nil {
			return err
		}
		cn = w.seenAs(&ref)
		return nil
	})
	return
}

// removeLinkPatch describes a link removed from a draft, for the updated event.
func removeLinkPatch(l domain.DraftLink) map[string]any {
	if l.External() {
		return map[string]any{"id": string(l.ID), "type": l.Type, "to": l.Target.String(), "target": l.Target.String()}
	}
	return map[string]any{"id": string(l.ID), "type": l.Type, "to": l.To.String(), "toId": string(l.To.ID)}
}

// linkPatch describes a link added to a draft, for the updated event: the audit trail reads to, the draft fold the rest.
func linkPatch(l domain.DraftLink) map[string]any {
	if l.External() {
		return map[string]any{"id": string(l.ID), "type": l.Type, "to": l.Target.String(), "target": l.Target.String(), "props": l.Properties}
	}
	return map[string]any{"id": string(l.ID), "type": l.Type, "to": l.To.String(), "toId": string(l.To.ID), "toVersion": int(l.To.Version), "props": l.Properties}
}

// ImpactLinkCreate adds an outgoing link to the draft of a change impact.
//
//goap:operation impact.link.create doer
func (g *Graph) ImpactLinkCreate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, l LinkWrite, flow, execution string) (link domain.Link, err error) {
	if err := g.allow(ctx, id, operations.ImpactLinkCreate); err != nil {
		return link, err
	}
	if ctx, err = g.withLinkSources(ctx, id, l); err != nil {
		return link, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.workOn(ctx, tx, id, flow)
		if err != nil {
			return err
		}
		if err := g.impact(ctx, tx, w, impact); err != nil {
			return err
		}
		d, err := g.working(ctx, tx, w)
		if err != nil {
			return err
		}
		dl, err := g.newDraftLink(ctx, tx, w, d, l)
		if err != nil {
			return err
		}
		link = domain.Link{ID: dl.ID, Type: dl.Type, From: d.Ref(), To: dl.To, Target: dl.Target, Properties: dl.Properties, ChangeID: id}
		return g.emitUpdated(ctx, tx, w, d.Ref(), execution, map[string]any{"addLink": linkPatch(dl)})
	})
	return
}

// emitUpdated records an edit of the draft ref (the updated event with its patch), then sends a decided review back to
// proposed (editedAgain).
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
	return g.emitUpdated(ctx, tx, w, d.Ref(), execution, map[string]any{"removeLink": removeLinkPatch(l)})
}

// linkWork finds the draft of the flow holding a link, and the link: by its id, or by the id of the stored link of the
// version the draft was checked out from, which the checkout copied.
func (g *Graph) linkWork(ctx context.Context, tx Tx, id domain.ChangeID, link domain.LinkID, flow string) (*work, domain.Draft, domain.DraftLink, error) {
	w, err := g.workOn(ctx, tx, id, flow)
	if err != nil {
		return nil, domain.Draft{}, domain.DraftLink{}, err
	}
	var stored *domain.Link
	if l, err := tx.Link(ctx, link); err == nil {
		stored = &l
	} else if !errors.Is(err, ErrNotFound) {
		return nil, domain.Draft{}, domain.DraftLink{}, err
	}
	// a stored link is held by the drafts of its source; a link a draft added, by any
	var rows []domain.Draft
	if stored != nil {
		rows, err = g.nodeDrafts(ctx, tx, id, stored.From.ID)
	} else {
		rows, err = g.drafts(ctx, tx, id)
	}
	if err != nil {
		return nil, domain.Draft{}, domain.DraftLink{}, err
	}
	other := false
	for _, d := range rows {
		var found *domain.DraftLink
		if l, ok := d.Link(link); ok {
			found = &l
		} else if stored != nil && d.Base != nil && d.Base.ID == stored.From.ID {
			// the stored link is one the version checked out holds (its set may be an earlier version's, ADR 0141)
			held, err := tx.OutLinks(ctx, *d.Base)
			if err != nil {
				return nil, domain.Draft{}, domain.DraftLink{}, err
			}
			if !slices.ContainsFunc(held, func(l domain.Link) bool { return l.ID == stored.ID }) {
				continue
			}
			for _, c := range d.Links {
				if c.Type == stored.Type && c.To.ID == stored.To.ID && c.Target == stored.Target {
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
//
//goap:operation impact.link.update doer
func (g *Graph) ImpactLinkUpdate(ctx context.Context, id domain.ChangeID, link domain.LinkID, props map[string]any, flow, execution string) (l domain.Link, err error) {
	if err := g.allow(ctx, id, operations.ImpactLinkUpdate); err != nil {
		return l, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, d, cur, err := g.linkWork(ctx, tx, id, link, flow)
		if err != nil {
			return err
		}
		if err := w.ix.checkLinkAttributes(cur.Type, props); err != nil {
			return err
		}
		l = domain.Link{ID: cur.ID, Type: cur.Type, From: d.Ref(), To: cur.To, Target: cur.Target, Properties: props, ChangeID: id}
		patch := map[string]any{"id": string(cur.ID), "type": cur.Type, "toId": string(cur.To.ID), "props": props}
		if cur.External() {
			patch = map[string]any{"id": string(cur.ID), "type": cur.Type, "target": cur.Target.String(), "props": props}
		}
		return g.emitUpdated(ctx, tx, w, d.Ref(), execution, map[string]any{"updateLink": patch})
	})
	return
}

// ImpactLinkDelete removes an outgoing link of a draft: removing a child is a modification of its parent (ADR 0024 §4).
//
//goap:operation impact.link.delete doer
func (g *Graph) ImpactLinkDelete(ctx context.Context, id domain.ChangeID, link domain.LinkID, flow, execution string) error {
	if err := g.allow(ctx, id, operations.ImpactLinkDelete); err != nil {
		return err
	}
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
// origins. Nothing is written: the version is written when the change commits. It does nothing when the impact has no
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
	return g.checkDraftOf(ctx, tx, w.c, w.flow, w.ix, w.seen, w.vcn, d)
}

// checkAcceptedIn is checkAccepted for a verdict of a batch on the main flow, over the scope of the batch.
func (g *Graph) checkAcceptedIn(ctx context.Context, tx Tx, s *reviewScope, cn domain.ChangeImpact) error {
	d, err := g.seenDraft(ctx, tx, s.c, "", cn.ID)
	if err != nil || d == nil {
		return err
	}
	if err := g.checkOrigins(s.seen, cn, d); err != nil {
		return err
	}
	return g.checkDraft(ctx, s.ix, s.c, *d, draftResolver(ctx, tx, s.r))
}

// checkDraftOf is the check of an accepted review on one draft, writing nothing: the origins gate over the change
// impacts the flow sees, then what a version must satisfy. It serves the review (checkAccepted) and the validation the
// views carry (ImpactValidation).
func (g *Graph) checkDraftOf(ctx context.Context, tx Tx, c domain.Change, flow string, ix *typeIndex, seen []domain.ChangeImpact, cn domain.ChangeImpact, d *domain.Draft) error {
	if err := g.checkOrigins(seen, cn, d); err != nil {
		return err
	}
	r, err := g.newDraftReader(ctx, tx, c, flow, seen)
	if err != nil {
		return err
	}
	return g.checkDraft(ctx, ix, c, *d, draftResolver(ctx, tx, r))
}

// editedAgain sends a decided review (accepted or rejected) back to proposed when the content of its draft changes (ADR
// 0077, 0102): an edit of its properties, owner or links, a cancel. What was reviewed is no longer what is there: a
// rejected impact is reworked this way, or withdrawn. A move of its lifecycle is no edit: it is judged by its transition
// (authorization, guard) when it is taken, and leaves the review as it is (a guard may read it). It follows the event
// that changed the draft.
func (g *Graph) editedAgain(ctx context.Context, tx Tx, w *work, execution string) error {
	if w.vcn.Review == domain.ReviewProposed {
		return nil
	}
	// accepted or rejected, the draft it was reviewed on is gone: the modification is proposed again (ADR 0102)
	r := domain.Review{Status: domain.ReviewProposed, By: g.caller(ctx), At: g.now(), Comment: "edited again", Flow: w.flow, Execution: execution}
	if err := g.emit(ctx, tx, domain.ImpactEvent{Change: w.c.ID, Impact: w.cn.ID, Op: domain.ImpactReviewed, Flow: w.flow, Execution: execution, By: r.By, Review: &r}); err != nil {
		return err
	}
	w.vcn.Review = domain.ReviewProposed
	return nil
}

// ImpactNodeCancel drops the draft of a change impact on the flow: the impact goes back to what the flow saw before (the
// draft of a parent flow), or to none. A creation cancelled leaves no node: the change impact is removed.
//
//goap:operation impact.cancel doer
func (g *Graph) ImpactNodeCancel(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow, execution string) (cn domain.ChangeImpact, err error) {
	if err := g.allow(ctx, id, operations.ImpactCancel); err != nil {
		return cn, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.workOn(ctx, tx, id, flow)
		if err != nil {
			return err
		}
		if err := g.impact(ctx, tx, w, impact); err != nil {
			return err
		}
		d, err := g.ownDraftOf(ctx, tx, id, impact, w.flow)
		if err != nil {
			return err
		}
		if d == nil {
			return fmt.Errorf("change impact %s (%s) is not checked out: %w", impact, w.cn.Key, ErrConflict)
		}
		// what the flow falls back to: the draft of its parent flows
		var back *domain.NodeRef
		if w.flow == "" && w.cn.Committed != nil {
			// committed already (ADR 0102): the impact falls back to the version the change wrote
			r := *w.cn.Committed
			back = &r
		} else if ids := flowIDs(flowChain(w.c, w.flow)); w.flow != "" {
			for _, f := range append(ids[1:], "") {
				if other, err := g.ownDraftOf(ctx, tx, id, impact, f); err != nil {
					return err
				} else if other != nil {
					r := d.Ref()
					back = &r
					break
				}
			}
		}
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: impact, Op: domain.ImpactCancelled, Flow: w.flow, Execution: execution, Post: back}); err != nil {
			return err
		}
		if w.cn.Intent == domain.IntentModified || w.cn.Committed != nil { // a creation cancelled is gone with its impact
			if err := g.editedAgain(ctx, tx, w, execution); err != nil {
				return err
			}
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
//
//goap:operation impact.transition doer
func (g *Graph) ImpactNodeTransition(ctx context.Context, id domain.ChangeID, in NodeTransition) (cn domain.ChangeImpact, err error) {
	if err := g.allow(ctx, id, operations.ImpactTransition); err != nil {
		return cn, err
	}
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
		own, err := g.ownDraftOf(ctx, tx, id, w.cn.ID, w.flow)
		if err != nil {
			return err
		}
		if own == nil {
			// the node is checked out first, the draft the move acts on
			if _, err := g.checkoutTx(ctx, tx, w, in.Execution); err != nil {
				return err
			}
			if err := g.reread(ctx, tx, w); err != nil {
				return err
			}
			if err := g.impact(ctx, tx, w, w.cn.ID); err != nil {
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
		reader.put(moved)
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
	tg := checkoutTarget(in.NodeCheckout)
	tg.transition = true
	w, err := g.resolve(ctx, tx, id, tg)
	if err != nil {
		return nil, domain.Node{}, domain.Transition{}, err
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
func (g *Graph) viewTarget(ctx context.Context, tx Tx, w *work) (*stateView, error) {
	if own, ok, err := ownBranch(ctx, tx, w.c); err != nil {
		return nil, err
	} else if ok {
		return newBranchView(ctx, tx, own), nil
	}
	b, err := branchHeadHeader(ctx, tx, w.c.Namespace, domain.BranchOf(w.c.Branch))
	if errors.Is(err, ErrNotFound) || (err == nil && b.ID == "") {
		b, err = headerOf(ctx, tx, w.c.BaselineID)
	}
	if err != nil {
		return nil, err
	}
	return newStateView(ctx, tx, b.ID), nil
}

// WithdrawImpact takes a change impact out of the change, at any time while the change is open (ADR 0076 §5b, ADR 0102):
// its draft, if any, is dropped (a creation leaves no node behind) and the impact leaves the list, whatever its review;
// the impacts that come with it (via: the sources of a merge or a split its parent realizes) leave with it. One the
// change committed already is reverted on its branch by the next commit (the node goes back to the version the branch
// was forked from). A rejected impact is withdrawn this way, or reworked (editedAgain). Only the flow that declared an
// impact withdraws it.
//
//goap:operation impact.withdraw doer
func (g *Graph) WithdrawImpact(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow, execution string) error {
	if err := g.allow(ctx, id, operations.ImpactWithdraw); err != nil {
		return err
	}
	return g.repo.InTx(ctx, func(tx Tx) error {
		return g.withdrawTx(ctx, tx, id, []domain.ChangeImpactID{impact}, flow, execution)
	})
}

// WithdrawImpacts takes several change impacts out of the change at once, in one transaction: all of them or none.
// Each is withdrawn as WithdrawImpact does (the impacts via one of them leave with it); an impact named twice, or
// leaving already with another one of the list, is withdrawn once.
func (g *Graph) WithdrawImpacts(ctx context.Context, id domain.ChangeID, impacts []domain.ChangeImpactID, flow, execution string) error {
	if len(impacts) == 0 {
		return invalidf("no change impact to withdraw")
	}
	if err := g.allow(ctx, id, operations.ImpactWithdraw); err != nil {
		return err
	}
	return g.repo.InTx(ctx, func(tx Tx) error {
		return g.withdrawTx(ctx, tx, id, impacts, flow, execution)
	})
}

// withdrawTx emits the withdrawal of the impacts and of the impacts via them, each once.
func (g *Graph) withdrawTx(ctx context.Context, tx Tx, id domain.ChangeID, impacts []domain.ChangeImpactID, flow, execution string) error {
	w, err := g.workOn(ctx, tx, id, flow)
	if err != nil {
		return err
	}
	w.withdraw = true
	gone := map[domain.ChangeImpactID]bool{}
	var events []domain.ImpactEvent
	withdraw := func(impact domain.ChangeImpactID) {
		if !gone[impact] {
			gone[impact] = true
			events = append(events, domain.ImpactEvent{Change: id, Impact: impact, Op: domain.ImpactWithdrawn, Flow: w.flow, Execution: execution})
		}
	}
	for _, impact := range impacts {
		if gone[impact] {
			continue
		}
		if err := g.impact(ctx, tx, w, impact); err != nil {
			return err
		}
		if w.cn.Flow != w.flow {
			return fmt.Errorf("change impact %s was declared on another flow: %w", impact, ErrConflict)
		}
		for _, o := range w.c.Nodes {
			if o.Via == impact {
				withdraw(o.ID)
			}
		}
		withdraw(impact)
	}
	return g.emit(ctx, tx, events...)
}
