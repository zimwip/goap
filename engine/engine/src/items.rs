//! The external (LLM / human) form of the items of a change.

use std::collections::{HashMap, HashSet};

use goap_domain::{Change, ChangeItem, Decision, ItemID, ItemType};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::decisions::DecisionOp;
use crate::error::{Error, Result};
use crate::serde_util::null_default;

/// ItemInput is the external (LLM / human) representation of an item of the change: a fact (artifact, decision) or,
/// with type "changeImpact", an operation on a change impact. Items of the same batch are designated by "#<ref>" and
/// existing items by "@<itemId>".
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ItemInput {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub r#ref: String,
    /// Type is artifact or decision; "changeImpact" carries an operation on a change impact (ChangeImpact: declare,
    /// write or review, ADR 0024), applied in order with the ones of the batch.
    pub r#type: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub target: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub decision: Option<DecisionInput>,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub data: Map<String, Value>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub derived_from: Vec<String>,
    /// ChangeImpact is the operation of a "changeImpact" item.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub change_impact: Option<goap_dsl::NodeOp>,
    /// DecisionPoint is the operation of a "decisionPoint" item (ADR 0009 §4).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub decision_point: Option<DecisionOp>,
}

/// DecisionInput is the external representation of a decision.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct DecisionInput {
    pub item: String,
    pub accept: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub comment: String,
}

/// Resolver turns ItemInputs into domain items.
pub(crate) struct Resolver<F: FnMut() -> String> {
    items: HashSet<ItemID>,
    local: HashMap<String, ItemID>,
    new_id: F,
}

impl<F: FnMut() -> String> Resolver<F> {
    pub(crate) fn new(change: &Change, new_id: F) -> Self {
        Resolver {
            items: change.items.iter().map(|it| it.id.clone()).collect(),
            local: HashMap::new(),
            new_id,
        }
    }

    fn item(&self, s: &str) -> Result<ItemID> {
        if let Some(r) = s.strip_prefix('#') {
            return self
                .local
                .get(r)
                .cloned()
                .ok_or_else(|| Error::msg(format!("unknown local item {s:?}")));
        }
        if let Some(r) = s.strip_prefix('@') {
            let id = ItemID::new(r);
            if self.items.contains(&id) {
                return Ok(id);
            }
            return Err(Error::msg(format!("unknown item {s:?}")));
        }
        let id = ItemID::new(s);
        if self.items.contains(&id) {
            return Ok(id);
        }
        if let Some(id) = self.local.get(s) {
            return Ok(id.clone());
        }
        Err(Error::msg(format!("unknown item {s:?}")))
    }

    /// resolve converts a batch. Item ids are assigned here so that "#ref" references inside the batch can be
    /// resolved.
    pub(crate) fn resolve(
        &mut self,
        input: &[ItemInput],
        produced_by: &str,
    ) -> Result<Vec<ChangeItem>> {
        for (i, it) in input.iter().enumerate() {
            let id = ItemID::new((self.new_id)());
            if !it.r#ref.is_empty() {
                self.local.insert(it.r#ref.clone(), id.clone());
            }
            self.local.insert(i.to_string(), id);
        }
        let mut out = Vec::with_capacity(input.len());
        for (i, it) in input.iter().enumerate() {
            let mut item = ChangeItem {
                id: self.local[&i.to_string()].clone(),
                r#type: ItemType::new(it.r#type.clone()),
                target: it.target.clone(),
                data: it.data.clone(),
                produced_by: produced_by.to_string(),
                ..Default::default()
            };
            for d in &it.derived_from {
                let id = self.item(d).map_err(|e| e.context(format!("item {i}")))?;
                item.derived_from.push(id);
            }
            if let Some(d) = &it.decision {
                let id = self
                    .item(&d.item)
                    .map_err(|e| e.context(format!("item {i}")))?;
                item.decision = Some(Decision {
                    item: id,
                    accept: d.accept,
                    comment: d.comment.clone(),
                });
            }
            item.validate()
                .map_err(|e| Error::from(e).context(format!("item {i}")))?;
            out.push(item);
        }
        Ok(out)
    }
}
