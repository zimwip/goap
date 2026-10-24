//! The facts a change's work writes on its blackboard (Go's `Graph.AddItems`): items of the log of the change, one
//! entry each (`domain.FactEntry`). The items a use case gives a meaning (flows, decisions, transitions, risks...) are
//! the Go service's; artifacts, signals, the reviews being built up and the types no code reads are served here.
use chrono::{DateTime, Utc};
use serde::Serialize;
use serde_json::{Map, Value};

use super::{check, tx::Tx, work::fold_change};
use crate::{
    GraphError, Result, gojson,
    impact::{LOG_FACT, LogEntry},
    items,
    model::ChangeId,
    store::WriteTx,
};

const ITEM_PROPOSED: &str = "proposed";

/// An item to write: what its writer gives (its id allocated when empty, its status `proposed` when empty).
#[derive(Debug, Clone, Default)]
pub struct ItemWrite {
    pub id: String,
    pub typ: String,
    pub target: String,
    pub status: String,
    pub flow: String,
    pub data: Option<Map<String, Value>>,
    pub produced_by: String,
    pub derived_from: Vec<String>,
    pub supersedes: Vec<String>,
    pub execution: String,
}

/// An item as Go marshals a `domain.ChangeItem` with no flow event, decision event, decision nor recipient.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct ItemJson<'a> {
    id: &'a str,
    #[serde(rename = "type")]
    typ: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    target: &'a str,
    status: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    flow: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    data: Option<&'a Map<String, Value>>,
    #[serde(skip_serializing_if = "str::is_empty")]
    produced_by: &'a str,
    #[serde(skip_serializing_if = "<[String]>::is_empty")]
    derived_from: &'a [String],
    #[serde(skip_serializing_if = "<[String]>::is_empty")]
    supersedes: &'a [String],
    #[serde(skip_serializing_if = "str::is_empty")]
    execution: &'a str,
    #[serde(with = "gojson::time")]
    created_at: DateTime<Utc>,
}

/// Whether the items of a type are served here: an artifact, a signal, or a type no code gives a meaning.
pub(crate) fn served(t: &str) -> bool {
    t == "artifact" || t == "signal" || t == TYPE_REVIEW || !items::has_code(t)
}

/// The item type of the record of a review (`domain.TypeReview`).
pub(crate) const TYPE_REVIEW: &str = "review";

/// The types an operation of their own records, which `AddItems` refuses.
fn recorded_elsewhere(t: &str) -> Option<&'static str> {
    match t {
        "flow" => Some("flow events are recorded by OpenFlow, AdoptFlow and DiscardFlow"),
        "transition" => Some("transitions are recorded by TransitionChange"),
        "decision_point" => {
            Some("decision points are recorded by OpenDecision, RuleDecision, AnswerQuestion and RatifyDecision")
        }
        _ => None,
    }
}

/// A trimmed text of the data of an item (`domain.reviewString`).
pub(crate) fn data_text<'a>(d: Option<&'a Map<String, Value>>, k: &str) -> &'a str {
    d.and_then(|d| d.get(k))
        .and_then(Value::as_str)
        .unwrap_or_default()
        .trim()
}

/// What `ChangeItem.Validate` asks of the record of a review (`domain.validateReview`).
pub(crate) fn validate_review(d: Option<&Map<String, Value>>) -> std::result::Result<(), String> {
    if data_text(d, "key").is_empty() {
        return Err("review item requires data.key".into());
    }
    let status = data_text(d, "status");
    if !matches!(status, "open" | "submitted" | "discarded") {
        return Err("review status must be one of open, submitted, discarded".into());
    }
    let entries = review_entries(d);
    let mut seen = std::collections::HashSet::new();
    for (impact, _, outcome) in &entries {
        if impact.is_empty() {
            return Err("review entry requires its impact".into());
        }
        if !seen.insert(impact.clone()) {
            return Err(format!("review names impact {impact} twice"));
        }
        if !outcome.is_empty() && outcome != "accept" && outcome != "reject" {
            return Err("review entry outcome must be accept or reject".into());
        }
    }
    if status == "submitted" {
        if entries.is_empty() {
            return Err("a submitted review has at least one entry".into());
        }
        if let Some((impact, _, _)) = entries.iter().find(|(_, _, o)| o != "accept" && o != "reject") {
            return Err(format!(
                "a submitted review decides every entry: impact {impact} has no outcome"
            ));
        }
        if data_text(d, "submittedAt").is_empty() {
            return Err("a submitted review requires data.submittedAt".into());
        }
    }
    Ok(())
}

