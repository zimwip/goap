package graph

import (
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graphapi"
)

// The requests and answers of the graph's operations are its contract, pkg/graphapi; these names keep the graph's own
// code and its tests reading as before.
type (
	NewChange           = graphapi.NewChange
	ChangePatch         = graphapi.ChangePatch
	ChangesFilter       = graphapi.ChangesFilter
	ChangeDependencies  = graphapi.ChangeDependencies
	NodeCreate          = graphapi.NodeCreate
	NodeCheckout        = graphapi.NodeCheckout
	NodeUpdate          = graphapi.NodeUpdate
	NodeTransition      = graphapi.NodeTransition
	LinkWrite           = graphapi.LinkWrite
	NodeName            = graphapi.NodeName
	MergeInput          = graphapi.MergeInput
	SplitInput          = graphapi.SplitInput
	Restructured        = graphapi.Restructured
	SuspectLink         = graphapi.SuspectLink
	OpenFlowRequest     = graphapi.OpenFlowRequest
	OpenOptionRequest   = graphapi.OpenOptionRequest
	OptionComparison    = graphapi.OptionComparison
	OptionNode          = graphapi.OptionNode
	OpenDecisionRequest = graphapi.OpenDecisionRequest
	RuleRequest         = graphapi.RuleRequest
	Resolution          = graphapi.Resolution
	MergePlan           = graphapi.MergePlan
	MergeCandidate      = graphapi.MergeCandidate
	ImpactConflicts     = graphapi.ImpactConflicts
	RebaseState         = graphapi.RebaseState
)

// IsWorking is graphapi.IsWorking.
func IsWorking(c domain.Change, flow string, n domain.Node) bool {
	return graphapi.IsWorking(c, flow, n)
}

func draftBranch(c domain.Change, flow string) string { return graphapi.DraftBranch(c, flow) }

func flowBranchName(flow string) string { return graphapi.FlowBranch(flow) }
