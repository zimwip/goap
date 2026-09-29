package graph

import (
	"context"
	"fmt"
	"maps"

	"github.com/zimwip/goap/pkg/domain"
)

// View levels of a change (ADR 0032): the same change impacts, filtered one step of their life earlier or later.
// A branch that is not validated yet (an option, a flow) is seen at written or accepted; only landed is a baseline.
const (
	// ViewWritten: every version written and not rejected, proposed or accepted: what the change would look like.
	ViewWritten = "written"
	// ViewAccepted: the accepted versions only: what the change would land if it were applied now.
	ViewAccepted = "accepted"
	// ViewLanded: what the change landed: its result baseline once applied, its starting point before.
	ViewLanded = "landed"
)

// ChangeView returns the graph a change sees at a level, on one of its flows ("" = the main flow): the head of the
// branch the change works on, with the post versions of the change impacts the level counts laid over it (a
// retired node leaves it). Nothing is stored: the result is a baseline without an id, computed from the change
// impact projection (ADR 0029). At landed it is a stored baseline: the result of the change, or its start.
func (g *Graph) ChangeView(ctx context.Context, id domain.ChangeID, flow, level string) (b domain.Baseline, err error) {
	switch level {
	case ViewWritten, ViewAccepted, ViewLanded:
	case "":
		level = ViewWritten
	default:
		return b, fmt.Errorf("unknown view level %q: %w", level, ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		flow := c.ResolveFlow(flow) // no flow: the active option
		if flow != "" {
			if _, ok := c.Flow(flow); !ok {
				return fmt.Errorf("flow %s of change %s: %w", flow, id, ErrNotFound)
			}
		}
		b, err = changeViewTx(ctx, g, tx, c, flow, level)
		return err
	})
	return b, err
}

func changeViewTx(ctx context.Context, g *Graph, tx Tx, c domain.Change, flow, level string) (domain.Baseline, error) {
	from := c.BaselineID
	if level == ViewLanded {
		if c.Status == domain.ChangeApplied && c.ResultBaselineID != "" {
			from = c.ResultBaselineID
		}
		return tx.Baseline(ctx, from)
	}
	if _, own, err := ownBranch(ctx, tx, c); err != nil {
		return domain.Baseline{}, err
	} else if own {
		// the head of the change branch: what its sub-changes merged into it since the fork
		head, err := branchHead(ctx, tx, c.Namespace, c.Branch)
		if err != nil {
			return domain.Baseline{}, err
		}
		from = head.ID
	}
	base, err := tx.Baseline(ctx, from)
	if err != nil {
		return base, err
	}
	nodes := maps.Clone(base.Nodes)
	impacts, err := g.newFlowNodes(tx, c, flow).nodes(ctx)
	if err != nil {
		return base, err
	}
	for _, cn := range impacts {
		if cn.Post == nil || cn.Review == domain.ReviewRejected || (level == ViewAccepted && cn.Review != domain.ReviewAccepted) {
			continue
		}
		n, err := tx.Node(ctx, *cn.Post)
		if err != nil {
			return base, err
		}
		if n.Deleted {
			delete(nodes, n.ID)
		} else {
			nodes[n.ID] = n.Version
		}
	}
	return domain.Baseline{Name: fmt.Sprintf("%s (%s)", c.Title, level), Namespace: base.Namespace, Branch: c.Branch, ParentID: base.ID,
		ChangeID: c.ID, Nodes: nodes, CreatedAt: base.CreatedAt}, nil
}
