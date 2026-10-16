package graphapi

import "github.com/zimwip/goap/contracts/domain"

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

// IsWorking reports whether a node as a call of the change reads it is the draft of the flow itself (ADR 0077, 0079):
// a draft (no version), whose branch is the one the flow names (flow "": the active option, else the main flow). The
// draft of a parent flow is not: the flow checks the node out to write its own. Callers use it to decide between a
// checkout and an update.
func IsWorking(c domain.Change, flow string, n domain.Node) bool {
	return n.IsDraft() && domain.BranchOf(n.Branch) == DraftBranch(c, c.ResolveFlow(flow))
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

// OptionComparison compares the open options of a change at a level (written or accepted): the nodes any of them
// changed from the main flow, both sides forking from the same graph (ADR 0032 §6).
type OptionComparison struct {
	Level   string        `json:"level"`
	Options []domain.Flow `json:"options"`
	Nodes   []OptionNode  `json:"nodes"`
}

// DraftBranch names the branch a flow's versions would be written on: the change branch for the main flow. It is the
// Branch of the node view of a draft, which says whose draft it is (IsWorking); no branch of that name is created.
func DraftBranch(c domain.Change, flow string) string {
	if flow == "" {
		return domain.BranchOf(c.Branch)
	}
	return FlowBranch(flow)
}

// FlowBranch names the branch of a flow other than the main one.
func FlowBranch(flow string) string {
	if len(flow) > 8 {
		flow = flow[:8]
	}
	return "flow-" + flow
}
