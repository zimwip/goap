//! The change impacts of a change are event-sourced (ADR 0029): every operation on them is an ImpactEvent appended to
//! the change's log, with its caller. Their state, for the main flow or for a flow, is a fold of the log.

use std::collections::HashMap;

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    BaselineID, ChangeID, ChangeImpact, ChangeImpactID, Draft, Error, INTENT_CREATED, NodeRef,
    REVIEW_PROPOSED, REVIEW_REJECTED, Review, gotime, ids::string_type, serde_util::null_default,
};

string_type!(
    /// ImpactOp is the operation an ImpactEvent records.
    ImpactOp
);

/// ImpactProposed adds a change impact to the change (State: as proposed): the modification of an existing node, or a
/// planned creation (intent created, no draft, ADR 0121), which the checkedOut event of its creation fulfils. A new
/// node is otherwise created, not proposed (ADR 0077).
pub const IMPACT_PROPOSED: ImpactOp = ImpactOp::from_static("proposed");
/// ImpactCreated is the creation of a node: one event adds the change impact (State, intent created) and the draft of
/// the node (Draft: its initial state; Post is the draft reference, ADR 0079), on Flow. No proposed event precedes it
/// and no checkout follows (ADR 0076, 0077). The merge change (graph.merge) records the versions it writes itself the
/// same way, with a version in Post and no Draft.
pub const IMPACT_CREATED: ImpactOp = ImpactOp::from_static("created");
/// ImpactCheckedOut records the draft a checkout makes for the change impact of an existing node (Draft: a copy of
/// the version the flow sees, Post the draft reference), on Flow (ADR 0076, 0079).
pub const IMPACT_CHECKED_OUT: ImpactOp = ImpactOp::from_static("checkedOut");
/// ImpactTransitioned records a lifecycle transition taken on the draft (Patch {"state": {"from", "to"}}, with the
/// properties its action algorithms set); or, carrying Draft, a draft installed on the flow by an adoption (Patch
/// {"adopted": flow}), or a version a merge writes in the merge change (Post a version, no Draft). ADR 0079.
pub const IMPACT_TRANSITIONED: ImpactOp = ImpactOp::from_static("transitioned");
/// ImpactUpdated records an edit of the draft (Patch): properties, owner, links (ADR 0076, 0079).
pub const IMPACT_UPDATED: ImpactOp = ImpactOp::from_static("updated");
/// ImpactCancelled records a checkout cancelled on Flow: the draft is dropped and Post is the draft the flow falls
/// back to (None: none; a creation cancelled removes the change impact).
pub const IMPACT_CANCELLED: ImpactOp = ImpactOp::from_static("cancelled");
/// ImpactWithdrawn takes a change impact out of the change, explicitly (its draft, if any, is dropped): the impact
/// leaves the list (ADR 0076 §5b).
pub const IMPACT_WITHDRAWN: ImpactOp = ImpactOp::from_static("withdrawn");
/// ImpactReviewed records a review (Review). An accepted review is a gate on the draft (ADR 0079): the checks of a
/// version run, nothing is written.
pub const IMPACT_REVIEWED: ImpactOp = ImpactOp::from_static("reviewed");
/// ImpactDiscarded rejects a candidate whose flow was discarded (Review).
pub const IMPACT_DISCARDED: ImpactOp = ImpactOp::from_static("discarded");
/// ImpactAdopted is change level: Flow is adopted, Stale are the runs it replaces (ADR 0025 §5).
pub const IMPACT_ADOPTED: ImpactOp = ImpactOp::from_static("adopted");
/// ImpactLanded records the version a change applied (Landed) in the baseline it produced (Baseline): on its own
/// branch when it is committed (the version is written from the draft then, which goes: Post becomes that version,
/// ADR 0079), and again on the branch it is integrated into. The last one is the version on the target branch. A
/// merge change records the same for the versions it writes.
pub const IMPACT_LANDED: ImpactOp = ImpactOp::from_static("landed");
/// ImpactRebased moves the pre version of a planned change impact to a newer head, to re-check (Pre).
pub const IMPACT_REBASED: ImpactOp = ImpactOp::from_static("rebased");

impl ImpactOp {
    /// WritesPost reports whether the operation sets the post of a change impact: the draft of its node (or, in the
    /// merge change, a version).
    pub fn writes_post(&self) -> bool {
        *self == IMPACT_TRANSITIONED || *self == IMPACT_CHECKED_OUT || *self == IMPACT_CREATED
    }
}

