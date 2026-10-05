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
	// ChangeGraph is the graph a call on a change reads: the active option's (or the option flow names), else the
	// reference baseline of the change (ADR 0032 §6).
	ChangeGraph(ctx context.Context, id domain.ChangeID, flow string) ([]domain.Node, []domain.Link, error)
	// ChangeView is the graph of a change at a level (written, accepted, landed) on a flow (ADR 0032 §5).
	ChangeView(ctx context.Context, id domain.ChangeID, flow, level string) (domain.Baseline, error)
	// Options of a change (ADR 0009 §3, ADR 0032 §6): open, activate, evaluate, list and compare them. Selecting and
	// rejecting one is a decision, made through the graph service.
	OpenOption(ctx context.Context, id domain.ChangeID, in graph.OpenOptionRequest) (domain.Flow, error)
	ActivateOption(ctx context.Context, id domain.ChangeID, option, by string) (string, error)
	EvaluateOption(ctx context.Context, id domain.ChangeID, option, by, comment string) (domain.Flow, error)
	Options(ctx context.Context, id domain.ChangeID) ([]domain.Flow, error)
	CompareOptions(ctx context.Context, id domain.ChangeID, level string, all bool) (graph.OptionComparison, error)
	// Decision points (ADR 0009 §4): the item operations of kind decisionPoint and decision.investigate go through them.
	OpenDecision(ctx context.Context, id domain.ChangeID, in graph.OpenDecisionRequest) (domain.DecisionPoint, error)
	RuleDecision(ctx context.Context, id domain.ChangeID, in graph.RuleRequest) (domain.DecisionPoint, error)
	AnswerQuestion(ctx context.Context, id domain.ChangeID, question, answer, process, by string) (domain.DecisionPoint, error)
	RatifyDecision(ctx context.Context, id domain.ChangeID, point string, accept bool, by, comment string) (domain.DecisionPoint, error)
	DecisionPoints(ctx context.Context, id domain.ChangeID) ([]domain.DecisionPoint, error)
	Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error)
	Baselines(ctx context.Context, namespace string) ([]domain.Baseline, error)
	// AppendLog / ChangeLog are the log of the changes, where the execution journal lives (ADR 0011, pkg/journal).
	AppendLog(ctx context.Context, entries []domain.LogEntry) error
	ChangeLog(ctx context.Context, f domain.LogFilter) ([]domain.LogEntry, map[string]int, error)
	// BranchHead gives the head of a branch: the state the last change left, the empty state (empty id) before any.
	BranchHead(ctx context.Context, namespace, name string) (domain.Baseline, error)
	// ListChanges lists the changes matching a filter (goap-change.list): which open change a request
	// continues, else a new one is proposed.
	ListChanges(ctx context.Context, f graph.ChangesFilter) ([]domain.Change, error)
}

// readGraph is the graph a process reads: the graph of its change on its flow (the active option when it runs on
// the main flow), else the baseline it started from.
func readGraph(ctx context.Context, g GraphPort, change domain.ChangeID, flow string, baseline domain.BaselineID) ([]domain.Node, []domain.Link, error) {
	if change != "" {
		return g.ChangeGraph(ctx, change, flow)
	}
	return g.BaselineGraph(ctx, baseline)
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
