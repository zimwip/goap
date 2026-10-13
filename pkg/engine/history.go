package engine

import (
	"context"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/journal"
)

// withHistory gives the blackboard of a change its history (ADR 0109 §4): the runs on the change and the runs of their
// steps, with their outcome and whether their success still stands, folded from the log of the change. The steps of
// each methodology rely on one another as the methodology says (its compiled step dependencies).
func (e *Engine) withHistory(ctx context.Context, bb domain.Blackboard) (domain.Blackboard, error) {
	if bb.Change.ID == "" {
		return bb, nil
	}
	entries, _, err := e.Graph.ChangeLog(ctx, domain.LogFilter{Change: bb.Change.ID, Types: journal.HistoryTypes})
	if err != nil {
		return bb, err
	}
	deps := map[string]map[string]map[string]bool{}
	relies := func(methodology, later, earlier string) bool {
		d, ok := deps[methodology]
		if !ok {
			if m, err := e.Methodologies.Methodology(ctx, methodology); err == nil {
				d = m.StepDependencies()
			}
			deps[methodology] = d
		}
		return d[later][earlier]
	}
	h, err := journal.Fold(entries, relies)
	if err != nil {
		return bb, err
	}
	return bb.WithFacet(domain.FacetHistory, h), nil
}

// blackboard is the blackboard of a change as a flow sees it, with its history.
func (e *Engine) blackboard(ctx context.Context, id domain.ChangeID, flow string) (domain.Blackboard, error) {
	bb, err := e.Graph.BlackboardIn(ctx, id, flow)
	if err != nil {
		return bb, err
	}
	return e.withHistory(ctx, bb)
}
