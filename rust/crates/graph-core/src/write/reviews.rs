//! The review object of a change (Go's `contracts/review`, ADR 0080, 0105): a reviewer builds a review up (a global
//! comment, one entry per change impact with its own comment and outcome), then submits it, which applies every entry
//! as a verdict of one batch ([`super::review`]), all of them or none. Each version of a review is an item of type
//! `review` holding the whole record of its `data.key`; the reviews of a change are the fold of those items.
use chrono::{DateTime, Utc};
use serde_json::{Map, Value};

use super::{
    items::{ItemWrite, TYPE_REVIEW, data_text, review_entries},
    review::{ReviewBatch, Verdict},
    tx::Tx,
};
use crate::{
    GraphError, Kind, Result,
    flow::{Flows, MAIN_FLOW},
    gojson::time_text,
    impact::{ChangeImpact, LOG_FACT, REVIEW_PROPOSED},
    model::ChangeId,
    store::{ReadTx, WriteTx},
};

const OPEN: &str = "open";
const SUBMITTED: &str = "submitted";
const DISCARDED: &str = "discarded";
const ACCEPT: &str = "accept";
const REJECT: &str = "reject";

/// The entry of a review on one change impact: its own comment and outcome (`accept`, `reject`, empty undecided).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ReviewEntry {
    pub impact: String,
    pub comment: String,
    pub outcome: String,
}

impl ReviewEntry {
    fn decided(&self) -> bool {
        self.outcome == ACCEPT || self.outcome == REJECT
    }
}

/// The current version of a review of a change (`review.Review`).
#[derive(Debug, Clone, Default)]
pub struct ReviewObject {
    pub key: String,
    pub flow: String,
    pub comment: String,
    pub status: String,
    pub entries: Vec<ReviewEntry>,
    pub by: String,
    pub submitted_at: Option<DateTime<Utc>>,
    /// the item holding this version, and how many versions the key has
    pub item: String,
    pub versions: i32,
}

impl ReviewObject {
    fn open(&self) -> bool {
        self.status == OPEN
    }

    fn entry(&self, impact: &str) -> Option<&ReviewEntry> {
        self.entries.iter().find(|e| e.impact == impact)
    }

    /// The data of an item holding the review (`domain.ReviewRecord.Data`).
    fn data(&self) -> Map<String, Value> {
        let entries = self
            .entries
            .iter()
            .map(|e| {
                let mut m = Map::new();
                m.insert("impact".into(), Value::String(e.impact.clone()));
                if !e.comment.is_empty() {
                    m.insert("comment".into(), Value::String(e.comment.clone()));
                }
                if !e.outcome.is_empty() {
                    m.insert("outcome".into(), Value::String(e.outcome.clone()));
                }
                Value::Object(m)
            })
            .collect();
        let mut d = Map::new();
        d.insert("key".into(), Value::String(self.key.clone()));
        d.insert("status".into(), Value::String(self.status.clone()));
        d.insert("entries".into(), Value::Array(entries));
        for (k, v) in [("flow", &self.flow), ("comment", &self.comment), ("by", &self.by)] {
            if !v.is_empty() {
                d.insert(k.into(), Value::String(v.clone()));
            }
        }
        if let Some(at) = &self.submitted_at {
            d.insert("submittedAt".into(), Value::String(time_text(at)));
        }
        d
    }
}

/// An edit of an open review (`review.Edit`): the comment replaces the global one when set; remove then add change
/// the impacts; the entries edits set the comment and outcome of entries once the set changed.
#[derive(Debug, Clone, Default)]
pub struct ReviewEdit {
    pub comment: Option<String>,
    pub remove: Vec<String>,
    pub add: Vec<String>,
    pub entries: Vec<EntryEdit>,
}

/// The edit of the entry of an impact: its comment and / or its outcome (empty: the decision taken back).
#[derive(Debug, Clone, Default)]
pub struct EntryEdit {
    pub impact: String,
    pub comment: Option<String>,
    pub outcome: Option<String>,
}

/// Who acts on a review: the subject, and whether it may act on the reviews of others (`review.Actor`).
#[derive(Debug, Clone, Default)]
pub struct Actor {
    pub subject: String,
    pub admin: bool,
}