/// The entries of the record of a review: impact, comment, outcome (`domain.ReviewEntriesOf`).
pub(crate) fn review_entries(d: Option<&Map<String, Value>>) -> Vec<(String, String, String)> {
    let Some(Value::Array(list)) = d.and_then(|d| d.get("entries")) else {
        return Vec::new();
    };
    list.iter()
        .filter_map(Value::as_object)
        .map(|m| {
            (
                data_text(Some(m), "impact").to_string(),
                data_text(Some(m), "comment").to_string(),
                data_text(Some(m), "outcome").to_string(),
            )
        })
        .collect()
}

/// What `ChangeItem.Validate` asks of an item served here.
fn validate(it: &ItemWrite) -> std::result::Result<(), String> {
    if it.typ == "signal" && it.target.is_empty() {
        return Err("signal item requires target, the name of the signal".into());
    }
    if it.typ == TYPE_REVIEW {
        return validate_review(it.data.as_ref());
    }
    Ok(())
}

impl<S: WriteTx> Tx<'_, S> {
    /// Checks an item and appends it to the log of a change (Go's `checkItem`, then `putItem`): its status the given
    /// default when it names none. The item comes back as written.
    pub(crate) async fn put_item(&mut self, id: &ChangeId, it: &ItemWrite, default_status: &str) -> Result<Value> {
        let status = if it.status.is_empty() {
            default_status
        } else {
            it.status.as_str()
        };
        let at = self.env.now();
        let data = it.data.as_ref().filter(|m| !m.is_empty());
        let problem = match validate(it) {
            Err(e) => Err(e),
            Ok(()) => check::item_data(self.catalog(), &it.typ, data)?,
        };
        if let Err(e) = problem {
            return Err(GraphError::invalid(format!("item {}: {e}", it.id)));
        }
        let payload = gojson::to_string(&ItemJson {
            id: &it.id,
            typ: &it.typ,
            target: &it.target,
            status,
            flow: &it.flow,
            data,
            produced_by: &it.produced_by,
            derived_from: &it.derived_from,
            supersedes: &it.supersedes,
            execution: &it.execution,
            created_at: at,
        });
        self.append_log(&LogEntry {
            seq: 0,
            id: it.id.clone(),
            change: id.clone(),
            typ: format!("{LOG_FACT}.{}", it.typ),
            flow: it.flow.clone(),
            process: String::new(),
            execution: it.execution.clone(),
            subject: it.target.clone(),
            by: it.produced_by.clone(),
            at,
            causation: String::new(),
            payload: payload.clone(),
        })
        .await?;
        serde_json::from_str(&payload).map_err(GraphError::internal)
    }

    /// Appends items to the log of an open change, on its main flow; a draft change becomes active. The items come
    /// back as written (`domain.ChangeItem` JSON).
    pub async fn add_items(&mut self, id: &ChangeId, mut its: Vec<ItemWrite>) -> Result<Vec<Value>> {
        if let Some(it) = its
            .iter()
            .find(|it| !served(&it.typ) && recorded_elsewhere(&it.typ).is_none())
        {
            return Err(GraphError::unimplemented(format!(
                "an item of type {}: written by the Go service",
                it.typ
            )));
        }
        self.lock_change(id).await?;
        let mut c = self.change_head(id).await?;
        // the change as read: its position
        let seq = fold_change(self.store, id).await?.seq;
        if matches!(c.status.as_str(), "applied" | "abandoned" | "committed") {
            return Err(GraphError::conflict(format!("change {id} is {}", c.status)));
        }
        let fx = super::flow::flows_of(self.store, id).await?;
        for it in its.iter_mut() {
            it.flow = fx.resolve(&it.flow); // no flow: the active option (ADR 0032 §6)
        }
        let batch = its.first().map(|it| it.flow.clone()).unwrap_or_default();
        for it in &its {
            if let Some(why) = recorded_elsewhere(&it.typ) {
                return Err(GraphError::invalid(why));
            }
            if it.typ == TYPE_REVIEW && data_text(it.data.as_ref(), "status") == "submitted" {
                return Err(GraphError::invalid(
                    "a review is submitted with its verdicts, by ImpactNodeReviewBatch (ADR 0105)",
                ));
            }
            if it.flow != batch {
                return Err(GraphError::invalid("the items of a batch belong to one flow"));
            }
        }
        if !batch.is_empty() && fx.effective(&batch) != crate::flow::FLOW_OPEN {
            return Err(GraphError::conflict(format!("flow {batch} is not open")));
        }
        for it in its.iter_mut().filter(|it| it.id.is_empty()) {
            it.id = self.env.new_id();
        }
        let mut out = Vec::with_capacity(its.len());
        for it in &its {
            out.push(self.put_item(id, it, ITEM_PROPOSED).await?);
        }
        if c.status == "draft" {
            c.status = "active".into();
            self.put_change(&c).await?;
        }
        self.raise_items(&c, seq, &out);
        Ok(out)
    }
}
