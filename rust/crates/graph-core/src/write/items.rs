//! The facts a change's work writes on its blackboard (Go's `Graph.AddItems`): items of the log of the change, one
//! entry each (`domain.FactEntry`). The items a use case gives a meaning (flows, decisions, transitions, reviews,
//! risks...) are the Go service's; artifacts, signals and the types no code reads are served here.
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
fn served(t: &str) -> bool {
    t == "artifact" || t == "signal" || !items::has_code(t)
}

/// What `ChangeItem.Validate` asks of an item served here.
fn validate(it: &ItemWrite) -> std::result::Result<(), String> {
    if it.typ == "signal" && it.target.is_empty() {
        return Err("signal item requires target, the name of the signal".into());
    }
    Ok(())
}

impl<S: WriteTx> Tx<'_, S> {
    /// Appends items to the log of an open change, on its main flow; a draft change becomes active. The items come
    /// back as written (`domain.ChangeItem` JSON).
    pub async fn add_items(&mut self, id: &ChangeId, mut its: Vec<ItemWrite>) -> Result<Vec<Value>> {
        if let Some(it) = its.iter().find(|it| !served(&it.typ)) {
            return Err(GraphError::unimplemented(format!(
                "an item of type {}: written by the Go service",
                it.typ
            )));
        }
        if its.iter().any(|it| !it.flow.is_empty()) {
            return Err(GraphError::unimplemented(
                "an item of an option (ADR 0032): written by the Go service",
            ));
        }
        self.lock_change(id).await?;
        let mut c = self.change_head(id).await?;
        // the change as read: its position, and no option (an item naming no flow goes to the active option)
        let seq = fold_change(self.store, id).await?.seq;
        if matches!(c.status.as_str(), "applied" | "abandoned" | "committed") {
            return Err(GraphError::conflict(format!("change {id} is {}", c.status)));
        }
        for it in its.iter_mut().filter(|it| it.id.is_empty()) {
            it.id = self.env.new_id();
        }
        let mut out = Vec::with_capacity(its.len());
        for it in &its {
            let status = if it.status.is_empty() {
                ITEM_PROPOSED
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
            out.push(serde_json::from_str(&payload).map_err(GraphError::internal)?);
        }
        if c.status == "draft" {
            c.status = "active".into();
            self.put_change(&c).await?;
        }
        self.raise_items(&c, seq, &out);
        Ok(out)
    }
}
