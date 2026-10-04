package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/zimwip/goap/pkg/domain"
)

// Bootstrap (ADR 0054) creates the roots of the two structures of the graph, the root organisational unit and the
// root project, when the graph has neither: every change is held by a unit and acts in a project, and every node
// version is owned by a unit and was created in a project, so the first change needs both to exist already. They are
// written in one transaction by one applied change per namespace (the structures may live in different namespaces),
// held by the root unit and acting in the root project it creates: the root unit owns itself and the root project,
// both were created in the root project. Everything else, seeds included, is an ordinary change after it.
//
// It is idempotent: a graph that has both roots is left alone; a graph that has only one of them is refused. The
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

// bootstrapProps are the properties of the roots.
var bootstrapProps = map[string]map[string]any{
	domain.StructureOrganisation: {"name": "Default organisation", "kind": "company",
		"description": "The root of the organisation: holds the changes that name no unit, and the adapters every unit inherits."},
	domain.StructureProject: {"name": "Root project", "status": "active", domain.PropDefaultProject: true,
		"description": "The root of the projects: every project not folded into another one resolves to it."},
}

func (g *Graph) bootstrap(ctx context.Context, tx Tx) error {
	org, proj := g.Structure(domain.StructureOrganisation), g.Structure(domain.StructureProject)
	have := 0
	for _, st := range []domain.Structure{org, proj} {
		if _, err := tx.NodeIDByKey(ctx, st.Namespace, st.Root); err == nil {
			have++
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	switch have {
	case 2:
		return nil
	case 1:
		return fmt.Errorf("the graph has only one of its roots %s and %s: %w", org.Root, proj.Root, ErrConflict)
	}
	now := g.now()
	ids := map[string]domain.NodeID{domain.StructureOrganisation: domain.NodeID(g.newID()), domain.StructureProject: domain.NodeID(g.newID())}
	// one change per namespace the roots live in
	changes := map[string]*domain.Change{}
	bases := map[string]domain.Baseline{}
	var namespaces []string
	for _, st := range []domain.Structure{org, proj} {
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
			Intent:     "Create the root organisational unit and the root project every change and node version is placed in (ADR 0054)",
			BaselineID: base.ID, Branch: domain.MainBranch, OwnerOrg: org.Root, ProjectID: proj.Root, Administrative: true, CreatedAt: now}
		if err := tx.PutChange(ctx, *c); err != nil {
			return err
		}
		changes[st.Namespace] = c
	}
	for _, st := range []domain.Structure{org, proj} {
		c := changes[st.Namespace]
		n := domain.Node{ID: ids[st.Kind], Version: 1, Branch: domain.MainBranch, Reason: domain.ReasonCreate, Namespace: st.Namespace,
			Key: st.Root, Type: st.Type, Properties: maps.Clone(bootstrapProps[st.Kind]), ChangeID: c.ID, Comment: c.Intent,
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
		for _, st := range []domain.Structure{org, proj} {
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
