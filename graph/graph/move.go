package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
)

// ProjectMoveGate is asked before a change moves to another project (ADR 0091), outside any transaction (it reads the
// graph): family is the change and its open sub-changes, each with the project it is in, and to the project they move
// to. It authorizes the caller on both projects and checks that the methodology of each change applies to both; the
// graph names no methodology and no access rule (ADR 0066, 0069), so both are the gate's. Nil: no check beyond the
// graph's own rules.
type ProjectMoveGate func(ctx context.Context, family []domain.Change, to string) error

// HeaderProjectID is the field of a change.updated log entry that records a move to another project.
const HeaderProjectID = "projectId"

// MoveChange moves a change to another project (ADR 0091): the root change of a family, still draft or active, and
// with it its open sub-changes, in one transaction. A sub-change cannot move alone: its project is its parent's. The
// target must be a project and not the one the change is in, and ProjectMoveGate (when set) must accept the move. The
// nodes keep their projects (the project of a node never changes), the nodes the change creates take the new project
// when it lands. The guardian chain of each moved change is computed again for the new project (ADR 0106). Each moved
// change logs a change.updated entry with a projectId field {from, to} (and a guardians field when its chain changed),
// by the caller. It returns the change as moved.
func (g *Graph) MoveChange(ctx context.Context, id domain.ChangeID, project string) (domain.Change, error) {
	if project == "" {
		return domain.Change{}, fmt.Errorf("move change %s: a project is required: %w", id, ErrInvalid)
	}
	var family []domain.Change
	if err := g.repo.InTx(ctx, func(tx Tx) (err error) {
		family, err = g.movable(ctx, tx, id, project)
		return err
	}); err != nil {
		return domain.Change{}, err
	}
	if g.ProjectMoveGate != nil {
		if err := g.ProjectMoveGate(ctx, family, project); err != nil {
			return domain.Change{}, err
		}
	}
	// the guardian chain of the root is computed again for the new project (ADR 0106), its sub-changes' from it
	var rootChain []domain.GuardianRef
	if root := family[0]; g.Lifecycles != nil && root.Methodology != "" {
		var err error
		if rootChain, err = g.Lifecycles.Guardians(ctx, root.MethodologyRef(), project); err != nil {
			return domain.Change{}, err
		}
	}
	var moved domain.Change
	err := g.changeTx(ctx, id, func(tx Tx) error {
		again, err := g.movable(ctx, tx, id, project)
		if err != nil {
			return err
		}
		if len(again) != len(family) {
			return fmt.Errorf("change %s changed while it was checked for a move: %w", id, ErrConflict)
		}
		for i, c := range again {
			if c.ID != family[i].ID || c.ProjectID != family[i].ProjectID {
				return fmt.Errorf("change %s changed while it was checked for a move: %w", id, ErrConflict)
			}
		}
		movedTo := map[domain.ChangeID]domain.Change{}
		for _, c := range again {
			fields := map[string]domain.HeaderValue{HeaderProjectID: {From: c.ProjectID, To: project}}
			chain := c.Guardians
			if g.Lifecycles != nil && c.Methodology != "" {
				if c.ID == id {
					chain = rootChain
				} else if p, ok := movedTo[c.ParentID]; ok {
					chain = chainUnder(p, c.Methodology, ownRef(c))
				}
			}
			if !slices.Equal(chain, c.Guardians) {
				fields[HeaderGuardians] = domain.HeaderValue{From: c.Guardians, To: chain}
			}
			e, err := domain.HeaderEntry(g.newID(), c.ID, g.caller(ctx), g.now(), domain.HeaderEdit{Fields: fields})
			if err != nil {
				return err
			}
			if _, err := tx.AppendLog(ctx, e); err != nil {
				return err
			}
			c.ProjectID, c.Guardians = project, chain
			movedTo[c.ID] = c
			if err := tx.PutChange(ctx, c); err != nil {
				return err
			}
			if c.ID == id {
				moved = c
			}
		}
		return nil
	})
	return moved, err
}

// movable checks the rules of the graph for a move and returns the change followed by the open sub-changes under it,
// parents first.
func (g *Graph) movable(ctx context.Context, tx Tx, id domain.ChangeID, project string) ([]domain.Change, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.ParentID != "" {
		return nil, fmt.Errorf("change %s is a sub-change: its project is its parent's, move %s: %w", id, c.ParentID, ErrInvalid)
	}
	if c.Status != domain.ChangeDraft && c.Status != domain.ChangeActive || hasCommits(c) {
		status := string(c.Status)
		if hasCommits(c) {
			status = "committed on its branch"
		}
		return nil, fmt.Errorf("change %s is %s: only a draft or active change that committed nothing moves to another project: %w", id, status, ErrConflict)
	}
	if c.ProjectID == project {
		return nil, fmt.Errorf("change %s already acts in project %s: %w", id, project, ErrInvalid)
	}
	if _, err := g.structureNode(ctx, tx, domain.StructureProject, project); err != nil {
		return nil, fmt.Errorf("target project: %w", err)
	}
	family := []domain.Change{c}
	for i := 0; i < len(family); i++ {
		subs, err := openSubChanges(ctx, tx, family[i].ID)
		if err != nil {
			return nil, err
		}
		slices.SortFunc(subs, func(a, b domain.Change) int { return strings.Compare(string(a.ID), string(b.ID)) })
		family = append(family, subs...)
	}
	return family, nil
}
