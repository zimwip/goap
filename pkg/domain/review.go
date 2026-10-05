package domain

// ReviewRequest is a review about to be made on a change impact (ADR 0075): the impact as the flow sees it (its
// ProducedBy, Execution and Flow say who made it and in which action run), the reviewer and the verdict.
type ReviewRequest struct {
	Impact   ChangeImpact
	Reviewer string
	Status   NodeReview
	// Items are the facts of the change, for a policy that reads what was recorded about the impact (loaded only when a
	// policy is plugged).
	Items []ChangeItem
}

// ReviewPolicy is the rule that says who may review a change impact (ADR 0075, as DecisionPolicy for decision points,
// ADR 0067). The graph keeps the mechanism (the review, its history, the events) and asks the policy before it writes
// anything; an error refuses the review and nothing is written. pkg/verify is the policy of the platform (the verifier
// is not the producer); a graph given none lets anyone with the right to review do it.
type ReviewPolicy interface {
	Review(ReviewRequest) error
}
