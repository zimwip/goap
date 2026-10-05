package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/zimwip/goap/pkg/domain"
)

// Bootstrap (ADR 0054) creates the roots of the structures the domains declare (the root organisational unit and the
// root project of the built-in organisation domain, with the initial properties its tags give them), when the graph
// has none of them: every change is held by a unit and acts in a project, and every node
// version is owned by a unit and was created in a project, so the first change needs both to exist already. They are
// written in one transaction by one applied change per namespace (the structures may live in different namespaces),
// held by the root unit and acting in the root project it creates: the root unit owns itself and the root project,
// both were created in the root project. Everything else, seeds included, is an ordinary change after it.
//
// It is idempotent: a graph that has every root is left alone; a graph that has only some of them is refused. The
// graph runs it before the first change it opens (CreateChange); services call it at start, before serving.
func (g *Graph) Bootstrap(ctx context.Context) error {
	if g.booted.Load() {
		return nil
	}
	err := g.repo.InTx(ctx, func(tx Tx) error { return g.bootstrap(ctx, tx) })
	if errors.Is(err, ErrConflict) {
		// another process bootstrapped meanwhile (the keys are unique): check it did
		err = g.repo.InTx(ctx, func(tx Tx) error { return g.bootstrap(ctx, tx) })
	}
	if err == nil {
		g.booted.Store(true)
	}
	return err
}

func (g *Graph) bootstrap(ctx context.Context, tx Tx) error {
	roots := g.structures()
	org, proj := roots[0], roots[1] // the owner axis and the project axis every node version is stamped with
	if org.Root == "" || proj.Root == "" {
		return fmt.Errorf("no domain tags the organisation and the project structures: %w", ErrInvalid)
	}
	have := 0
	for _, st := range roots {
		if _, err := tx.NodeIDByKey(ctx, st.Namespace, st.Root); err == nil {
			have++
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	switch have {
	case len(roots):
		return nil
	case 0:
	default:
		return fmt.Errorf("the graph has only some of its roots (%d of %d): %w", have, len(roots), ErrConflict)
	}
	now := g.now()
	ids := map[string]domain.NodeID{}
	for _, st := range roots {
		ids[st.Kind] = domain.NodeID(g.newID())
	}
	// one change per namespace the roots live in
	changes := map[string]*domain.Change{}
	bases := map[string]domain.Baseline{}
	var namespaces []string
	for _, st := range roots {
		if _, ok := changes[st.Namespace]; ok {
			continue
		}
		namespaces = append(namespaces, st.Namespace)
		// the state the change starts from: the head of main, the empty state when no change landed yet (ADR 0056)
		base, err := branchHead(ctx, tx, st.Namespace, domain.MainBranch)
		if err != nil {
			return err
		}
		bases[st.Namespace] = base
		c := &domain.Change{ID: domain.ChangeID(g.newID()), Title: "Bootstrap", Namespace: st.Namespace, Status: domain.ChangeApplied,
			Intent:     "Create the roots of the structures every change and node version is placed in (ADR 0054)",
			BaselineID: base.ID, Branch: domain.MainBranch, OwnerOrg: org.Root, ProjectID: proj.Root, CreatedAt: now}
		if err := tx.PutChange(ctx, *c); err != nil {
			return err
		}
		changes[st.Namespace] = c
	}
	for _, st := range roots {
		c := changes[st.Namespace]
		n := domain.Node{ID: ids[st.Kind], Version: 1, Branch: domain.MainBranch, Reason: domain.ReasonCreate, Namespace: st.Namespace,
			Key: st.Root, Type: st.Type, Properties: maps.Clone(st.Bootstrap), ChangeID: c.ID, Comment: c.Intent,
			Owner: ids[domain.StructureOrganisation], Project: ids[domain.StructureProject], CreatedAt: now}
		if err := tx.PutNode(ctx, n); err != nil {
			return err
		}
		if st.SelfParent {
			if err := tx.PutLink(ctx, domain.Link{ID: domain.LinkID(g.newID()), Type: st.Parent, From: n.Ref(), To: n.Ref(), ChangeID: c.ID}); err != nil {
				return err
			}
		}
	}
	for _, ns := range namespaces {
		c, base := changes[ns], bases[ns]
		nodes := maps.Clone(base.Nodes)
		for _, st := range roots {
			if st.Namespace == ns {
				nodes[ids[st.Kind]] = 1
			}
		}
		res := domain.Baseline{ID: domain.BaselineID(g.newID()), Name: "Bootstrap", Namespace: ns, Branch: domain.MainBranch, ParentID: base.ID,
			ChangeID: c.ID, Kind: domain.BaselineSnapshot, Nodes: nodes, CreatedAt: now}
		if err := tx.PutBaseline(ctx, res); err != nil {
			return err
		}
		if err := g.advanceBranch(ctx, tx, ns, domain.MainBranch, res.ID); err != nil {
			return err
		}
		c.ResultBaselineID = res.ID
		if err := tx.PutChange(ctx, *c); err != nil {
			return err
		}
	}
	return nil
}
