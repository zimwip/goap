//! The record of a review (ADR 0080, built in since ADR 0105).

use std::collections::HashSet;

use chrono::{DateTime, Utc};
use serde_json::{Map, Value};

use crate::{ChangeItem, Error, ItemType, NodeReview, REVIEW_ACCEPTED, gotime};

/// TypeReview is the kind of the items that hold a review of a change (ADR 0080, built in since ADR 0105): every
/// verdict on a change impact is made by a submitted review, so the graph writes the record of one with the verdicts
/// it decides. Each item is a version of the record of its data.key; pkg/review is the use case that builds one up
/// before it is submitted.
pub const TYPE_REVIEW: ItemType = ItemType::from_static("review");

/// Statuses of a review record: open while the reviewer builds it, then submitted or discarded, which are final.
pub const REVIEW_STATUS_OPEN: &str = "open";
pub const REVIEW_STATUS_SUBMITTED: &str = "submitted";
pub const REVIEW_STATUS_DISCARDED: &str = "discarded";

/// Outcomes of a review entry: the verdict on its change impact. Empty while the reviewer has not decided.
pub const REVIEW_OUTCOME_ACCEPT: &str = "accept";
pub const REVIEW_OUTCOME_REJECT: &str = "reject";

/// ReviewEntry is the review of one change impact in a review record: its own comment and outcome.
#[derive(Debug, Clone, Default, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(default)]
pub struct ReviewEntry {
    pub impact: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub comment: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub outcome: String,
}

impl ReviewEntry {
    /// Decided reports whether the entry has an outcome.
    pub fn decided(&self) -> bool {
        self.outcome == REVIEW_OUTCOME_ACCEPT || self.outcome == REVIEW_OUTCOME_REJECT
    }
}

/// ReviewOutcome is the outcome of a verdict.
pub fn review_outcome(s: &NodeReview) -> &'static str {
    if *s == REVIEW_ACCEPTED {
        REVIEW_OUTCOME_ACCEPT
    } else {
        REVIEW_OUTCOME_REJECT
    }
}

/// ReviewRecord is the data of a review item (the whole record, a version never restates by omission): key, flow (the
/// flow the entries are reviewed on, "" the main flow), comment (the global comment), status, entries, by (the
/// author), submittedAt (once submitted; None: Go's zero time).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ReviewRecord {
    pub key: String,
    pub flow: String,
    pub comment: String,
    pub status: String,
    pub entries: Vec<ReviewEntry>,
    pub by: String,
    pub submitted_at: Option<DateTime<Utc>>,
}

impl ReviewRecord {
    /// Data is the data of the item holding the record.
    pub fn data(&self) -> Map<String, Value> {
        let entries: Vec<Value> = self
            .entries
            .iter()
            .map(|e| {
                let mut m = Map::new();
                m.insert("impact".into(), Value::from(e.impact.clone()));
                if !e.comment.is_empty() {
                    m.insert("comment".into(), Value::from(e.comment.clone()));
                }
                if !e.outcome.is_empty() {
                    m.insert("outcome".into(), Value::from(e.outcome.clone()));
                }
                Value::Object(m)
            })
            .collect();
        let mut d = Map::new();
        d.insert("key".into(), Value::from(self.key.clone()));
        d.insert("status".into(), Value::from(self.status.clone()));
        d.insert("entries".into(), Value::Array(entries));
        if !self.flow.is_empty() {
            d.insert("flow".into(), Value::from(self.flow.clone()));
        }
        if !self.comment.is_empty() {
            d.insert("comment".into(), Value::from(self.comment.clone()));
        }
        if !self.by.is_empty() {
            d.insert("by".into(), Value::from(self.by.clone()));
        }
        if let Some(t) = self.submitted_at.filter(|t| !gotime::is_zero(t)) {
            d.insert("submittedAt".into(), Value::from(gotime::format(&t)));
        }
        d
    }
}

