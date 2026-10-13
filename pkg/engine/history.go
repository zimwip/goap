package engine

import (
	"context"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/journal"
)

// withHistory gives the blackboard of a change its history (ADR 0109 §4) when the graph did not (a graph reached over
// RPC carries no log facet): the runs on the change and the runs of their steps, with their outcome and whether their
// success still stands, folded from the log of the change.
func (e *Engine) withHistory(ctx context.Context, bb domain.Blackboard) (domain.Blackboard, error) {
	if bb.Change.ID == "" {
		return bb, nil
	}
	if _, ok := bb.Facets[domain.FacetHistory]; ok {
		return bb, nil
	}
	entries, _, err := e.Graph.ChangeLog(ctx, domain.LogFilter{Change: bb.Change.ID, Types: journal.HistoryTypes})
	if err != nil {
		return bb, err
	}
	h, err := journal.FoldWith(e.StepDependencies)(ctx, bb.Change, entries)
	if err != nil {
		return bb, err
	}
	return bb.WithFacet(domain.FacetHistory, h), nil
}

// StepDependencies are the step dependencies of a methodology as the engine compiles it (journal.StepDependencies).
func (e *Engine) StepDependencies(ctx context.Context, methodology string) map[string]map[string]bool {
	m, err := e.Methodologies.Methodology(ctx, methodology)
	if err != nil {
		return nil
	}
	return m.StepDependencies()
}

// blackboard is the blackboard of a change as a flow sees it, with its history.
func (e *Engine) blackboard(ctx context.Context, id domain.ChangeID, flow string) (domain.Blackboard, error) {
	bb, err := e.Graph.BlackboardIn(ctx, id, flow)
	if err != nil {
		return bb, err
	}
	return e.withHistory(ctx, bb)
}
