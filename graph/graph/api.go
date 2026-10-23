package graph

import (
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
)

// The requests and answers of the graph's operations are its contract, pkg/graphapi; these names keep the graph's own
// code and its tests reading as before.
type (
	View                = graphapi.View
	QueryOptions        = graphapi.QueryOptions
	QueryResult         = graphapi.QueryResult
	ViewState           = graphapi.ViewState
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
	NewBranch           = graphapi.NewBranch
	MergeCandidate      = graphapi.MergeCandidate
	ImpactConflicts     = graphapi.ImpactConflicts
	RebaseState         = graphapi.RebaseState
	RebaseResult        = graphapi.RebaseResult
	RebasedImpact       = graphapi.RebasedImpact
	NodeEdit            = graphapi.NodeEdit
	LinkEdit            = graphapi.LinkEdit
	Commit              = graphapi.Commit
	CarriedEvent        = graphapi.CarriedEvent
	CommitResult        = graphapi.CommitResult
	TransitionRequest   = graphapi.TransitionRequest
	NodeValidator       = graphapi.NodeValidator
	ValidatedNode       = graphapi.ValidatedNode
	ValidatorQuery      = graphapi.ValidatorQuery
)

// IsWorking is graphapi.IsWorking.
func IsWorking(c domain.Change, flow string, n domain.Node) bool {
	return graphapi.IsWorking(c, flow, n)
}

func draftBranch(c domain.Change, flow string) string { return graphapi.DraftBranch(c, flow) }

func flowBranchName(flow string) string { return graphapi.FlowBranch(flow) }