fn invalid(m: impl std::fmt::Display) -> GraphError {
    GraphError::new(Kind::Invalid, format!("invalid review: {m}"))
}

fn conflict(m: impl std::fmt::Display) -> GraphError {
    GraphError::new(Kind::Conflict, format!("review conflict: {m}"))
}

fn not_found(m: impl std::fmt::Display) -> GraphError {
    GraphError::new(Kind::NotFound, format!("review not found: {m}"))
}

/// The flow a call names for a resolved flow: the main flow explicitly, so that no active option takes it.
fn flow_arg(flow: &str) -> String {
    if flow.is_empty() { MAIN_FLOW.into() } else { flow.into() }
}

/// The comment the review of an impact keeps at the submission: the entry's own, then the global one.
fn effective_comment(global: &str, entry: &str) -> String {
    let (global, entry) = (global.trim(), entry.trim());
    match (entry.is_empty(), global.is_empty()) {
        (true, _) => global.into(),
        (_, true) => entry.into(),
        _ => format!("{entry}\n\nReview: {global}"),
    }
}

/// The reviews of the items of a change, the last version of each key in the order the keys appeared; a submitted
/// or discarded review is final, a rejected or superseded item is no version (`review.Reviews`).
pub fn reviews(items: &[Value]) -> Vec<ReviewObject> {
    let mut out: Vec<ReviewObject> = Vec::new();
    for it in items {
        let text = |k: &str| it.get(k).and_then(Value::as_str).unwrap_or_default();
        if text("type") != TYPE_REVIEW || matches!(text("status"), "rejected" | "superseded") {
            continue;
        }
        let d = it.get("data").and_then(Value::as_object);
        let key = data_text(d, "key");
        let status = data_text(d, "status");
        if key.is_empty() || !matches!(status, OPEN | SUBMITTED | DISCARDED) {
            continue;
        }
        let mut r = ReviewObject {
            key: key.into(),
            flow: text("flow").into(),
            comment: data_text(d, "comment").into(),
            status: status.into(),
            entries: review_entries(d)
                .into_iter()
                .map(|(impact, comment, outcome)| ReviewEntry {
                    impact,
                    comment,
                    outcome,
                })
                .collect(),
            by: data_text(d, "by").into(),
            submitted_at: DateTime::parse_from_rfc3339(data_text(d, "submittedAt"))
                .ok()
                .map(|t| t.with_timezone(&Utc)),
            item: text("id").into(),
            versions: 1,
        };
        if r.by.is_empty() {
            r.by = text("producedBy").into();
        }
        match out.iter_mut().find(|o| o.key == r.key) {
            Some(o) if o.open() => {
                r.versions = o.versions + 1;
                *o = r;
            }
            Some(_) => {}
            None => out.push(r),
        }
    }
    out
}

/// The flows from one up to the main flow, the main flow ("") last (`review.chain`): an unknown flow is alone.
fn chain(fx: &Flows, flow: &str) -> Vec<String> {
    let mut out = vec![flow.to_string()];
    let mut flow = flow.to_string();
    for _ in 0..64 {
        if flow.is_empty() {
            break;
        }
        let Some(f) = fx.get(&flow) else { break };
        flow = f.parent.clone();
        out.push(flow.clone());
    }
    out
}

/// The review a change impact has for a flow: the last made on that flow, else the impact's (`review.statusOn`).
fn status_on<'a>(cn: &'a ChangeImpact, flow: &str) -> &'a str {
    if !flow.is_empty()
        && let Some(r) = cn.reviews.iter().rev().find(|r| r.flow == flow && !r.superseded)
    {
        return &r.status;
    }
    &cn.review
}