/// ImpactEvent is one operation on the change impacts of a change.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ImpactEvent {
    pub id: String,
    #[serde(rename = "changeId")]
    pub change: ChangeID,
    pub seq: i64,
    #[serde(rename = "impactId", skip_serializing_if = "ChangeImpactID::is_empty")]
    pub impact: ChangeImpactID,
    pub op: ImpactOp,
    /// The caller: the flow branch the operation was made on ("" = the main flow), the journal record of the action
    /// run (process, step, action: ADR 0011) and the principal or component.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub flow: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub execution: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    /// At is when the event was made (None: Go's zero time, set by the graph on emit).
    #[serde(with = "gotime::option")]
    pub at: Option<DateTime<Utc>>,
    /// Causation is the log entry the event follows from (ADR 0105): on a decided reviewed event, the fact.review
    /// entry of the submitted review that decided it.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub causation: String,
    /// State is the change impact (proposed, created).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub state: Option<ChangeImpact>,
    /// Post is the draft reference of the node (Version 0) the event leaves the impact with, or a version (merge
    /// change).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub post: Option<NodeRef>,
    /// Draft is, on created, checkedOut and an adopting transitioned, the draft the event installs for (impact, flow):
    /// its initial state, so the log alone rebuilds the drafts (ADR 0079).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub draft: Option<Draft>,
    /// Pre is set on rebased; on transitioned: the rebase of a draft moved its pre (ADR 0101).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pre: Option<NodeRef>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub landed: Option<NodeRef>,
    /// Baseline is, on landed, the baseline of the branch the version landed in (ADR 0032).
    #[serde(skip_serializing_if = "BaselineID::is_empty")]
    pub baseline: BaselineID,
    /// Branch is, on landed, the branch the version landed on: the change's own branch at commit, the branch it is
    /// integrated into after (the main flow's: branch_of).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub branch: String,
    /// Review is set on reviewed, discarded.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub review: Option<Review>,
    /// Stale is, on adopted, the stale executions.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub stale: Vec<String>,
    /// Patch is what an updated event changed on the draft: {"props": {...}, "unset": [...], "owner": unit key,
    /// "ownerId": unit node, "addLink": {id, type, to, toId, toVersion, props}, "updateLink": {id, props},
    /// "removeLink": {id, type, to}}; a transitioned event: {"state": {"from", "to"}} and the "props" / "unset" its
    /// actions made. apply_draft_event reads it.
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub patch: Map<String, Value>,
}

impl ImpactEvent {
    /// StartsDraft reports whether the event installs a draft for the impact on its flow: a creation, a checkout or an
    /// adoption (a transition without Draft edits the one it finds, ADR 0079). A change goes through its lifecycle
    /// states while it works: the state an impact was written in is the one of the event that started its draft (ADR
    /// 0058).
    pub fn starts_draft(&self) -> bool {
        self.draft.is_some() && self.op.writes_post()
    }

    /// Validate checks that an event carries what its operation needs.
    pub fn validate(&self) -> Result<(), Error> {
        let need = |ok: bool, what: &str| -> Result<(), Error> {
            if ok {
                Ok(())
            } else {
                Err(Error::invalid(format!("event {} needs {what}", self.op)))
            }
        };
        let op = &self.op;
        let state_ok = self.state.as_ref().is_some_and(|s| s.id == self.impact);
        if *op == IMPACT_PROPOSED {
            need(state_ok, "the change impact")
        } else if *op == IMPACT_CREATED {
            need(
                state_ok && self.post.is_some(),
                "the change impact and its draft",
            )?;
            need(
                !self.post.as_ref().is_some_and(NodeRef::is_draft) || self.draft.is_some(),
                "the initial state of the draft",
            )
        } else if *op == IMPACT_TRANSITIONED || *op == IMPACT_CHECKED_OUT {
            need(
                !self.impact.is_empty() && self.post.is_some(),
                "a change impact and a post",
            )?;
            need(
                *op == IMPACT_TRANSITIONED
                    || !self.post.as_ref().is_some_and(NodeRef::is_draft)
                    || self.draft.is_some(),
                "the initial state of the draft",
            )
        } else if *op == IMPACT_REVIEWED || *op == IMPACT_DISCARDED {
            need(
                !self.impact.is_empty() && self.review.is_some(),
                "a change impact and a review",
            )
        } else if *op == IMPACT_ADOPTED {
            need(
                self.impact.is_empty() && !self.flow.is_empty(),
                "a flow and no change impact",
            )
        } else if *op == IMPACT_LANDED {
            need(
                !self.impact.is_empty() && self.landed.is_some(),
                "a change impact and a landed version",
            )
        } else if *op == IMPACT_REBASED {
            need(
                !self.impact.is_empty() && self.pre.is_some(),
                "a change impact and a pre version",
            )
        } else if *op == IMPACT_UPDATED {
            need(
                !self.impact.is_empty() && self.post.is_some() && !self.patch.is_empty(),
                "a change impact, its draft and a patch",
            )
        } else if *op == IMPACT_CANCELLED || *op == IMPACT_WITHDRAWN {
            need(!self.impact.is_empty(), "a change impact")
        } else {
            Err(Error::invalid(format!(
                "unknown event operation {:?}",
                op.as_str()
            )))
        }
    }
}

