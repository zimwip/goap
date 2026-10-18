//! Risks and actions of a change.

use std::collections::HashMap;

use goap_domain::{Change, ChangeItem, Error, ITEM_REJECTED, ItemID, ItemType};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

// Risks and actions are facts of the change (ADR 0036 §1): an item of type risk or action is a version of a record
// identified by its key; the register is the last version of each key among the items in effect, and the change log
// keeps every version with who wrote it.
pub const TYPE_RISK: ItemType = ItemType::from_static("risk");
pub const TYPE_ACTION: ItemType = ItemType::from_static("action");

// Risk statuses.
pub const RISK_OPEN: &str = "open";
pub const RISK_MITIGATING: &str = "mitigating";
pub const RISK_ACCEPTED: &str = "accepted";
pub const RISK_OCCURRED: &str = "occurred";
pub const RISK_CLOSED: &str = "closed";

// Action statuses.
pub const ACTION_OPEN: &str = "open";
pub const ACTION_DONE: &str = "done";
pub const ACTION_CANCELLED: &str = "cancelled";

/// RiskStatuses lists the statuses a risk may have.
pub const RISK_STATUSES: &[&str] = &[
    RISK_OPEN,
    RISK_MITIGATING,
    RISK_ACCEPTED,
    RISK_OCCURRED,
    RISK_CLOSED,
];
/// ActionStatuses lists the statuses an action may have.
pub const ACTION_STATUSES: &[&str] = &[ACTION_OPEN, ACTION_DONE, ACTION_CANCELLED];

fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

/// Risk is the current version of a risk of the change.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Risk {
    pub key: String,
    pub title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    pub probability: i64,
    pub impact: i64,
    pub status: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub owner: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub actions: Vec<String>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub step: String,
    /// Item is the item holding this version, By what produced it, Versions how many versions the key has.
    pub item: ItemID,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    pub versions: i64,
}

impl Risk {
    /// Score is probability × impact.
    pub fn score(&self) -> i64 {
        self.probability * self.impact
    }

    /// Live reports whether the risk still needs attention (open or being mitigated).
    pub fn live(&self) -> bool {
        self.status == RISK_OPEN || self.status == RISK_MITIGATING
    }
}

/// ActionItem is the current version of an action of the change.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ActionItem {
    pub key: String,
    pub title: String,
    pub status: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub owner: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub due: String,
    /// For is the risk key or the decision point the action answers.
    #[serde(rename = "for", skip_serializing_if = "String::is_empty")]
    pub r#for: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub result: String,
    pub item: ItemID,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    pub versions: i64,
}

pub(crate) fn data_string(d: &Map<String, Value>, k: &str) -> String {
    d.get(k)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .trim()
        .to_string()
}

/// A number of the data, truncated toward zero (Go's int conversion); 0 when absent or not a number.
pub(crate) fn data_int(d: &Map<String, Value>, k: &str) -> i64 {
    match d.get(k) {
        Some(Value::Number(n)) => n
            .as_i64()
            .or_else(|| n.as_f64().map(|f| f as i64))
            .unwrap_or_default(),
        _ => 0,
    }
}

pub(crate) fn data_strings(d: &Map<String, Value>, k: &str) -> Vec<String> {
    match d.get(k) {
        Some(Value::Array(l)) => l
            .iter()
            .filter_map(|x| x.as_str().map(str::to_string))
            .collect(),
        _ => Vec::new(),
    }
}

/// validateRecord checks the data of a risk or an action item.
pub(crate) fn validate_record(it: &ChangeItem) -> Result<(), Error> {
    let d = &it.data;
    if data_string(d, "key").is_empty() || data_string(d, "title").is_empty() {
        return Err(Error::invalid(format!(
            "{} item requires data.key and data.title",
            it.r#type
        )));
    }
    let status = data_string(d, "status");
    if it.r#type == TYPE_RISK {
        if !status.is_empty() && !RISK_STATUSES.contains(&status.as_str()) {
            return Err(Error::invalid(format!(
                "risk status must be one of {}",
                RISK_STATUSES.join(", ")
            )));
        }
        for k in ["probability", "impact"] {
            // absent: a version of the risk that does not restate it (ADR 0036); given, it is 1 to 5
            if d.get(k).is_some_and(|v| !v.is_null()) {
                let v = data_int(d, k);
                if !(1..=5).contains(&v) {
                    return Err(Error::invalid(format!("risk {k} must be between 1 and 5")));
                }
            }
        }
    } else if it.r#type == TYPE_ACTION
        && !status.is_empty()
        && !ACTION_STATUSES.contains(&status.as_str())
    {
        return Err(Error::invalid(format!(
            "action status must be one of {}",
            ACTION_STATUSES.join(", ")
        )));
    }
    Ok(())
}

/// The fold of the items of a type: the keys in the order they appeared, the last version of each (with what it
/// does not restate kept from the previous ones) and the number of versions of each.
pub(crate) struct Register {
    pub order: Vec<String>,
    pub last: HashMap<String, ChangeItem>,
    pub versions: HashMap<String, i64>,
}