/// ReviewRecordOf reads the record of the data of a review item.
pub fn review_record_of(d: &Map<String, Value>) -> ReviewRecord {
    let s = review_string(d, "submittedAt");
    ReviewRecord {
        key: review_string(d, "key"),
        flow: review_string(d, "flow"),
        comment: review_string(d, "comment"),
        status: review_string(d, "status"),
        entries: review_entries_of(d),
        by: review_string(d, "by"),
        submitted_at: if s.is_empty() {
            None
        } else {
            gotime::parse(&s).filter(|t| !gotime::is_zero(t))
        },
    }
}

fn review_string(d: &Map<String, Value>, k: &str) -> String {
    d.get(k)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .trim()
        .to_string()
}

/// ReviewEntriesOf reads the entries of the data of a review item.
pub fn review_entries_of(d: &Map<String, Value>) -> Vec<ReviewEntry> {
    match d.get("entries") {
        Some(Value::Array(v)) => v
            .iter()
            .filter_map(Value::as_object)
            .map(|m| ReviewEntry {
                impact: review_string(m, "impact"),
                comment: review_string(m, "comment"),
                outcome: review_string(m, "outcome"),
            })
            .collect(),
        _ => Vec::new(),
    }
}

/// ValidReviewStatus reports whether s is a status of a review record.
pub fn valid_review_status(s: &str) -> bool {
    s == REVIEW_STATUS_OPEN || s == REVIEW_STATUS_SUBMITTED || s == REVIEW_STATUS_DISCARDED
}

/// validateReview checks the data of a review item: what any version satisfies, and what a submitted one adds.
pub(crate) fn validate_review(it: &ChangeItem) -> Result<(), Error> {
    let d = &it.data;
    if review_string(d, "key").is_empty() {
        return Err(Error::invalid("review item requires data.key"));
    }
    let status = review_string(d, "status");
    if !valid_review_status(&status) {
        return Err(Error::invalid(format!(
            "review status must be one of {REVIEW_STATUS_OPEN}, {REVIEW_STATUS_SUBMITTED}, {REVIEW_STATUS_DISCARDED}"
        )));
    }
    let entries = review_entries_of(d);
    let mut seen = HashSet::new();
    for e in &entries {
        if e.impact.is_empty() {
            return Err(Error::invalid("review entry requires its impact"));
        }
        if !seen.insert(e.impact.as_str()) {
            return Err(Error::invalid(format!(
                "review names impact {} twice",
                e.impact
            )));
        }
        if !e.outcome.is_empty() && !e.decided() {
            return Err(Error::invalid(format!(
                "review entry outcome must be {REVIEW_OUTCOME_ACCEPT} or {REVIEW_OUTCOME_REJECT}"
            )));
        }
    }
    if status == REVIEW_STATUS_SUBMITTED {
        if entries.is_empty() {
            return Err(Error::invalid("a submitted review has at least one entry"));
        }
        if let Some(e) = entries.iter().find(|e| !e.decided()) {
            return Err(Error::invalid(format!(
                "a submitted review decides every entry: impact {} has no outcome",
                e.impact
            )));
        }
        if review_string(d, "submittedAt").is_empty() {
            return Err(Error::invalid(
                "a submitted review requires data.submittedAt",
            ));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;

    #[test]
    fn record_round_trip_and_validation() {
        let r = ReviewRecord {
            key: "R1".into(),
            status: REVIEW_STATUS_SUBMITTED.into(),
            entries: vec![ReviewEntry {
                impact: "I1".into(),
                outcome: REVIEW_OUTCOME_ACCEPT.into(),
                ..Default::default()
            }],
            by: "ann".into(),
            submitted_at: Some(Utc.with_ymd_and_hms(2026, 9, 1, 0, 0, 0).unwrap()),
            ..Default::default()
        };
        let d = r.data();
        assert_eq!(d["submittedAt"], "2026-09-01T00:00:00Z");
        assert_eq!(review_record_of(&d), r);
        let it = ChangeItem {
            r#type: TYPE_REVIEW,
            data: d,
            ..Default::default()
        };
        it.validate().unwrap();
        let mut open = it.clone();
        open.data
            .insert("entries".into(), serde_json::json!([{"impact": "I1"}]));
        assert_eq!(
            open.validate().unwrap_err().to_string(),
            "a submitted review decides every entry: impact I1 has no outcome"
        );
    }
}
