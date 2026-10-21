package graphsvc

import (
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/journal"
	"github.com/zimwip/goap/graph/graph"
)

// LogFacets are the facets the graph folds from the log of a change for its blackboard: its history (ADR 0109 §4),
// read by the conditions, the guards, the gates and the guardians the graph evaluates. deps gives the step
// dependencies of a methodology (the registry in process); nil: no step relies on another, a success stands while
// nothing it started from moved since.
func LogFacets(deps journal.StepDependencies) map[string]graph.LogFacet {
	return map[string]graph.LogFacet{domain.FacetHistory: {Types: journal.HistoryTypes, Fold: journal.FoldWith(deps), Folder: journal.FolderWith(deps)}}
}
