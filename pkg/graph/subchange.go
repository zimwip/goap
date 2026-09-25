package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// Organisation namespace conventions (domains/organisation.yaml).
const (
	NamespaceOrganisation = "organisation"
	LinkPartOf            = "part_of" // OrgUnit -> parent OrgUnit
	LinkOwner             = "owner"   // any node -> OrgUnit
)

// prepareSubChange applies the rules of a sub-change to c: its parent must be
// open and have a branch of its own, the namespace is the parent's, and the
// change forks from (and is merged into) the parent branch. Its organisational
// unit must be inside the unit of the parent.
func (g *Graph) prepareSubChange(ctx context.Context, tx Tx, c *domain.ChangeSet, in *NewChange) error {
	if c.ParentID == "" {
		return nil
	}
	parent, err := tx.Change(ctx, c.ParentID)
	if err != nil {
		return err
	}
	switch parent.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned, domain.ChangeMergePending:
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
		head, err := branchHead(ctx, tx, own.Name)
		if err != nil {
			return err
		}
		c.BaselineID = head.ID
	}
	if c.Methodology == "" {
		c.Methodology = parent.Methodology
	}
	if parent.OwnerOrg != "" && c.OwnerOrg != "" && c.OwnerOrg != parent.OwnerOrg {
		ok, err := orgWithin(ctx, tx, c.OwnerOrg, parent.OwnerOrg)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("unit %s is not part of %s, the unit of the parent change: %w", c.OwnerOrg, parent.OwnerOrg, ErrInvalid)
		}
	}
	return nil
}

// checkOwnerOrg verifies that key designates an OrgUnit of the organisation namespace.
func checkOwnerOrg(ctx context.Context, tx Tx, key string) error {
	id, err := tx.NodeIDByKey(ctx, NamespaceOrganisation, key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("owner org %q is not a node of namespace %s: %w", key, NamespaceOrganisation, ErrInvalid)
		}
		return err
	}
	n, err := tx.LatestOn(ctx, id, domain.MainBranch)
	if err != nil {
		return err
	}
	if n.Deleted {
		return fmt.Errorf("owner org %q is deleted: %w", key, ErrInvalid)
	}
	return nil
}

// orgWithin reports whether the unit with key `unit` is `ancestor` or below it,
// following the part_of links (child -> parent) of the latest version on main.
func orgWithin(ctx context.Context, tx Tx, unit, ancestor string) (bool, error) {
	seen := map[string]bool{}
	for cur := unit; cur != "" && !seen[cur]; {
		if cur == ancestor {
			return true, nil
		}
		seen[cur] = true
		id, err := tx.NodeIDByKey(ctx, NamespaceOrganisation, cur)
		if err != nil {
			return false, err
		}
		n, err := tx.LatestOn(ctx, id, domain.MainBranch)
		if err != nil {
			return false, err
		}
		links, err := tx.OutLinks(ctx, n.Ref())
		if err != nil {
			return false, err
		}
		cur = ""
		for _, l := range links {
			if l.Type != LinkPartOf {
				continue
			}
			p, err := tx.Node(ctx, l.To)
			if err != nil {
				return false, err
			}
			cur = p.Key
			break
		}
	}
	return false, nil
}

// SubChanges lists the sub-changes of a change, oldest first.
func (g *Graph) SubChanges(ctx context.Context, id domain.ChangeID) (out []domain.ChangeSet, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { out, err = subChanges(ctx, tx, id); return err })
	return
}

func subChanges(ctx context.Context, tx Tx, id domain.ChangeID) ([]domain.ChangeSet, error) {
	all, err := tx.Changes(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.ChangeSet
	for _, c := range all {
		if c.ParentID == id {
			out = append(out, c)
		}
	}
	return out, nil
}

// openSubChanges are the sub-changes that still have to be applied or abandoned.
func openSubChanges(ctx context.Context, tx Tx, id domain.ChangeID) ([]domain.ChangeSet, error) {
	subs, err := subChanges(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(subs, func(c domain.ChangeSet) bool {
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

// SplitByOwner splits a change along organisational boundaries: one
// sub-change per unit owning nodes the change has an impact on (`owner` link
// of the impacted version). The sub-change gets copies of the impacts of its
// nodes, derived from the parent's. Units that already have an open sub-change
// are left as they are. Impacts on unowned nodes stay with the parent.
func (g *Graph) SplitByOwner(ctx context.Context, id domain.ChangeID) (created []domain.ChangeSet, err error) {
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
			items     []domain.ChangeItem
		}
		var groups []*group
		byOrg := map[string]*group{}
		for _, it := range parent.Items {
			if it.Kind != domain.KindImpact || it.Target == nil || !parent.InEffect(it.ID) {
				continue
			}
			links, err := tx.OutLinks(ctx, *it.Target)
			if err != nil {
				return err
			}
			for _, l := range links {
				if l.Type != LinkOwner {
					continue
				}
				unit, err := tx.Node(ctx, l.To)
				if err != nil {
					return err
				}
				if domain.NamespaceOf(unit.Namespace) != NamespaceOrganisation {
					continue
				}
				gr := byOrg[unit.Key]
				if gr == nil {
					name, _ := unit.Properties["name"].(string)
					gr = &group{org: unit.Key, name: firstNonEmpty(name, unit.Key)}
					byOrg[unit.Key] = gr
					groups = append(groups, gr)
				}
				gr.items = append(gr.items, it)
				break
			}
		}
		for _, gr := range groups {
			if have[gr.org] {
				continue
			}
			in := NewChange{Title: parent.Title + " / " + gr.name, Intent: parent.Intent, Methodology: parent.Methodology, ParentID: id, OwnerOrg: gr.org}
			sub := domain.ChangeSet{ID: domain.ChangeID(g.newID()), Title: in.Title, Intent: in.Intent, Methodology: in.Methodology, Status: domain.ChangeDraft,
				ParentID: id, OwnerOrg: gr.org, CreatedAt: g.now()}
			if err := g.prepareSubChange(ctx, tx, &sub, &in); err != nil {
				return err
			}
			if err := checkOwnerOrg(ctx, tx, gr.org); err != nil {
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
			for _, it := range gr.items {
				cp := domain.ChangeItem{ID: domain.ItemID(g.newID()), Kind: domain.KindImpact, Type: it.Type, Status: domain.ItemProposed,
					Target: it.Target, Data: it.Data, ProducedBy: "graph.split_by_owner", DerivedFrom: []domain.ItemID{it.ID}, CreatedAt: g.now()}
				if err := tx.PutItem(ctx, sub.ID, cp); err != nil {
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
