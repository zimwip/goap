//! The review record folded from the items of a change.

use chrono::{DateTime, Utc};
use goap_domain::{self as domain, Change, ChangeImpact, ChangeItem, ItemID, ItemType, NodeReview};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use std::collections::HashMap;

/// KindReview is the kind of the items that hold a review of a change: built in (`domain::TYPE_REVIEW`, ADR 0105).
pub const KIND_REVIEW: ItemType = domain::TYPE_REVIEW;

/// Statuses of a review: it is open while the reviewer builds it, then submitted or discarded, which are final.
pub const OPEN: &str = domain::REVIEW_STATUS_OPEN;
pub const SUBMITTED: &str = domain::REVIEW_STATUS_SUBMITTED;
pub const DISCARDED: &str = domain::REVIEW_STATUS_DISCARDED;

/// Outcomes of an entry: the verdict on its change impact. Empty while the reviewer has not decided.
pub const ACCEPT: &str = domain::REVIEW_OUTCOME_ACCEPT;
pub const REJECT: &str = domain::REVIEW_OUTCOME_REJECT;

/// The refusals of the operations; the services map them as the graph's own (rpcerr). A refusal of the port is
/// carried as is ([`Error::Port`]).
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// ErrInvalid.
    #[error("invalid review: {0}")]
    Invalid(String),
    /// ErrConflict.
    #[error("review conflict: {0}")]
    Conflict(String),
    /// ErrNotFound.
    #[error("review not found: {0}")]
    NotFound(String),
    /// ErrForbidden (`authz::ErrForbidden`).
    #[error("permission denied: {0}")]
    Forbidden(String),
    /// An error of the port (the graph's), unchanged.
    #[error(transparent)]
    Port(Box<dyn std::error::Error + Send + Sync>),
}

impl Error {
    /// An error of the port.
    pub fn port(e: impl Into<Box<dyn std::error::Error + Send + Sync>>) -> Self {
        Error::Port(e.into())
    }

    /// `errors.Is(err, ErrInvalid)`.
    pub fn is_invalid(&self) -> bool {
        matches!(self, Error::Invalid(_))
    }

    /// `errors.Is(err, ErrConflict)`.
    pub fn is_conflict(&self) -> bool {
        matches!(self, Error::Conflict(_))
    }

    /// `errors.Is(err, ErrNotFound)`.
    pub fn is_not_found(&self) -> bool {
        matches!(self, Error::NotFound(_))
    }

    /// `errors.Is(err, ErrForbidden)`.
    pub fn is_forbidden(&self) -> bool {
        matches!(self, Error::Forbidden(_))
    }
}

/// Entry is the review of one change impact: its own comment and outcome.
pub type Entry = domain::ReviewEntry;

/// Review is the current version of a review of the change.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Review {
    pub key: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub flow: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub comment: String,
    pub status: String,
    pub entries: Vec<Entry>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    /// None: Go's zero time (written, as `omitempty` does not omit a struct).
    #[serde(with = "domain::gotime::option")]
    pub submitted_at: Option<DateTime<Utc>>,
    /// Item is the item holding this version, Versions how many versions the key has.
    pub item: ItemID,
    pub versions: i64,
}

impl Review {
    /// Open reports whether the reviewer may still change the review.
    pub fn open(&self) -> bool {
        self.status == OPEN
    }

    /// Ready reports whether the review may be submitted: open, at least one entry, an outcome on every one.
    pub fn ready(&self) -> bool {
        self.open() && !self.entries.is_empty() && self.entries.iter().all(Entry::decided)
    }

    /// Entry returns the entry of a change impact.
    pub fn entry(&self, impact: &str) -> Option<&Entry> {
        self.entries.iter().find(|e| e.impact == impact)
    }
}

