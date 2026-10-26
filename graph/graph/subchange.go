package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/events"
	"github.com/zimwip/goap/contracts/lnk"
)

// requirement is a number of outgoing links of one type a node must have: what the parent of a structure (ADR 0054)
// and the required links a node type declares (ADR 0065) come down to. of describes it in error messages.
type requirement struct {
	linkType string
	count    int
	of       string
}

// requirementsOf returns the links a node of type typ and key must carry: the parent of a node of a structure,
// except the root of it, and the links its node type requires. Nil for any other node.
func (g *Graph) requirementsOf(typ, key string) []requirement {
	var out []requirement
	for _, st := range g.structures() {
		if typ == st.Type && key != st.Root {
			out = append(out, requirement{st.Parent, 1, fmt.Sprintf("a parent %s (%s)", st.Type, st.Parent)})
		}
	}
	for _, r := range g.requires(typ) {
		out = append(out, requirement{r.Link, r.Exactly(), fmt.Sprintf("exactly %d %s", r.Exactly(), r.Link)})
	}
	return out
}

// checkRequiredParent enforces that a created node names the links its type requires: exactly one parent link for a
// node of a structure (ADR 0054: a unit of the organisation, a project), the links a node type declares through `requires`
// (ADR 0065: a User needs one member_of, ADR 0040) — except the roots of the structures, created by the bootstrap.
// Only creation is checked here: an edit that modifies an existing node's membership (the move action) is
// responsible for its own atomicity (removing the old link and adding the new one in the same edit).
func (g *Graph) checkRequiredParent(e NodeEdit) error {
	if e.Pre != nil {
		return nil
	}
	for _, r := range g.requirementsOf(e.Type, e.Key) {
		n := 0
		for _, l := range e.Links {
			if l.Type == r.linkType {
				n++
			}
		}
		if n != r.count {
			return fmt.Errorf("%s needs %s, got %d: %w", nodeName(e), r.of, n, ErrInvalid)
		}
	}
	return nil
}

// checkRequiredLinks checks that a version or a draft carries the links its type requires (the parent of a node of a
// structure, the membership of a User): read back from the node itself, so a client that built its edits from a stale
// read (a link it did not know of) fails instead of leaving the node with zero or two of them. Checked when a draft is
// accepted (ADR 0079) and when the version commits (Apply), never while the draft is edited, which may be on its way there.
func (g *Graph) checkRequiredLinks(n domain.Node, links []domain.Link) error {
	reqs := g.requirementsOf(n.Type, n.Key)
	if len(reqs) == 0 || n.Deleted {
		return nil
	}
	for _, r := range reqs {
		count := 0
		for _, l := range links {
			if l.Type == r.linkType {
				count++
			}
		}
		if count != r.count {
			return fmt.Errorf("%s needs %s, got %d: %w", n.Key, r.of, count, ErrInvalid)
		}
	}
	return nil
}

// nodeResolver reads the node a link of a draft or of a written version reaches, as it will be: the draft for a draft
// reference, else the stored version. ok is false when it cannot be found.
type nodeResolver func(ref domain.NodeRef) (n domain.Node, ok bool, err error)

// storedResolver resolves exact versions from the store: the links of a version written by a commit, whose drafts
// references have been replaced by the versions it writes.
func storedResolver(ctx context.Context, tx Tx) nodeResolver {
	return func(ref domain.NodeRef) (domain.Node, bool, error) {
		n, err := tx.Node(ctx, ref)
		if errors.Is(err, ErrNotFound) {
			return domain.Node{}, false, nil
		}
		return n, err == nil, err
	}
}

// draftResolver resolves the references of a draft through the drafts the reader holds (the change's own, and the ones
// of its parents), else the store.
func draftResolver(ctx context.Context, tx Tx, r *draftReader) nodeResolver {
	stored := storedResolver(ctx, tx)
	return func(ref domain.NodeRef) (domain.Node, bool, error) {
		ref = r.normalize(ref)
		if ref.IsDraft() {
			d, ok := r.get(ref.ID)
			if !ok {
				return domain.Node{}, false, nil
			}
			return r.draftNode(d), true, nil
		}
		return stored(ref)
	}
}

// checkVersion is what a draft must satisfy to be accepted and the version written from it to commit (ADR 0076, 0079): its
// properties (attributes, attribute validators and node validators of its type), the links its type requires, and for
// each outgoing link its attributes, their validators and the link validators of its type. A link whose target cannot
// be resolved is not given to the link validators: a dangling link is reported by the checks of the links themselves.
func (g *Graph) checkVersion(ctx context.Context, ix *typeIndex, n domain.Node, links []domain.Link, resolve nodeResolver) error {
	if err := g.validateProps(ctx, ix, n, n.Properties); err != nil {
		return err
	}
	if err := g.checkRequiredLinks(n, links); err != nil {
		return err
	}
	for _, l := range links {
		if err := ix.checkLinkAttributes(l.Type, l.Properties); err != nil {
			return fmt.Errorf("%s: %w", n.Key, err)
		}
		if err := g.runLinkValidators(ctx, ix, n, l, resolve); err != nil {
			return err
		}
	}
	return nil
}

