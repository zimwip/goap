use goap_domain::ItemID;
use goap_gen::graph::v1 as pb;
use goap_review as review;

use crate::{from_opt_time, opt_time};

/// ReviewToPB converts a review of the review object (ADR 0080).
pub fn review_to_pb(r: &review::Review) -> pb::ReviewRecord {
    pb::ReviewRecord {
        key: r.key.clone(),
        flow: r.flow.clone(),
        comment: r.comment.clone(),
        status: r.status.clone(),
        by: r.by.clone(),
        item: r.item.to_string(),
        versions: r.versions as i32,
        submitted_at: opt_time(&r.submitted_at),
        entries: r
            .entries
            .iter()
            .map(|e| pb::ReviewEntry {
                change_impact_id: e.impact.clone(),
                comment: e.comment.clone(),
                outcome: e.outcome.clone(),
            })
            .collect(),
    }
}

/// ReviewFromPB is the reverse of ReviewToPB (the zero review for None).
pub fn review_from_pb(m: Option<&pb::ReviewRecord>) -> review::Review {
    let Some(m) = m else {
        return review::Review::default();
    };
    review::Review {
        key: m.key.clone(),
        flow: m.flow.clone(),
        comment: m.comment.clone(),
        status: m.status.clone(),
        by: m.by.clone(),
        item: ItemID::new(m.item.clone()),
        versions: i64::from(m.versions),
        submitted_at: from_opt_time(m.submitted_at.as_ref()),
        entries: m
            .entries
            .iter()
            .map(|e| review::Entry {
                impact: e.change_impact_id.clone(),
                comment: e.comment.clone(),
                outcome: e.outcome.clone(),
            })
            .collect(),
    }
}

/// ReviewEditFromPB reads the edit of an open review.
pub fn review_edit_from_pb(m: &pb::ReviewUpdateRequest) -> review::Edit {
    review::Edit {
        add: m.add.clone(),
        remove: m.remove.clone(),
        comment: m.set_comment.then(|| m.comment.clone()),
        entries: m
            .entries
            .iter()
            .map(|x| review::EntryEdit {
                impact: x.change_impact_id.clone(),
                comment: x.set_comment.then(|| x.comment.clone()),
                outcome: x.set_outcome.then(|| x.outcome.clone()),
            })
            .collect(),
    }
}

/// ReviewEditToPB is the reverse of ReviewEditFromPB (the request of an edit).
pub fn review_edit_to_pb(change: &str, key: &str, e: &review::Edit) -> pb::ReviewUpdateRequest {
    pb::ReviewUpdateRequest {
        change_id: change.to_string(),
        key: key.to_string(),
        add: e.add.clone(),
        remove: e.remove.clone(),
        set_comment: e.comment.is_some(),
        comment: e.comment.clone().unwrap_or_default(),
        entries: e
            .entries
            .iter()
            .map(|x| pb::ReviewEntryEdit {
                change_impact_id: x.impact.clone(),
                set_comment: x.comment.is_some(),
                comment: x.comment.clone().unwrap_or_default(),
                set_outcome: x.outcome.is_some(),
                outcome: x.outcome.clone().unwrap_or_default(),
            })
            .collect(),
    }
}