/// The change impacts a review of a flow may take (`review.Awaiting`): those the flow sees whose review is proposed
/// and that no other open review of the flow holds.
fn awaiting<'a>(
    impacts: &'a [ChangeImpact],
    fx: &Flows,
    flow: &str,
    all: &[ReviewObject],
    except: &str,
) -> Vec<&'a ChangeImpact> {
    let flows = chain(fx, flow);
    let held: Vec<&str> = all
        .iter()
        .filter(|r| r.open() && r.flow == flow && r.key != except)
        .flat_map(|r| r.entries.iter().map(|e| e.impact.as_str()))
        .collect();
    impacts
        .iter()
        .filter(|cn| {
            !cn.superseded
                && flows.contains(&cn.flow)
                && status_on(cn, flow) == REVIEW_PROPOSED
                && !held.contains(&cn.id.as_str())
        })
        .collect()
}

/// The items of type review of a change, as their log entries hold them.
async fn review_items<T: ReadTx>(tx: &T, id: &ChangeId) -> Result<Vec<Value>> {
    tx.log(id, 0, &[&format!("{LOG_FACT}.{TYPE_REVIEW}")])
        .await?
        .iter()
        .filter(|l| l.typ == format!("{LOG_FACT}.{TYPE_REVIEW}"))
        .map(|l| serde_json::from_str(&l.payload).map_err(GraphError::internal))
        .collect()
}

