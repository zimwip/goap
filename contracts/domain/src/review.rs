//! Reviews of change impacts: the policy that says who may review (ADR 0075) and the batches of reviews (ADR 0080).

use crate::{ChangeImpact, ChangeImpactID, ChangeItem, Error, NodeReview};

/// ReviewRequest is a review about to be made on a change impact (ADR 0075): the impact as the flow sees it (its
/// ProducedBy, Execution and Flow say who made it and in which action run), the reviewer and the verdict.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ReviewRequest {
    pub impact: ChangeImpact,
    pub reviewer: String,
    pub status: NodeReview,
    /// Items are the facts of the change, for a policy that reads what was recorded about the impact (loaded only
    /// when a policy is plugged).
    pub items: Vec<ChangeItem>,
}

/// ReviewPolicy is the rule that says who may review a change impact (ADR 0075, as DecisionPolicy for decision
/// points, ADR 0067). The graph keeps the mechanism (the review, its history, the events) and asks the policy before
/// it writes anything; an error refuses the review and nothing is written. pkg/verify is the policy of the platform
/// (the verifier is not the producer); a graph given none lets anyone with the right to review do it.
pub trait ReviewPolicy: Send + Sync {
    fn review(&self, req: &ReviewRequest) -> Result<(), Error>;
}

/// ImpactVerdict is one verdict of a ReviewBatch: the review of one change impact.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ImpactVerdict {
    pub impact: ChangeImpactID,
    /// Status is accepted or rejected.
    pub status: NodeReview,
    /// Comment is mandatory, as for a review on its own.
    pub comment: String,
}

/// ReviewBatch is a set of reviews applied together (ADR 0080): all of them or none. ID is opaque to the graph, it
/// only stamps it on every review the batch writes (Review.review_id); Item, when set, is a fact written in the same
/// transaction once every verdict stood (the use case's record of the submission). Every verdict is made on Flow by
/// By.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ReviewBatch {
    pub id: String,
    pub flow: String,
    pub execution: String,
    pub by: String,
    pub verdicts: Vec<ImpactVerdict>,
    pub item: Option<ChangeItem>,
}