/// EffectiveComment is the comment the review of an impact keeps when the review is submitted: the entry's own, then
/// the global one, so that each impact's review keeps both. It is empty only when both are.
pub fn effective_comment(global: &str, entry: &str) -> String {
    let (global, entry) = (global.trim(), entry.trim());
    if entry.is_empty() {
        return global.to_string();
    }
    if global.is_empty() {
        return entry.to_string();
    }
    format!("{entry}\n\nReview: {global}")
}

/// dataOf is the data of an item holding the review (the whole record).
pub(crate) fn data_of(r: &Review) -> Map<String, Value> {
    domain::ReviewRecord {
        key: r.key.clone(),
        flow: r.flow.clone(),
        comment: r.comment.clone(),
        status: r.status.clone(),
        entries: r.entries.clone(),
        by: r.by.clone(),
        submitted_at: r.submitted_at,
    }
    .data()
}

/// Reviews folds the items of a change into its reviews: the last version of each key, in the order the keys
/// appeared. A review that is submitted or discarded is final: a later version of its key is ignored. A rejected or
/// superseded item is not a version. It is pure.
pub fn reviews(items: &[ChangeItem]) -> Vec<Review> {
    let mut out: Vec<Review> = Vec::new();
    let mut at: HashMap<String, usize> = HashMap::new();
    for it in items {
        if it.r#type != KIND_REVIEW
            || it.status == domain::ITEM_REJECTED
            || it.status == domain::ITEM_SUPERSEDED
        {
            continue;
        }
        let rec = domain::review_record_of(&it.data);
        if rec.key.is_empty() || !domain::valid_review_status(&rec.status) {
            continue;
        }
        let mut r = Review {
            key: rec.key.clone(),
            flow: it.flow.clone(),
            comment: rec.comment,
            status: rec.status,
            entries: rec.entries,
            by: rec.by,
            submitted_at: rec.submitted_at,
            item: it.id.clone(),
            versions: 1,
        };
        if r.by.is_empty() {
            r.by = it.produced_by.clone();
        }
        if let Some(&i) = at.get(&rec.key) {
            if !out[i].open() {
                continue;
            }
            r.versions = out[i].versions + 1;
            out[i] = r;
            continue;
        }
        at.insert(rec.key, out.len());
        out.push(r);
    }
    out
}

/// Find returns the review of a key.
pub fn find(items: &[ChangeItem], key: &str) -> Option<Review> {
    reviews(items).into_iter().find(|r| r.key == key)
}

/// chain lists the flows from flow up to the main flow ("" included, last).
fn chain(c: &Change, flow: &str) -> Vec<String> {
    let mut flow = flow.to_string();
    let mut out = vec![flow.clone()];
    let mut hops = 0;
    while !flow.is_empty() && hops < 64 {
        let Some(f) = c.flow(&flow) else { break };
        flow = f.parent;
        out.push(flow.clone());
        hops += 1;
    }
    out
}

/// statusOn is the review a change impact has for a flow: the last review made on that flow, else the one of the
/// impact (the main flow's).
pub(crate) fn status_on(cn: &ChangeImpact, flow: &str) -> NodeReview {
    if !flow.is_empty()
        && let Some(r) = cn
            .reviews
            .iter()
            .rev()
            .find(|r| r.flow == flow && !r.superseded)
    {
        return r.status.clone();
    }
    cn.review.clone()
}