impl<S: WriteTx> Tx<'_, S> {
    /// A key for a new review (`review.Service.newKey`).
    fn review_key(&self) -> String {
        let id = self.env.new_id().replace('-', "");
        format!("REV-{}", id[..10].to_ascii_uppercase())
    }

    /// Starts a review on a flow (empty: the active option, else the main flow) with its global comment.
    pub async fn review_open(&mut self, id: &ChangeId, flow: &str, comment: &str, who: &Actor) -> Result<ReviewObject> {
        self.change_head(id).await?;
        let flow = super::flow::flows_of(self.store, id).await?.resolve(flow);
        let r = ReviewObject {
            key: self.review_key(),
            flow,
            comment: comment.trim().into(),
            status: OPEN.into(),
            by: who.subject.clone(),
            ..ReviewObject::default()
        };
        self.review_write(id, r, who).await
    }

    /// Changes an open review: its author's or an administrator's. The impacts it adds await review on its flow and
    /// are in no other open review of the flow.
    pub async fn review_update(
        &mut self,
        id: &ChangeId,
        key: &str,
        e: ReviewEdit,
        who: &Actor,
    ) -> Result<ReviewObject> {
        let (all, mut r) = self.review_load(id, key, who).await?;
        if let Some(c) = &e.comment {
            r.comment = c.trim().into();
        }
        for imp in &e.remove {
            if r.entry(imp).is_none() {
                return Err(invalid(format!("impact {imp} is not in review {key}")));
            }
            r.entries.retain(|x| &x.impact != imp);
        }
        if !e.add.is_empty() {
            let fx = super::flow::flows_of(self.store, id).await?;
            self.view(id).await?;
            let impacts = self.views[id].fold.impacts.clone();
            let ready: Vec<String> = awaiting(&impacts, &fx, &r.flow, &all, key)
                .iter()
                .map(|cn| cn.id.clone())
                .collect();
            for imp in &e.add {
                if r.entry(imp).is_some() {
                    continue; // already in: adding is idempotent
                }
                if !ready.contains(imp) {
                    return Err(why_not_awaiting(id, &impacts, &r.flow, &all, key, imp));
                }
                r.entries.push(ReviewEntry {
                    impact: imp.clone(),
                    ..ReviewEntry::default()
                });
            }
        }
        for ee in &e.entries {
            let Some(x) = r.entries.iter_mut().find(|x| x.impact == ee.impact) else {
                return Err(invalid(format!("impact {} is not in review {key}", ee.impact)));
            };
            if let Some(c) = &ee.comment {
                x.comment = c.trim().into();
            }
            if let Some(o) = &ee.outcome {
                if !o.is_empty() && o != ACCEPT && o != REJECT {
                    return Err(invalid(format!(
                        "outcome of {} must be {ACCEPT} or {REJECT}",
                        ee.impact
                    )));
                }
                x.outcome = o.clone();
            }
        }
        self.review_write(id, r, who).await
    }

    /// Drops an open review: a review never submitted stays in the log, final.
    pub async fn review_discard(&mut self, id: &ChangeId, key: &str, who: &Actor) -> Result<ReviewObject> {
        let (_, mut r) = self.review_load(id, key, who).await?;
        r.status = DISCARDED.into();
        self.review_write(id, r, who).await
    }

    /// Applies every entry as a verdict on its change impact, by the submitter, with the entry's comment and the
    /// global one, all of them or none; the review becomes submitted, final.
    pub async fn review_submit(&mut self, id: &ChangeId, key: &str, who: &Actor) -> Result<ReviewObject> {
        let (_, mut r) = self.review_load(id, key, who).await?;
        if r.entries.is_empty() {
            return Err(invalid(format!("review {key} has no entry")));
        }
        let mut verdicts = Vec::with_capacity(r.entries.len());
        for e in &r.entries {
            if !e.decided() {
                return Err(invalid(format!("impact {} has no outcome yet", e.impact)));
            }
            let comment = effective_comment(&r.comment, &e.comment);
            if comment.is_empty() {
                return Err(invalid(format!(
                    "impact {} needs a comment, its own or the review's",
                    e.impact
                )));
            }
            verdicts.push(Verdict {
                impact: e.impact.clone(),
                accept: e.outcome == ACCEPT,
                comment,
            });
        }
        r.status = SUBMITTED.into();
        r.submitted_at = Some(self.env.now());
        let b = ReviewBatch {
            id: key.into(),
            flow: flow_arg(&r.flow),
            verdicts,
            record: Some(r.data()),
            record_type: TYPE_REVIEW.into(),
            ..ReviewBatch::default()
        };
        self.impact_node_review_batch(id, b).await?;
        let all = reviews(&review_items(self.store, id).await?);
        Ok(all.into_iter().find(|o| o.key == key).unwrap_or_default())
    }

    /// A review of a change the actor may change: it exists, is open and is the actor's (or the actor an
    /// administrator). It comes with every review of the change.
    async fn review_load(
        &mut self,
        id: &ChangeId,
        key: &str,
        who: &Actor,
    ) -> Result<(Vec<ReviewObject>, ReviewObject)> {
        self.change_head(id).await?;
        let all = reviews(&review_items(self.store, id).await?);
        let Some(r) = all.iter().find(|r| r.key == key).cloned() else {
            return Err(not_found(format!("review {key} of change {id}")));
        };
        if !who.admin && who.subject != r.by {
            return Err(GraphError::new(
                Kind::Forbidden,
                format!("permission denied: review {key} belongs to {}", r.by),
            ));
        }
        if !r.open() {
            return Err(conflict(format!("review {key} is {}: it is final", r.status)));
        }
        Ok((all, r))
    }

    /// Stores a version of a review as an item and answers what the change then folds it to.
    async fn review_write(&mut self, id: &ChangeId, r: ReviewObject, who: &Actor) -> Result<ReviewObject> {
        let it = ItemWrite {
            typ: TYPE_REVIEW.into(),
            flow: flow_arg(&r.flow),
            data: Some(r.data()),
            produced_by: who.subject.clone(),
            ..ItemWrite::default()
        };
        let written = self.add_items(id, vec![it]).await?;
        match reviews(&written).into_iter().next() {
            Some(mut got) => {
                got.versions = r.versions + 1;
                Ok(got)
            }
            None => Ok(r),
        }
    }
}

/// The refusal of an impact that cannot join a review (`review.Service.whyNotAwaiting`).
fn why_not_awaiting(
    id: &ChangeId,
    impacts: &[ChangeImpact],
    flow: &str,
    all: &[ReviewObject],
    key: &str,
    impact: &str,
) -> GraphError {
    let Some(cn) = impacts.iter().find(|cn| cn.id == impact) else {
        return not_found(format!("change impact {impact} is not in change {id}"));
    };
    if let Some(o) = all
        .iter()
        .find(|o| o.open() && o.flow == flow && o.key != key && o.entry(impact).is_some())
    {
        return conflict(format!(
            "change impact {impact} ({}) is already in the open review {}",
            cn.key, o.key
        ));
    }
    conflict(format!(
        "change impact {impact} ({}) does not await review on this flow ({})",
        cn.key,
        status_on(cn, flow)
    ))
}