/// registerOf folds the items of a type in effect into the last version of each key, in the order the keys appeared.
pub(crate) fn register_of(c: &Change, kind: &ItemType) -> Register {
    let mut r = Register {
        order: Vec::new(),
        last: HashMap::new(),
        versions: HashMap::new(),
    };
    for it in &c.items {
        if it.r#type != *kind || !c.active(&it.id) || it.status == ITEM_REJECTED {
            continue;
        }
        let key = data_string(&it.data, "key");
        if key.is_empty() {
            continue;
        }
        let mut it = it.clone();
        match r.last.get(&key) {
            None => r.order.push(key.clone()),
            Some(prev) => {
                // a new version keeps what it does not restate
                let mut merged = prev.data.clone();
                merged.extend(std::mem::take(&mut it.data));
                it.data = merged;
            }
        }
        r.last.insert(key.clone(), it);
        *r.versions.entry(key).or_default() += 1;
    }
    r
}

/// Risks returns the risk register of the change: the current version of each risk, in the order they were raised.
pub fn risks(c: &Change) -> Vec<Risk> {
    let r = register_of(c, &TYPE_RISK);
    r.order
        .iter()
        .map(|key| {
            let it = &r.last[key];
            let d = &it.data;
            let mut risk = Risk {
                key: key.clone(),
                title: data_string(d, "title"),
                description: data_string(d, "description"),
                probability: data_int(d, "probability"),
                impact: data_int(d, "impact"),
                status: data_string(d, "status"),
                owner: data_string(d, "owner"),
                actions: data_strings(d, "actions"),
                step: data_string(d, "step"),
                item: it.id.clone(),
                by: it.produced_by.clone(),
                versions: r.versions[key],
            };
            if risk.status.is_empty() {
                risk.status = RISK_OPEN.into();
            }
            risk
        })
        .collect()
}

/// Actions returns the actions of the change: the current version of each, in the order they were created.
pub fn actions(c: &Change) -> Vec<ActionItem> {
    let r = register_of(c, &TYPE_ACTION);
    r.order
        .iter()
        .map(|key| {
            let it = &r.last[key];
            let d = &it.data;
            let mut a = ActionItem {
                key: key.clone(),
                title: data_string(d, "title"),
                status: data_string(d, "status"),
                owner: data_string(d, "owner"),
                due: data_string(d, "due"),
                r#for: data_string(d, "for"),
                result: data_string(d, "result"),
                item: it.id.clone(),
                by: it.produced_by.clone(),
                versions: r.versions[key],
            };
            if a.status.is_empty() {
                a.status = ACTION_OPEN.into();
            }
            a
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn obj(v: Value) -> Map<String, Value> {
        match v {
            Value::Object(m) => m,
            _ => unreachable!(),
        }
    }

    /// The probability and the impact of a risk are 1 to 5 when given (ADR 0036), and may be left out of a version
    /// that does not restate them.
    #[test]
    fn risk_scale_is_one_to_five() {
        crate::register();
        let risk = |extra: Value| {
            let mut d = obj(json!({"key": "RSK-1", "title": "t"}));
            if let Value::Object(m) = extra {
                d.extend(m);
            }
            ChangeItem {
                r#type: TYPE_RISK,
                data: d,
                ..Default::default()
            }
        };
        let cases = [
            ("absent", Value::Null, true),
            ("lowest", json!({"probability": 1.0, "impact": 1.0}), true),
            ("highest", json!({"probability": 5, "impact": 5i64}), true),
            ("zero", json!({"probability": 0.0}), false),
            ("above the top", json!({"impact": 6.0}), false),
            ("negative", json!({"probability": -1.0}), false),
            ("not a number", json!({"impact": "high"}), false),
            (
                "one of the two",
                json!({"probability": 3.0, "impact": 9.0}),
                false,
            ),
        ];
        for (name, data, ok) in cases {
            let r = risk(data).validate();
            assert_eq!(r.is_ok(), ok, "{name}: {r:?}");
        }
    }

    #[test]
    fn risks_and_actions_fold() {
        let item = |id: &str, t: ItemType, data: Value| ChangeItem {
            id: id.into(),
            r#type: t,
            data: obj(data),
            produced_by: "p".into(),
            ..Default::default()
        };
        let c = Change {
            items: vec![
                item(
                    "1",
                    TYPE_RISK,
                    json!({"key": "R1", "title": " t ", "probability": 2, "impact": 3}),
                ),
                item(
                    "2",
                    TYPE_ACTION,
                    json!({"key": "A1", "title": "do", "for": "R1"}),
                ),
                item(
                    "3",
                    TYPE_RISK,
                    json!({"key": "R1", "status": "mitigating", "actions": ["A1"]}),
                ),
            ],
            ..Default::default()
        };
        let rs = risks(&c);
        assert_eq!(rs.len(), 1);
        let r = &rs[0];
        assert!(
            r.title == "t"
                && r.score() == 6
                && r.live()
                && r.versions == 2
                && r.actions == ["A1"]
                && r.item == "3"
        );
        let a = actions(&c);
        assert!(a.len() == 1 && a[0].status == ACTION_OPEN && a[0].r#for == "R1");
        assert_eq!(
            serde_json::to_value(&a[0]).unwrap(),
            json!({"key": "A1", "title": "do", "status": "open", "for": "R1", "item": "2", "by": "p", "versions": 1})
        );
    }
}