/// Awaiting returns the change impacts a review of a flow may take: those the flow sees (not superseded, declared on
/// the flow or its parents) whose review is proposed and that no other open review of the same flow holds. Pass the
/// reviews of the change ([`reviews`]); except is the key of a review whose own entries do not count (the one being
/// edited).
pub fn awaiting(c: &Change, flow: &str, reviews: &[Review], except: &str) -> Vec<ChangeImpact> {
    let flows = chain(c, flow);
    let mut held = std::collections::HashSet::new();
    for r in reviews {
        if r.open() && r.flow == flow && r.key != except {
            for e in &r.entries {
                held.insert(e.impact.as_str());
            }
        }
    }
    c.nodes
        .iter()
        .filter(|cn| {
            !cn.superseded
                && flows.contains(&cn.flow)
                && status_on(cn, flow) == domain::REVIEW_PROPOSED
                && !held.contains(cn.id.as_str())
        })
        .cloned()
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;
    use serde_json::json;

    fn obj(v: Value) -> Map<String, Value> {
        v.as_object().cloned().unwrap()
    }

    fn item(id: &str, d: Value) -> ChangeItem {
        ChangeItem {
            id: ItemID::new(id),
            r#type: KIND_REVIEW,
            status: domain::ITEM_PROPOSED,
            data: obj(d),
            produced_by: "alice".into(),
            ..Default::default()
        }
    }

    // Each version of an item is the whole record of its key: the review is the last one, in the order the keys
    // appeared.
    #[test]
    fn reviews_fold_keeps_the_last_version_of_each_key() {
        let items = vec![
            item(
                "1",
                json!({"key": "R1", "status": OPEN, "comment": "first", "entries": []}),
            ),
            item("2", json!({"key": "R2", "status": OPEN})),
            item(
                "3",
                json!({"key": "R1", "status": OPEN, "comment": "second", "entries": [{"impact": "i1", "outcome": "accept", "comment": "c"}]}),
            ),
            ChangeItem {
                id: ItemID::new("x"),
                r#type: domain::TYPE_ARTIFACT,
                ..Default::default()
            },
        ];
        let got = reviews(&items);
        assert!(
            got.len() == 2 && got[0].key == "R1" && got[1].key == "R2",
            "order: {got:?}"
        );
        let r = &got[0];
        assert!(
            r.comment == "second"
                && r.versions == 2
                && r.item.as_str() == "3"
                && r.by == "alice"
                && r.entries
                    == vec![Entry {
                        impact: "i1".into(),
                        comment: "c".into(),
                        outcome: ACCEPT.into()
                    }],
            "R1: {r:?}"
        );
    }

    // A submitted or discarded review is final: later versions of its key are ignored; so are rejected items.
    #[test]
    fn reviews_final_status_is_immutable() {
        let at = Utc.with_ymd_and_hms(2026, 1, 2, 3, 4, 5).unwrap();
        let sub = domain::gotime::format(&at);
        let mut rejected = item("6", json!({"key": "R3", "status": OPEN}));
        rejected.status = domain::ITEM_REJECTED;
        let items = vec![
            item("1", json!({"key": "R1", "status": OPEN})),
            item(
                "2",
                json!({"key": "R1", "status": SUBMITTED, "submittedAt": sub, "entries": [{"impact": "i", "outcome": "reject"}]}),
            ),
            item(
                "3",
                json!({"key": "R1", "status": OPEN, "comment": "sneaky"}),
            ),
            item("4", json!({"key": "R2", "status": DISCARDED})),
            item("5", json!({"key": "R2", "status": OPEN})),
            rejected,
        ];
        let got = reviews(&items);
        assert!(
            got.len() == 2
                && got[0].status == SUBMITTED
                && got[0].comment.is_empty()
                && got[0].submitted_at == Some(at)
                && got[1].status == DISCARDED,
            "{got:?}"
        );
        assert!(
            !got[0].open() && !got[0].ready(),
            "a submitted review is neither open nor ready"
        );
    }

    #[test]
    fn review_ready_needs_an_outcome_on_every_entry() {
        let mut r = Review {
            status: OPEN.into(),
            ..Default::default()
        };
        assert!(!r.ready(), "no entry");
        r.entries = vec![
            Entry {
                impact: "a".into(),
                outcome: ACCEPT.into(),
                ..Default::default()
            },
            Entry {
                impact: "b".into(),
                ..Default::default()
            },
        ];
        assert!(!r.ready(), "an entry has no outcome");
        r.entries[1].outcome = REJECT.into();
        assert!(r.ready(), "every entry is decided");
    }

    #[test]
    fn effective_comment_keeps_both() {
        for (global, entry, want) in [
            ("", "", ""),
            ("g", "", "g"),
            ("", "e", "e"),
            (" g ", " e ", "e\n\nReview: g"),
        ] {
            assert_eq!(
                effective_comment(global, entry),
                want,
                "{global:?} + {entry:?}"
            );
        }
    }

    #[test]
    fn validate() {
        let e = |imp: &str, out: &str| json!({"impact": imp, "outcome": out});
        let cases = [
            ("open, empty", json!({"key": "R", "status": OPEN}), true),
            ("no key", json!({"status": OPEN}), false),
            (
                "unknown status",
                json!({"key": "R", "status": "done"}),
                false,
            ),
            (
                "undecided while open",
                json!({"key": "R", "status": OPEN, "entries": [e("a", "")]}),
                true,
            ),
            (
                "bad outcome",
                json!({"key": "R", "status": OPEN, "entries": [e("a", "maybe")]}),
                false,
            ),
            (
                "twice",
                json!({"key": "R", "status": OPEN, "entries": [e("a", ""), e("a", "")]}),
                false,
            ),
            (
                "submitted no entry",
                json!({"key": "R", "status": SUBMITTED, "submittedAt": "x"}),
                false,
            ),
            (
                "submitted undecided",
                json!({"key": "R", "status": SUBMITTED, "submittedAt": "x", "entries": [e("a", "")]}),
                false,
            ),
            (
                "submitted no date",
                json!({"key": "R", "status": SUBMITTED, "entries": [e("a", "accept")]}),
                false,
            ),
            (
                "submitted",
                json!({"key": "R", "status": SUBMITTED, "submittedAt": "x", "entries": [e("a", "accept")]}),
                true,
            ),
            (
                "discarded with entries",
                json!({"key": "R", "status": DISCARDED, "entries": [e("a", "")]}),
                true,
            ),
        ];
        for (name, d, ok) in cases {
            let err = item("1", d).validate();
            assert_eq!(err.is_ok(), ok, "{name}: {err:?}");
        }
    }

    // Awaiting lists the impacts the flow sees whose review is proposed and that no other open review of the flow
    // holds.
    #[test]
    fn awaiting_lists_what_the_flow_sees() {
        let imp = |id: &str, review: NodeReview, superseded: bool, flow: &str| ChangeImpact {
            id: id.into(),
            review,
            superseded,
            flow: flow.into(),
            ..Default::default()
        };
        let c = Change {
            nodes: vec![
                imp("a", domain::REVIEW_PROPOSED, false, ""),
                imp("b", domain::REVIEW_ACCEPTED, false, ""),
                imp("c", domain::REVIEW_PROPOSED, false, ""),
                imp("d", domain::REVIEW_PROPOSED, true, ""),
                imp("e", domain::REVIEW_PROPOSED, false, "f1"),
            ],
            ..Default::default()
        };
        let ids = |l: Vec<ChangeImpact>| -> Vec<String> {
            l.into_iter().map(|cn| cn.id.into_string()).collect()
        };
        let entry = |i: &str| Entry {
            impact: i.into(),
            ..Default::default()
        };
        let rs = vec![
            Review {
                key: "R1".into(),
                status: OPEN.into(),
                entries: vec![entry("c")],
                ..Default::default()
            },
            Review {
                key: "R2".into(),
                status: SUBMITTED.into(),
                entries: vec![entry("a")],
                ..Default::default()
            },
        ];
        // the main flow does not see the impact of flow f1; c is held by the open review R1
        assert_eq!(ids(awaiting(&c, "", &rs, "")), vec!["a"]);
        assert_eq!(
            ids(awaiting(&c, "", &rs, "R1")),
            vec!["a", "c"],
            "the review being edited does not hold its own"
        );
    }
}
