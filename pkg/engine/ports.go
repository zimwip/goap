package engine

import (
	"context"
	"sort"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

// GraphPort is what the engine needs from the graph service. *graph.Graph
// implements it in-process; the engine service uses a connect client.
type GraphPort interface {
	CreateChange(ctx context.Context, in graph.NewChange) (domain.Change, error)
	UpdateChange(ctx context.Context, id domain.ChangeID, p graph.ChangePatch) (domain.Change, error)
	AddItems(ctx context.Context, id domain.ChangeID, items []domain.ChangeItem) ([]domain.ChangeItem, error)
	// Change impacts (ADR 0024): declare the nodes a change acts on, write their versions on the
	// change branch, review them.
	AddNodes(ctx context.Context, id domain.ChangeID, nodes []domain.ChangeImpact) ([]domain.ChangeImpact, error)
	WriteNode(ctx context.Context, id domain.ChangeID, node domain.ChangeImpactID, w graph.NodeWrite) (domain.ChangeImpact, error)
	ReviewNodeOn(ctx context.Context, id domain.ChangeID, flow, execution string, node domain.ChangeImpactID, status domain.NodeReview, by, comment string) (domain.ChangeImpact, error)
	Blackboard(ctx context.Context, id domain.ChangeID) (domain.Blackboard, error)
	// BlackboardIn is the blackboard of a flow branch ("" = main); OpenFlow /
	// AdoptFlow / DiscardFlow relaunch a step on a new branch and decide it.
	BlackboardIn(ctx context.Context, id domain.ChangeID, flow string) (domain.Blackboard, error)
	OpenFlow(ctx context.Context, id domain.ChangeID, in graph.OpenFlowRequest) (domain.Flow, error)
	AdoptFlow(ctx context.Context, id domain.ChangeID, flow, by string) (domain.Flow, error)
	DiscardFlow(ctx context.Context, id domain.ChangeID, flow, by string) (domain.Flow, error)
	// ValidateBoard checks the consistency of the blackboard seen from a flow.
	ValidateBoard(ctx context.Context, id domain.ChangeID, flow string) ([]domain.BoardIssue, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
	Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error)
	Baselines(ctx context.Context) ([]domain.Baseline, error)
	// Record / Journal write and read the execution journal of changes (ADR 0011).
	Record(ctx context.Context, recs []domain.ExecutionRecord) error
	Journal(ctx context.Context, f domain.ExecutionFilter) ([]domain.ExecutionRecord, error)
	// BranchHead / CreateBaseline give the head of a branch and make a baseline.
	BranchHead(ctx context.Context, name string) (domain.Baseline, error)
	CreateBaseline(ctx context.Context, name string, nodes []domain.NodeRef) (domain.Baseline, error)
}

// MethodologyPort resolves methodologies (the registry): the latest
// published version of one, or of every methodology.
type MethodologyPort interface {
	Methodology(ctx context.Context, name string) (*methodology.Compiled, error)
	List(ctx context.Context) ([]*methodology.Compiled, error)
}

// Publisher publishes process events (NATS in services).
type Publisher interface {
	Publish(ctx context.Context, subject string, v any) error
}

// StaticMethodologies serves compiled methodologies from memory.
type StaticMethodologies map[string]*methodology.Compiled

// Methodology implements MethodologyPort.
func (s StaticMethodologies) Methodology(_ context.Context, name string) (*methodology.Compiled, error) {
	m, ok := s[name]
	if !ok {
		return nil, ErrUnknownMethodology{Name: name}
	}
	return m, nil
}

// List implements MethodologyPort.
func (s StaticMethodologies) List(context.Context) ([]*methodology.Compiled, error) {
	out := make([]*methodology.Compiled, 0, len(s))
	for _, m := range s {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ErrUnknownMethodology is returned for unknown methodologies.
type ErrUnknownMethodology struct{ Name string }

func (e ErrUnknownMethodology) Error() string { return "unknown methodology " + e.Name }