/// FoldImpacts replays a log: the change impacts of the change, in declaration order.
pub fn fold_impacts(events: &[ImpactEvent]) -> Vec<ChangeImpact> {
    let mut out = Vec::new();
    for e in events {
        out = apply_impact_event(&out, e);
    }
    out
}

/// ApplyImpactEvent returns the change impacts after an event (the list is not modified). An event missing what its
/// operation needs (see ImpactEvent::validate) leaves the impacts as they are.
pub fn apply_impact_event(impacts: &[ChangeImpact], e: &ImpactEvent) -> Vec<ChangeImpact> {
    let mut out = impacts.to_vec();
    let at = out.iter().position(|c| c.id == e.impact);
    if e.op == IMPACT_PROPOSED || e.op == IMPACT_CREATED {
        let Some(state) = &e.state else { return out };
        let mut cn = state.clone();
        if e.op == IMPACT_CREATED {
            cn.post = e.post.clone();
        }
        match at {
            Some(i) => out[i] = cn,
            None => out.push(cn),
        }
        return out;
    }
    if e.op == IMPACT_ADOPTED {
        for c in out.iter_mut() {
            *c = adopt_impact(c, &e.flow, &e.stale);
        }
        return out;
    }
    let Some(at) = at else { return out };
    if e.op == IMPACT_WITHDRAWN {
        out.remove(at);
        return out;
    }
    let cn = &mut out[at];
    let op = &e.op;
    if *op == IMPACT_TRANSITIONED || *op == IMPACT_CHECKED_OUT {
        // the stored post is the main flow's, or the one of the flow that declared the change impact
        if (e.flow.is_empty() || e.flow == cn.flow)
            && let Some(p) = &e.post
        {
            cn.post = Some(p.clone());
            if let Some(pre) = &e.pre
                && *op == IMPACT_TRANSITIONED
            {
                cn.pre = Some(pre.clone()); // a rebase moved the draft (and its pre) onto the head of the target (ADR 0101)
            }
        }
    } else if *op == IMPACT_REVIEWED {
        if let Some(r) = &e.review {
            cn.reviews.push(r.clone());
            if r.flow.is_empty() {
                cn.review = r.status.clone();
            }
        }
    } else if *op == IMPACT_DISCARDED {
        if let Some(r) = &e.review {
            cn.reviews.push(r.clone());
            cn.review = REVIEW_REJECTED;
        }
    } else if *op == IMPACT_LANDED {
        if let Some(l) = &e.landed {
            cn.landed = Some(l.clone());
            if cn.post.as_ref().is_none_or(NodeRef::is_draft) {
                cn.post = Some(l.clone()); // the draft is written as this version (ADR 0079)
            }
        }
    } else if *op == IMPACT_REBASED {
        if let Some(p) = &e.pre {
            cn.pre = Some(p.clone());
            cn.recheck = true;
        }
    } else if *op == IMPACT_CANCELLED {
        if !e.flow.is_empty() && e.flow != cn.flow {
            return out; // the stored post is the main flow's (see written)
        }
        if e.post.is_none() && cn.intent == INTENT_CREATED {
            out.remove(at); // a creation cancelled before it is accepted: nothing is left
            return out;
        }
        cn.post = e.post.clone();
    }
    out
}

