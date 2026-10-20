package graphapi

import (
	"context"

	"github.com/zimwip/goap/contracts/domain"
)

// Port is the graph as its callers see it: the operations on the changes and the reads the engine
// (engine.GraphPort) and the built-in MCPs make. *graph.Graph implements it in-process, graphclient.Client over RPC.
type Port interface {
	CreateChange(ctx context.Context, in NewChange) (domain.Change, error)
	UpdateChange(ctx context.Context, id domain.ChangeID, p ChangePatch) (domain.Change, error)
	// MoveChange moves a root change to another project, with its open sub-changes (ADR 0091).
	MoveChange(ctx context.Context, id domain.ChangeID, project string) (domain.Change, error)
	// AttachChange makes a root change a sub-change of a change whose methodology integrates its own, DetachChange
	// gives it back (ADR 0106).
	AttachChange(ctx context.Context, id, parent domain.ChangeID) (domain.Change, error)
	DetachChange(ctx context.Context, id domain.ChangeID) (domain.Change, error)
	// SetChangeDependencies replaces the changes a change depends on; ChangeDependencies reads them, the changes
	// depending on it and the merge order of its sub-changes (ADR 0108).
	SetChangeDependencies(ctx context.Context, id domain.ChangeID, deps []domain.ChangeID) (domain.Change, error)
	ChangeDependencies(ctx context.Context, id domain.ChangeID) (ChangeDependencies, error)
	// UpgradeChange moves an open change to another version of its methodology, the latest when version is "" (ADR
	// 0113).
	UpgradeChange(ctx context.Context, id domain.ChangeID, version string) (domain.Change, error)
	// Change is the change as stored, its items of every flow included (the blackboard is a view of one flow).
	Change(ctx context.Context, id domain.ChangeID) (domain.Change, error)
	AddItems(ctx context.Context, id domain.ChangeID, items []domain.ChangeItem) ([]domain.ChangeItem, error)
	// Change impacts (ADR 0024, 0076, 0079): declare the nodes a change acts on, check them out, edit their draft,
	// review and move them.
	ProposeImpact(ctx context.Context, id domain.ChangeID, nodes []domain.ChangeImpact) ([]domain.ChangeImpact, error)
	ImpactNodeCreate(ctx context.Context, id domain.ChangeID, in NodeCreate) (domain.ChangeImpact, error)
	ImpactNodeCheckout(ctx context.Context, id domain.ChangeID, in NodeCheckout) (domain.ChangeImpact, error)
	ImpactNodeUpdate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, in NodeUpdate) (domain.ChangeImpact, error)
	ImpactLinkCreate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, l LinkWrite, flow, execution string) (domain.Link, error)
	ImpactLinkDelete(ctx context.Context, id domain.ChangeID, link domain.LinkID, flow, execution string) error
	ImpactNodeTransition(ctx context.Context, id domain.ChangeID, in NodeTransition) (domain.ChangeImpact, error)
	ImpactNodeCancel(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow, execution string) (domain.ChangeImpact, error)
	WithdrawImpact(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow, execution string) error
	// WithdrawImpacts takes several change impacts out of the change at once, all or none.
	WithdrawImpacts(ctx context.Context, id domain.ChangeID, impacts []domain.ChangeImpactID, flow, execution string) error
	// ImpactNodeMerge and ImpactNodeSplit replace nodes by successors from the side of their parents (ADR 0077).
	ImpactNodeMerge(ctx context.Context, id domain.ChangeID, in MergeInput) (Restructured, error)
	ImpactNodeSplit(ctx context.Context, id domain.ChangeID, in SplitInput) (Restructured, error)
	ImpactNodeReviewOn(ctx context.Context, id domain.ChangeID, flow, execution string, node domain.ChangeImpactID, status domain.NodeReview, by, comment string) (domain.ChangeImpact, error)
	// ImpactNodeReviewBatch applies reviews together, all or none (ADR 0080: the submission of a review).
	ImpactNodeReviewBatch(ctx context.Context, id domain.ChangeID, b domain.ReviewBatch) ([]domain.ChangeImpact, error)
	// ReopenImpacts sends decided impacts back to proposed (ADR 0075: a derogation expired).
	ReopenImpacts(ctx context.Context, id domain.ChangeID, impacts []domain.ChangeImpactID, comment string) ([]domain.ChangeImpactID, error)
	Blackboard(ctx context.Context, id domain.ChangeID) (domain.Blackboard, error)
	// BlackboardIn is the blackboard of a flow branch ("" = main); OpenFlow /
	// AdoptFlow / DiscardFlow relaunch a step on a new branch and decide it.
	BlackboardIn(ctx context.Context, id domain.ChangeID, flow string) (domain.Blackboard, error)
	OpenFlow(ctx context.Context, id domain.ChangeID, in OpenFlowRequest) (domain.Flow, error)
	AdoptFlow(ctx context.Context, id domain.ChangeID, flow, by string) (domain.Flow, error)
	DiscardFlow(ctx context.Context, id domain.ChangeID, flow, by string) (domain.Flow, error)
	// ValidateBoard checks the consistency of the blackboard seen from a flow.
	ValidateBoard(ctx context.Context, id domain.ChangeID, flow string) ([]domain.BoardIssue, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
	// ChangeGraph is the graph a call on a change reads: the active option's (or the option flow names), else the
	// reference baseline of the change (ADR 0032 §6).
	ChangeGraph(ctx context.Context, id domain.ChangeID, flow string) ([]domain.Node, []domain.Link, error)
	// ChangeView is the graph of a change at a level (written, accepted, committed) on a flow (ADR 0032 §5).
	ChangeView(ctx context.Context, id domain.ChangeID, flow, level string) (domain.Baseline, error)
	// Options of a change (ADR 0009 §3, ADR 0032 §6): open, activate, evaluate, list and compare them. Selecting and
	// rejecting one is a decision, made through the graph service.
	OpenOption(ctx context.Context, id domain.ChangeID, in OpenOptionRequest) (domain.Flow, error)
	ActivateOption(ctx context.Context, id domain.ChangeID, option, by string) (string, error)
	EvaluateOption(ctx context.Context, id domain.ChangeID, option, by, comment string) (domain.Flow, error)
	Options(ctx context.Context, id domain.ChangeID) ([]domain.Flow, error)
	CompareOptions(ctx context.Context, id domain.ChangeID, level string, all bool) (OptionComparison, error)
	// Decision points (ADR 0009 §4): the item operations of kind decisionPoint and decision.investigate go through them.
	OpenDecision(ctx context.Context, id domain.ChangeID, in OpenDecisionRequest) (domain.DecisionPoint, error)
	RuleDecision(ctx context.Context, id domain.ChangeID, in RuleRequest) (domain.DecisionPoint, error)
	AnswerQuestion(ctx context.Context, id domain.ChangeID, question, answer, process, by string) (domain.DecisionPoint, error)
	RatifyDecision(ctx context.Context, id domain.ChangeID, point string, accept bool, by, comment string) (domain.DecisionPoint, error)
	DecisionPoints(ctx context.Context, id domain.ChangeID) ([]domain.DecisionPoint, error)
	// TransitionChange moves the lifecycle of a change along a transition (ADR 0058): a gate of a methodology takes its
	// transition once its decision says go (ADR 0132 §3).
	TransitionChange(ctx context.Context, id domain.ChangeID, in TransitionRequest) (domain.Change, error)
	Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error)
	// Branch, PlanMerge and IntegrateChange are the second step of Apply: the merge of a committed change into the
	// branch it was forked from, with the resolutions of the conflicts a person settled (ADR 0056).
	Branch(ctx context.Context, namespace, name string) (domain.Branch, error)
	PlanMerge(ctx context.Context, namespace, from, into string) (MergePlan, error)
	IntegrateChange(ctx context.Context, id domain.ChangeID, resolutions map[domain.NodeID]Resolution) (domain.Change, error)
	// RebaseState tells where a change stands against what it builds on: the conflicts a rebase left to settle (ADR 0101).
	RebaseState(ctx context.Context, id domain.ChangeID) (RebaseState, error)
	Baselines(ctx context.Context, namespace string) ([]domain.Baseline, error)
	// TagVersion names one exact node version for the caller; Tags lists the tags matching a filter (ADR 0143 §6).
	TagVersion(ctx context.Context, ref domain.NodeRef, name string) (domain.Tag, error)
	Tags(ctx context.Context, f domain.TagFilter) ([]domain.Tag, error)
	// AppendLog / ChangeLog are the log of the changes, where the execution journal lives (ADR 0011, pkg/journal).
	AppendLog(ctx context.Context, entries []domain.LogEntry) error
	ChangeLog(ctx context.Context, f domain.LogFilter) ([]domain.LogEntry, map[string]int, error)
	// BranchHead gives the head of a branch: the state the last change left, the empty state (empty id) before any.
	BranchHead(ctx context.Context, namespace, name string) (domain.Baseline, error)
	// ListChanges lists the changes matching a filter (goap-change.list): which open change a request
	// continues, else a new one is proposed.
	ListChanges(ctx context.Context, f ChangesFilter) ([]domain.Change, error)
	// Structures are the organisation and project hierarchies in force (ADR 0054); AdminOnlyType tells a node type
	// platform administrators alone write (ADR 0068).
	Structures(ctx context.Context) (domain.Structures, error)
	AdminOnlyType(ctx context.Context, typ string) (bool, error)
}