// checkDraft checks a draft as the version written from it will be.
func (g *Graph) checkDraft(ctx context.Context, ix *typeIndex, c domain.Change, d domain.Draft, resolve nodeResolver) error {
	return g.checkVersion(ctx, ix, draftNode(c, d), d.OutLinks(), resolve)
}

// prepareSubChange applies the rules of a sub-change to c: its parent must be
// open and is given a branch of its own when it has none, the namespace is the
// parent's, and the change is forked from the head of the parent's branch, the
// branch it merges into (ADR 0103). Its organisational unit must be inside the
// unit of the parent.
func (g *Graph) prepareSubChange(ctx context.Context, tx Tx, c *domain.Change, in *NewChange) error {
	if c.ParentID == "" {
		return nil
	}
	parent, err := tx.Change(ctx, c.ParentID)
	if err != nil {
		return err
	}
	switch parent.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned, domain.ChangeCommitted:
		return fmt.Errorf("parent change %s is %s: %w", parent.ID, parent.Status, ErrConflict)
	}
	own, ok, err := ownBranch(ctx, tx, parent)
	if err != nil {
		return err
	}
	if !ok {
		// the branch of the parent is where its sub-changes are forked from and merged into
		if parent, own, err = g.ensureOwnBranch(ctx, tx, parent); err != nil {
			return err
		}
	}
	if in.Namespace != "" && domain.NamespaceOf(in.Namespace) != domain.NamespaceOf(parent.Namespace) {
		return fmt.Errorf("a sub-change acts on the namespace of its parent (%s): %w", domain.NamespaceOf(parent.Namespace), ErrInvalid)
	}
	c.Namespace = domain.NamespaceOf(parent.Namespace)
	c.Branch = own.Name
	in.OwnBranch = true
	if c.BaselineID == "" {
		// the lasting baseline under the branch of the parent: the branch of a change holds none (ADR 0140)
		c.BaselineID = own.ForkBaseline
	}
	if c.Methodology == "" {
		c.Methodology = parent.Methodology
	}
	// a sub-change works towards its parent's goal unless it names one of its own, or a methodology other than the
	// parent's (whose default goal CreateChange has set, ADR 0096)
	if in.Goal == "" && (in.Methodology == "" || in.Methodology == parent.Methodology) {
		c.Goal = parent.Goal
	}
	if c.OwnerOrg.IsZero() {
		c.OwnerOrg = parent.OwnerOrg
	} else if c.OwnerOrg != parent.OwnerOrg {
		ok, err := g.within(ctx, tx, domain.StructureOrganisation, c.OwnerOrg, parent.OwnerOrg)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("unit %s is not part of %s, the unit of the parent change: %w", c.OwnerOrg, parent.OwnerOrg, ErrInvalid)
		}
	}
	if c.ProjectID.IsZero() {
		c.ProjectID = parent.ProjectID
	} else if c.ProjectID != parent.ProjectID {
		ok, err := g.within(ctx, tx, domain.StructureProject, c.ProjectID, parent.ProjectID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("project %s is not part of %s, the project of the parent change: %w", c.ProjectID, parent.ProjectID, ErrInvalid)
		}
	}
	return nil
}

// SubChanges lists the sub-changes of a change, oldest first.
func (g *Graph) SubChanges(ctx context.Context, id domain.ChangeID) (out []domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { out, err = subChanges(ctx, tx, id); return err })
	return
}

func subChanges(ctx context.Context, tx Tx, id domain.ChangeID) ([]domain.Change, error) {
	return tx.SubChanges(ctx, id)
}

