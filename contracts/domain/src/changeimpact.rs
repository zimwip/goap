//! Change impacts: the links from a change to the nodes it reads, modifies or creates (ADR 0024).

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

use crate::{
    Error, ItemID, NodeRef, gotime,
    ids::string_type,
    serde_util::{is_false, null_default},
};

string_type!(
    /// ChangeImpactID identifies a ChangeImpact.
    ChangeImpactID
);

string_type!(
    /// NodeIntent is what a change means to do with a node (ADR 0024). A removal is not an intent: removing a child is
    /// a modification of its parent.
    NodeIntent
);

pub const INTENT_CREATED: NodeIntent = NodeIntent::from_static("created");
pub const INTENT_MODIFIED: NodeIntent = NodeIntent::from_static("modified");

string_type!(
    /// NodeReview is the review state of a change impact.
    NodeReview
);

pub const REVIEW_PROPOSED: NodeReview = NodeReview::from_static("proposed");
pub const REVIEW_ACCEPTED: NodeReview = NodeReview::from_static("accepted");
pub const REVIEW_REJECTED: NodeReview = NodeReview::from_static("rejected");

/// Review is one entry of the review history of a change impact.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Review {
    pub status: NodeReview,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    pub comment: String,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub at: DateTime<Utc>,
    /// Flow is the flow branch the review was made on ("" = the main flow), Execution the action run that made it;
    /// Superseded marks a review replaced by an adopted flow (ADR 0025).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub flow: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub execution: String,
    #[serde(skip_serializing_if = "is_false")]
    pub superseded: bool,
    /// ReviewID names the review (the key of its KindReview record, ADR 0080) that decided the impact: every accepted
    /// or rejected review carries it (ADR 0105: a verdict is always made by a submitted review). Empty when the status
    /// went back to proposed (an edit, a reopening, a lifecycle move).
    #[serde(rename = "reviewId", skip_serializing_if = "String::is_empty")]
    pub review_id: String,
}

/// ChangeImpact is the link from a change to a node: the node version the change reads (Pre), the version it produces
/// on the change branch (Post, empty until realized) and the one that landed on the target branch (Landed), with the
/// reason why (Rationale).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ChangeImpact {
    pub id: ChangeImpactID,
    pub key: String,
    pub r#type: String,
    pub intent: NodeIntent,
    pub rationale: String,
    /// Pre is the released version of the reference baseline (None when Intent is created).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pre: Option<NodeRef>,
    /// Post is the draft of the node while the change works (a draft reference: the node, Version 0, ADR 0079), the
    /// version written once the change is committed; None while the change impact is only planned.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub post: Option<NodeRef>,
    /// Landed is the version on the target branch once the change is applied.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub landed: Option<NodeRef>,
    pub review: NodeReview,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub reviews: Vec<Review>,
    /// Via is the change impact of the parent that realizes the removal of this node.
    #[serde(skip_serializing_if = "ChangeImpactID::is_empty")]
    pub via: ChangeImpactID,
    /// Recheck marks a rationale written against a version that is no longer the head.
    #[serde(skip_serializing_if = "is_false")]
    pub recheck: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub produced_by: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub derived_from: Vec<ItemID>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub execution: String,
    /// Items are the impact and proposal items this change impact stands for (the bridge of ADR 0024): set on change
    /// impacts derived from items, empty on the ones written directly.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub items: Vec<ItemID>,
    /// Flow is the flow branch that declared the change impact ("" = the main flow): a candidate until the flow is
    /// adopted. Superseded marks a change impact replaced by an adopted flow (ADR 0025).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub flow: String,
    #[serde(skip_serializing_if = "is_false")]
    pub superseded: bool,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub created_at: DateTime<Utc>,
    /// Validation is a read model: what the accepted-review gate would say of the draft as it stands. The graph fills
    /// it when it reads the change impacts for display (Graph.ChangeChecked, BlackboardIn); it is never stored and no
    /// event carries it. None: not checked, or nothing to check (no draft).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub validation: Option<ImpactValidation>,
}

/// ImpactValidation is the outcome of checking a draft as an accepted review would: Valid, or the first failure.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ImpactValidation {
    pub valid: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub message: String,
    /// Modified: the draft differs from the version it was checked out from (or creates a node); only a modification
    /// is accepted and committed (ADR 0102).
    #[serde(skip_serializing_if = "is_false")]
    pub modified: bool,
}

impl ChangeImpact {
    /// Planned reports whether the change impact has no post yet: its node is not checked out, no draft exists.
    pub fn planned(&self) -> bool {
        self.post.is_none()
    }

    /// Drafted reports whether the node of the change impact has a draft: Post is then the draft reference (Version 0,
    /// ADR 0079); once the change landed it is the version written.
    pub fn drafted(&self) -> bool {
        self.post.as_ref().is_some_and(NodeRef::is_draft)
    }

    /// Validate checks the shape of a change impact.
    pub fn validate(&self) -> Result<(), Error> {
        if self.key.is_empty() || self.r#type.is_empty() {
            return Err(Error::invalid("change impact needs a key and a type"));
        }
        if self.rationale.trim().is_empty() {
            return Err(Error::invalid(format!(
                "change impact {} needs a rationale",
                self.key
            )));
        }
        if self.intent == INTENT_CREATED {
            if self.pre.is_some() {
                return Err(Error::invalid(format!(
                    "change impact {}: a created node has no pre version",
                    self.key
                )));
            }
        } else if self.intent == INTENT_MODIFIED {
            let Some(pre) = self.pre.as_ref().filter(|p| p.version != 0) else {
                return Err(Error::invalid(format!(
                    "change impact {}: a modified node needs its pre version",
                    self.key
                )));
            };
            if let Some(post) = &self.post
                && (post.id != pre.id || (!post.is_draft() && post.version <= pre.version))
            {
                return Err(Error::invalid(format!(
                    "change impact {}: post {post} is not a successor of pre {pre}",
                    self.key
                )));
            }
        } else {
            return Err(Error::invalid(format!(
                "change impact {}: unknown intent {:?}",
                self.key,
                self.intent.as_str()
            )));
        }
        if self.landed.is_some() && self.post.is_none() {
            return Err(Error::invalid(format!(
                "change impact {}: landed without post",
                self.key
            )));
        }
        if self.review != REVIEW_PROPOSED
            && self.review != REVIEW_ACCEPTED
            && self.review != REVIEW_REJECTED
        {
            return Err(Error::invalid(format!(
                "change impact {}: unknown review {:?}",
                self.key,
                self.review.as_str()
            )));
        }
        Ok(())
    }
}