/// adoptImpact is ADR 0025 §5.3 for one change impact: the flow's change impacts and reviews become the main flow's,
/// the stale ones are superseded, and the review is recomputed.
fn adopt_impact(cn: &ChangeImpact, flow: &str, stale_executions: &[String]) -> ChangeImpact {
    let mut cn = cn.clone();
    let stale = |e: &str| !e.is_empty() && stale_executions.iter().any(|s| s == e);
    if cn.flow == flow {
        cn.flow.clear();
    } else if cn.flow.is_empty() && !cn.superseded && stale(&cn.execution) {
        cn.superseded = true;
    }
    for r in cn.reviews.iter_mut() {
        if r.flow == flow {
            r.flow.clear();
        } else if r.flow.is_empty() && stale(&r.execution) {
            r.superseded = true;
        }
    }
    if !cn.superseded {
        cn.review = REVIEW_PROPOSED;
        for r in &cn.reviews {
            if !r.superseded && r.flow.is_empty() {
                cn.review = r.status.clone();
            }
        }
    }
    cn
}

/// ImpactsSeenBy is what a process on a flow sees of the change impacts (ADR 0025 §3, ADR 0029 §3): impacts is the
/// stored state (fold_impacts), chain the flow and its ancestors (innermost first, empty for the main flow) and stale
/// the runs the chain invalidates. The change impacts declared by another flow or by a stale run are left out; the
/// post is the last version written by a run that is not stale on the innermost flow of the chain that wrote one,
/// else on the main flow; the review is the last one on the chain or the main flow that is neither superseded nor
/// stale.
pub fn impacts_seen_by(
    impacts: &[ChangeImpact],
    events: &[ImpactEvent],
    chain: &[String],
    stale: &HashMap<String, bool>,
) -> Vec<ChangeImpact> {
    let is_stale = |e: &str| !e.is_empty() && stale.get(e).copied().unwrap_or(false);
    let in_chain = |f: &str| chain.iter().any(|c| c == f);
    let mut out = Vec::new();
    for cn in impacts {
        if cn.superseded
            || (!cn.flow.is_empty() && !in_chain(&cn.flow))
            || (!chain.is_empty() && is_stale(&cn.execution))
        {
            continue;
        }
        if chain.is_empty() {
            out.push(cn.clone());
            continue;
        }
        let mut cn = cn.clone();
        cn.post = flow_post(events, &cn.id, chain, &is_stale);
        cn.review = REVIEW_PROPOSED;
        for r in &cn.reviews {
            if r.superseded || (!r.flow.is_empty() && !in_chain(&r.flow)) || is_stale(&r.execution)
            {
                continue;
            }
            cn.review = r.status.clone();
        }
        out.push(cn);
    }
    out
}