// openSubChanges are the sub-changes that still have to be applied or abandoned.
func openSubChanges(ctx context.Context, tx Tx, id domain.ChangeID) ([]domain.Change, error) {
	subs, err := subChanges(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(subs, func(c domain.Change) bool {
		return c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned
	}), nil
}

// abandonSubChanges abandons the open sub-changes of a change (and their branches), recursively.
func (g *Graph) abandonSubChanges(ctx context.Context, tx Tx, id domain.ChangeID) error {
	open, err := openSubChanges(ctx, tx, id)
	if err != nil {
		return err
	}
	for _, sc := range open {
		if err := g.abandonSubChanges(ctx, tx, sc.ID); err != nil {
			return err
		}
		if own, ok, err := ownBranch(ctx, tx, sc); err != nil {
			return err
		} else if ok {
			own.Status = domain.BranchAbandoned
			if err := tx.PutBranch(ctx, own); err != nil {
				return err
			}
		}
		sc.Status = domain.ChangeAbandoned
		if err := tx.PutChange(ctx, sc); err != nil {
			return err
		}
	}
	return nil
}

// SplitByOwner splits a change along organisational boundaries: one sub-change per unit owning nodes the change has
// a candidate impact on (the owner of the pre version of a modified change impact, ADR 0024, 0054). The candidate moves to
// the sub-change (a copy derived from the parent's, withdrawn from the parent: ADR 0103). Units that already have an open sub-change are left
// as they are. Impacts on nodes the unit holding the change owns, or a unit outside it, stay with the parent.
func (g *Graph) SplitByOwner(ctx context.Context, id domain.ChangeID) (created []domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		parent, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		existing, err := openSubChanges(ctx, tx, id)
		if err != nil {
			return err
		}
		have := map[lnk.URL]bool{}
		for _, sc := range existing {
			have[sc.OwnerOrg] = true
		}
		type group struct {
			org   lnk.URL
			name  string
			nodes []domain.ChangeImpact
		}
		var groups []*group
		byOrg := map[lnk.URL]*group{}
		seen, err := g.newFlowNodes(tx, parent, "").nodes(ctx) // the change impacts of the main flow: stored and derived from items
		if err != nil {
			return err
		}
		for _, cn := range parent.Nodes {
			if !slices.ContainsFunc(seen, func(x domain.ChangeImpact) bool { return x.ID == cn.ID }) && len(cn.Items) == 0 {
				continue // a candidate of a flow, or replaced by one
			}
			if cn.Pre == nil || cn.Intent != domain.IntentModified || cn.Post != nil || cn.Superseded {
				continue // only a candidate moves: a node the change works on already stays with it
			}
			pre, err := tx.Node(ctx, *cn.Pre)
			if err != nil {
				return err
			}
			if pre.Responsible == parent.OwnerOrg {
				continue // the unit holding the change owns it
			}
			unit, err := g.structureNode(ctx, tx, domain.StructureOrganisation, pre.Responsible)
			if err != nil {
				return err
			}
			if below, err := g.within(ctx, tx, domain.StructureOrganisation, pre.Responsible, parent.OwnerOrg); err != nil {
				return err
			} else if !below {
				continue // owned outside the unit holding the change: nothing to delegate to
			}
			gr := byOrg[pre.Responsible]
			if gr == nil {
				name, _ := unit.Properties["name"].(string)
				gr = &group{org: pre.Responsible, name: firstNonEmpty(name, unit.Key)}
				byOrg[pre.Responsible] = gr
				groups = append(groups, gr)
			}
			gr.nodes = append(gr.nodes, cn)
		}
		for _, gr := range groups {
			if have[gr.org] {
				continue
			}
			in := NewChange{Title: parent.Title + " / " + gr.name, Intent: parent.Intent, Methodology: parent.Methodology, ParentID: id, OwnerOrg: gr.org.String()}
			sub := domain.Change{ID: domain.ChangeID(g.newID()), Title: in.Title, Intent: in.Intent, Methodology: in.Methodology, Status: domain.ChangeDraft,
				ParentID: id, OwnerOrg: gr.org, CreatedAt: g.now()}
			if err := g.prepareSubChange(ctx, tx, &sub, &in); err != nil {
				return err
			}
			if err := g.scopeChange(ctx, tx, &sub); err != nil {
				return err
			}
			fork, err := headerOf(ctx, tx, sub.BaselineID)
			if err != nil {
				return err
			}
			// forked as a sub-change is created (createChange): at the point of the branch of its parent (ADR 0103, 0140)
			own := domain.Branch{Ephemeral: true, Name: changeBranchName(sub.ID), Namespace: sub.Namespace, Parent: sub.Branch, ForkBaseline: fork.ID,
				Origin: domain.ChangeBranchOrigin(sub.ID), Intent: domain.IntentDerive, Status: domain.BranchOpen, CreatedAt: g.now()}
			if pb, err := tx.Branch(ctx, sub.Namespace, sub.Branch); err != nil {
				return err
			} else if pb.Ephemeral {
				if own.ForkBaseline, own.ForkSeq, err = forkPoint(ctx, tx, pb); err != nil {
					return err
				}
			}
			if err := tx.PutBranch(ctx, own); err != nil {
				return err
			}
			sub.Branch = own.Name
			if err := tx.PutChange(ctx, sub); err != nil {
				return err
			}
			for _, cn := range gr.nodes {
				// the candidate moves to the sub-change (ADR 0103): withdrawn from the parent, it comes back to it with the
				// version the sub-change commits, when the sub-change merges
				if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: cn.ID, Op: domain.ImpactWithdrawn, By: "graph.split_by_owner"}); err != nil {
					return err
				}
				// DerivedFrom holds the id of the parent's change impact it is copied from
				cp := domain.ChangeImpact{ID: domain.ChangeImpactID(g.newID()), Key: cn.Key, Type: cn.Type, Intent: domain.IntentModified, Rationale: cn.Rationale,
					Pre: cn.Pre, Review: domain.ReviewProposed, ProducedBy: "graph.split_by_owner", DerivedFrom: []domain.ItemID{domain.ItemID(cn.ID)}, CreatedAt: g.now()}
				if err := g.emit(ctx, tx, domain.ImpactEvent{Change: sub.ID, Impact: cp.ID, Op: domain.ImpactProposed, By: cp.ProducedBy, State: &cp}); err != nil {
					return err
				}
			}
			sub.Status = domain.ChangeActive
			if err := tx.PutChange(ctx, sub); err != nil {
				return err
			}
			g.raise(ctx, tx, events.ChangeCreated, sub, nil)
			created = append(created, sub)
		}
		return nil
	})
	return
}
