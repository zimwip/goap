package graph

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
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

// checkParentInvariant re-verifies, after a write lands a node version, that a node still has the links its type
// requires (the parent of a node of a structure, the membership of a User). Unlike checkRequiredParent (creation
// only, checked against the edit's own declared links before anything is written), this reads the node's
// actual links back from the version just written: a client that built its edit from a stale read (e.g. a
// baseline snapshot older than an import write outside of any change, see EnsureUser/ADR 0040) would
// otherwise silently leave the node with zero or two links instead of failing loudly.
func (g *Graph) checkParentInvariant(ctx context.Context, ref domain.NodeRef) error {
	n, err := g.Node(ctx, ref)
	if err != nil {
		return err
	}
	reqs := g.requirementsOf(n.Type, n.Key)
	if len(reqs) == 0 {
		return nil
	}
	links, err := g.OutLinksOf(ctx, ref)
	if err != nil {
		return err
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

// prepareSubChange applies the rules of a sub-change to c: its parent must be
// open and have a branch of its own, the namespace is the parent's, and the
// change forks from (and is merged into) the parent branch. Its organisational
// unit must be inside the unit of the parent.
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
		return fmt.Errorf("parent change %s has no branch of its own to merge sub-changes into: %w", parent.ID, ErrInvalid)
	}
	if in.Namespace != "" && domain.NamespaceOf(in.Namespace) != domain.NamespaceOf(parent.Namespace) {
		return fmt.Errorf("a sub-change acts on the namespace of its parent (%s): %w", domain.NamespaceOf(parent.Namespace), ErrInvalid)
	}
	c.Namespace = domain.NamespaceOf(parent.Namespace)
	c.Branch = own.Name
	in.OwnBranch = true
	if c.BaselineID == "" {
		head, err := branchHead(ctx, tx, c.Namespace, own.Name)
		if err != nil {
			return err
		}
		c.BaselineID = head.ID
	}
	if c.Methodology == "" {
		c.Methodology = parent.Methodology
	}
	if c.OwnerOrg == "" {
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
	if c.ProjectID == "" {
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
	all, err := tx.Changes(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Change
	for _, c := range all {
		if c.ParentID == id {
			out = append(out, c)
		}
	}
	return out, nil
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
// an impact on (the owner of the pre version of a modified change impact, ADR 0024, 0054). The sub-change gets a copy
// of the change impacts of its nodes, derived from the parent's. Units that already have an open sub-change are left
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
		have := map[string]bool{}
		for _, sc := range existing {
			have[sc.OwnerOrg] = true
		}
		type group struct {
			org, name string
			nodes     []domain.ChangeImpact
		}
		var groups []*group
		byOrg := map[string]*group{}
		seen, err := g.newFlowNodes(tx, parent, "").nodes(ctx) // the change impacts of the main flow: stored and derived from items
		if err != nil {
			return err
		}
		for _, cn := range parent.Nodes {
			if !slices.ContainsFunc(seen, func(x domain.ChangeImpact) bool { return x.ID == cn.ID }) && len(cn.Items) == 0 {
				continue // a candidate of a flow, or replaced by one
			}
			if cn.Pre == nil || cn.Intent != domain.IntentModified || cn.Review == domain.ReviewRejected || cn.Superseded {
				continue
			}
			pre, err := tx.Node(ctx, *cn.Pre)
			if err != nil {
				return err
			}
			unit, err := tx.Node(ctx, domain.NodeRef{ID: pre.Owner})
			if err != nil {
				return err
			}
			if unit.Key == parent.OwnerOrg {
				continue // the unit holding the change owns it
			}
			if below, err := g.within(ctx, tx, domain.StructureOrganisation, unit.Key, parent.OwnerOrg); err != nil {
				return err
			} else if !below {
				continue // owned outside the unit holding the change: nothing to delegate to
			}
			gr := byOrg[unit.Key]
			if gr == nil {
				name, _ := unit.Properties["name"].(string)
				gr = &group{org: unit.Key, name: firstNonEmpty(name, unit.Key)}
				byOrg[unit.Key] = gr
				groups = append(groups, gr)
			}
			gr.nodes = append(gr.nodes, cn)
		}
		for _, gr := range groups {
			if have[gr.org] {
				continue
			}
			in := NewChange{Title: parent.Title + " / " + gr.name, Intent: parent.Intent, Methodology: parent.Methodology, ParentID: id, OwnerOrg: gr.org}
			sub := domain.Change{ID: domain.ChangeID(g.newID()), Title: in.Title, Intent: in.Intent, Methodology: in.Methodology, Status: domain.ChangeDraft,
				ParentID: id, OwnerOrg: gr.org, CreatedAt: g.now()}
			if err := g.prepareSubChange(ctx, tx, &sub, &in); err != nil {
				return err
			}
			if err := g.scopeChange(ctx, tx, &sub); err != nil {
				return err
			}
			fork, err := tx.Baseline(ctx, sub.BaselineID)
			if err != nil {
				return err
			}
			own := domain.Branch{Name: changeBranchName(sub.ID), Parent: sub.Branch, ForkBaseline: fork.ID, Head: fork.ID,
				Origin: domain.ChangeBranchOrigin(sub.ID), Status: domain.BranchOpen, CreatedAt: g.now()}
			if err := tx.PutBranch(ctx, own); err != nil {
				return err
			}
			sub.Branch = own.Name
			if err := tx.PutChange(ctx, sub); err != nil {
				return err
			}
			for _, cn := range gr.nodes {
				// the impact is confirmed for the parent, the work moves to the sub-change
				if stored := slices.IndexFunc(parent.Nodes, func(x domain.ChangeImpact) bool { return x.ID == cn.ID && len(x.Items) == 0 }); stored >= 0 && cn.Review == domain.ReviewProposed && cn.Post == nil {
					r := domain.Review{Status: domain.ReviewAccepted, By: "graph.split_by_owner", At: g.now(),
						Comment: fmt.Sprintf("delegated to %s (sub-change %s)", gr.org, sub.ID)}
					if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: cn.ID, Op: domain.ImpactReviewed, By: r.By, Review: &r}); err != nil {
						return err
					}
				}
				// DerivedFrom holds the id of the parent's change impact it is copied from
				cp := domain.ChangeImpact{ID: domain.ChangeImpactID(g.newID()), Key: cn.Key, Type: cn.Type, Intent: domain.IntentModified, Rationale: cn.Rationale,
					Pre: cn.Pre, Review: domain.ReviewProposed, ProducedBy: "graph.split_by_owner", DerivedFrom: []domain.ItemID{domain.ItemID(cn.ID)}, CreatedAt: g.now()}
				if err := g.emit(ctx, tx, domain.ImpactEvent{Change: sub.ID, Impact: cp.ID, Op: domain.ImpactDeclared, By: cp.ProducedBy, State: &cp}); err != nil {
					return err
				}
			}
			sub.Status = domain.ChangeActive
			if err := tx.PutChange(ctx, sub); err != nil {
				return err
			}
			created = append(created, sub)
		}
		return nil
	})
	return
}
