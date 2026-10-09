// Package changeapi is the contract of the change component (ADR 0098 §9): the inputs and results of its operations,
// its errors and the few pure rules a caller needs, over the neutral types of pkg/domain. The graph implements it
// (pkg/graph aliases these types); the engine talks to it through its ports without importing pkg/graph.
package changeapi

import (
	"context"
	"errors"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// Errors of the change component, matched with errors.Is.
var (
	ErrNotFound = errors.New("not found")
	// ErrConflict means a concurrent or contradictory change.
	ErrConflict = errors.New("conflict")
	// ErrInvalid means a request the change refuses as it stands.
	ErrInvalid = errors.New("invalid")
)

// NewChange describes a change to open.
type NewChange struct {
	Title       string
	Intent      string
	Methodology string
	// Goal the change works towards (the goal of a methodology or a process of it). Empty: the main goal of the
	// methodology (ChangeLifecycles.DefaultGoal, ADR 0096), a sub-change's parent goal.
	Goal string
	// Namespace the change acts on (default: domain.DefaultNamespace).
	Namespace  string
	BaselineID domain.BaselineID
	// Branch the change applies to (default main); it must be open. With
	// OwnBranch it is the branch the change is finally merged into.
	Branch string
	// OwnBranch gives the change a branch of its own, named after it and forked
	// from BaselineID: its versions live there until the change is merged.
	OwnBranch bool
	// BranchIntent says why the own branch exists relative to its parent (derive/revise/refine, same vocabulary
	// as an Option's); only meaningful with OwnBranch or ParentID. "" (the default) is domain.IntentDerive.
	BranchIntent domain.OptionIntent
	// ParentID makes the change a sub-change of another one (see prepareSubChange).
	ParentID domain.ChangeID
	// OwnerOrg is the key of the unit responsible for the change (empty: the default organisation).
	OwnerOrg string
	// ProjectID is the key of the project this change acts in and its new nodes belong to (ADR 0039, 0091). A
	// change cannot be created without one: a sub-change inherits it from its parent when unset (and must stay
	// within the parent's project when set), any other change names it, and an empty one is refused with
	// ErrInvalid. The edges resolve a caller's active project (empty: the root project), the graph does not.
	ProjectID string
	Data      map[string]any
	// Guardian names the guardian of the change (ADR 0098); empty: a sub-change takes its parent's, a root change
	// Graph.DefaultGuardian.
	Guardian string
}

// ChangesFilter narrows ListChanges; the zero value matches every change.
type ChangesFilter struct {
	Namespace string
	OwnerOrg  string
	// Status: draft, active, committed, applied, abandoned (empty: every status).
	Status []domain.ChangeStatus
	// Request keeps the changes linked to a request (ADR 0098).
	Request domain.RequestID
}

// ChangePatch updates mutable fields of a change header. Nil fields are kept. Title and Intent are
// the current definition of the change; goap-change/reformulate is the only caller that sets them,
// after superseding the previous definition as an "intent" item so the history is kept.
type ChangePatch struct {
	Title  *string
	Intent *string
	Goal   *string
	Status *domain.ChangeStatus
	Data   map[string]any // merged
}

// LinkWrite is an outgoing link of a draft: to an exact node version, or, with Version 0, to the draft of a node the
// change holds (resolved to the version landing writes for it).
type LinkWrite struct {
	Type       string
	To         domain.NodeRef
	Properties map[string]any
}

// NodeCreate is a node ImpactNodeCreate creates.
type NodeCreate struct {
	Key, Type  string
	Properties map[string]any
	// Owner is the key of the organisational unit owning the node (ADR 0054); empty: the unit holding the change.
	Owner     string
	Rationale string
	// Links are outgoing links of the draft.
	Links []LinkWrite
	// Flow is the flow the call works on ("": the active option, else the main flow; domain.MainFlow names the main
	// flow) and Execution the action run that makes it (ADR 0025).
	Flow, Execution string
	ProducedBy      string
	DerivedFrom     []domain.ItemID
}

// NodeCheckout names the node ImpactNodeCheckout or ImpactNodeTransition works on: a change impact, or a node (then the impact the
// flow sees on it, declared when the change holds none).
type NodeCheckout struct {
	Impact domain.ChangeImpactID
	Node   domain.NodeID
	// Key names the node by its key, when neither Impact nor Node is given.
	Key string
	// Rationale says why, when the call declares the impact.
	Rationale       string
	Flow, Execution string
	ProducedBy      string
}

// NodeUpdate edits a draft.
type NodeUpdate struct {
	// Node and Key name the node when ImpactNodeUpdate is given no change impact (see resolve).
	Node domain.NodeID
	Key  string
	// Properties are merged over the ones of the draft.
	Properties map[string]any
	// Owner transfers the node to another organisational unit (its key, ADR 0054).
	Owner           string
	Flow, Execution string
}

// NodeTransition moves a node along its lifecycle (ImpactNodeTransition): To is the state it goes to.
type NodeTransition struct {
	NodeCheckout
	To string
}

// NodeName names a node a merge or a split works on: a change impact, or a node (its id, or its key).
type NodeName struct {
	Impact domain.ChangeImpactID
	Node   domain.NodeID
	Key    string
}

// MergeInput is a merge (ImpactNodeMerge): Sources are replaced, in their parents, by the new node Into.
type MergeInput struct {
	Sources []NodeName
	Into    NodeCreate
	// Rationale says why, on the impacts the merge declares (the sources, the parents); empty: the one of Into.
	Rationale string
	// Flow and Execution replace the ones of Into (see NodeCreate).
	Flow, Execution string
	// Gate, when set, is asked about the type of every node the call writes or modifies, the parents it discovers
	// included (the access nodes, ADR 0068, are not the caller's to change): its error refuses the call. It runs inside
	// the transaction and must not read the graph.
	Gate func(nodeType string) error
}

// SplitInput is a split (ImpactNodeSplit): Source is replaced, in its parents, by the new nodes Into.
type SplitInput struct {
	Source          NodeName
	Into            []NodeCreate
	Rationale       string
	Flow, Execution string
	// Gate is the one of MergeInput.
	Gate func(nodeType string) error
}

// SuspectLink is a link of another node to a source that a split leaves as it is: which successor it should follow
// is for the reviewer to say (ADR 0003: it becomes suspect).
type SuspectLink struct {
	From    domain.NodeRef
	FromKey string
	Type    string
	To      domain.NodeRef
	ToKey   string
}

// Restructured is what a merge or a split did to the change.
type Restructured struct {
	// Successors are the impacts of the new nodes (created, checked out, with their origins).
	Successors []domain.ChangeImpact
	// Sources are the impacts of the merged or split nodes, Via the impact of their parent.
	Sources []domain.ChangeImpact
	// Parents are the impacts of the nodes whose links moved (each checked out): the parents, and for a merge the nodes
	// holding other links to the sources.
	Parents []domain.ChangeImpact
	// Suspect lists the links a split leaves to its source (see SuspectLink).
	Suspect []SuspectLink
}

// OptionComparison compares the open options of a change at a level (written or accepted): the nodes any of them
// changed from the main flow, both sides forking from the same graph (ADR 0032 §6).
type OptionComparison struct {
	Level   string        `json:"level"`
	Options []domain.Flow `json:"options"`
	Nodes   []OptionNode  `json:"nodes"`
}

// OptionNode is a node written by at least one option, with its version in the main flow and in each option (nil:
// not in the graph of that side, absent or retired; Version 0: the draft that side holds, ADR 0079).
type OptionNode struct {
	Node    domain.NodeID              `json:"node"`
	Key     string                     `json:"key"`
	Type    string                     `json:"type"`
	Main    *domain.NodeRef            `json:"main,omitempty"`
	Options map[string]*domain.NodeRef `json:"options"`
	// Props are the properties of the node on each side: "main" or the option id.
	Props map[string]map[string]any `json:"props,omitempty"`
}

// OpenOptionRequest opens an option of a change.
type OpenOptionRequest struct {
	Name       string
	Hypothesis string
	// Parent is the option this one is a sub-branch of ("" = forked from the main flow). Non-empty only makes
	// sense with Intent refine: isolating narrower work within an already-open option.
	Parent string
	// Intent says why the option exists relative to Parent (derive, revise or refine; "" = unspecified).
	Intent domain.OptionIntent
	// Activate makes the new option the one the change works on.
	Activate bool
	By       string
}

// OpenFlowRequest relaunches a step of the action flow on a new flow branch.
type OpenFlowRequest struct {
	// Parent is the flow the relaunched step ran on ("" = the main flow).
	Parent string
	// ForkAfter is the last item of the parent's view before the step ("" = none).
	ForkAfter domain.ItemID
	// Seeds are the items produced by the relaunched step and by what followed
	// it; the items derived from them are invalidated too.
	Seeds []domain.ItemID
	// StaleRuns are opaque producer ids (the Execution of change impacts and node versions) of the
	// relaunched step and of what followed it: what they produced is stale until the branch is adopted
	// (ADR 0025).
	StaleRuns []string
	// Origin is an opaque record the opener keeps on the flow (the graph stores and returns it, never reads it).
	Origin map[string]any
	// Items are written on the new flow at creation (a note for whoever works on it): the graph gives each
	// the flow, an id, a time and, unset, the accepted status.
	Items []domain.ChangeItem
}

// OpenDecisionRequest opens a decision point.
type OpenDecisionRequest struct {
	Question string
	// Options are the options it chooses among; nil: the open options of the change, [] none (a free question).
	Options  []string
	Criteria []string
	// Policy is the policy values the opener gives (who decides, a confidence threshold, a number of rounds, a
	// duration: the keys of pkg/decision). The graph hands them to Graph.DecisionPolicy, which resolves them, and
	// stores the result on the point.
	Policy map[string]any
	By     string
}

// RuleRequest is a ruling of a decision point.
type RuleRequest struct {
	Point string
	// Outcome is decided (Option, Confidence) or undecidable (Questions: what blocks it).
	Outcome       string
	Option        string
	Confidence    float64
	Justification string
	Questions     []string
	// Human tells the ruling comes from a person: the only one a point reserved to a person accepts.
	Human bool
	By    string
}

// NewRequest is a request to create.
type NewRequest struct {
	Title string
	// Text is the voice of the requester: never rewritten.
	Text string
	// Requester is the subject who asks; empty: the caller.
	Requester string
	// ProjectID is the project of the request when it is known; empty: untriaged.
	ProjectID string
	// Origin says where the request comes from; an empty kind is manual.
	Origin domain.RequestOrigin
}

// Match reports whether a change satisfies the filter.
func (f ChangesFilter) Match(c domain.Change) bool {
	if f.Namespace != "" && c.Namespace != f.Namespace {
		return false
	}
	if f.OwnerOrg != "" && c.OwnerOrg != f.OwnerOrg {
		return false
	}
	if len(f.Status) > 0 && !slices.Contains(f.Status, c.Status) {
		return false
	}
	return true
}

// FlowBranchName is the name of the branch of the drafts of a flow.
func FlowBranchName(flow string) string {
	if len(flow) > 8 {
		flow = flow[:8]
	}
	return "flow-" + flow
}

// DraftBranch is the branch the drafts of a flow of a change are read on: the change's own for the main flow ("").
func DraftBranch(c domain.Change, flow string) string {
	if flow == "" {
		return domain.BranchOf(c.Branch)
	}
	return FlowBranchName(flow)
}

// IsWorking reports whether a node as a call of the change reads it is the draft of the flow itself (ADR 0077, 0079):
// a draft (no version), whose branch is the one the flow names (flow "": the active option, else the main flow). The
// draft of a parent flow is not: the flow checks the node out to write its own. Callers use it to decide between a
// checkout and an update.
func IsWorking(c domain.Change, flow string, n domain.Node) bool {
	return n.IsDraft() && domain.BranchOf(n.Branch) == DraftBranch(c, c.ResolveFlow(flow))
}

// Guardian is what a change asks of whoever governs it (ADR 0098): the rules of its methodology, which a manual
// operation on the change must not skip. The change knows only that it is open or closed; the state of a change in the
// lifecycle of its methodology, its transitions and what they freeze are the guardian's. The graph resolves the
// guardian a change names (Change.Guardian) and refuses the operation when it cannot reach it. Every method runs
// outside any transaction of the change and may read it.
type Guardian interface {
	// MayCommit decides whether c lands, on the blackboard its landing builds (the change and its impacts hydrated with
	// the versions it writes): decided replaces the floor of the landable states (ADR 0078) by ok; not decided leaves
	// the floor in force; an error refuses the landing.
	MayCommit(ctx context.Context, c domain.Change, bb domain.Blackboard) (decided, ok bool, err error)
	// MayCreateChild accepts or refuses a sub-change of parent, before it is stored.
	MayCreateChild(ctx context.Context, parent, child domain.Change) error
	// MayMove accepts or refuses the move of a family of changes (a root change and its open sub-changes) to project to
	// (ADR 0091), after the move was authorized.
	MayMove(ctx context.Context, family []domain.Change, to string) error
	// MayEdit accepts or refuses an operation on an impact the change already holds (an edit of its draft, a link, a
	// transition of the node, a cancellation, a withdrawal): a guardian freezes the impacts written in a state of the
	// lifecycle the change has left (ADR 0058).
	MayEdit(ctx context.Context, c domain.Change, impact domain.ChangeImpactID) error
}