fn flow_post(
    events: &[ImpactEvent],
    id: &ChangeImpactID,
    chain: &[String],
    stale: &dyn Fn(&str) -> bool,
) -> Option<NodeRef> {
    let mut last: HashMap<&str, Option<NodeRef>> = HashMap::new(); // by flow
    for e in events {
        if (!e.op.writes_post() && e.op != IMPACT_CANCELLED) || e.impact != *id {
            continue;
        }
        if e.flow.is_empty() && !stale(&e.execution) {
            last.insert("", e.post.clone());
        } else if !e.flow.is_empty() && chain.contains(&e.flow) && !stale(&e.execution) {
            // a flow inside a flow may relaunch a step of its parent flow: that step's writes are stale too
            last.insert(&e.flow, e.post.clone());
        }
        if e.op == IMPACT_CANCELLED && e.post.is_none() {
            last.remove(e.flow.as_str()); // back to what the flow saw before its checkout
        }
    }
    chain
        .iter()
        .map(String::as_str)
        .chain(std::iter::once(""))
        .find_map(|f| last.get(f).cloned().flatten())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{INTENT_MODIFIED, REVIEW_ACCEPTED, Version};

    fn r(v: Version) -> Option<NodeRef> {
        Some(NodeRef {
            id: "N".into(),
            version: v,
        })
    }

    #[test]
    fn fold_impacts_test() {
        let a = ChangeImpact {
            id: "A".into(),
            key: "K".into(),
            r#type: "T".into(),
            intent: INTENT_MODIFIED,
            rationale: "r".into(),
            pre: r(1),
            review: REVIEW_PROPOSED,
            execution: "x1".into(),
            ..Default::default()
        };
        let b = ChangeImpact {
            id: "B".into(),
            key: "K2".into(),
            r#type: "T".into(),
            intent: INTENT_CREATED,
            rationale: "r".into(),
            review: REVIEW_PROPOSED,
            flow: "F".into(),
            execution: "y1".into(),
            ..Default::default()
        };
        let log = vec![
            ImpactEvent {
                op: IMPACT_PROPOSED,
                impact: "A".into(),
                state: Some(a),
                execution: "x1".into(),
                ..Default::default()
            },
            ImpactEvent {
                op: IMPACT_TRANSITIONED,
                impact: "A".into(),
                post: r(2),
                execution: "x2".into(),
                ..Default::default()
            },
            ImpactEvent {
                op: IMPACT_REVIEWED,
                impact: "A".into(),
                review: Some(Review {
                    status: REVIEW_ACCEPTED,
                    comment: "ok".into(),
                    execution: "x2".into(),
                    ..Default::default()
                }),
                ..Default::default()
            },
            // the flow F replaces the run x2: it writes A again on its branch and declares B
            ImpactEvent {
                op: IMPACT_TRANSITIONED,
                impact: "A".into(),
                post: r(3),
                flow: "F".into(),
                execution: "y1".into(),
                ..Default::default()
            },
            ImpactEvent {
                op: IMPACT_PROPOSED,
                impact: "B".into(),
                state: Some(b),
                flow: "F".into(),
                execution: "y1".into(),
                ..Default::default()
            },
        ];
        for e in &log {
            e.validate().unwrap();
        }
        let stored = fold_impacts(&log);
        assert!(
            stored.len() == 2 && stored[0].post == r(2) && stored[0].review == REVIEW_ACCEPTED,
            "stored = {stored:?}"
        );

        // the main flow does not see the candidate; the flow sees its own post and no review (made by a stale run)
        let main = impacts_seen_by(&stored, &log, &[], &HashMap::new());
        assert_eq!(main.len(), 1, "main view = {main:?}");
        let stale: HashMap<String, bool> = [("x2".to_string(), true)].into();
        let seen = impacts_seen_by(&stored, &log, &["F".to_string()], &stale);
        assert!(
            seen.len() == 2 && seen[0].post == r(3) && seen[0].review == REVIEW_PROPOSED,
            "flow view = {seen:?}"
        );
        // a sibling flow sees the main flow minus its own stale runs, not F
        let sib = impacts_seen_by(&stored, &log, &["G".to_string()], &stale);
        assert!(
            sib.len() == 1 && sib[0].post.is_none(),
            "sibling view = {sib:?}"
        );

        // adoption: B becomes the main flow's, the review of the stale run is superseded
        let adopted = apply_impact_event(
            &stored,
            &ImpactEvent {
                op: IMPACT_ADOPTED,
                flow: "F".into(),
                stale: vec!["x2".into()],
                ..Default::default()
            },
        );
        assert!(
            adopted[1].flow.is_empty()
                && adopted[0].reviews[0].superseded
                && adopted[0].review == REVIEW_PROPOSED,
            "adopted = {adopted:?}"
        );
        assert!(
            !stored[0].reviews[0].superseded,
            "apply_impact_event modified its input"
        );
    }

    // A creation is one event: it adds the change impact and records its first version (no proposal precedes it).
    #[test]
    fn created_event_adds_the_impact_and_its_post() {
        let b = ChangeImpact {
            id: "B".into(),
            key: "K".into(),
            r#type: "T".into(),
            intent: INTENT_CREATED,
            rationale: "r".into(),
            review: REVIEW_PROPOSED,
            ..Default::default()
        };
        let post = r(1);
        let e = ImpactEvent {
            op: IMPACT_CREATED,
            impact: "B".into(),
            state: Some(b.clone()),
            post: post.clone(),
            ..Default::default()
        };
        e.validate().unwrap();
        let bare = ImpactEvent {
            op: IMPACT_CREATED,
            impact: "B".into(),
            post: post.clone(),
            ..Default::default()
        };
        assert!(
            bare.validate().is_err(),
            "a created event carries the impact"
        );
        let got = fold_impacts(std::slice::from_ref(&e));
        assert!(
            got.len() == 1 && got[0].post == post && got[0].intent == INTENT_CREATED,
            "fold = {got:?}"
        );
        assert!(b.post.is_none(), "the event's state was modified");
    }
}
